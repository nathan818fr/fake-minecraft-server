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
	"maps"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/legacyproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/proxyproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/script"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
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
	// Rules tell which virtual server answers each client: the one of the
	// first rule that applies. The last one must always apply, without
	// conditions.
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
	// ConnectionRateLimitPerIP bounds the number of new connections per second
	// per client, counted like MaxConnectionsPerIP, in bursts of up to twice as
	// many (see rateLimiter). 0 means unlimited.
	ConnectionRateLimitPerIP int

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
	// State returns what is known of the server now. Its status is valid (see
	// status.Status.Check), but checked against the length limit only when
	// answered, as it changes.
	State() upstream.State
}

type Server struct {
	logger       *slog.Logger
	makeUpstream upstreamMaker
	// state is the configuration that new connections take (see state).
	state atomic.Pointer[state]

	connLimiter        connLimiter
	clientLimiter      clientLimiter
	rateLimiter        rateLimiter
	logLimiter         logLimiter
	scriptErrorLimiter scriptErrorLimiter

	// reloads takes the reloads to Serve, which applies them, and stopped is
	// closed once it no longer does.
	reloads chan reload
	stopped chan struct{}
}

// upstreamMaker makes the upstream with the ID id, polled with cfg. previous
// is the upstream that had this ID until a reload changed its configuration,
// if any, stopped: the new one starts from what it knew, so that changing the
// address of an upstream does not make it unavailable.
type upstreamMaker func(id string, cfg upstream.Config, previous Upstream) Upstream

// state is a configuration with its upstreams. Each connection takes the
// current one when it starts, and keeps it until it ends, so that a reload
// never answers it with parts of two configurations.
type state struct {
	cfg       Config
	upstreams map[string]Upstream
	// upstreamIndexes number the upstreams, for the states that requests read
	// (see upstreamState).
	upstreamIndexes map[string]int
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	return newServer(cfg, func(id string, cfg upstream.Config, previous Upstream) Upstream {
		p := upstream.NewPoller(id, cfg, logger)
		if previous, ok := previous.(*upstream.Poller); ok {
			p.Inherit(previous)
		}
		return p
	}, logger)
}

// newServer is New with makeUpstream making the upstreams, such as fakes in
// tests.
func newServer(cfg Config, makeUpstream upstreamMaker, logger *slog.Logger) (*Server, error) {
	if err := checkConfig(&cfg); err != nil {
		return nil, err
	}
	s := &Server{
		logger:       logger,
		makeUpstream: makeUpstream,
		reloads:      make(chan reload),
		stopped:      make(chan struct{}),
	}
	s.state.Store(s.newState(cfg, nil))
	s.setLimits(cfg)
	return s, nil
}

func checkConfig(cfg *Config) error {
	if cfg.ConnectionTimeout <= 0 || cfg.HandshakeTimeout <= 0 {
		return errors.New("connection and handshake timeouts must be positive")
	}
	return checkServers(cfg)
}

// newState returns the state of cfg, with the upstreams of previous that it
// keeps (see state.keeps), and new ones for the others.
func (s *Server) newState(cfg Config, previous *state) *state {
	st := &state{
		cfg:             cfg,
		upstreams:       make(map[string]Upstream, len(cfg.Upstreams)),
		upstreamIndexes: make(map[string]int, len(cfg.Upstreams)),
	}
	for i, id := range slices.Sorted(maps.Keys(cfg.Upstreams)) {
		st.upstreamIndexes[id] = i
		var old Upstream
		if previous != nil {
			old = previous.upstreams[id]
			if previous.keeps(cfg, id) {
				st.upstreams[id] = old
				continue
			}
		}
		st.upstreams[id] = s.makeUpstream(id, cfg.Upstreams[id], old)
	}
	return st
}

// keeps reports whether the state of cfg keeps the upstream id of st, which
// it does if its configuration is unchanged.
func (st *state) keeps(cfg Config, id string) bool {
	upstreamCfg, ok := cfg.Upstreams[id]
	return ok && upstreamCfg == st.cfg.Upstreams[id]
}

// setLimits sets the limits of cfg, which apply to the connections already
// open too.
func (s *Server) setLimits(cfg Config) {
	s.connLimiter.setLimit(cfg.MaxConnections)
	s.clientLimiter.setLimit(cfg.MaxConnectionsPerIP)
	s.rateLimiter.setRate(cfg.ConnectionRateLimitPerIP)
	s.logLimiter.setLimit(cfg.LogRateLimit)
}

