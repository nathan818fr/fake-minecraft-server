package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

const clientTimeout = 2 * time.Second

var testConfig = Config{
	KickMessage:     json.RawMessage(`{"text":"Go away"}`),
	MOTD:            json.RawMessage(`{"text":"Hello"}`),
	Favicon:         "data:image/png;base64,AAAA",
	MaxPlayers:      20,
	OnlinePlayers:   3,
	VersionName:     "Fake 1.0",
	ProtocolVersion: 47,
	// Both longer than the client timeout, so that tests only see deliberate
	// closes.
	ConnectionTimeout: time.Minute,
	HandshakeTimeout:  time.Minute,
}

func startServer(t testing.TB, cfg Config) string {
	t.Helper()
	addr, _ := startServerWithLogger(t, cfg, slog.New(slog.DiscardHandler))
	return addr
}

// startServerWithLogger also returns a function that stops the server and waits
// for its connections to finish.
func startServerWithLogger(t testing.TB, cfg Config, logger *slog.Logger) (string, func()) {
	t.Helper()
	return startServerWithOptions(t, cfg, logger, ListenerOptions{})
}

// startServerWithOptions is like startServerWithLogger, with the options of
// its listener.
func startServerWithOptions(t testing.TB, cfg Config, logger *slog.Logger, options ListenerOptions) (string, func()) {
	t.Helper()
	ln := listenTCP(t)
	return ln.Addr().String(), serve(t, cfg, logger, Listener{Listener: ln, Options: options})
}

// startProxiedServer is like startServer, with the PROXY protocol (see
// testProxyProtocol).
func startProxiedServer(t testing.TB, cfg Config) string {
	t.Helper()
	addr, _ := startServerWithOptions(t, cfg, slog.New(slog.DiscardHandler), testProxyProtocol)
	return addr
}

func listenTCP(t testing.TB) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// serve serves the listeners and returns a function that stops the server and
// waits for its connections to finish.
func serve(t testing.TB, cfg Config, logger *slog.Logger, listeners ...Listener) func() {
	t.Helper()
	srv, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listeners...) }()
	stop := sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error: %v", err)
		}
	})
	t.Cleanup(stop)
	return stop
}

type client struct {
	t    testing.TB
	conn net.Conn
	r    *bufio.Reader
}

func dial(t testing.TB, addr string) *client {
	t.Helper()
	return dialNetwork(t, "tcp", addr)
}

func dialNetwork(t testing.TB, network, addr string) *client {
	t.Helper()
	conn, err := net.DialTimeout(network, addr, clientTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(clientTimeout))
	return &client{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *client) write(data []byte) {
	c.t.Helper()
	if _, err := c.conn.Write(data); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) handshake(protocolVersion int32, intent modernproto.Intent) {
	c.t.Helper()
	c.write(modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: protocolVersion,
		ServerAddress:   "localhost",
		ServerPort:      25565,
		Intent:          intent,
	}))
}

func (c *client) readPacket(id byte) []byte {
	c.t.Helper()
	payload, err := modernproto.ReadPacket(c.r, id, make([]byte, 1<<16))
	if err != nil {
		c.t.Fatalf("reading packet %#x: %v", id, err)
	}
	return payload
}

func (c *client) readString(id byte) string {
	c.t.Helper()
	r := bytes.NewReader(c.readPacket(id))
	length, err := modernproto.ReadVarInt(r)
	if err != nil {
		c.t.Fatalf("reading string length: %v", err)
	}
	if int(length) != r.Len() {
		c.t.Fatalf("string length %d does not match remaining payload length %d", length, r.Len())
	}
	s, _ := io.ReadAll(r)
	return string(s)
}

func (c *client) readStatus() map[string]json.RawMessage {
	c.t.Helper()
	var status map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.readString(modernproto.StatusResponseID)), &status); err != nil {
		c.t.Fatalf("decoding status: %v", err)
	}
	return status
}

// expectClosed asserts that the server closed the connection before the
// client timed out.
func (c *client) expectClosed() {
	c.t.Helper()
	_, err := io.ReadAll(c.r)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		c.t.Fatal("server did not close the connection")
	}
}

// holdConnection opens a connection to addr that the server counts in its
// limits until it is closed: the server answered its status request, and waits
// for a ping that it never sends.
func holdConnection(t testing.TB, addr string) *client {
	t.Helper()
	c := dial(t, addr)
	c.statusExchange(777)
	return c
}

