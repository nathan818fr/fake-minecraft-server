// Package upstream polls the status of real servers, and keeps the last one
// received.
package upstream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

type Config struct {
	// Host and Port are the address of the server. Without Port, it is looked
	// up as clients do (see Poller.resolve).
	Host string
	Port uint16
	// ServerAddress, ServerPort and ProtocolVersion are sent in the handshake.
	ServerAddress   string
	ServerPort      uint16
	ProtocolVersion int32

	// Interval is the time between polls, and RetryInterval the time after a
	// failed one.
	Interval      time.Duration
	RetryInterval time.Duration
	Timeout       time.Duration
	// MaxAge is how long the last status received stays in use while polls
	// fail.
	MaxAge time.Duration
}

type Poller struct {
	cfg      Config
	logger   *slog.Logger
	resolver resolver
	latest   atomic.Pointer[received]
}

// resolver looks up SRV records, like net.Resolver.
type resolver interface {
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

type received struct {
	status  *status.Status
	at      time.Time
	latency time.Duration
}

// NewPoller returns a poller for the upstream identified by id in logs.
func NewPoller(id string, cfg Config, logger *slog.Logger) *Poller {
	return newPoller(id, cfg, logger, net.DefaultResolver)
}

func newPoller(id string, cfg Config, logger *slog.Logger, r resolver) *Poller {
	return &Poller{cfg: cfg, logger: logger.With("upstream", id, "address", cfg.Address()), resolver: r}
}

// Inherit makes p, which replaces previous, stopped, with another
// configuration, start from the last status that previous received. The status
// keeps its age, so that it stays in use until p receives one, or until the
// max age of p after it was received.
func (p *Poller) Inherit(previous *Poller) {
	p.latest.Store(previous.latest.Load())
}

// Address returns the address of the server as configured: host, or
// host:port.
func (cfg Config) Address() string {
	if cfg.Port == 0 {
		return cfg.Host
	}
	return net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
}

// State is what is known of a server at a time.
type State struct {
	// Status is the last status received, or nil if none was received within
	// MaxAge.
	Status *status.Status
	// Polled reports whether a poll succeeded, the last one of which had a
	// ping of Latency, Age ago.
	Polled  bool
	Latency time.Duration
	Age     time.Duration
}

func (p *Poller) State() State {
	r := p.latest.Load()
	if r == nil {
		return State{}
	}
	age := time.Since(r.at)
	st := State{Polled: true, Latency: r.latency, Age: age}
	if age <= p.cfg.MaxAge {
		st.Status = r.status
	}
	return st
}

// availability is the state of a server, as last logged.
type availability int

const (
	unknown availability = iota
	available
	unavailable
)

// Run polls the server until ctx is canceled. It logs when the server becomes
// available, and when it becomes unavailable: when the status of State
// becomes nil. The other polls are logged at the debug level.
func (p *Poller) Run(ctx context.Context) {
	state := unknown
	for {
		st, latency, err := p.poll(ctx)
		if ctx.Err() != nil {
			return
		}

		wait := p.cfg.Interval
		if err == nil {
			p.latest.Store(&received{status: st, at: time.Now(), latency: latency})
			if state != available {
				p.logStatus(slog.LevelInfo, "upstream available", st, latency)
				state = available
			} else {
				p.logStatus(slog.LevelDebug, "upstream polled", st, latency)
			}
		} else {
			wait = p.cfg.RetryInterval
			if state != unavailable && p.State().Status == nil {
				p.logger.Warn("upstream unavailable", logging.Error(err))
				state = unavailable
			} else {
				p.logger.Debug("upstream poll failed", logging.Error(err))
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (p *Poller) logStatus(level slog.Level, msg string, st *status.Status, latency time.Duration) {
	version, players := st.Version(), st.Players()
	p.logger.LogAttrs(context.Background(), level, msg,
		// Servers can make it as long as their whole status.
		logging.Untrusted("version", logging.Shorten(version.Name)),
		slog.Int("protocol", int(version.Protocol)),
		slog.Int("online", int(players.Online)),
		slog.Int("max", int(players.Max)),
		logging.Duration("latency", latency),
	)
}

func (p *Poller) poll(ctx context.Context) (*status.Status, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	addr, err := p.resolve(ctx)
	if err != nil {
		return nil, 0, err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = conn.Close() }()
	// Interrupts the exchange at the timeout, or when the poller stops.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	return Poll(conn, p.cfg)
}

// Poll asks the server on conn for its status, with the handshake of cfg, then
// pings it as clients do: some proxies take clients that do not for bots. The
// ping measures the latency of the server.
func Poll(conn io.ReadWriter, cfg Config) (*status.Status, time.Duration, error) {
	request := modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: cfg.ProtocolVersion,
		ServerAddress:   cfg.ServerAddress,
		ServerPort:      cfg.ServerPort,
		Intent:          modernproto.IntentStatus,
	})
	request = modernproto.AppendPacket(request, modernproto.StatusRequestID, nil)
	if _, err := conn.Write(request); err != nil {
		return nil, 0, err
	}
	r := bufio.NewReader(conn)
	data, err := modernproto.ReadStatusResponse(r)
	if err != nil {
		return nil, 0, fmt.Errorf("reading status: %w", err)
	}
	st, err := status.Parse(data)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid status: %w", err)
	}

	start := time.Now()
	ping := binary.BigEndian.AppendUint64(nil, uint64(start.UnixMilli()))
	if _, err := conn.Write(modernproto.AppendPacket(nil, modernproto.PingRequestID, ping)); err != nil {
		return nil, 0, err
	}
	if err := modernproto.ReadPacketExact(r, modernproto.PongResponseID, ping); err != nil {
		return nil, 0, fmt.Errorf("reading pong: %w", err)
	}
	return st, time.Since(start), nil
}

// resolve returns the address to connect to. As clients do for a host without
// port, it is the target of the Minecraft SRV record of the host
// (_minecraft._tcp.host) if it has one, or else the host on the default port.
// The record is looked up at each poll, so that its changes apply.
func (p *Poller) resolve(ctx context.Context) (string, error) {
	if p.cfg.Port != 0 {
		return net.JoinHostPort(p.cfg.Host, strconv.Itoa(int(p.cfg.Port))), nil
	}
	// IP addresses have no SRV record.
	if _, err := netip.ParseAddr(p.cfg.Host); err == nil {
		return net.JoinHostPort(p.cfg.Host, strconv.Itoa(modernproto.DefaultPort)), nil
	}
	_, records, err := p.resolver.LookupSRV(ctx, "minecraft", "tcp", p.cfg.Host)
	var dnsErr *net.DNSError
	switch {
	case len(records) > 0:
		// Sorted by priority, then by weight at random (RFC 2782).
		srv := records[0]
		if srv.Target == "." {
			return "", fmt.Errorf("the SRV record of %s says that it has no Minecraft server", p.cfg.Host)
		}
		return net.JoinHostPort(srv.Target, strconv.Itoa(int(srv.Port))), nil
	case err == nil, errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return net.JoinHostPort(p.cfg.Host, strconv.Itoa(modernproto.DefaultPort)), nil
	default:
		return "", fmt.Errorf("looking up SRV record: %w", err)
	}
}
