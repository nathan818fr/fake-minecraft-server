// Package server answers Minecraft server list pings with a configurable
// status and disconnects every player trying to join.
package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/internal/legacyproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/proxyproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/status"
	"github.com/nathan818fr/fake-minecraft-server/internal/upstream"
)

// acceptRetryDelay paces retries after accept errors, such as running out of
// file descriptors, which would otherwise make the server spin.
const acceptRetryDelay = 100 * time.Millisecond

// legacyPingWait bounds the wait for the end of a legacy ping, which can
// arrive a round trip after its start. Clients whose ping is only such a start
// (Beta 1.8 to 1.5) get their response that much later.
const legacyPingWait = 500 * time.Millisecond

type Config struct {
	// Servers holds the virtual servers that the rules choose from, by ID.
	Servers map[string]VirtualServer
	// Upstreams holds the upstreams of the virtual servers, by ID, polled while
	// the server serves.
	Upstreams map[string]upstream.Config
	// Rules tell which virtual server clients see. The last one must always
	// apply: without conditions, and with a server with a static status.
	Rules []Rule

	// ConnectionTimeout bounds the whole lifetime of a connection. It must be
	// positive.
	ConnectionTimeout time.Duration
	// HandshakeTimeout bounds the wait for the handshake (or legacy ping or
	// login) of a client, with its PROXY protocol header and the request sent
	// along with it. Clients send all of it as soon as they connect, so a limit
	// shorter than ConnectionTimeout frees the slots of connections that send
	// nothing or too little, which makes the server harder to flood. It must be
	// positive.
	HandshakeTimeout time.Duration

	// MaxConnections and MaxConnectionsPerIP bound the number of concurrent
	// connections. Connections over the limits are reset as soon as they are
	// identified. 0 means unlimited. MaxConnectionsPerIP counts IPv6 clients
	// per /64 prefix rather than per address, and does not apply to Unix
	// socket peers, which have no address (see clientLimiter).
	MaxConnections      int
	MaxConnectionsPerIP int

	// LogRateLimit bounds the number of lines logged per second about
	// connections. 0 means unlimited.
	LogRateLimit int

	// SoftwareSignature adds the signature of this software to the statuses
	// answered, so that it can be told from real servers (see
	// signatureMember).
	SoftwareSignature bool
}

// Listener is a listener of the server, with the options of the connections
// that it accepts.
type Listener struct {
	net.Listener
	Options ListenerOptions
}

type ListenerOptions struct {
	// ProxyProtocol requires every connection to start with a PROXY protocol
	// header, whose source address then identifies the client.
	ProxyProtocol bool
	// TrustedProxies holds the addresses of the TCP peers allowed to send
	// PROXY protocol headers. Unix socket peers always are (see
	// isTrustedProxy).
	TrustedProxies []netip.Prefix
}

// Upstream is a server whose status changes, such as an upstream.Poller.
type Upstream interface {
	// Run keeps the status up to date until ctx is canceled.
	Run(ctx context.Context)
	// Status returns the current status of the server, or nil if it has
	// none. It is valid (see status.Status.Check), but checked against the
	// length limit only when answered, as it changes.
	Status() *status.Status
}

type Server struct {
	cfg       Config
	logger    *slog.Logger
	upstreams map[string]Upstream

	connLimiter      semaphore
	clientLimiter    *clientLimiter
	logLimiter       logLimiter
	ruleErrorLimiter ruleErrorLimiter
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	upstreams := make(map[string]Upstream, len(cfg.Upstreams))
	for id, upstreamCfg := range cfg.Upstreams {
		upstreams[id] = upstream.NewPoller(id, upstreamCfg, logger)
	}
	return newServer(cfg, upstreams, logger)
}

// newServer is New with the upstreams already made, such as fakes in tests.
func newServer(cfg Config, upstreams map[string]Upstream, logger *slog.Logger) (*Server, error) {
	if cfg.ConnectionTimeout <= 0 || cfg.HandshakeTimeout <= 0 {
		return nil, errors.New("connection and handshake timeouts must be positive")
	}
	if err := checkServers(&cfg, upstreams); err != nil {
		return nil, err
	}

	return &Server{
		cfg:       cfg,
		logger:    logger,
		upstreams: upstreams,

		connLimiter:   newSemaphore(cfg.MaxConnections),
		clientLimiter: newClientLimiter(cfg.MaxConnectionsPerIP),
		logLimiter:    logLimiter{perSecond: cfg.LogRateLimit},
	}, nil
}