// expectAccepted asserts that a new connection to addr gets its status, within
// clientTimeout: the server frees the place of a closed connection only once
// it reads the close, so the first tries may be rejected.
func expectAccepted(t testing.TB, addr string) {
	t.Helper()
	deadline := time.Now().Add(clientTimeout)
	for {
		err := tryStatus(addr)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection still rejected: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// tryStatus asks the server at addr for its status.
func tryStatus(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, clientTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(clientTimeout))
	request := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", ServerPort: 25565, Intent: modernproto.IntentStatus})
	if _, err := conn.Write(modernproto.AppendPacket(request, modernproto.StatusRequestID, nil)); err != nil {
		return err
	}
	_, err = modernproto.ReadPacket(bufio.NewReader(conn), modernproto.StatusResponseID, make([]byte, 1<<16))
	return err
}

// expectRejected asserts that the server resets a new connection to addr
// right away. The reset can arrive before the dial returns, which then fails.
func expectRejected(t testing.TB, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, clientTimeout)
	if errors.Is(err, syscall.ECONNRESET) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(clientTimeout))
	(&client{t: t, conn: conn, r: bufio.NewReader(conn)}).expectClosed()
}

// readLegacyResponse reads the response to a legacy ping until the server
// closes the connection, and returns its text.
func (c *client) readLegacyResponse() string {
	c.t.Helper()
	response, err := io.ReadAll(c.r)
	if err != nil {
		c.t.Fatal(err)
	}
	if len(response) < 3 || response[0] != 0xff || int(binary.BigEndian.Uint16(response[1:]))*2 != len(response)-3 {
		c.t.Fatalf("not a kick packet: % x", response)
	}
	units := make([]uint16, (len(response)-3)/2)
	_, _ = binary.Decode(response[3:], binary.BigEndian, units)
	return string(utf16.Decode(units))
}

func (c *client) statusExchange(protocolVersion int32) map[string]json.RawMessage {
	c.t.Helper()
	c.handshake(protocolVersion, modernproto.IntentStatus)
	c.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
	return c.readStatus()
}

func TestStatus(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	status := c.statusExchange(777)

	want := map[string]string{
		"version":     `{"name":"Fake 1.0","protocol":47}`,
		"players":     `{"max":20,"online":3,"sample":[]}`,
		"description": `{"text":"Hello"}`,
		"favicon":     `"data:image/png;base64,AAAA"`,

		"fakeMinecraftServer": "true",
	}
	if len(status) != len(want) {
		t.Errorf("status has fields %v, want %v", status, want)
	}
	for key, value := range want {
		if string(status[key]) != value {
			t.Errorf("status %s = %s, want %s", key, status[key], value)
		}
	}

	ping := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, ping))
	if pong := c.readPacket(modernproto.PongResponseID); !bytes.Equal(pong, ping) {
		t.Errorf("pong payload = % x, want % x", pong, ping)
	}
	c.expectClosed()
}

func TestStatusWithoutFavicon(t *testing.T) {
	cfg := testConfig
	cfg.Favicon = ""
	status := dial(t, startServer(t, cfg)).statusExchange(777)
	if _, ok := status["favicon"]; ok {
		t.Errorf("status has a favicon: %s", status["favicon"])
	}
}

func TestStatusMirrorsClientProtocolVersion(t *testing.T) {
	cfg := testConfig
	cfg.MirrorProtocolVersion = true
	addr := startServer(t, cfg)

	// 1.7.10, 26.3, a 26.1 snapshot and the lowest possible value.
	for _, version := range []int32{5, 777, 0x4000011f, math.MinInt32} {
		got := dial(t, addr).statusExchange(version)["version"]
		if want := fmt.Sprintf(`{"name":"Fake 1.0","protocol":%d}`, version); string(got) != want {
			t.Errorf("version = %s, want %s", got, want)
		}
	}
}

func TestLoginIsKicked(t *testing.T) {
	addr := startServer(t, testConfig)
	for name, intent := range map[string]modernproto.Intent{"login": modernproto.IntentLogin, "transfer": modernproto.IntentTransfer} {
		t.Run(name, func(t *testing.T) {
			c := dial(t, addr)
			c.handshake(777, intent)
			c.write(modernproto.AppendPacket(nil, 0x00, modernproto.AppendString(nil, "Steve")))

			if got := c.readString(modernproto.LoginDisconnectID); got != string(testConfig.KickMessage) {
				t.Errorf("kick message = %s, want %s", got, testConfig.KickMessage)
			}
			c.expectClosed()
		})
	}
}

