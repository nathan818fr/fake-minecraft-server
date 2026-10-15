package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

func TestLogging(t *testing.T) {
	const timing = `duration=\S+`
	tests := []struct {
		name          string
		proxyProtocol bool
		client        func(c *client)
		want          string
	}{
		{
			name: "status and ping",
			client: func(c *client) {
				c.statusExchange(777)
				c.write(modernproto.AppendPacket(nil, modernproto.PingRequestID, make([]byte, 8)))
				c.readPacket(modernproto.PongResponseID)
			},
			want: `INFO  \[127\.0\.0\.1\]:\d+ server list ping: protocol=777 host="localhost" port=25565 rule=0 server=test source=static ping_request=true ` + timing,
		},
		{
			name:   "status without ping",
			client: func(c *client) { c.statusExchange(777); _ = c.conn.(*net.TCPConn).CloseWrite(); c.expectClosed() },
			want:   `INFO  \S+ server list ping: protocol=777 host="localhost" port=25565 rule=0 server=test source=static ping_request=false ` + timing,
		},
		{
			name:   "status then reset",
			client: func(c *client) { c.statusExchange(777); _ = c.conn.(*net.TCPConn).SetLinger(0) },
			want:   `INFO  \S+ server list ping: protocol=777 host="localhost" port=25565 rule=0 server=test source=static ping_request=false ` + timing,
		},
		{
			name: "status of a Forge client",
			client: func(c *client) {
				c.write(modernproto.AppendHandshake(nil, modernproto.Handshake{
					ProtocolVersion:    767,
					ServerAddress:      "localhost",
					ServerAddressExtra: "FML3\x00",
					ServerPort:         25565,
					Intent:             modernproto.IntentStatus,
				}))
				c.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
				c.readStatus()
				_ = c.conn.(*net.TCPConn).CloseWrite()
				c.expectClosed()
			},
			want: `INFO  \S+ server list ping: protocol=767 host="localhost" host_extra="FML3\\x00" port=25565 rule=0 server=test source=static ping_request=false ` + timing,
		},
		{
			name: "login",
			client: func(c *client) {
				c.handshake(777, modernproto.IntentLogin)
				uuid := []byte{0x06, 0x9a, 0x79, 0xf4, 0x44, 0xe9, 0x47, 0x26, 0xa5, 0xbe, 0xfc, 0xa9, 0x0e, 0x38, 0xaa, 0xf5}
				c.write(modernproto.AppendPacket(nil, modernproto.LoginStartID, append(modernproto.AppendString(nil, "Notch"), uuid...)))
				c.readPacket(modernproto.LoginDisconnectID)
			},
			want: `INFO  \S+ login attempt: protocol=777 host="localhost" port=25565 rule=0 server=test username="Notch" uuid=069a79f4-44e9-4726-a5be-fca90e38aaf5 ` + timing,
		},
		{
			name: "login without username",
			client: func(c *client) {
				c.handshake(5, modernproto.IntentLogin)
				_ = c.conn.(*net.TCPConn).CloseWrite()
				c.readPacket(modernproto.LoginDisconnectID)
			},
			want: `INFO  \S+ login attempt: protocol=5 host="localhost" port=25565 rule=0 server=test ` + timing,
		},
		{
			name: "transfer",
			client: func(c *client) {
				c.handshake(1518, modernproto.IntentTransfer)
				c.loginStart()
				c.readPacket(modernproto.LoginDisconnectID)
			},
			want: `INFO  \S+ transfer attempt: protocol=1518 host="localhost" port=25565 rule=0 server=test username="Steve" ` + timing,
		},
		{
			name:   "timeout",
			client: func(c *client) { c.statusExchange(777); c.expectClosed() },
			want:   `INFO  \S+ server list ping: protocol=777 host="localhost" port=25565 rule=0 server=test source=static ping_request=false ` + timing + ` error="reading ping request: read tcp .*: i/o timeout"`,
		},
		{
			name:          "proxied",
			proxyProtocol: true,
			client:        func(c *client) { c.write(proxyHeader("192.0.2.1:56324")); c.statusExchange(777) },
			want:          `INFO  \[192\.0\.2\.1\]:56324 server list ping: proxy=127\.0\.0\.1 protocol=777 host="localhost" port=25565 rule=0 server=test source=static ping_request=false ` + timing,
		},
		{
			name:   "invalid",
			client: func(c *client) { c.write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n")); c.expectClosed() },
			want:   `INFO  \S+ invalid connection: data="GET / HTTP/1\.1\\r\\nHost: localhost\\r\\n" ` + timing + ` error="reading handshake: unexpected packet: id 0x45, want 0x0"`,
		},
		{
			name: "invalid binary",
			client: func(c *client) {
				c.write(append([]byte{0x16, 0x03, 0x01}, strings.Repeat("a", 100)...))
				c.expectClosed()
			},
			want: `INFO  \S+ invalid connection: data="\\x16\\x03\\x01a{61}" ` + timing + ` error=.*`,
		},
		{
			name:          "invalid after PROXY protocol header",
			proxyProtocol: true,
			client:        func(c *client) { c.write(append(proxyHeader("192.0.2.1:56324"), "GET /"...)); c.expectClosed() },
			want:          `INFO  \[192\.0\.2\.1\]:56324 invalid connection: proxy=127\.0\.0\.1 data="GET /" ` + timing + ` error=.*`,
		},
		{
			name:          "invalid PROXY protocol header",
			proxyProtocol: true,
			client:        func(c *client) { c.handshake(777, modernproto.IntentStatus); c.expectClosed() },
			want:          `INFO  invalid connection: proxy=127\.0\.0\.1 data=".*localhost.*" ` + timing + ` error="reading PROXY protocol header: invalid PROXY protocol header: missing signature"`,
		},
		{
			name:   "legacy ping",
			client: func(c *client) { c.write(legacyPing16); c.expectClosed() },
			want:   `INFO  \S+ legacy server list ping: legacy_ping_version=1\.6 protocol=73 host="localhost" port=25565 rule=0 server=test source=static ` + timing,
		},
		{
			name:   "legacy login",
			client: func(c *client) { c.write(legacyLogin13); c.expectClosed() },
			want:   `INFO  \S+ legacy login attempt: protocol=78 host="localhost" port=25565 rule=0 server=test username="Notch" ` + timing,
		},
		{
			name:   "legacy login before 1.3",
			client: func(c *client) { c.write(legacyLoginBeta); c.expectClosed() },
			want:   `INFO  \S+ legacy login attempt: protocol=-1 host="localhost" port=25565 rule=0 server=test username="Notch" ` + timing,
		},
		{
			name:   "empty",
			client: func(c *client) { _ = c.conn.(*net.TCPConn).CloseWrite(); c.expectClosed() },
			want:   `INFO  \S+ connection closed before handshake: data="" ` + timing + ` error="reading handshake: EOF"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			cfg := testConfig
			cfg.ConnectionTimeout = 200 * time.Millisecond
			var options ListenerOptions
			listening := `listening: listener=127\.0\.0\.1:\d+`
			if tc.proxyProtocol {
				options = testProxyProtocol
				listening += ` proxy_protocol=true`
			}
			addr, stop := startServerWithOptions(t, cfg, nil, slog.New(logging.NewTextHandler(&logs, nil)), options)

			c := dial(t, addr)
			tc.client(c)
			_ = c.conn.Close()
			stop()

			// The connection line, between the lines that Serve logs when
			// it starts and stops, before or after the one when it starts
			// stopping, as the connection can end last.
			line := `\[\S+\] (?:` + tc.want + `)\n`
			stopping := `\[\S+\] INFO  stopping\n`
			want := `^\[\S+\] INFO  ` + listening + `\n` +
				`(?:` + line + stopping + `|` + stopping + line + `)` +
				`\[\S+\] INFO  stopped\n$`
			if !regexp.MustCompile(want).MatchString(logs.String()) {
				t.Errorf("logs = %q, want lines matching %q", logs.String(), want)
			}
		})
	}
}

// Pings older than 1.6 do not hold the protocol version, logged as -1. Unlike
// in TestLogging, the connection timeout leaves time for the wait for the rest
// of the ping.
func TestLoggingLegacyPingWithoutProtocolVersion(t *testing.T) {
	var logs bytes.Buffer
	addr, stop := startServerWithLogger(t, testConfig, slog.New(logging.NewTextHandler(&logs, nil)))

	c := dial(t, addr)
	c.write([]byte{0xfe, 0x01})
	c.readLegacyResponse()
	_ = c.conn.Close()
	stop()

	want := `INFO  \S+ legacy server list ping: legacy_ping_version=1\.4-1\.5 protocol=-1 rule=0 server=test source=static duration=\S+`
	if !regexp.MustCompile(`(?m)^\[\S+\] ` + want + `$`).MatchString(logs.String()) {
		t.Errorf("logs = %q, want a line matching %q", logs.String(), want)
	}
}

func TestLoggingRejectedConnections(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Config, *ListenerOptions)
		want  string
	}{
		{"global", func(cfg *Config, _ *ListenerOptions) { cfg.MaxConnections = 1 }, `WARN  \[127\.0\.0\.1\]:\d+ connection rejected: limit=connections\.max`},
		// The client is not known before the header of the proxy is read.
		{
			"global, behind a proxy", func(cfg *Config, options *ListenerOptions) { cfg.MaxConnections = 1; *options = testProxyProtocol },
			`WARN  connection rejected: proxy=127\.0\.0\.1 limit=connections\.max`,
		},
		{
			"per IP", func(cfg *Config, _ *ListenerOptions) { cfg.MaxConnectionsPerIP = 1 },
			`WARN  \[127\.0\.0\.1\]:\d+ connection rejected: limit=connections\.max_per_ip`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			cfg := testConfig
			var options ListenerOptions
			tc.setup(&cfg, &options)
			addr, stop := startServerWithOptions(t, cfg, nil, slog.New(logging.NewTextHandler(&logs, nil)), options)

			held := dial(t, addr)
			if options.ProxyProtocol {
				held.write(proxyHeader("192.0.2.1:50000"))
			}
			held.statusExchange(777)
			expectRejected(t, addr)
			_ = held.conn.Close()
			stop()

			if !regexp.MustCompile(`(?m)^\[\S+\] ` + tc.want + `$`).MatchString(logs.String()) {
				t.Errorf("logs = %q, want a line matching %q", logs.String(), tc.want)
			}
		})
	}
}

// Lines that the log level hides take no room in the rate limit.
func TestLogLimiterSkipsHiddenLines(t *testing.T) {
	s := &Server{logger: slog.New(logging.NewTextHandler(io.Discard, slog.LevelWarn)), logLimiter: logLimiter{perSecond: 1}}
	for range 3 {
		s.logInvalid(&connInfo{start: time.Now()}, headRecorder{}, io.EOF)
	}
	if dropped, ok := s.logLimiter.allow(time.Now()); !ok || !dropped.Equal(slog.Attr{}) {
		t.Errorf("allow() = %v, %v after hidden lines, want an allowed line without drops", dropped, ok)
	}
}

func TestLogLimiter(t *testing.T) {
	l := logLimiter{perSecond: 2}
	second := time.Unix(1000, 0)

	for i := range 2 {
		if dropped, ok := l.allow(second); !ok || !dropped.Equal(slog.Attr{}) {
			t.Fatalf("line %d: allow() = %v, %v, want an allowed line without drops", i, dropped, ok)
		}
	}
	for i := range 3 {
		if _, ok := l.allow(second.Add(999 * time.Millisecond)); ok {
			t.Fatalf("line %d over the limit was allowed", i)
		}
	}

	dropped, ok := l.allow(second.Add(time.Second))
	if !ok {
		t.Fatal("allow() refused a line in the next second")
	}
	if want := slog.Int("dropped", 3); !dropped.Equal(want) {
		t.Errorf("allow() reported %v, want %v", dropped, want)
	}
	if dropped, _ := l.allow(second.Add(time.Second)); !dropped.Equal(slog.Attr{}) {
		t.Errorf("allow() reported %v again", dropped)
	}
}

// Each rule that keeps failing is logged once per interval, with the count of
// its lines suppressed in between, independently of the other rules, and of
// the hooks of servers of the same name.
func TestScriptErrorLimiter(t *testing.T) {
	var l scriptErrorLimiter
	start := time.Unix(1000, 0)

	for _, rule := range []string{"a", "b"} {
		if suppressed, ok := l.allow(scriptKey{name: rule}, start); !ok || !suppressed.Equal(slog.Attr{}) {
			t.Fatalf("first failure of %s: allow() = %v, %v, want an allowed line without suppressed ones", rule, suppressed, ok)
		}
	}
	for i := range 3 {
		if _, ok := l.allow(scriptKey{name: "a"}, start.Add(scriptErrorInterval-time.Millisecond)); ok {
			t.Fatalf("failure %d of a within the interval was allowed", i)
		}
	}
	if _, ok := l.allow(scriptKey{hook: "ping_script", name: "a"}, start); !ok {
		t.Fatal("the first failure of the ping script of the server a was refused, as if it were the rule a")
	}

	suppressed, ok := l.allow(scriptKey{name: "a"}, start.Add(scriptErrorInterval))
	if !ok {
		t.Fatal("allow() refused a failure of a after the interval")
	}
	if want := slog.Int("suppressed", 3); !suppressed.Equal(want) {
		t.Errorf("allow() reported %v, want %v", suppressed, want)
	}
	if suppressed, ok := l.allow(scriptKey{name: "b"}, start.Add(scriptErrorInterval)); !ok || !suppressed.Equal(slog.Attr{}) {
		t.Errorf("failure of b after the interval: allow() = %v, %v, want an allowed line without suppressed ones", suppressed, ok)
	}
}

// Failures that the log level hides are not counted either.
func TestScriptErrorLimiterSkipsHiddenLines(t *testing.T) {
	s := &Server{logger: slog.New(logging.NewTextHandler(io.Discard, slog.LevelError))}
	s.logRuleError(&connInfo{}, ruleError{rule: "a", err: errors.New("failed")})
	if len(s.scriptErrorLimiter.scripts) != 0 {
		t.Errorf("hidden failures were counted: %v", s.scriptErrorLimiter.scripts)
	}
}

// A rule that fails for every client logs one rule failed line, and every
// connection line tells that it failed.
func TestLoggingRuleErrorSuppressed(t *testing.T) {
	var logs bytes.Buffer
	cfg := withWhenRules(t, testConfig, `client.request == "ping" and (client.protocol != 777 or int(client.host) > 0)`, `False`)
	addr, stop := startServerWithLogger(t, cfg, slog.New(logging.NewTextHandler(&logs, nil)))
	for range 3 {
		c := dial(t, addr)
		c.statusExchange(777)
		_ = c.conn.Close()
	}
	stop()

	if n := strings.Count(logs.String(), " rule failed: "); n != 1 {
		t.Errorf("logs = %q, want 1 rule failed line, got %d", logs.String(), n)
	}
	// Strings that the server formats itself are not quoted, like on the other
	// lines.
	if want := ` client.request=ping client.host="localhost" client.protocol=777 `; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want them to contain %q", logs.String(), want)
	}
	if n := strings.Count(logs.String(), " failed_rules=a "); n != 3 {
		t.Errorf("logs = %q, want 3 connection lines with failed_rules=a, got %d", logs.String(), n)
	}
}

// Statuses are logged with their source, so that operators see when the
// static status takes over.
func TestLoggingStatusOfUpstream(t *testing.T) {
	live, err := status.Parse([]byte(`{"description":"Live"}`))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cfg := withServer(testConfig, func(server *VirtualServer) { server.Upstream = "live" })
	upstreams := map[string]Upstream{"live": fakeUpstream{live}}
	addr, stop := startServerWithUpstreams(t, cfg, upstreams, slog.New(logging.NewTextHandler(&logs, nil)))
	c := dial(t, addr)
	c.statusExchange(777)
	_ = c.conn.Close()
	stop()

	if want := " rule=0 server=test source=upstream ping_request=false "; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want them to contain %q", logs.String(), want)
	}
}

// Conditions that fail are logged, and do not apply.
func TestLoggingRuleError(t *testing.T) {
	var logs bytes.Buffer
	cfg := withWhenRules(t, testConfig, `fms.matches(client.host, client.host_extra or "^$")`, `False`)
	addr, stop := startServerWithLogger(t, cfg, slog.New(logging.NewTextHandler(&logs, nil)))
	c := dial(t, addr)
	c.write(modernproto.AppendHandshake(nil, modernproto.Handshake{
		ProtocolVersion: 777, ServerAddress: "localhost", ServerAddressExtra: "(", ServerPort: 25565, Intent: modernproto.IntentLogin,
	}))
	c.loginStart()
	c.readPacket(modernproto.LoginDisconnectID)
	_ = c.conn.Close()
	stop()

	for _, want := range []string{
		`WARN  \[127\.0\.0\.1\]:\d+ rule failed: rule=a client\.host="localhost" client\.host_extra="\(" error="fms\.matches: error parsing regexp: missing closing \): ` + "`\\(`" + `"`,
		`INFO  \S+ login attempt: protocol=777 host="localhost" host_extra="\(" port=25565 rule=0 server=test failed_rules=a `,
	} {
		if !regexp.MustCompile(`(?m)^\[\S+\] ` + want).MatchString(logs.String()) {
			t.Errorf("logs = %q, want a line matching %q", logs.String(), want)
		}
	}
}

// Statuses are logged with the rule that answered, the first one that applies,
// and its server, even while its upstream is down.
func TestLoggingRuleOfStatus(t *testing.T) {
	var logs bytes.Buffer
	cfg := testConfig
	cfg.Servers = maps.Clone(cfg.Servers)
	cfg.Servers["lobby"] = VirtualServer{Status: mustParseStatus(t, `{"description":"Lobby"}`), Upstream: "lobby", Kick: json.RawMessage(testKick)}
	cfg.Rules = []Rule{{Name: "0", Server: "lobby"}, {Name: "default", Server: testServerID}}
	upstreams := map[string]Upstream{"lobby": fakeUpstream{nil}}
	addr, stop := startServerWithUpstreams(t, cfg, upstreams, slog.New(logging.NewTextHandler(&logs, nil)))
	c := dial(t, addr)
	c.statusExchange(777)
	_ = c.conn.Close()
	stop()

	if want := " rule=0 server=lobby source=static ping_request=false "; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want them to contain %q", logs.String(), want)
	}
}

// What conditions print is logged at the debug level, with the rule, and
// escaped like the other values that clients can choose.
func TestLoggingScriptPrint(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		var logs bytes.Buffer
		cfg := withWhenRules(t, testConfig, `print("host", client.host) == None`, `False`)
		addr, stop := startServerWithLogger(t, cfg, slog.New(logging.NewTextHandler(&logs, level)))
		c := dial(t, addr)
		c.handshakeTo("evil\nhost", 777, modernproto.IntentLogin)
		c.loginStart()
		c.readPacket(modernproto.LoginDisconnectID)
		_ = c.conn.Close()
		stop()

		want := regexp.MustCompile(`(?m)^\[\S+\] DEBUG \[127\.0\.0\.1\]:\d+ script print: rule=a text="host evil\\nhost"$`)
		if got := want.MatchString(logs.String()); got != (level == slog.LevelDebug) {
			t.Errorf("at level %v, logs = %q, want a script print line: %v", level, logs.String(), level == slog.LevelDebug)
		}
	}
}