// Serve accepts connections on the listeners until ctx is canceled or one of
// them fails, then closes them all and waits for the remaining connections to
// finish. The limits apply to all the listeners together. The upstreams are
// polled meanwhile.
func (s *Server) Serve(ctx context.Context, listeners ...Listener) error {
	if len(listeners) == 0 {
		return errors.New("no listener to serve")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var polling sync.WaitGroup
	for _, u := range s.upstreams {
		polling.Go(func() { u.Run(ctx) })
	}

	var conns sync.WaitGroup

	errs := make(chan error, len(listeners))
	for _, ln := range listeners {
		var proxyProtocol slog.Attr
		if ln.Options.ProxyProtocol {
			proxyProtocol = slog.Bool("proxy_protocol", true)
		}
		s.logger.LogAttrs(ctx, slog.LevelInfo, "listening", slog.String("listener", listenerAddr(ln)), proxyProtocol)
		go func() { errs <- s.accept(ctx, ln, len(listeners) > 1, &conns) }()
	}

	var err error
	for range listeners {
		if lnErr := <-errs; lnErr != nil && err == nil {
			err = lnErr
			cancel() // Stop the other listeners.
		}
	}
	if err == nil {
		s.logger.Info("stopping")
	}
	conns.Wait()
	cancel() // Stops polling the upstreams.
	polling.Wait()
	if err == nil {
		s.logger.Info("stopped")
	}
	return err
}

// accept accepts connections on ln until ctx is canceled, then closes ln. It
// returns the error of ln if it fails before. Logs show the address of ln if
// logListener.
func (s *Server) accept(ctx context.Context, ln Listener, logListener bool, conns *sync.WaitGroup) error {
	listener := listenerInfo{addr: listenerAddr(ln), logged: logListener, ListenerOptions: ln.Options}
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			s.logger.Error("accept failed", "listener", listener.addr, logging.Error(err))
			time.Sleep(acceptRetryDelay)
			continue
		}

		if !s.connLimiter.tryAcquire() {
			abort(conn)
			c := newConnInfo(conn, listener)
			s.logRejected("connection rejected: too many connections", &c)
			continue
		}
		conns.Go(func() {
			defer s.connLimiter.release()
			s.handle(conn, listener)
		})
	}
}

// listenerInfo is what connections know about the listener that accepted
// them: its address (see listenerAddr), whether logs show it, which they do
// with several listeners, and its options.
type listenerInfo struct {
	addr   string
	logged bool
	ListenerOptions
}

// listenerAddr returns the address of ln as host:port, or unix:path for Unix
// sockets.
func listenerAddr(ln net.Listener) string {
	addr := ln.Addr()
	if addr.Network() == "unix" {
		return "unix:" + addr.String()
	}
	return addr.String()
}

// session is a connection, and what the client sent on it, recorded by
// exchange and logged once it ends.
type session struct {
	conn connInfo

	handshake modernproto.Handshake
	// rule is the name of the rule that answered, if any, server the ID of its
	// virtual server, and statusSource where its status came from, for status
	// requests.
	rule         string
	server       string
	statusSource statusSource
	// sentPingRequest reports whether the client sent a ping request after the
	// status request, to measure its latency.
	sentPingRequest bool
	loginStart      modernproto.LoginStart
	// legacyPing and legacyLogin hold what clients older than 1.7 sent, if any.
	legacyPing  *legacyproto.Ping
	legacyLogin *legacyproto.Login

	// client is what conditions know about the client, made when first
	// needed (see conditionClient).
	client *condition.Client
	// ruleErrors are the errors of the conditions that failed.
	ruleErrors []ruleError
}