// legacyPing16 is the ping of a 1.6 client, protocol 73, to localhost:25565.
var legacyPing16 = []byte("\xfe\x01\xfa\x00\x0b\x00M\x00C\x00|\x00P\x00i\x00n\x00g\x00H\x00o\x00s\x00t" +
	"\x00\x19\x49\x00\x09\x00l\x00o\x00c\x00a\x00l\x00h\x00o\x00s\x00t\x00\x00\x63\xdd")

func TestLegacyPing(t *testing.T) {
	tests := []struct {
		name   string
		ping   []byte
		mirror bool
		want   string
	}{
		{"beta to 1.3", []byte{0xfe}, false, "Hello§3§20"},
		{"1.4 to 1.5", []byte{0xfe, 0x01}, false, "§1\x0047\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.4 to 1.5 with mirrored protocol version", []byte{0xfe, 0x01}, true, "§1\x0061\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6", legacyPing16, false, "§1\x0047\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6 with mirrored protocol version", legacyPing16, true, "§1\x0073\x00Fake 1.0\x00Hello\x003\x0020"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			cfg.MirrorProtocolVersion = tc.mirror
			c := dial(t, startServer(t, cfg))
			c.write(tc.ping)

			if got := c.readLegacyResponse(); got != tc.want {
				t.Errorf("response = %q, want %q", got, tc.want)
			}
		})
	}
}

// legacyLoginBeta is the login handshake of a client older than 1.3, for Notch
// connecting to localhost:25565.
var legacyLoginBeta = func() []byte {
	units := utf16.Encode([]rune("Notch;localhost:25565"))
	b := binary.BigEndian.AppendUint16([]byte{0x02}, uint16(len(units)))
	for _, u := range units {
		b = binary.BigEndian.AppendUint16(b, u)
	}
	return b
}()

// legacyLogin13 is the login handshake of a 1.3 to 1.6 client, protocol 78,
// for Notch connecting to localhost:25565.
var legacyLogin13 = []byte("\x02\x4e\x00\x05\x00N\x00o\x00t\x00c\x00h" +
	"\x00\x09\x00l\x00o\x00c\x00a\x00l\x00h\x00o\x00s\x00t\x00\x00\x63\xdd")

func TestLegacyLoginIsKicked(t *testing.T) {
	tests := []struct {
		name      string
		handshake []byte
	}{
		{"1.3 to 1.6", legacyLogin13},
		{"before 1.3", []byte("\x02\x00\x05\x00N\x00o\x00t\x00c\x00h")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, startServer(t, testConfig))
			c.write(tc.handshake)
			if got := c.readLegacyResponse(); got != "Go away" {
				t.Errorf("kick message = %q, want %q", got, "Go away")
			}
		})
	}
}

// Clients write their legacy ping byte by byte, so its end can arrive after
// its start, as seen with 1.6.4 clients.
func TestFragmentedLegacyPing(t *testing.T) {
	tests := []struct {
		name  string
		ping  []byte
		split int
		want  string
	}{
		{"1.4 to 1.5", []byte{0xfe, 0x01}, 1, "§1\x0061\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6, after its first byte", legacyPing16, 1, "§1\x0073\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6, before MC|PingHost", legacyPing16, 3, "§1\x0073\x00Fake 1.0\x00Hello\x003\x0020"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			// The mirrored protocol version shows which ping was read.
			cfg.MirrorProtocolVersion = true
			c := dial(t, startServer(t, cfg))
			c.write(tc.ping[:tc.split])
			time.Sleep(100 * time.Millisecond)
			c.write(tc.ping[tc.split:])
			if got := c.readLegacyResponse(); got != tc.want {
				t.Errorf("response = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFragmentedHandshake(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	handshake := modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: 777,
		ServerAddress:   "localhost",
		ServerPort:      25565,
		Intent:          modernproto.IntentLogin,
	})
	for _, b := range handshake {
		c.write([]byte{b})
		time.Sleep(time.Millisecond)
	}
	c.readString(modernproto.LoginDisconnectID)
}

// A 254-byte handshake starts with the same bytes as a legacy ping (0xfe 0x01)
// and must not be mistaken for one.
func TestHandshakeResemblingLegacyPing(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	handshake := modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: 777,
		ServerAddress:   strings.Repeat("a", 246),
		ServerPort:      25565,
		Intent:          modernproto.IntentLogin,
	})
	if handshake[0] != 0xfe || handshake[1] != 0x01 {
		t.Fatalf("handshake starts with % x, want fe 01", handshake[:2])
	}

	c.write(handshake)
	c.readString(modernproto.LoginDisconnectID)
}

