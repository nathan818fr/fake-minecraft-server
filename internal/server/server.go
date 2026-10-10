// Package server answers Minecraft server list pings with a configurable
// status and disconnects every player trying to join.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/legacyproto"
	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/modernproto"
)

// acceptRetryDelay paces retries after accept errors, such as running out of
// file descriptors, which would otherwise make the server spin.
const acceptRetryDelay = 100 * time.Millisecond

// legacyPingWait bounds the wait for the end of a legacy ping, which can
// arrive a round trip after its start. Clients whose ping is only such a start
// (Beta 1.8 to 1.5) get their response that much later.
const legacyPingWait = 500 * time.Millisecond

// maxProtocolVersionTextLength is the length of the longest protocol version
// written in the status.
const maxProtocolVersionTextLength = len("-2147483648")

type Config struct {
	// KickMessage and MOTD are JSON text components.
	KickMessage json.RawMessage
	MOTD        json.RawMessage

	// Favicon is a data URI of a 64x64 PNG image, or empty for none.
	Favicon string

	MaxPlayers    int32
	OnlinePlayers int32

	VersionName     string
	ProtocolVersion int32
	// MirrorProtocolVersion reports the client's own protocol version instead
	// of ProtocolVersion, so that the server never appears as incompatible.
	MirrorProtocolVersion bool

	// ConnectionTimeout bounds the whole lifetime of a connection. It must be
	// positive.
	ConnectionTimeout time.Duration
	// HandshakeTimeout bounds the wait for the handshake (or legacy ping or
	// login) of a client, with the request sent along with it. Clients send all
	// of it as soon as they connect, so a limit shorter than ConnectionTimeout
	// frees the connections that send nothing or too little sooner, which makes
	// the server harder to flood. It must be positive.
	HandshakeTimeout time.Duration

	// LogRateLimit bounds the number of lines logged per second about
	// connections. 0 means unlimited.
	LogRateLimit int
}

type Server struct {
	cfg    Config
	logger *slog.Logger

	logLimiter logLimiter

	// Encoded once, as they are the same for every client.
	kickPacket       []byte
	legacyKickPacket []byte
	legacyMOTD       string
	// statusHead and statusTail surround the protocol version in the status
	// JSON, which is the only part that may change between clients.
	statusHead []byte
	statusTail []byte
}

func New(cfg Config, logger *slog.Logger) (*Server, error) {
	if cfg.ConnectionTimeout <= 0 || cfg.HandshakeTimeout <= 0 {
		return nil, errors.New("connection and handshake timeouts must be positive")
	}
	if n := modernproto.UTF16Length(cfg.KickMessage); n > modernproto.MaxDisconnectReasonLength {
		return nil, fmt.Errorf("kick message is too long: %d characters, the limit is %d", n, modernproto.MaxDisconnectReasonLength)
	}
	statusHead, statusTail, err := encodeStatus(cfg)
	if err != nil {
		return nil, err
	}
	n := modernproto.UTF16Length(statusHead) + maxProtocolVersionTextLength + modernproto.UTF16Length(statusTail)
	if n > modernproto.MaxStatusResponseLength {
		return nil, fmt.Errorf("status is too long: %d characters, the limit is %d (is the favicon too large?)", n, modernproto.MaxStatusResponseLength)
	}

	return &Server{
		cfg:    cfg,
		logger: logger,

		logLimiter: logLimiter{perSecond: cfg.LogRateLimit},

		kickPacket:       modernproto.AppendPacket(nil, modernproto.LoginDisconnectID, modernproto.AppendString(nil, string(cfg.KickMessage))),
		legacyKickPacket: legacyproto.AppendKick(nil, legacyproto.TextFromComponent(cfg.KickMessage)),
		legacyMOTD:       legacyproto.MOTDFromComponent(cfg.MOTD),
		statusHead:       statusHead,
		statusTail:       statusTail,
	}, nil
}

// Serve accepts connections on the listeners until ctx is canceled or one of
// them fails, then closes them all and waits for the remaining connections to
// finish.
func (s *Server) Serve(ctx context.Context, listeners ...net.Listener) error {
	if len(listeners) == 0 {
		return errors.New("no listener to serve")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var conns sync.WaitGroup

	errs := make(chan error, len(listeners))
	for _, ln := range listeners {
		s.logger.Info("listening", "listener", listenerAddr(ln))
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
	if err == nil {
		s.logger.Info("stopped")
	}
	return err
}

// accept accepts connections on ln until ctx is canceled, then closes ln. It
// returns the error of ln if it fails before. Logs show the address of ln if
// logListener.
func (s *Server) accept(ctx context.Context, ln net.Listener, logListener bool, conns *sync.WaitGroup) error {
	listener := listenerInfo{addr: listenerAddr(ln), logged: logListener}
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

		conns.Go(func() { s.handle(conn, listener) })
	}
}

// listenerInfo is what connections know about the listener that accepted
// them: its address (see listenerAddr), and whether logs show it, which they do
// with several listeners.
type listenerInfo struct {
	addr   string
	logged bool
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
	// sentPingRequest reports whether the client sent a ping request after the
	// status request, to measure its latency.
	sentPingRequest bool
	// loginStart holds the username and UUID sent by clients trying to join.
	loginStart modernproto.LoginStart
	// legacyPing and legacyLogin hold what clients older than 1.7 sent, if any.
	legacyPing  *legacyproto.Ping
	legacyLogin *legacyproto.Login
}

// identified reports whether the client sent a valid handshake, legacy ping or
// legacy login, which tells it is a Minecraft client.
func (sess *session) identified() bool {
	return sess.handshake.Intent != 0 || sess.legacyPing != nil || sess.legacyLogin != nil
}

func (s *Server) handle(conn net.Conn, listener listenerInfo) {
	sess := session{conn: connInfo{start: time.Now(), remote: peerAddr(conn), listener: listener}}
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
		_, err := conn.Write(legacyproto.AppendPingResponse(nil, sess.legacyPing.Version, s.legacyStatus(*sess.legacyPing)))
		return err
	case sess.legacyLogin != nil:
		_, err := conn.Write(s.legacyKickPacket)
		return err
	case sess.handshake.Intent == modernproto.IntentStatus:
		return s.answerStatus(conn, buffers, sess, connDeadline)
	default:
		// Clients send a Login Start packet right after the handshake. It is
		// only read for logging, after the kick, so that an invalid or missing
		// one neither delays the kick nor aborts the connection.
		if _, err := conn.Write(s.kickPacket); err != nil {
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
	if err := s.writeStatus(conn, sess.handshake.ProtocolVersion); err != nil {
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