// conditionClient returns what conditions know about the client of sess.
func (sess *session) conditionClient() *condition.Client {
	if sess.client != nil {
		return sess.client
	}
	c := &condition.Client{Event: condition.EventJoin, Host: sess.host(), Listener: sess.conn.listener.addr}
	if remote := sess.conn.remote; !isUnixPeer(remote) {
		c.IP = remote.Addr().WithZone("").String()
	}
	switch {
	case sess.legacyPing != nil:
		ping := sess.legacyPing
		c.Event, c.Legacy, c.LegacyPingVersion = condition.EventPing, true, ping.Version.String()
		c.Port, c.Protocol = int(ping.ServerPort), int(ping.ProtocolVersion)
	case sess.legacyLogin != nil:
		login := sess.legacyLogin
		c.Legacy = true
		c.Port, c.Protocol = int(login.ServerPort), int(login.ProtocolVersion)
	default:
		h := sess.handshake
		if h.Intent == modernproto.IntentStatus {
			c.Event = condition.EventPing
		}
		c.Transferred = h.Intent == modernproto.IntentTransfer
		c.HostExtra, c.Port, c.Protocol = h.ServerAddressExtra, int(h.ServerPort), int(h.ProtocolVersion)
	}
	sess.client = c
	return c
}

// host returns the host that the client of sess connected to, or "" if it did
// not send one.
func (sess *session) host() string {
	switch {
	case sess.legacyPing != nil:
		return sess.legacyPing.ServerAddress
	case sess.legacyLogin != nil:
		return sess.legacyLogin.ServerAddress
	default:
		return sess.handshake.ServerAddress
	}
}

// identified reports whether the client sent a valid handshake, legacy ping or
// legacy login, which tells it is a Minecraft client.
func (sess *session) identified() bool {
	return sess.handshake.Intent != 0 || sess.legacyPing != nil || sess.legacyLogin != nil
}

func (s *Server) handle(conn net.Conn, listener listenerInfo) {
	sess := session{conn: newConnInfo(conn, listener)}
	c := &sess.conn
	connDeadline := c.start.Add(s.cfg.ConnectionTimeout)
	handshakeDeadline := earliest(c.start.Add(s.cfg.HandshakeTimeout), connDeadline)
	_ = conn.SetDeadline(connDeadline)
	_ = conn.SetReadDeadline(handshakeDeadline)

	buffers := connBuffersPool.Get().(*connBuffers)
	buffers.head = headRecorder{r: conn}
	buffers.reader.Reset(&buffers.head)
	defer func() {
		buffers.head.r = nil // The pool must not keep the connection alive.
		connBuffersPool.Put(buffers)
	}()

	if listener.ProxyProtocol {
		client, err := readProxyHeader(c, buffers.reader)
		if err != nil {
			abort(conn)
			s.logInvalid(c, buffers.head, err)
			return
		}
		c.remote, c.unknownClient = client, false
		buffers.head.restart(buffers.reader) // Log what the client sent, not the header.
	}

	client := c.remote.Addr()
	if !s.clientLimiter.acquire(client) {
		abort(conn)
		s.logRejected("connection rejected: too many connections from this address", c)
		return
	}
	defer s.clientLimiter.release(client)

	err := s.exchange(conn, buffers, &sess, handshakeDeadline, connDeadline)
	if err != nil {
		abort(conn)
	} else {
		closeGracefully(conn, connDeadline)
	}

	if !sess.identified() {
		s.logInvalid(c, buffers.head, err)
		return
	}
	s.logSession(&sess, err)
}

// readProxyHeader reads the PROXY protocol header that the peer of c sends,
// if it is a trusted proxy, and returns the address of the client, or the
// proxy's own address for headers that carry none, such as health checks.
func readProxyHeader(c *connInfo, r *bufio.Reader) (netip.AddrPort, error) {
	if !c.proxied {
		return netip.AddrPort{}, errors.New("not a trusted proxy, see trusted_proxies")
	}
	client, err := proxyproto.ReadHeader(r)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("reading PROXY protocol header: %w", err)
	}
	if !client.IsValid() {
		return c.peer, nil
	}
	return unmapAddrPort(client), nil
}

