package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

// maxLoggedBytes bounds the number of bytes received from invalid connections
// that are logged, to help identify them.
const maxLoggedBytes = 64

// loggedDurationPrecision is the precision of logged durations: finer digits
// would only be noise.
const loggedDurationPrecision = 100 * time.Microsecond

// unixPeer is logged in place of the address of Unix socket peers, which have
// none.
const unixPeer = "unix"

// connInfo holds the start time and addresses of a connection, for its log
// lines. Its log attributes are only made for the lines logged, which the rate
// limit may drop.
type connInfo struct {
	start time.Time
	// remote is the address of the client, invalid for Unix socket peers.
	remote   netip.AddrPort
	listener listenerInfo
}

// maxLineAttrs is the room for the attributes of a line about a connection,
// kept on the stack so that making a line does not allocate them: the
// longest, a login, has 11.
const maxLineAttrs = 11

// appendAddrs appends the attributes holding the addresses of the connection.
func (c *connInfo) appendAddrs(attrs []slog.Attr) []slog.Attr {
	attrs = append(attrs, remoteAttr(c.remote))
	if c.listener.logged {
		attrs = append(attrs, slog.String("listener", c.listener.addr))
	}
	return attrs
}

// remoteAttr returns the attribute logging the client at addr, or unixPeer for
// Unix socket peers.
func remoteAttr(addr netip.AddrPort) slog.Attr {
	if isUnixPeer(addr) {
		return slog.String(logging.RemoteKey, unixPeer)
	}
	return slog.Any(logging.RemoteKey, addr)
}

// logSession logs a connection that sent a valid handshake, legacy ping or
// legacy login.
func (s *Server) logSession(sess *session, err error) {
	c := &sess.conn
	dropped, ok := s.allowConnection()
	if !ok {
		return
	}
	var buf [maxLineAttrs]slog.Attr
	line := c.appendAddrs(buf[:0])
	var msg string
	switch {
	case sess.legacyPing != nil:
		ping := sess.legacyPing
		msg = "legacy server list ping"
		line = append(line, slog.String("legacy_ping_version", ping.Version.String()), slog.Int("protocol", int(ping.ProtocolVersion)))
		if ping.HasHost {
			line = appendHostAttrs(line, ping.ServerAddress, "", ping.ServerPort)
		}

	case sess.legacyLogin != nil:
		login := sess.legacyLogin
		msg = "legacy login attempt"
		line = append(line, slog.Int("protocol", int(login.ProtocolVersion)))
		if login.HasHost {
			line = appendHostAttrs(line, login.ServerAddress, "", login.ServerPort)
		}
		line = append(line, optionalUntrusted("username", login.Username))

	default:
		h := &sess.handshake
		msg = eventMessage(h.Intent)
		line = append(line, slog.Int("protocol", int(h.ProtocolVersion)))
		line = appendHostAttrs(line, h.ServerAddress, h.ServerAddressExtra, h.ServerPort)
		if h.Intent == modernproto.IntentStatus {
			line = append(line, slog.Bool("ping_request", sess.sentPingRequest))
		} else {
			line = append(line, optionalUntrusted("username", sess.loginStart.Username), optionalString("uuid", sess.loginStart.UUID))
		}
	}
	s.logConnection(msg, c, line, dropped, err)
}

// appendHostAttrs appends the attributes of the server address and port that
// the client sent (see modernproto.Handshake).
func appendHostAttrs(attrs []slog.Attr, host, extra string, port uint16) []slog.Attr {
	return append(attrs, logging.Untrusted("server_address", host), optionalUntrusted("server_address_extra", extra), slog.Int("server_port", int(port)))
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
	var buf [maxLineAttrs]slog.Attr
	line := append(c.appendAddrs(buf[:0]), logging.Untrusted("data", string(head.bytes())))
	s.logConnection(msg, c, line, dropped, err)
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

// logConnection logs a closed connection that allowConnection allowed: line,
// which starts with its addresses (see appendAddrs), then how long it lasted,
// dropped and how it ended.
func (s *Server) logConnection(msg string, c *connInfo, line []slog.Attr, dropped slog.Attr, err error) {
	line = append(line, slog.Duration("duration", roundDuration(time.Since(c.start))), dropped)
	if err != nil {
		line = append(line, logging.Error(err))
	}
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, msg, line...)
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

func roundDuration(d time.Duration) time.Duration {
	return d.Round(loggedDurationPrecision)
}

// headRecorder reads from r and keeps a copy of the first bytes read.
type headRecorder struct {
	r   io.Reader
	buf [maxLoggedBytes]byte
	n   int
}

func (h *headRecorder) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.n += copy(h.buf[h.n:], p[:n])
	return n, err
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