// Invalid traffic must be rejected as soon as it is recognized, well before
// the connection timeout.
func TestInvalidTrafficIsRejectedImmediately(t *testing.T) {
	addr := startServer(t, testConfig)

	validHandshake := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", Intent: modernproto.IntentStatus})
	tests := []struct {
		name string
		data []byte
	}{
		{"http request", []byte("GET / HTTP/1.1\r\n")},
		{"invalid legacy ping", []byte{0xfe, 0x02}},
		{"tls client hello", []byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01}},
		{"oversized packet", modernproto.AppendVarInt(nil, modernproto.MaxHandshakePayloadLength+2)},
		{"unknown intent", modernproto.AppendHandshake(nil, modernproto.Handshake{Intent: 4})},
		{"ping instead of status request", append(validHandshake, modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8))...)},
		{"status request with payload", append(validHandshake, modernproto.AppendPacket(nil, modernproto.StatusRequestID, []byte{0})...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, addr)
			c.write(tc.data)
			c.expectClosed()
		})
	}
}

func TestInvalidPing(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	c.statusExchange(777)
	c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 4)))
	c.expectClosed()
}

// Clients must send their handshake, with what comes along with it, by the
// handshake timeout: it frees the slots of connections that send nothing or
// too little sooner than the connection timeout.
func TestHandshakeTimeout(t *testing.T) {
	handshake := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", Intent: modernproto.IntentStatus})
	tests := []struct {
		name      string
		setup     func(*Config, *ListenerOptions)
		data      []byte
		closedAt  time.Duration
		wantReply bool
	}{
		{"silent", nil, nil, 200 * time.Millisecond, false},
		{"partial handshake", nil, handshake[:4], 200 * time.Millisecond, false},
		{"handshake without status request", nil, handshake, 200 * time.Millisecond, false},
		{
			"partial PROXY protocol header", func(_ *Config, options *ListenerOptions) { *options = testProxyProtocol },
			proxyHeader("192.0.2.1:1")[:8], 200 * time.Millisecond, false,
		},
		{
			"connection timeout shorter", func(cfg *Config, _ *ListenerOptions) { cfg.ConnectionTimeout = 100 * time.Millisecond },
			nil, 100 * time.Millisecond, false,
		},
		// Shorter than the wait for the rest of a legacy ping, which ends with
		// it.
		{
			"beta legacy ping", func(cfg *Config, _ *ListenerOptions) { cfg.HandshakeTimeout = 100 * time.Millisecond },
			[]byte{0xfe}, 100 * time.Millisecond, true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			cfg.HandshakeTimeout = 200 * time.Millisecond
			var options ListenerOptions
			if tc.setup != nil {
				tc.setup(&cfg, &options)
			}
			addr, _ := startServerWithOptions(t, cfg, slog.New(slog.DiscardHandler), options)
			c := dial(t, addr)
			c.write(tc.data)

			start := time.Now()
			reply, err := io.ReadAll(c.r)
			elapsed := time.Since(start)
			if errors.Is(err, os.ErrDeadlineExceeded) || elapsed < tc.closedAt-50*time.Millisecond || elapsed > tc.closedAt+500*time.Millisecond {
				t.Errorf("closed after %v (%v), want about %v", elapsed, err, tc.closedAt)
			}
			if gotReply := len(reply) > 0; gotReply != tc.wantReply {
				t.Errorf("reply = % x, want a reply: %v", reply, tc.wantReply)
			}
		})
	}
}

// The ping is sent a round trip after the status, so it is only bounded by the
// connection timeout.
func TestPingAfterHandshakeTimeout(t *testing.T) {
	cfg := testConfig
	cfg.HandshakeTimeout = 200 * time.Millisecond
	c := dial(t, startServer(t, cfg))
	c.statusExchange(777)
	time.Sleep(cfg.HandshakeTimeout + 200*time.Millisecond)
	c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8)))
	c.readPacket(modernproto.PongResponseID)
}

func TestConnectionLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(*Config)
	}{
		{"global", func(cfg *Config) { cfg.MaxConnections = 3 }},
		{"per IP", func(cfg *Config) { cfg.MaxConnectionsPerIP = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			tc.cfg(&cfg)
			addr := startServer(t, cfg)

			var held []*client
			for range 3 {
				held = append(held, holdConnection(t, addr))
			}

			expectRejected(t, addr)

			_ = held[0].conn.Close()
			expectAccepted(t, addr)
		})
	}
}

