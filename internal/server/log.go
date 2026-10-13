package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
)

// unixPeer is logged in place of the address of Unix socket peers, which have
// none.
const unixPeer = "unix"

// connInfo holds the start time and addresses of a connection, for its log
// lines. Its log attributes are only made for the lines logged, which the rate
// limit may drop.
type connInfo struct {
	start time.Time
	// peer is the address of the peer, invalid for Unix socket peers, and
	// remote the address of the client: the one of the peer, or if proxied,
	// the one that the peer, a trusted proxy, sends in a PROXY protocol
	// header. The client is unknown until the header is read.
	peer          netip.AddrPort
	remote        netip.AddrPort
	proxied       bool
	unknownClient bool
	listener      listenerInfo
}

// newConnInfo returns what is known about conn when listener accepts it.
func newConnInfo(conn net.Conn, listener listenerInfo) connInfo {
	peer := peerAddr(conn)
	c := connInfo{start: time.Now(), peer: peer, remote: peer, listener: listener}
	if listener.ProxyProtocol && listener.isTrustedProxy(peer) {
		c.proxied, c.unknownClient = true, true
	}
	return c
}

// addrs returns the attributes holding the addresses of the connection.
// Empty attributes are not logged.
func (c *connInfo) addrs() []slog.Attr {
	var remote, proxy, listener slog.Attr
	if !c.unknownClient {
		remote = remoteAttr(c.remote)
	}
	if c.proxied {
		proxy = proxyAttr(c.peer)
	}
	if c.listener.logged {
		listener = slog.String("listener", c.listener.addr)
	}
	return []slog.Attr{remote, proxy, listener}
}

// remoteAttr returns the attribute logging the client at addr, or unixPeer for
// Unix socket peers.
func remoteAttr(addr netip.AddrPort) slog.Attr {
	if isUnixPeer(addr) {
		return slog.String(logging.RemoteKey, unixPeer)
	}
	return slog.Any(logging.RemoteKey, addr)
}

// proxyAttr returns the attribute logging the proxy at peer, or unixPeer for
// Unix socket peers.
func proxyAttr(peer netip.AddrPort) slog.Attr {
	if isUnixPeer(peer) {
		return slog.String("proxy", unixPeer)
	}
	return slog.Any("proxy", peer.Addr())
}

// logSession logs a connection that sent a valid handshake, legacy ping or
// legacy login.
func (s *Server) logSession(sess *session, err error) {
	c := &sess.conn
	dropped, ok := s.allowConnection()
	if !ok {
		return
	}
	switch {
	case sess.legacyPing != nil:
		ping := sess.legacyPing
		attrs := []slog.Attr{
			slog.String("legacy_ping_version", ping.Version.String()),
			slog.Int("protocol", int(ping.ProtocolVersion)),
		}
		if ping.HasHost {
			attrs = append(attrs, hostAttrs(ping.ServerAddress, "", ping.ServerPort)...)
		}
		attrs = append(attrs, ruleAttrs(sess)...)
		s.logConnection("legacy server list ping", c, attrs, dropped, err)

	case sess.legacyLogin != nil:
		login := sess.legacyLogin
		attrs := []slog.Attr{slog.Int("protocol", int(login.ProtocolVersion))}
		if login.HasHost {
			attrs = append(attrs, hostAttrs(login.ServerAddress, "", login.ServerPort)...)
		}
		attrs = append(attrs, ruleAttrs(sess)...)
		attrs = append(attrs, optionalUntrusted("username", login.Username))
		s.logConnection("legacy login attempt", c, attrs, dropped, err)

	default:
		attrs := []slog.Attr{slog.Int("protocol", int(sess.handshake.ProtocolVersion))}
		attrs = append(attrs, hostAttrs(sess.handshake.ServerAddress, sess.handshake.ServerAddressExtra, sess.handshake.ServerPort)...)
		attrs = append(attrs, ruleAttrs(sess)...)
		if sess.handshake.Intent == modernproto.IntentStatus {
			attrs = append(attrs, slog.Bool("ping_request", sess.sentPingRequest))
		} else {
			attrs = append(attrs, optionalUntrusted("username", sess.loginStart.Username), optionalString("uuid", sess.loginStart.UUID))
		}
		s.logConnection(eventMessage(sess.handshake.Intent), c, attrs, dropped, err)
	}
}

// hostAttrs returns the attributes of the server address and port that the
// client sent (see modernproto.Handshake).
func hostAttrs(host, extra string, port uint16) []slog.Attr {
	return []slog.Attr{
		logging.Untrusted("host", host),
		optionalUntrusted("host_extra", extra),
		slog.Int("port", int(port)),
	}
}

