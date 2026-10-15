package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/script"
)

// unixPeer is logged in place of the address of Unix socket peers, which have
// none.
const unixPeer = "unix"

// connInfo holds the start time and addresses of a connection, for its log
// lines and the conditions of rules. Its log attributes are only made for the
// lines logged, which the rate limit may drop.
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

// maxLineAttrs is the room for the attributes of a line about a connection,
// kept on the stack so that making a line does not allocate them: the
// longest, a login behind a proxy, has 16.
const maxLineAttrs = 16

// appendAddrs appends the attributes holding the addresses of the connection.
func (c *connInfo) appendAddrs(attrs []slog.Attr) []slog.Attr {
	if !c.unknownClient {
		attrs = append(attrs, remoteAttr(c.remote))
	}
	if c.proxied {
		attrs = append(attrs, proxyAttr(c.peer))
	}
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
	for _, ruleErr := range sess.ruleErrors {
		s.logRuleError(c, ruleErr)
	}
	for _, hookErr := range sess.hookErrors {
		s.logHookError(c, hookErr)
	}
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
		line = appendRuleAttrs(line, sess)

	case sess.legacyLogin != nil:
		login := sess.legacyLogin
		msg = "legacy login attempt"
		line = append(line, slog.Int("protocol", int(login.ProtocolVersion)))
		if login.HasHost {
			line = appendHostAttrs(line, login.ServerAddress, "", login.ServerPort)
		}
		line = appendRuleAttrs(line, sess)
		line = append(line, optionalUntrusted("username", login.Username))

	default:
		h := &sess.handshake
		msg = eventMessage(h.Intent)
		line = append(line, slog.Int("protocol", int(h.ProtocolVersion)))
		line = appendHostAttrs(line, h.ServerAddress, h.ServerAddressExtra, h.ServerPort)
		line = appendRuleAttrs(line, sess)
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
	return append(attrs, logging.Untrusted("host", host), optionalUntrusted("host_extra", extra), slog.Int("port", int(port)))
}

// appendRuleAttrs appends the attributes telling which rule and virtual
// server answered sess, if any, where its status came from, for status
// requests, and which rules failed, whose rule failed lines may be suppressed.
func appendRuleAttrs(attrs []slog.Attr, sess *session) []slog.Attr {
	attrs = append(attrs, optionalString("rule", sess.rule), optionalString("server", sess.server), optionalString("source", string(sess.source)))
	if len(sess.ruleErrors) == 0 {
		return attrs
	}
	failed := make([]string, len(sess.ruleErrors))
	for i, ruleErr := range sess.ruleErrors {
		failed[i] = ruleErr.rule
	}
	return append(attrs, slog.String("failed_rules", strings.Join(failed, ","))) // Rule names have no commas.
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
	line = append(line, logging.Duration("duration", time.Since(c.start)), dropped)
	if err != nil {
		line = append(line, logging.Error(err))
	}
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, msg, line...)
}

// logRuleError logs a rule whose condition failed, which did not apply.
func (s *Server) logRuleError(c *connInfo, ruleErr ruleError) {
	s.logScriptError(c, scriptKey{name: ruleErr.rule}, ruleErr.err, slog.String("rule", ruleErr.rule))
}

// logHookError logs a hook that failed, which the server answered without.
func (s *Server) logHookError(c *connInfo, hookErr hookError) {
	s.logScriptError(c, scriptKey{hook: hookErr.hook, name: hookErr.server}, hookErr.err, slog.String("server", hookErr.server))
}

// logScriptError logs err, the error of the script key about the connection c,
// with where, the rule or the server of the script, and the values of the
// fields of the client that it read, in a client group, as written in scripts,
// so that their names never clash with the other attributes. Its lines are
// limited for each script (see scriptErrorLimiter).
func (s *Server) logScriptError(c *connInfo, key scriptKey, err error, where slog.Attr) {
	if !s.logger.Enabled(context.Background(), slog.LevelWarn) {
		return
	}
	now := time.Now()
	suppressed, ok := s.scriptErrorLimiter.allow(key, now)
	if !ok {
		return
	}
	dropped, ok := s.logLimiter.allow(now)
	if !ok {
		return
	}
	var fields []slog.Attr
	var scriptErr *script.Error
	if errors.As(err, &scriptErr) {
		for _, f := range scriptErr.Fields {
			fields = append(fields, clientFieldAttr(f))
		}
	}
	msg := "rule failed"
	if key.hook != "" {
		msg = strings.ReplaceAll(key.hook, "_", " ") + " failed" // Such as "ping script failed".
	}
	var buf [maxLineAttrs]slog.Attr
	line := append(c.appendAddrs(buf[:0]), where, slog.GroupAttrs("client", fields...), suppressed, dropped, logging.Error(err))
	s.logger.LogAttrs(context.Background(), slog.LevelWarn, msg, line...)
}