// Over its rate, a client is reset and logged. Behind a proxy, the rate counts
// the clients that the proxy tells, not the proxy.
func TestConnectionRateLimit(t *testing.T) {
	cfg := testConfig
	cfg.ConnectionRateLimitPerIP = 1 // In bursts of 2.
	tests := []struct {
		name    string
		options ListenerOptions
		want    string
	}{
		{"direct", ListenerOptions{}, `WARN  \[127\.0\.0\.1\]:\d+ connection rejected: limit=connections\.rate_limit_per_ip`},
		{
			"behind a proxy", testProxyProtocol,
			`WARN  \[192\.0\.2\.1\]:50000 connection rejected: proxy=127\.0\.0\.1 limit=connections\.rate_limit_per_ip`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			addr, stop := startServerWithOptions(t, cfg, slog.New(logging.NewTextHandler(&logs, nil)), tc.options)
			connect := func(client string) *client {
				c := dial(t, addr)
				if tc.options.ProxyProtocol {
					c.write(proxyHeader(client))
				}
				return c
			}
			status := func(client string) {
				c := connect(client)
				c.statusExchange(777)
				_ = c.conn.Close()
			}

			for range 2 {
				status("192.0.2.1:50000")
			}
			if tc.options.ProxyProtocol {
				connect("192.0.2.1:50000").expectClosed()
				status("192.0.2.2:50000")
			} else {
				expectRejected(t, addr)
			}
			stop()

			if !regexp.MustCompile(`(?m)^\[\S+\] ` + tc.want + `$`).MatchString(logs.String()) {
				t.Errorf("logs = %q, want a line matching %q", logs.String(), tc.want)
			}
		})
	}
}

// After its pong, a client sends nothing: the server closes without waiting
// for it to close, which frees its place at once.
func TestStatusExchangeFreesConnection(t *testing.T) {
	cfg := testConfig
	cfg.MaxConnections = 1
	addr := startServer(t, cfg)
	c := dial(t, addr)
	c.statusExchange(777)
	c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8)))
	c.readPacket(modernproto.PongResponseID)
	c.expectClosed()
	expectAccepted(t, addr)
}

// A client that sent more than its request gets the end of the connection,
// not a reset, on which some systems drop the answer not read yet. More than
// the read buffer of the server, so that some is left unread.
func TestUnreadDataClosesGracefully(t *testing.T) {
	c := dial(t, startServer(t, testConfig))
	c.statusExchange(777)
	c.write(append(modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8)), make([]byte, 4*readBufferSize)...))
	c.readPacket(modernproto.PongResponseID)
	_ = c.conn.(*net.TCPConn).CloseWrite()
	if _, err := io.ReadAll(c.r); err != nil {
		t.Fatalf("reading after the pong: %v, want the end of the connection", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  func(*Config)
	}{
		{"oversized status", func(cfg *Config) {
			cfg.Favicon = "data:image/png;base64," + strings.Repeat("A", modernproto.MaxStatusResponseLength)
		}},
		{"oversized kick message", func(cfg *Config) {
			cfg.KickMessage = json.RawMessage(`"` + strings.Repeat("a", modernproto.MaxDisconnectReasonLength) + `"`)
		}},
		{"invalid MOTD", func(cfg *Config) { cfg.MOTD = json.RawMessage(`{`) }},
		{"zero connection timeout", func(cfg *Config) { cfg.ConnectionTimeout = 0 }},
		{"zero handshake timeout", func(cfg *Config) { cfg.HandshakeTimeout = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			tc.cfg(&cfg)
			if _, err := New(cfg, slog.New(slog.DiscardHandler)); err == nil {
				t.Error("New() succeeded, want an error")
			}
		})
	}
}

func BenchmarkStatusExchange(b *testing.B) {
	cfg := testConfig
	cfg.Favicon = "data:image/png;base64," + strings.Repeat("A", 8000)
	addr := startServer(b, cfg)
	request := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", ServerPort: 25565, Intent: modernproto.IntentStatus})
	request = modernproto.AppendPacket(request, modernproto.StatusRequestID, nil)
	ping := modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8))

	b.ReportAllocs()
	for b.Loop() {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			b.Fatal(err)
		}
		r := bufio.NewReader(conn)
		buf := make([]byte, 1<<14)
		if _, err := conn.Write(request); err != nil {
			b.Fatal(err)
		}
		if _, err := modernproto.ReadPacket(r, modernproto.StatusResponseID, buf); err != nil {
			b.Fatal(err)
		}
		if _, err := conn.Write(ping); err != nil {
			b.Fatal(err)
		}
		if _, err := modernproto.ReadPacket(r, modernproto.PongResponseID, buf); err != nil {
			b.Fatal(err)
		}
		_ = conn.Close()
	}
}
