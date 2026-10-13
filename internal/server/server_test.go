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
	"maps"
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

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

const clientTimeout = 2 * time.Second

const testStatus = `{"version":{"name":"Fake 1.0","protocol":47},"players":{"max":20,"online":3,"sample":[]},` +
	`"description":{"text":"Hello"},"favicon":"data:image/png;base64,AAAA"}`

// testKick is the kick message of the virtual server of testConfig.
const testKick = `{"text":"Go away"}`

var testConfig = withStatus(Config{
	// Both longer than the client timeout, so that tests only see deliberate
	// closes.
	ConnectionTimeout: time.Minute,
	HandshakeTimeout:  time.Minute,
}, testStatus)

// testServerID is the ID of the virtual server of testConfig, which its single
// rule targets, and which answers its status as it is.
const testServerID = "test"

func mustParseStatus(t testing.TB, data string) *status.Status {
	t.Helper()
	st, err := status.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func withStatus(cfg Config, data string) Config {
	st, err := status.Parse([]byte(data))
	if err != nil {
		panic(err)
	}
	cfg.Servers = map[string]VirtualServer{testServerID: {Status: st, ClientProtocol: ClientProtocolNever, Kick: json.RawMessage(testKick)}}
	cfg.Rules = []Rule{{Name: "0", Server: testServerID}}
	return cfg
}

func withClientProtocol(cfg Config, clientProtocol ClientProtocol) Config {
	return withServer(cfg, func(server *VirtualServer) { server.ClientProtocol = clientProtocol })
}

// withServer returns cfg with its virtual server testServerID changed by
// change, and its servers copied so that cfg can be changed further.
func withServer(cfg Config, change func(*VirtualServer)) Config {
	cfg.Servers = maps.Clone(cfg.Servers)
	server := cfg.Servers[testServerID]
	change(&server)
	cfg.Servers[testServerID] = server
	return cfg
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
	return startServerWithUpstreams(t, cfg, nil, logger)
}

// startServerWithUpstreams is like startServerWithLogger, with upstreams in
// place of the pollers of cfg.Upstreams, such as fakes.
func startServerWithUpstreams(t testing.TB, cfg Config, upstreams map[string]Upstream, logger *slog.Logger) (string, func()) {
	t.Helper()
	return startServerWithOptions(t, cfg, upstreams, logger, ListenerOptions{})
}

// startServerWithOptions is like startServerWithUpstreams, with the options of
// its listener.
func startServerWithOptions(t testing.TB, cfg Config, upstreams map[string]Upstream, logger *slog.Logger, options ListenerOptions) (string, func()) {
	t.Helper()
	ln := listenTCP(t)
	return ln.Addr().String(), serve(t, cfg, upstreams, logger, Listener{Listener: ln, Options: options})
}

// startProxiedServer is like startServer, with the PROXY protocol (see
// testProxyProtocol).
func startProxiedServer(t testing.TB, cfg Config) string {
	t.Helper()
	addr, _ := startServerWithOptions(t, cfg, nil, slog.New(slog.DiscardHandler), testProxyProtocol)
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

// serve serves the listeners with the server made of cfg and upstreams (see
// newServer), and returns a function that stops it and waits for its
// connections to finish.
func serve(t testing.TB, cfg Config, upstreams map[string]Upstream, logger *slog.Logger, listeners ...Listener) func() {
	t.Helper()
	srv, err := newServer(cfg, upstreams, logger)
	if err != nil {
		t.Fatalf("newServer() error: %v", err)
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
	c.handshakeTo("localhost", protocolVersion, intent)
}

func (c *client) handshakeTo(host string, protocolVersion int32, intent modernproto.Intent) {
	c.t.Helper()
	c.write(modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: protocolVersion,
		ServerAddress:   host,
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.readString(modernproto.StatusResponseID)), &fields); err != nil {
		c.t.Fatalf("decoding status: %v", err)
	}
	return fields
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
	got := c.statusExchange(777)

	want := map[string]string{
		"version":     `{"name":"Fake 1.0","protocol":47}`,
		"players":     `{"max":20,"online":3,"sample":[]}`,
		"description": `{"text":"Hello"}`,
		"favicon":     `"data:image/png;base64,AAAA"`,
	}
	if len(got) != len(want) {
		t.Errorf("status has fields %v, want %v", got, want)
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("status %s = %s, want %s", key, got[key], value)
		}
	}

	ping := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, ping))
	if pong := c.readPacket(modernproto.PongResponseID); !bytes.Equal(pong, ping) {
		t.Errorf("pong payload = % x, want % x", pong, ping)
	}
	c.expectClosed()
}

