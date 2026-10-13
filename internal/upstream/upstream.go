// Package upstream polls the status of real servers, and keeps the last one
// received.
package upstream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/status"
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
	status *status.Status
	at     time.Time
}

// NewPoller returns a poller for the upstream identified by id in logs.
func NewPoller(id string, cfg Config, logger *slog.Logger) *Poller {
	return newPoller(id, cfg, logger, net.DefaultResolver)
}

func newPoller(id string, cfg Config, logger *slog.Logger, r resolver) *Poller {
	address := cfg.Host
	if cfg.Port != 0 {
		address = net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	}
	return &Poller{cfg: cfg, logger: logger.With("upstream", id, "address", address), resolver: r}
}

// Status returns the last status received, or nil if none was received within
// MaxAge.
func (p *Poller) Status() *status.Status {
	r := p.latest.Load()
	if r == nil || time.Since(r.at) > p.cfg.MaxAge {
		return nil
	}
	return r.status
}

// availability is the state of a server, as last logged.
type availability int

const (
	unknown availability = iota
	available
	unavailable
)

// Run polls the server until ctx is canceled. It logs when the server becomes
// available, and when it becomes unavailable: when Status starts returning
// nil. The other polls are logged at the debug level.
func (p *Poller) Run(ctx context.Context) {
	state := unknown
	for {
		st, latency, err := p.poll(ctx)
		if ctx.Err() != nil {
			return
		}

		wait := p.cfg.Interval
		if err == nil {
			p.latest.Store(&received{status: st, at: time.Now()})
			if state != available {
				p.logStatus(slog.LevelInfo, "upstream available", st, latency)
				state = available
			} else {
				p.logStatus(slog.LevelDebug, "upstream polled", st, latency)
			}
		} else {
			wait = p.cfg.RetryInterval
			if state != unavailable && p.Status() == nil {
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

// poll asks the server for its status, then pings it as clients do: some
// proxies take clients that do not for bots. The ping measures the latency of
// the server.
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

	request := modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: p.cfg.ProtocolVersion,
		ServerAddress:   p.cfg.ServerAddress,
		ServerPort:      p.cfg.ServerPort,
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