// trustedFields are the fields of the client of scripts that this server
// formats itself, which cannot hold characters to escape, logged as plain
// strings like on the other lines. The others, and the ones to come, are
// untrusted.
var trustedFields = map[string]bool{"request": true, "legacy_ping_version": true, "ip": true, "listener": true, "uuid": true}

// clientFieldAttr returns the attribute logging f as the other lines log the
// same value.
func clientFieldAttr(f script.Field) slog.Attr {
	switch value := f.Value.(type) {
	case string:
		if trustedFields[f.Name] {
			return slog.String(f.Name, value)
		}
		return logging.Untrusted(f.Name, value)
	case int:
		return slog.Int(f.Name, value)
	case bool:
		return slog.Bool(f.Name, value)
	}
	return slog.Any(f.Name, f.Value)
}

// scriptPrinter returns the function that logs what scripts print about the
// connection c, at the debug level, with where, the rule or hook that printed.
// It is nil, which drops prints, when debug lines are not logged.
func (s *Server) scriptPrinter(c *connInfo, where ...slog.Attr) func(msg string) {
	if !s.logger.Enabled(context.Background(), slog.LevelDebug) {
		return nil
	}
	// A copy, so that the arguments of callers stay on their stack when debug
	// lines are not logged.
	attrs := slices.Clone(where)
	return func(msg string) {
		dropped, ok := s.logLimiter.allow(time.Now())
		if !ok {
			return
		}
		var buf [maxLineAttrs]slog.Attr
		line := append(append(c.appendAddrs(buf[:0]), attrs...), logging.Untrusted("text", msg), dropped)
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "script print", line...)
	}
}

// logRejected logs a connection reset for exceeding limit, the setting of that
// limit, such as connections.max.
func (s *Server) logRejected(limit string, c *connInfo) {
	if !s.logger.Enabled(context.Background(), slog.LevelWarn) {
		return
	}
	dropped, ok := s.logLimiter.allow(time.Now())
	if !ok {
		return
	}
	var buf [maxLineAttrs]slog.Attr
	line := append(c.appendAddrs(buf[:0]), slog.String("limit", limit), dropped)
	s.logger.LogAttrs(context.Background(), slog.LevelWarn, "connection rejected", line...)
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

// scriptErrorInterval is how often each script that keeps failing is logged.
const scriptErrorInterval = time.Minute

// scriptErrorLimiter bounds the failure lines of each rule, and of each hook
// of each server, to one per scriptErrorInterval, so that a script failing for
// many clients does not repeat the same line for each, nor take the lines of
// the others. Suppressed lines are counted and reported by the next line of
// the same script. Its zero value is ready to use.
type scriptErrorLimiter struct {
	mu      sync.Mutex
	scripts map[scriptKey]*scriptErrors // As many as the rules and hooks.
}

// scriptKey identifies a script for scriptErrorLimiter: the conditions of the
// rule name, or the hook of the server name.
type scriptKey struct {
	hook string // "" for the conditions of a rule.
	name string
}

type scriptErrors struct {
	logged     time.Time
	suppressed int
}

// allow reports whether a failure of the script key can be logged now and, if
// so, an attribute counting the ones of that script suppressed before it
// (empty if none, so not logged). The count restarts even if logLimiter then
// drops the line, which dropped= reports.
func (l *scriptErrorLimiter) allow(key scriptKey, now time.Time) (slog.Attr, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	errs := l.scripts[key]
	if errs == nil {
		if l.scripts == nil {
			l.scripts = make(map[scriptKey]*scriptErrors)
		}
		errs = &scriptErrors{}
		l.scripts[key] = errs
	} else if now.Sub(errs.logged) < scriptErrorInterval {
		errs.suppressed++
		return slog.Attr{}, false
	}

	var suppressed slog.Attr
	if errs.suppressed > 0 {
		suppressed = slog.Int("suppressed", errs.suppressed)
	}
	errs.logged, errs.suppressed = now, 0
	return suppressed, true
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