// The status is sent as configured, with the fields that the server does not
// read.
func TestStatusIsSentAsConfigured(t *testing.T) {
	const want = `{"description":"Hi","enforcesSecureChat":true,"forgeData":{"fmlNetworkVersion":3}}`
	c := dial(t, startServer(t, withStatus(testConfig, want)))
	c.handshake(777, modernproto.IntentStatus)
	c.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
	if got := c.readString(modernproto.StatusResponseID); got != want {
		t.Errorf("status = %s, want %s", got, want)
	}
}

// Signed statuses get the member that identifies this software, unless they
// have their own.
func TestStatusSignature(t *testing.T) {
	for data, want := range map[string]string{
		`{"description":"Hi"}`:              `{"description":"Hi","fakeMinecraftServer":true}`,
		`{}`:                                `{"fakeMinecraftServer":true}`,
		`{"fakeMinecraftServer":"lobby-2"}`: `{"fakeMinecraftServer":"lobby-2"}`,
	} {
		cfg := withStatus(testConfig, data)
		cfg.SoftwareSignature = true
		c := dial(t, startServer(t, cfg))
		c.handshake(777, modernproto.IntentStatus)
		c.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
		if got := c.readString(modernproto.StatusResponseID); got != want {
			t.Errorf("status %s: got %s, want %s", data, got, want)
		}
	}
}

type fakeUpstream struct{ st *status.Status }

func (fakeUpstream) Run(context.Context) {}

func (u fakeUpstream) Status() *status.Status { return u.st }

// The status of the upstream of a server is answered while it has one, and its
// static status otherwise. Servers that no rule targets are not answered.
func TestStatusOfUpstream(t *testing.T) {
	live, err := status.Parse([]byte(`{"description":"Live","version":{"name":"Real 1.0","protocol":47}}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		upstream Upstream
		want     string
	}{
		{"available", fakeUpstream{live}, `"Live"`},
		{"unavailable", fakeUpstream{nil}, `{"text":"Hello"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := withServer(testConfig, func(server *VirtualServer) { server.Upstream = "live" })
			cfg.Servers["other"] = VirtualServer{Status: mustParseStatus(t, `{"description":"Other"}`), Upstream: "other", Kick: json.RawMessage(testKick)}
			upstreams := map[string]Upstream{"live": tc.upstream, "other": fakeUpstream{live}}
			addr, _ := startServerWithUpstreams(t, cfg, upstreams, slog.New(slog.DiscardHandler))
			if got := dial(t, addr).statusExchange(777)["description"]; string(got) != tc.want {
				t.Errorf("description = %s, want %s", got, tc.want)
			}
		})
	}
}

// runningUpstream is a fake upstream that tells when it starts and stops
// running.
type runningUpstream struct {
	fakeUpstream
	started, stopped chan struct{}
}

func (u runningUpstream) Run(ctx context.Context) {
	close(u.started)
	<-ctx.Done()
	close(u.stopped)
}

// The upstreams run while the server serves, and stop before Serve returns.
func TestServeRunsUpstreams(t *testing.T) {
	u := runningUpstream{started: make(chan struct{}), stopped: make(chan struct{})}
	cfg := withServer(testConfig, func(server *VirtualServer) { server.Upstream = "live" })
	_, stop := startServerWithUpstreams(t, cfg, map[string]Upstream{"live": u}, slog.New(slog.DiscardHandler))
	select {
	case <-u.started:
	case <-time.After(clientTimeout):
		t.Fatal("the upstream did not start")
	}
	stop()
	select {
	case <-u.stopped:
	default:
		t.Error("Serve returned before the upstream stopped")
	}
}

// The first rule that applies answers, even while the upstream of its server is
// down: with the static status of its server, never with the next rules.
func TestRules(t *testing.T) {
	live, err := status.Parse([]byte(`{"description":"Live"}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		upstream Upstream
		want     string
	}{
		{"upstream available", fakeUpstream{live}, `"Live"`},
		{"upstream unavailable", fakeUpstream{nil}, `"Lobby is down"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig
			cfg.Servers = maps.Clone(cfg.Servers)
			cfg.Servers["lobby"] = VirtualServer{Status: mustParseStatus(t, `{"description":"Lobby is down"}`), Upstream: "lobby", Kick: json.RawMessage(testKick)}
			cfg.Rules = []Rule{{Server: "lobby"}, {Server: testServerID}}
			addr, _ := startServerWithUpstreams(t, cfg, map[string]Upstream{"lobby": tc.upstream}, slog.New(slog.DiscardHandler))
			if got := dial(t, addr).statusExchange(777)["description"]; string(got) != tc.want {
				t.Errorf("description = %s, want %s", got, tc.want)
			}
		})
	}
}