// ruleAttrs returns the attributes telling which rule and virtual server
// answered sess, if any, and where its status came from, for status requests.
func ruleAttrs(sess *session) []slog.Attr {
	return []slog.Attr{
		optionalString("rule", sess.rule),
		optionalString("server", sess.server),
		optionalString("source", string(sess.statusSource)),
	}
}

// eventMessage returns the log message for a handshake with the given intent,
// worded for Minecraft server operators.
func eventMessage(intent modernproto.Intent) string {
	switch intent {
	case modernproto.IntentStatus:
		return "server list ping"
	case modernproto.IntentTransfer:
		return "transfer attempt"
	default:
		return "login attempt"
	}
}

// logInvalid logs a connection that did not send a valid handshake, with the
// first bytes it sent to help identify it.
func (s *Server) logInvalid(c *connInfo, head headRecorder, err error) {
	dropped, ok := s.allowConnection()
	if !ok {
		return
	}
	msg := "invalid connection"
	if errors.Is(err, io.EOF) {
		msg = "connection closed before handshake"
	}
	s.logConnection(msg, c, []slog.Attr{logging.Untrusted("data", string(head.bytes()))}, dropped, err)
}

// allowConnection reports whether the line of a closed connection can be
// logged now and, if so, the attribute counting the lines dropped before it
// (see logLimiter). Callers make the attributes of the line only then.
func (s *Server) allowConnection() (slog.Attr, bool) {
	if !s.logger.Enabled(context.Background(), slog.LevelInfo) {
		return slog.Attr{}, false
	}
	return s.logLimiter.allow(time.Now())
}

// logConnection logs a closed connection that allowConnection allowed: its
// addresses, then attrs, then how long it lasted, dropped and how it ended.
func (s *Server) logConnection(msg string, c *connInfo, attrs []slog.Attr, dropped slog.Attr, err error) {
	line := append(c.addrs(), attrs...)
	line = append(line, logging.Duration("duration", time.Since(c.start)), dropped)
	if err != nil {
		line = append(line, logging.Error(err))
	}
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, msg, line...)
}

func (s *Server) logRejected(msg string, c *connInfo) {
	if !s.logger.Enabled(context.Background(), slog.LevelWarn) {
		return
	}
	dropped, ok := s.logLimiter.allow(time.Now())
	if !ok {
		return
	}
	s.logger.LogAttrs(context.Background(), slog.LevelWarn, msg, append(c.addrs(), dropped)...)
}

// optionalString returns an empty attribute, which is not logged, for an empty
// value.
func optionalString(key, value string) slog.Attr {
	if value == "" {
		return slog.Attr{}
	}
	return slog.String(key, value)
}

// optionalUntrusted is like logging.Untrusted, but returns an empty attribute,
// which is not logged, for an empty value.
func optionalUntrusted(key, value string) slog.Attr {
	if value == "" {
		return slog.Attr{}
	}
	return logging.Untrusted(key, value)
}

// headRecorder reads from r and keeps a copy of the first bytes read.
type headRecorder struct {
	r io.Reader
	// buf holds as many bytes as logs show of untrusted values, to help
	// identify invalid connections.
	buf [logging.MaxUntrustedLength]byte
	n   int
}

func (h *headRecorder) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.n += copy(h.buf[h.n:], p[:n])
	return n, err
}

// restart makes the recording start at the next byte that r returns.
func (h *headRecorder) restart(r *bufio.Reader) {
	buffered, _ := r.Peek(r.Buffered())
	h.n = copy(h.buf[:], buffered)
}

func (h *headRecorder) bytes() []byte {
	return h.buf[:h.n]
}

// logLimiter bounds how many lines are logged per second, so that floods
// cannot overwhelm the logs. Dropped lines are counted and reported by the
// next line logged. A limit of 0 disables it. Callers check the level first,
// so that the lines it hides take no room.
type logLimiter struct {
	perSecond int

	mu      sync.Mutex
	second  int64
	count   int
	dropped int
}

// allow reports whether a line can be logged now and, if so, an attribute
// counting the lines dropped before it (empty if none, so not logged).
func (l *logLimiter) allow(now time.Time) (slog.Attr, bool) {
	if l.perSecond <= 0 {
		return slog.Attr{}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if second := now.Unix(); second != l.second {
		l.second = second
		l.count = 0
	}
	if l.count >= l.perSecond {
		l.dropped++
		return slog.Attr{}, false
	}
	l.count++

	if l.dropped == 0 {
		return slog.Attr{}, true
	}
	dropped := slog.Int("dropped", l.dropped)
	l.dropped = 0
	return dropped, true
}