// serving is what Serve runs, which reloads change: the accept loops of the
// listeners, and the polling of the upstreams, by ID.
type serving struct {
	ctx context.Context
	// failed receives the error of the first listener that fails.
	failed chan error
	conns  sync.WaitGroup
	loops  map[net.Listener]*acceptLoop
	polls  map[string]task
}

// task is a goroutine of Serve, which can be stopped alone.
type task struct {
	stop context.CancelFunc
	done chan struct{}
}

func (r *serving) start(run func(ctx context.Context)) task {
	ctx, stop := context.WithCancel(r.ctx)
	t := task{stop: stop, done: make(chan struct{})}
	go func() {
		defer close(t.done)
		run(ctx)
	}()
	return t
}

// end stops t and waits for it to return.
func (t task) end() {
	t.stop()
	<-t.done
}

// acceptLoop accepts the connections of a listener (see Server.accept).
type acceptLoop struct {
	task
	// info is what its connections know of the listener, which reloads change.
	info atomic.Pointer[listenerInfo]
}

// reload is a call to Reload, which Serve applies, then closes done.
type reload struct {
	cfg       Config
	listeners []Listener
	done      chan struct{}
}

// Serve accepts connections on the listeners until ctx is canceled or one of
// them fails, then closes them all and waits for the remaining connections to
// finish. The limits apply to all the listeners together. The upstreams are
// polled meanwhile. Reload changes the configuration and the listeners while
// it serves. A server serves once.
func (s *Server) Serve(ctx context.Context, listeners ...Listener) error {
	if len(listeners) == 0 {
		return errors.New("no listener to serve")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r := &serving{
		ctx:    ctx,
		failed: make(chan error, 1),
		loops:  make(map[net.Listener]*acceptLoop, len(listeners)),
		polls:  make(map[string]task),
	}
	s.poll(r)
	s.setListeners(r, listeners)

	var err error
serve:
	for {
		select {
		case <-ctx.Done():
			break serve
		case err = <-r.failed:
			break serve
		case req := <-s.reloads:
			s.apply(r, req.cfg, req.listeners)
			close(req.done)
		}
	}
	close(s.stopped)

	cancel() // Stops the accept loops and the upstreams.
	for _, loop := range r.loops {
		<-loop.done
	}
	if err == nil {
		s.logger.Info("stopping")
	}
	r.conns.Wait()
	for _, t := range r.polls {
		<-t.done
	}
	if err == nil {
		s.logger.Info("stopped")
	}
	return err
}

// Reload makes Serve use cfg and listeners from now on, or returns an error,
// and leaves both as they were, if cfg is invalid. Connections that already
// started end with their configuration, but count against the new limits.
// The upstreams whose configuration is unchanged keep polling, and the others
// start over, from what the upstream they replace knew. Serve starts
// accepting on the new listeners, keeps the others open with their new
// options, and closes the ones not in listeners, once their accept loop
// ends. Reload waits for Serve to start.
func (s *Server) Reload(cfg Config, listeners ...Listener) error {
	if len(listeners) == 0 {
		return errors.New("no listener to serve")
	}
	if err := checkConfig(&cfg); err != nil {
		return err
	}
	req := reload{cfg: cfg, listeners: listeners, done: make(chan struct{})}
	select {
	case s.reloads <- req:
		<-req.done
		return nil
	case <-s.stopped:
		return errors.New("server stopped")
	}
}

// apply applies a reload to what Serve runs: the upstreams that the new state
// does not keep stop before it replaces them, so that their replacements start
// from all they knew.
func (s *Server) apply(r *serving, cfg Config, listeners []Listener) {
	previous := s.state.Load()
	for id, t := range r.polls {
		if !previous.keeps(cfg, id) {
			t.end()
			delete(r.polls, id)
		}
	}
	s.state.Store(s.newState(cfg, previous))
	s.setLimits(cfg)
	s.poll(r)
	s.setListeners(r, listeners)
}

// poll starts polling the upstreams of the current state that are not polled
// yet.
func (s *Server) poll(r *serving) {
	for id, u := range s.state.Load().upstreams {
		if _, ok := r.polls[id]; !ok {
			r.polls[id] = r.start(u.Run)
		}
	}
}

// setListeners makes Serve accept connections on listeners: it starts on the
// new ones, gives the others their options, and closes the ones not in
// listeners.
func (s *Server) setListeners(r *serving, listeners []Listener) {
	listed := make(map[net.Listener]bool, len(listeners))
	for _, ln := range listeners {
		listed[ln.Listener] = true
		info := &listenerInfo{addr: listenerAddr(ln), logged: len(listeners) > 1, ListenerOptions: ln.Options}
		if loop, ok := r.loops[ln.Listener]; ok {
			loop.info.Store(info)
			continue
		}

		var proxyProtocol slog.Attr
		if ln.Options.ProxyProtocol {
			proxyProtocol = slog.Bool("proxy_protocol", true)
		}
		s.logger.LogAttrs(r.ctx, slog.LevelInfo, "listening", slog.String("listener", info.addr), proxyProtocol)
		loop := &acceptLoop{}
		loop.info.Store(info)
		loop.task = r.start(func(ctx context.Context) {
			if err := s.accept(ctx, ln, loop, &r.conns); err != nil {
				select {
				case r.failed <- err:
				default: // Another listener failed first.
				}
			}
		})
		r.loops[ln.Listener] = loop
	}

	for ln, loop := range r.loops {
		if !listed[ln] {
			loop.end()
			delete(r.loops, ln)
			s.logger.Info("stopped listening", "listener", loop.info.Load().addr)
		}
	}
}

// accept accepts connections on ln until ctx is canceled, then closes ln. It
// returns the error of ln if it fails before.
func (s *Server) accept(ctx context.Context, ln Listener, loop *acceptLoop, conns *sync.WaitGroup) error {
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
			s.logger.Error("accept failed", "listener", loop.info.Load().addr, logging.Error(err))
			time.Sleep(acceptRetryDelay)
			continue
		}

		listener := *loop.info.Load()
		if !s.connLimiter.tryAcquire() {
			abort(conn)
			c := newConnInfo(conn, listener)
			s.logRejected("connections.max", &c)
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
	conn  connInfo
	state *state

	handshake modernproto.Handshake
	// rule is the name of the rule that answered, if any, server the ID of its
	// virtual server, and source where its answer came from: for status
	// requests, and for joins answered by a hook.
	rule   string
	server string
	source answerSource
	// sentPingRequest reports whether the client sent a ping request after the
	// status request, to measure its latency.
	sentPingRequest bool
	loginStart      modernproto.LoginStart
	// legacyPing and legacyLogin hold what clients older than 1.7 sent, if any.
	legacyPing  *legacyproto.Ping
	legacyLogin *legacyproto.Login

	// request is what scripts know about the request, made when first needed
	// (see scriptRequest).
	request script.Request
	// upstreamStates are the states of the upstreams that the request read,
	// by index (see upstreamState), so that its scripts and its answer see
	// one state of each.
	upstreamStates []upstreamRead
	// ruleErrors are the errors of the conditions that failed, and hookErrors
	// the ones of the hooks.
	ruleErrors []ruleError
	hookErrors []hookError
}

type upstreamRead struct {
	read  bool
	state upstream.State
}

// upstreamState returns the state of the upstream with the ID id, read once
// per request, or an unavailable state if there is no such upstream.
func (s *Server) upstreamState(sess *session, id string) upstream.State {
	i, ok := sess.state.upstreamIndexes[id]
	if !ok {
		return upstream.State{}
	}
	if sess.upstreamStates == nil {
		sess.upstreamStates = make([]upstreamRead, len(sess.state.upstreamIndexes))
	}
	r := &sess.upstreamStates[i]
	if !r.read {
		r.state, r.read = sess.state.upstreams[id].State(), true
	}
	return r.state
}

// scriptRequest returns what scripts know about the request of sess, made
// when first needed.
func (s *Server) scriptRequest(sess *session) *script.Request {
	if sess.request.Client == nil {
		sess.request.Client = sess.scriptClient()
		sess.request.Upstream = func(id string) upstream.State { return s.upstreamState(sess, id) }
	}
	return &sess.request
}

func (sess *session) scriptClient() *script.Client {
	var c script.Client
	switch {
	case sess.legacyPing != nil:
		ping := sess.legacyPing
		c = script.Client{
			Request:           script.RequestPing,
			Host:              ping.ServerAddress,
			Port:              int(ping.ServerPort),
			Protocol:          int(ping.ProtocolVersion),
			Legacy:            true,
			LegacyPingVersion: ping.Version.String(),
		}
	case sess.legacyLogin != nil:
		login := sess.legacyLogin
		c = script.Client{
			Request:  script.RequestJoin,
			Host:     login.ServerAddress,
			Port:     int(login.ServerPort),
			Protocol: int(login.ProtocolVersion),
			Legacy:   true,
			Username: login.Username,
		}
	default:
		h := sess.handshake
		request := script.RequestJoin
		if h.Intent == modernproto.IntentStatus {
			request = script.RequestPing
		}
		c = script.Client{
			Request:     request,
			Transferred: h.Intent == modernproto.IntentTransfer,
			Host:        h.ServerAddress,
			HostExtra:   h.ServerAddressExtra,
			Port:        int(h.ServerPort),
			Protocol:    int(h.ProtocolVersion),
			Username:    sess.loginStart.Username,
			UUID:        sess.loginStart.UUID,
		}
	}
	c.Listener = sess.conn.listener.addr
	if remote := sess.conn.remote; !isUnixPeer(remote) {
		c.IP = remote.Addr().WithZone("").String()
	}
	return &c
}

// readWhole reports whether the server read the whole request of the client
// before answering it, which clients then send nothing after: status
// exchanges, and 1.6 legacy pings. The pings of older clients may be the
// start of a 1.6 one, whose end can come later, and Login Start packets are
// not always read whole.
func (sess *session) readWhole() bool {
	if sess.legacyPing != nil {
		return sess.legacyPing.Version == legacyproto.Ping16
	}
	return sess.handshake.Intent == modernproto.IntentStatus
}

// identified reports whether the client sent a valid handshake, legacy ping or
// legacy login, which tells it is a Minecraft client.
func (sess *session) identified() bool {
	return sess.handshake.Intent != 0 || sess.legacyPing != nil || sess.legacyLogin != nil
}

func (s *Server) handle(conn net.Conn, listener listenerInfo) {
	buffers := connBuffersPool.Get().(*connBuffers)
	buffers.head = headRecorder{r: conn}
	buffers.reader.Reset(&buffers.head)
	// Set in place, as a session literal would be made on the stack first.
	sess := &buffers.session
	sess.conn, sess.state = newConnInfo(conn, listener), s.state.Load()
	defer func() {
		// The pool must not keep the connection, nor what the client sent,
		// alive.
		buffers.head.r = nil
		*sess = session{}
		connBuffersPool.Put(buffers)
	}()

	c, cfg := &sess.conn, &sess.state.cfg
	connDeadline := c.start.Add(cfg.ConnectionTimeout)
	handshakeDeadline := earliest(c.start.Add(cfg.HandshakeTimeout), connDeadline)
	_ = conn.SetDeadline(connDeadline)
	_ = conn.SetReadDeadline(handshakeDeadline)

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
	if !s.rateLimiter.allow(client, c.start) {
		abort(conn)
		s.logRejected("connections.rate_limit_per_ip", c)
		return
	}
	if !s.clientLimiter.acquire(client) {
		abort(conn)
		s.logRejected("connections.max_per_ip", c)
		return
	}
	defer s.clientLimiter.release(client)

	err := s.exchange(conn, buffers, sess, handshakeDeadline, connDeadline)
	switch {
	case err != nil:
		abort(conn)
	case sess.readWhole():
		closeAnswered(conn, connDeadline)
	default:
		closeGracefully(conn, connDeadline)
	}

	if !sess.identified() {
		s.logInvalid(c, buffers.head, err)
		return
	}
	s.logSession(sess, err)
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
		// Clients send a Login Start packet right after the handshake, which
		// scripts see. An invalid or missing one leaves the username and UUID
		// empty, and the player is still answered, once the handshake deadline
		// is over.
		sess.loginStart, _ = modernproto.ReadLoginStart(buffers.reader, sess.handshake.ProtocolVersion)
		kick := modernproto.AppendString(nil, string(s.kickFor(sess)))
		_, err := conn.Write(modernproto.AppendPacket(nil, modernproto.LoginDisconnectID, kick))
		return err
	}
}

// answerStatus answers the status request of the client, then its ping
// request if it sends one.
func (s *Server) answerStatus(conn net.Conn, buffers *connBuffers, sess *session, connDeadline time.Time) error {
	if err := modernproto.ReadPacketExact(buffers.reader, modernproto.StatusRequestID, nil); err != nil {
		return fmt.Errorf("reading status request: %w", err)
	}
	if err := writeStatus(conn, s.statusFor(sess), sess.state.cfg.SoftwareSignature); err != nil {
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