// withConditionRules returns cfg with the servers "a" and "b", and a rule with
// conditions for each one, before the rule of its server testServerID.
func withConditionRules(cfg Config, condsA, condsB []condition.Condition) Config {
	cfg.Servers = maps.Clone(cfg.Servers)
	for _, id := range []string{"a", "b"} {
		st, err := status.Parse([]byte(`{"description":"Server ` + id + `"}`))
		if err != nil {
			panic(err)
		}
		cfg.Servers[id] = VirtualServer{Status: st, ClientProtocol: ClientProtocolNever, Kick: json.RawMessage(`"Kick ` + id + `"`)}
	}
	cfg.Rules = []Rule{{Name: "a", Conditions: condsA, Server: "a"}, {Name: "b", Conditions: condsB, Server: "b"}, {Name: "0", Server: testServerID}}
	return cfg
}

// withHostRules is like withConditionRules, with conditions on the hosts, or
// none for nil.
func withHostRules(t *testing.T, cfg Config, hostsA, hostsB []string) Config {
	t.Helper()
	conds := func(patterns []string) []condition.Condition {
		if patterns == nil {
			return nil
		}
		c, err := condition.Hosts(patterns)
		if err != nil {
			t.Fatalf("Hosts(%q) error: %v", patterns, err)
		}
		return []condition.Condition{c}
	}
	return withConditionRules(cfg, conds(hostsA), conds(hostsB))
}

// Rules with hosts apply to the clients that connected to one of them, in any
// case, with or without a trailing dot, and whatever modded clients add after
// the host.
func TestRuleHosts(t *testing.T) {
	addr := startServer(t, withHostRules(t, testConfig, []string{"play.a.com"}, []string{"*.b.com", ".c.com"}))
	tests := []struct {
		host string
		want string // The server.
	}{
		{"play.a.com", "a"},
		{"PLAY.A.com.", "a"},
		{"play.a.com\x00FML3\x00", "a"},
		{"x.b.com", "b"},
		{"y.x.b.com", "b"},
		{"b.com", ""},
		{"c.com", "b"},
		{"x.y.c.com", "b"},
		{"xc.com", ""},
		{"play.a.com.example", ""},
		{"", ""},
	}
	for _, tc := range tests {
		wantStatus, wantKick := `{"text":"Hello"}`, testKick
		if tc.want != "" {
			wantStatus, wantKick = `"Server `+tc.want+`"`, `"Kick `+tc.want+`"`
		}
		c := dial(t, addr)
		c.handshakeTo(tc.host, 777, modernproto.IntentStatus)
		c.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
		if got := c.readStatus()["description"]; string(got) != wantStatus {
			t.Errorf("host %q: description = %s, want %s", tc.host, got, wantStatus)
		}
		c = dial(t, addr)
		c.handshakeTo(tc.host, 777, modernproto.IntentLogin)
		if got := c.readString(modernproto.LoginDisconnectID); got != wantKick {
			t.Errorf("host %q: kick message = %s, want %s", tc.host, got, wantKick)
		}
	}
}

// Legacy clients match rules with hosts too, from 1.6 for pings and 1.3 for
// logins: older ones send no host, and match none.
func TestRuleHostsOfLegacyClients(t *testing.T) {
	addr := startServer(t, withHostRules(t, testConfig, []string{"localhost"}, nil))
	c := dial(t, addr)
	c.write(legacyPing16)
	if got := c.readLegacyResponse(); !strings.Contains(got, "Server a") {
		t.Errorf("legacy ping response = %q, want the status of server a", got)
	}
	c = dial(t, addr)
	c.write(legacyLogin13)
	if got := c.readLegacyResponse(); got != "Kick a" {
		t.Errorf("legacy kick message = %q, want %q", got, "Kick a")
	}
	c = dial(t, addr)
	c.write([]byte{0xfe, 0x01})
	if got := c.readLegacyResponse(); !strings.Contains(got, "Server b") {
		t.Errorf("legacy ping response without host = %q, want the status of server b", got)
	}
}