// isTrustedProxy reports whether peer may send PROXY protocol headers. Unix
// socket peers always may: enabling the PROXY protocol on a Unix socket trusts
// whoever can connect to it.
func (o ListenerOptions) isTrustedProxy(peer netip.AddrPort) bool {
	if isUnixPeer(peer) {
		return true
	}
	// Prefixes never match addresses with a zone, which link-local peers have.
	addr := peer.Addr().WithZone("")
	for _, prefix := range o.TrustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// awaitLegacyPing waits briefly for the rest of what may be a legacy ping, so
// that legacyproto.ReadPing can tell its version.
func awaitLegacyPing(conn net.Conn, r *bufio.Reader, handshakeDeadline time.Time) {
	// Waits for the first bytes of the client, until the handshake deadline.
	if _, err := r.Peek(1); err != nil || !legacyproto.Incomplete(r) {
		return
	}
	_ = conn.SetReadDeadline(earliest(time.Now().Add(legacyPingWait), handshakeDeadline))
	for legacyproto.Incomplete(r) {
		if _, err := r.Peek(r.Buffered() + 1); err != nil {
			break
		}
	}
	_ = conn.SetReadDeadline(handshakeDeadline)
}

// readHandshake reads the handshake of the client, or its legacy ping or login.
func readHandshake(conn net.Conn, buffers *connBuffers, sess *session, handshakeDeadline time.Time) error {
	awaitLegacyPing(conn, buffers.reader, handshakeDeadline)
	if ping, ok, err := legacyproto.ReadPing(buffers.reader); ok {
		if err != nil {
			return err
		}
		sess.legacyPing = &ping
		return nil
	}
	if login, ok, err := legacyproto.ReadLogin(buffers.reader); ok {
		if err != nil {
			return err
		}
		sess.legacyLogin = &login
		return nil
	}

	payload, err := modernproto.ReadPacket(buffers.reader, modernproto.HandshakeID, buffers.payload[:])
	if err != nil {
		return fmt.Errorf("reading handshake: %w", err)
	}
	sess.handshake, err = modernproto.ParseHandshake(payload)
	return err
}

// exchange runs the protocol with the client, recording its progress in sess.
//
// Clients send their status request or Login Start along with their
// handshake, so these are read by the handshake deadline too. Only the ping,
// sent a round trip after the status, is read by the connection deadline.
func (s *Server) exchange(conn net.Conn, buffers *connBuffers, sess *session, handshakeDeadline, connDeadline time.Time) error {
	if err := readHandshake(conn, buffers, sess, handshakeDeadline); err != nil {
		return err
	}

	switch {
	case sess.legacyPing != nil:
		legacy := legacyStatus(s.statusFor(sess))
		_, err := conn.Write(legacyproto.AppendPingResponse(nil, sess.legacyPing.Version, legacy))
		return err
	case sess.legacyLogin != nil:
		_, err := conn.Write(legacyproto.AppendKick(nil, legacyproto.TextFromComponent(s.kickFor(sess))))
		return err
	case sess.handshake.Intent == modernproto.IntentStatus:
		return s.answerStatus(conn, buffers, sess, connDeadline)
	default:
		// Clients send a Login Start packet right after the handshake. It is
		// only read for logging, after the kick, so that an invalid or missing
		// one neither delays the kick nor aborts the connection.
		kick := modernproto.AppendString(nil, string(s.kickFor(sess)))
		if _, err := conn.Write(modernproto.AppendPacket(nil, modernproto.LoginDisconnectID, kick)); err != nil {
			return err
		}
		sess.loginStart, _ = modernproto.ReadLoginStart(buffers.reader, sess.handshake.ProtocolVersion)
		return nil
	}
}

// answerStatus answers the status request of the client, then its ping
// request if it sends one.
func (s *Server) answerStatus(conn net.Conn, buffers *connBuffers, sess *session, connDeadline time.Time) error {
	if err := modernproto.ReadPacketExact(buffers.reader, modernproto.StatusRequestID, nil); err != nil {
		return fmt.Errorf("reading status request: %w", err)
	}
	if err := writeStatus(conn, s.statusFor(sess), s.cfg.SoftwareSignature); err != nil {
		return err
	}

	_ = conn.SetReadDeadline(connDeadline)
	ping := buffers.payload[:modernproto.PingPayloadLength]
	err := modernproto.ReadPacketExact(buffers.reader, modernproto.PingRequestID, ping)
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return nil // Clients may close the connection, even abruptly, without pinging.
	}
	if err != nil {
		return fmt.Errorf("reading ping request: %w", err)
	}
	sess.sentPingRequest = true
	_, err = conn.Write(modernproto.AppendPacket(nil, modernproto.PongResponseID, ping))
	return err
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