// By default, statuses report the protocol version of each client, unless
// they have one that no client has, which clients show as incompatible with
// the version name.
func TestClientProtocol(t *testing.T) {
	tests := []struct {
		clientProtocol ClientProtocol
		version        string
		want           string
	}{
		{ClientProtocolAuto, `{"name":"Fake","protocol":47}`, `{"name":"Fake","protocol":777}`},
		{ClientProtocolAuto, `{"name":"Fake","protocol":4}`, `{"name":"Fake","protocol":777}`},
		{ClientProtocolAuto, `{"name":"§cMaintenance","protocol":-1}`, `{"name":"§cMaintenance","protocol":-1}`},
		{ClientProtocolAuto, `{"name":"§cMaintenance","protocol":0}`, `{"name":"§cMaintenance","protocol":0}`},
		{ClientProtocolAuto, `{"name":"§cMaintenance","protocol":1}`, `{"name":"§cMaintenance","protocol":1}`},
		{ClientProtocolAlways, `{"name":"§cMaintenance","protocol":-1}`, `{"name":"§cMaintenance","protocol":777}`},
		{ClientProtocolNever, `{"name":"Fake","protocol":47}`, `{"name":"Fake","protocol":47}`},
	}
	for _, tc := range tests {
		cfg := withClientProtocol(withStatus(testConfig, `{"version":`+tc.version+`}`), tc.clientProtocol)
		if got := dial(t, startServer(t, cfg)).statusExchange(777)["version"]; string(got) != tc.want {
			t.Errorf("ClientProtocol %d with %s: version = %s, want %s", tc.clientProtocol, tc.version, got, tc.want)
		}
	}
}

func TestStatusUsesClientProtocolVersion(t *testing.T) {
	addr := startServer(t, withClientProtocol(testConfig, ClientProtocolAlways))

	// 1.7.10, 26.3, a 26.1 snapshot and the lowest possible value.
	for _, version := range []int32{5, 777, 0x4000011f, math.MinInt32} {
		got := dial(t, addr).statusExchange(version)["version"]
		if want := fmt.Sprintf(`{"name":"Fake 1.0","protocol":%d}`, version); string(got) != want {
			t.Errorf("version = %s, want %s", got, want)
		}
	}
}

// Using the protocol version of the client adds a version to statuses that
// have none, so that clients still see the server as compatible.
func TestStatusUsesClientProtocolVersionWithoutVersion(t *testing.T) {
	cfg := withClientProtocol(withStatus(testConfig, `{"description":"Hi"}`), ClientProtocolAuto)
	got := dial(t, startServer(t, cfg)).statusExchange(777)["version"]
	if want := `{"name":"","protocol":777}`; string(got) != want {
		t.Errorf("version = %s, want %s", got, want)
	}
}

// The server of the first rule that applies answers pings and joins alike,
// without falling through to the next rules.
func TestFirstRuleAnswers(t *testing.T) {
	cfg := testConfig
	cfg.Servers = maps.Clone(cfg.Servers)
	cfg.Servers["closed"] = VirtualServer{Status: mustParseStatus(t, `{"description":"Closed"}`), Kick: json.RawMessage(`{"text":"Closed"}`)}
	cfg.Rules = []Rule{{Server: "closed"}, {Server: testServerID}}
	addr := startServer(t, cfg)

	c := dial(t, addr)
	c.handshake(777, modernproto.IntentLogin)
	c.write(modernproto.AppendPacket(nil, 0x00, modernproto.AppendString(nil, "Steve")))
	if got, want := c.readString(modernproto.LoginDisconnectID), `{"text":"Closed"}`; got != want {
		t.Errorf("kick message = %s, want %s", got, want)
	}
	c = dial(t, addr)
	c.write(legacyLogin13)
	if got := c.readLegacyResponse(); got != "Closed" {
		t.Errorf("legacy kick message = %q, want %q", got, "Closed")
	}
	if got := dial(t, addr).statusExchange(777)["description"]; string(got) != `"Closed"` {
		t.Errorf("description = %s, want the one of the first rule", got)
	}
}

func TestLoginIsKicked(t *testing.T) {
	addr := startServer(t, testConfig)
	for name, intent := range map[string]modernproto.Intent{"login": modernproto.IntentLogin, "transfer": modernproto.IntentTransfer} {
		t.Run(name, func(t *testing.T) {
			c := dial(t, addr)
			c.handshake(777, intent)
			c.write(modernproto.AppendPacket(nil, 0x00, modernproto.AppendString(nil, "Steve")))

			if got := c.readString(modernproto.LoginDisconnectID); got != testKick {
				t.Errorf("kick message = %s, want %s", got, testKick)
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
		name           string
		ping           []byte
		clientProtocol ClientProtocol
		want           string
	}{
		{"beta to 1.3", []byte{0xfe}, ClientProtocolNever, "Hello§3§20"},
		{"1.4 to 1.5", []byte{0xfe, 0x01}, ClientProtocolNever, "§1\x0047\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.4 to 1.5 with the client's protocol version", []byte{0xfe, 0x01}, ClientProtocolAlways, "§1\x0061\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6", legacyPing16, ClientProtocolNever, "§1\x0047\x00Fake 1.0\x00Hello\x003\x0020"},
		{"1.6 with the client's protocol version", legacyPing16, ClientProtocolAlways, "§1\x0073\x00Fake 1.0\x00Hello\x003\x0020"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, startServer(t, withClientProtocol(testConfig, tc.clientProtocol)))
			c.write(tc.ping)

			if got := c.readLegacyResponse(); got != tc.want {
				t.Errorf("response = %q, want %q", got, tc.want)
			}
		})
	}
}

// Statuses may lack any field, which legacy clients get as empty or zero.
func TestLegacyPingOfEmptyStatus(t *testing.T) {
	c := dial(t, startServer(t, withStatus(testConfig, `{}`)))
	c.write(legacyPing16)
	if got, want := c.readLegacyResponse(), "§1\x000\x00\x00\x000\x000"; got != want {
		t.Errorf("response = %q, want %q", got, want)
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
			// The protocol version of the client shows which ping was read.
			c := dial(t, startServer(t, withClientProtocol(testConfig, ClientProtocolAlways)))
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
			addr, _ := startServerWithOptions(t, cfg, nil, slog.New(slog.DiscardHandler), options)
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
			addr, stop := startServerWithOptions(t, cfg, nil, slog.New(logging.NewTextHandler(&logs, nil)), tc.options)
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
			*cfg = withStatus(*cfg, `{"favicon":"data:image/png;base64,`+strings.Repeat("A", modernproto.MaxStatusResponseLength)+`"}`)
		}},
		{"no rules", func(cfg *Config) { cfg.Rules = nil }},
		{"rule with an unknown server", func(cfg *Config) { cfg.Rules = []Rule{{Server: "other"}, {Server: testServerID}} }},
		{"server without a status", func(cfg *Config) {
			*cfg = withServer(*cfg, func(server *VirtualServer) { server.Status = nil })
		}},
		{"server with an unknown upstream", func(cfg *Config) {
			*cfg = withServer(*cfg, func(server *VirtualServer) { server.Upstream = "missing" })
		}},
		{"oversized kick message of a server", func(cfg *Config) {
			*cfg = withServer(*cfg, func(server *VirtualServer) {
				server.Kick = json.RawMessage(`"` + strings.Repeat("a", modernproto.MaxDisconnectReasonLength) + `"`)
			})
		}},
		{"server without a kick message", func(cfg *Config) {
			*cfg = withServer(*cfg, func(server *VirtualServer) { server.Kick = nil })
		}},
		{"last rule with conditions", func(cfg *Config) {
			hosts, _ := condition.Hosts([]string{"a.com"})
			cfg.Rules = []Rule{{Conditions: []condition.Condition{hosts}, Server: testServerID}}
		}},
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

type recordingConn struct {
	net.Conn
	written []byte
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.written = append(c.written, p...)
	return len(p), nil
}

// The limit of status responses is in UTF-16 code units, not bytes, and clients
// reject longer ones.
func TestWriteStatusLength(t *testing.T) {
	tests := []struct {
		name        string
		description string
		signed      bool
		fits        bool
	}{
		{"longer in bytes only", strings.Repeat("§", modernproto.MaxStatusResponseLength-100), false, true},
		{"too long", strings.Repeat("a", modernproto.MaxStatusResponseLength), false, false},
		// Of the longest length, without the signature: {"description":"…"}.
		{"longest", strings.Repeat("a", modernproto.MaxStatusResponseLength-18), false, true},
		{"too long with the signature", strings.Repeat("a", modernproto.MaxStatusResponseLength-18), true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := status.Parse([]byte(`{"description":"` + tc.description + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			conn := &recordingConn{}
			err = writeStatus(conn, st, tc.signed)
			if tc.fits && (err != nil || len(conn.written) == 0) {
				t.Errorf("writeStatus() error = %v, wrote %d bytes, want the status", err, len(conn.written))
			}
			if !tc.fits && (err == nil || !strings.HasPrefix(err.Error(), "writing status: too long:") || len(conn.written) != 0) {
				t.Errorf("writeStatus() error = %v, wrote %d bytes, want a length error and nothing", err, len(conn.written))
			}
		})
	}
}

func BenchmarkStatusExchange(b *testing.B) {
	addr := startServer(b, withStatus(testConfig, strings.Replace(testStatus, "AAAA", strings.Repeat("A", 8000), 1)))
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
