package config

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/condition"
	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/internal/server"
	"github.com/nathan818fr/fake-minecraft-server/internal/upstream"
)

func loadEnv(vars map[string]string) (Config, error) {
	var environ []string
	for name, value := range vars {
		environ = append(environ, name+"="+value)
	}
	return Load(environ)
}

// writeConfigFile writes content to a configuration file, next to a PNG file
// named icon.png, and returns its path.
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), pngSignature, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadFile(t *testing.T, content string, vars map[string]string) (Config, error) {
	t.Helper()
	all := map[string]string{"CONFIG_FILE": writeConfigFile(t, content)}
	maps.Copy(all, vars)
	return loadEnv(all)
}

func statusJSON(t *testing.T, cfg Config, id string) string {
	t.Helper()
	vs, ok := cfg.Server.Servers[id]
	if !ok || vs.Status == nil {
		t.Fatalf("no %s server with a status", id)
	}
	return string(vs.Status.AppendJSON(nil))
}

func TestDefaults(t *testing.T) {
	cfg, err := loadEnv(nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []Listener{{Network: "tcp", Address: ":25565"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	if got := statusJSON(t, cfg, "default"); len(cfg.Server.Servers) != 1 || got != defaultStatus {
		t.Errorf("servers = %v, default status = %s, want only %s", cfg.Server.Servers, got, defaultStatus)
	}
	if vs := cfg.Server.Servers["default"]; string(vs.Kick) != `{"text":"§cNot available"}` || vs.ClientProtocol != server.ClientProtocolAuto {
		t.Errorf("default server = %+v, want the built-in kick message and the client protocol by default", vs)
	}
	if want := []server.Rule{{Name: "default", Server: "default"}}; len(cfg.Server.Upstreams) != 0 || !reflect.DeepEqual(cfg.Server.Rules, want) {
		t.Errorf("upstreams = %v, rules = %v, want none and %v", cfg.Server.Upstreams, cfg.Server.Rules, want)
	}
	if cfg.Server.ConnectionTimeout != 3*time.Second || cfg.Server.HandshakeTimeout != time.Second ||
		cfg.Server.MaxConnections != 10000 || cfg.Server.MaxConnectionsPerIP != 8 {
		t.Errorf("ConnectionTimeout = %v, HandshakeTimeout = %v, MaxConnections = %d, MaxConnectionsPerIP = %d",
			cfg.Server.ConnectionTimeout, cfg.Server.HandshakeTimeout, cfg.Server.MaxConnections, cfg.Server.MaxConnectionsPerIP)
	}
	if cfg.LogFormat != logging.FormatText || cfg.LogLevel != slog.LevelInfo || cfg.Server.LogRateLimit != 1000 {
		t.Errorf("logFormat = %q, logLevel = %v, LogRateLimit = %d, want text, INFO and 1000", cfg.LogFormat, cfg.LogLevel, cfg.Server.LogRateLimit)
	}
	if !cfg.Server.SoftwareSignature {
		t.Error("SoftwareSignature = false, want true")
	}
}

func TestSettings(t *testing.T) {
	cfg, err := loadFile(t, `
listen:
  - ":25566"
  - { address: "[::]:25567", proxy_protocol: true, trusted_proxies: [10.0.0.0/8, 192.0.2.7] }
  - { address: unix:/run/fake-minecraft-server.sock, proxy_protocol: true }

connections:
  timeout: 500ms
  handshake_timeout: 200ms
  max: 0
  max_per_ip: 2

log:
  format: json
  level: debug
  rate_limit: 0

no_software_signature: true

servers:
  default:
    status:
      description:
        text: "Hello\n"
        extra: [{ text: world, color: gold }]
      favicon: icon.png
      version: { name: "1.21.10", protocol: 773 }
      players:
        max: 100
        online: 2
        sample:
          - { name: Notch, id: 069a79f4-44e9-4726-a5be-fca90e38aaf5 }
      enforcesSecureChat: true
    kick: { text: Bye, color: red }
  maintenance:
    status:
      description: "§cMaintenance"
      forgeData: { fmlNetworkVersion: 3 }
    client_protocol: Never
    kick: { text: Maintenance, color: red }

upstreams:
  lobby:
    address: lobby.example.com
  creative:
    address: "[2001:db8::1]:25566"
    handshake: { host: creative.example.com, port: 25565, protocol: 773 }
    interval: 1m
    retry_interval: 5s
    timeout: 3s
    max_age: 5m

rules:
  - name: creative
    host: [.Creative.Example.com., "*.creative.example.net"]
    server: creative
  - server: maintenance
`, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if want := []Listener{
		{Network: "tcp", Address: ":25566"},
		{Network: "tcp", Address: "[::]:25567", Options: server.ListenerOptions{
			ProxyProtocol:  true,
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.7/32")},
		}},
		{Network: "unix", Address: "/run/fake-minecraft-server.sock", Options: server.ListenerOptions{ProxyProtocol: true}},
	}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	s := cfg.Server
	if s.ConnectionTimeout != 500*time.Millisecond || s.HandshakeTimeout != 200*time.Millisecond ||
		s.MaxConnections != 0 || s.MaxConnectionsPerIP != 2 || s.LogRateLimit != 0 || s.SoftwareSignature {
		t.Errorf("unexpected server config: %+v", s)
	}
	// The implicit last rule targets the default server.
	if want := []server.Rule{
		{Name: "creative", Conditions: hostConditions(t, ".creative.example.com", "*.creative.example.net"), Server: "creative"},
		{Name: "1", Server: "maintenance"},
		{Name: "default", Server: "default"},
	}; !reflect.DeepEqual(s.Rules, want) {
		t.Errorf("Rules = %v, want %v", s.Rules, want)
	}
	// Client protocols are names, in any case.
	if vs := s.Servers["maintenance"]; vs.ClientProtocol != server.ClientProtocolNever || string(vs.Kick) != `{"color":"red","text":"Maintenance"}` {
		t.Errorf("maintenance server = %+v, want its client protocol and kick message", vs)
	}
	// Servers without a kick message get the one of the default server.
	for _, id := range []string{"default", "creative"} {
		if got, want := string(s.Servers[id].Kick), `{"color":"red","text":"Bye"}`; got != want {
			t.Errorf("server %s: kick message = %s, want %s", id, got, want)
		}
	}
	if cfg.LogFormat != logging.FormatJSON || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("logFormat = %q, logLevel = %v, want json and DEBUG", cfg.LogFormat, cfg.LogLevel)
	}

	// Every field is kept, and the favicon is read.
	want := `{"description":{"extra":[{"color":"gold","text":"world"}],"text":"Hello\n"},"enforcesSecureChat":true,` +
		`"favicon":"data:image/png;base64,iVBORw0KGgo=",` +
		`"players":{"max":100,"online":2,"sample":[{"id":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"}]},` +
		`"version":{"name":"1.21.10","protocol":773}}`
	if got := statusJSON(t, cfg, "default"); got != want {
		t.Errorf("default status = %s, want %s", got, want)
	}
	if got, want := statusJSON(t, cfg, "maintenance"), `{"description":"§cMaintenance","forgeData":{"fmlNetworkVersion":3}}`; got != want {
		t.Errorf("maintenance status = %s, want %s", got, want)
	}

	wantUpstreams := map[string]upstream.Config{
		"lobby": {
			Host:            "lobby.example.com",
			ServerAddress:   "lobby.example.com",
			ServerPort:      25565,
			ProtocolVersion: -1,
			Interval:        10 * time.Second,
			RetryInterval:   10 * time.Second,
			Timeout:         5 * time.Second,
			MaxAge:          30 * time.Second,
		},
		"creative": {
			Host:            "2001:db8::1",
			Port:            25566,
			ServerAddress:   "creative.example.com",
			ServerPort:      25565,
			ProtocolVersion: 773,
			Interval:        time.Minute,
			RetryInterval:   5 * time.Second,
			Timeout:         3 * time.Second,
			MaxAge:          5 * time.Minute,
		},
	}
	if !maps.Equal(cfg.Server.Upstreams, wantUpstreams) {
		t.Errorf("upstreams = %+v, want %+v", cfg.Server.Upstreams, wantUpstreams)
	}
}

// Log formats and levels are names, in any case.
func TestLogSettings(t *testing.T) {
	cfg, err := loadEnv(map[string]string{"LOG_FORMAT": "JSON", "LOG_LEVEL": "Warn"})
	if err != nil || cfg.LogFormat != logging.FormatJSON || cfg.LogLevel != slog.LevelWarn {
		t.Errorf("logFormat = %q, logLevel = %v, %v, want json and WARN", cfg.LogFormat, cfg.LogLevel, err)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	// The default handshake timeout must not exceed a short connection timeout.
	cfg, err := loadEnv(map[string]string{"CONNECTIONS_TIMEOUT": "500ms"})
	if err != nil || cfg.Server.HandshakeTimeout != 500*time.Millisecond {
		t.Errorf("HandshakeTimeout = %v, %v, want 500ms", cfg.Server.HandshakeTimeout, err)
	}

	cfg, err = loadEnv(map[string]string{"CONNECTIONS_TIMEOUT": "2s", "CONNECTIONS_HANDSHAKE_TIMEOUT": "2s"})
	if err != nil || cfg.Server.HandshakeTimeout != 2*time.Second {
		t.Errorf("HandshakeTimeout = %v, %v, want 2s", cfg.Server.HandshakeTimeout, err)
	}
}

// A server uses the upstream with its own ID unless it sets another one, or
// none, and keeps its static status, answered while the upstream does not
// answer. An upstream without a server makes one.
func TestServerUpstreams(t *testing.T) {
	cfg, err := loadFile(t, `
upstreams:
  lobby: { address: lobby.example.com }
  hub: { address: hub.example.com }
  solo: { address: solo.example.com }
servers:
  default: { status: { description: Fallback } }
  lobby: { kick: Lobby }
  play_b: { upstream: lobby }
  hub: { upstream: "", status: { description: Hub } }
rules: [{ server: play_b }, { server: solo }]
`, map[string]string{"UPSTREAMS_DEFAULT": "{ address: mc.example.com }"})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	for id, want := range map[string]string{"default": "default", "lobby": "lobby", "play_b": "lobby", "hub": "", "solo": "solo"} {
		if got := cfg.Server.Servers[id].Upstream; got != want {
			t.Errorf("server %s: upstream = %q, want %q", id, got, want)
		}
	}
	if got := statusJSON(t, cfg, "default"); got != `{"description":"Fallback"}` {
		t.Errorf("default status = %s, want the one of the file", got)
	}
	if got := slices.Sorted(maps.Keys(cfg.Server.Upstreams)); !slices.Equal(got, []string{"default", "hub", "lobby", "solo"}) {
		t.Errorf("upstreams = %v, want each one once", got)
	}
}

// The default retry interval, timeout and max age follow the interval and the
// timeout.
func TestUpstreamDefaults(t *testing.T) {
	tests := []struct {
		entry                          string
		retryInterval, timeout, maxAge time.Duration
	}{
		{"{ address: lobby.example.com, interval: 1s }", time.Second, time.Second, 3 * time.Second},
		{"{ address: lobby.example.com, interval: 1s, timeout: 5s }", time.Second, 5 * time.Second, 6 * time.Second},
	}
	for _, tc := range tests {
		cfg, err := loadEnv(map[string]string{"UPSTREAMS_LOBBY": tc.entry})
		got := cfg.Server.Upstreams["lobby"]
		if err != nil || got.RetryInterval != tc.retryInterval || got.Timeout != tc.timeout || got.MaxAge != tc.maxAge {
			t.Errorf("UPSTREAMS_LOBBY=%q: RetryInterval = %v, Timeout = %v, MaxAge = %v, %v, want %v, %v and %v",
				tc.entry, got.RetryInterval, got.Timeout, got.MaxAge, err, tc.retryInterval, tc.timeout, tc.maxAge)
		}
	}
}

// Addresses without a port keep none, so that their SRV record is looked up,
// while the handshake has the default port, as clients send it.
func TestUpstreamAddresses(t *testing.T) {
	tests := []struct {
		address    string
		host       string
		port       uint16
		serverPort uint16
	}{
		{"lobby.example.com", "lobby.example.com", 0, 25565},
		{"lobby.example.com:25566", "lobby.example.com", 25566, 25566},
		{"[2001:db8::1]", "2001:db8::1", 0, 25565},
		{"[2001:db8::1]:25566", "2001:db8::1", 25566, 25566},
	}
	for _, tc := range tests {
		cfg, err := loadEnv(map[string]string{"UPSTREAMS_LOBBY": `{ address: "` + tc.address + `" }`})
		got := cfg.Server.Upstreams["lobby"]
		if err != nil || got.Host != tc.host || got.Port != tc.port || got.ServerAddress != tc.host || got.ServerPort != tc.serverPort {
			t.Errorf("address %q: Host = %q, Port = %d, ServerAddress = %q, ServerPort = %d, %v, want %q, %d, %q and %d",
				tc.address, got.Host, got.Port, got.ServerAddress, got.ServerPort, err, tc.host, tc.port, tc.host, tc.serverPort)
		}
	}
}

// Listeners are addresses alone, comma-separated, or written in YAML with
// their options.
func TestListeners(t *testing.T) {
	proxied := server.ListenerOptions{ProxyProtocol: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	tests := []struct {
		value string
		want  []Listener
	}{
		{":25565", []Listener{{Network: "tcp", Address: ":25565"}}},
		{"0.0.0.0:25565, [::]:25566", []Listener{{Network: "tcp", Address: "0.0.0.0:25565"}, {Network: "tcp", Address: "[::]:25566"}}},
		// IPv6 addresses start like YAML lists.
		{"[::]:25565,[::1]:25566", []Listener{{Network: "tcp", Address: "[::]:25565"}, {Network: "tcp", Address: "[::1]:25566"}}},
		{"unix:/run/a.sock,unix:@abstract", []Listener{{Network: "unix", Address: "/run/a.sock"}, {Network: "unix", Address: "@abstract"}}},
		{":25565,,", []Listener{{Network: "tcp", Address: ":25565"}}},
		{
			`["[::]:25565", { address: ":25566", proxy_protocol: true, trusted_proxies: [10.0.0.0/8] }]`,
			[]Listener{{Network: "tcp", Address: "[::]:25565"}, {Network: "tcp", Address: ":25566", Options: proxied}},
		},
		// The PROXY protocol alone trusts the peers of Unix sockets.
		{
			"[{ address: unix:/run/a.sock, proxy_protocol: true }]",
			[]Listener{{Network: "unix", Address: "/run/a.sock", Options: server.ListenerOptions{ProxyProtocol: true}}},
		},
	}
	for _, tc := range tests {
		cfg, err := loadEnv(map[string]string{"LISTEN": tc.value})
		if err != nil || !reflect.DeepEqual(cfg.Listeners, tc.want) {
			t.Errorf("LISTEN=%q: Listeners = %v, %v, want %v", tc.value, cfg.Listeners, err, tc.want)
		}
	}
	// An invalid IPv6 address is still an address, not YAML.
	if _, err := loadEnv(map[string]string{"LISTEN": "[::1]:0"}); err == nil || err.Error() != `listen.0: "[::1]:0" has an invalid port, must be between 1 and 65535` {
		t.Errorf("LISTEN=[::1]:0: error = %v, want an invalid port", err)
	}
}

func TestTrustedProxies(t *testing.T) {
	cfg, err := loadFile(t, `listen:
  - address: ":25565"
    proxy_protocol: true
    trusted_proxies: [10.0.0.0/8, 192.0.2.7, "2001:db8::/32", "::ffff:198.51.100.0/120", 10.1.2.3/8]
`, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	if len(cfg.Listeners) != 1 || !slices.Equal(cfg.Listeners[0].Options.TrustedProxies, want) {
		t.Errorf("Listeners = %v, want trusted proxies %v", cfg.Listeners, want)
	}
}

// Text components are written in YAML, or are text: JSON if it starts with
// "{", or else plain text.
func TestTextComponent(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{"plain", `{"text":"plain"}`},
		{`say "hi"`, `{"text":"say \"hi\""}`},
		{"§cRed\nLine", `{"text":"§cRed\nLine"}`},
		{`{"text":"json","bold":true}`, `{"text":"json","bold":true}`},
		{`["array"]`, `{"text":"[\"array\"]"}`},
		{map[string]any{"text": "yaml", "bold": true}, `{"bold":true,"text":"yaml"}`},
		{[]any{"a", map[string]any{"text": "b"}}, `["a",{"text":"b"}]`},
	}
	for _, tc := range tests {
		got, err := textComponent(tc.input)
		if err != nil || string(got) != tc.want {
			t.Errorf("textComponent(%#v) = %s, %v, want %s", tc.input, got, err, tc.want)
		}
		if !json.Valid(got) {
			t.Errorf("textComponent(%#v) is not valid JSON", tc.input)
		}
	}
}

// Descriptions are text components like kick messages, so text that starts
// with "{" is JSON.
func TestJSONDescription(t *testing.T) {
	cfg, err := loadFile(t, "servers:\n  default:\n    status: { description: '{\"text\":\"Hi\",\"color\":\"red\"}' }\n", nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got, want := statusJSON(t, cfg, "default"), `{"description":{"text":"Hi","color":"red"}}`; got != want {
		t.Errorf("default status = %s, want %s", got, want)
	}
}

// The status of the default server replaces the built-in one, which it has
// otherwise.
func TestDefaultStatus(t *testing.T) {
	for _, content := range []string{"servers:\n", "servers:\n  maintenance: { status: { description: Maintenance } }\n", "servers:\n  default: { kick: Bye }\n"} {
		cfg, err := loadFile(t, content, nil)
		if err != nil {
			t.Fatalf("Load() with %q error: %v", content, err)
		}
		if got := statusJSON(t, cfg, "default"); got != defaultStatus {
			t.Errorf("Load() with %q: default status = %s, want %s", content, got, defaultStatus)
		}
	}

	// Without a default server in the file, its variable changes the built-in
	// status.
	cfg, err := loadEnv(map[string]string{"SERVERS_DEFAULT": `{"status":{"players":{"online":42}}}`})
	want := `{"description":{"text":"§cNot available"},"players":{"max":0,"online":42,"sample":[]}}`
	if got := statusJSON(t, cfg, "default"); err != nil || got != want {
		t.Errorf("default status = %s, %v, want %s", got, err, want)
	}
}

// Errors must tell what is expected.
func TestCheckErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"invalid address", "listen:\n  - :25565\n  - :0\n", `listen.1: ":0" has an invalid port, must be between 1 and 65535`},
		{"address without port", "listen: [0.0.0.0]\n", `listen.0: "0.0.0.0" must be host:port or unix:path`},
		{"IPv6 address without port", "listen: [\"[::1]\"]\n", `listen.0: "[::1]" must be host:port or unix:path`},
		{"named port", "listen: [\":minecraft\"]\n", `listen.0: ":minecraft" has an invalid port, must be between 1 and 65535`},
		{"socket without path", "listen: [\"unix:\"]\n", `listen.0: "unix:" has no socket path`},
		{"listener without address", "listen: [{ proxy_protocol: true }]\n", "listen.0.address: must be set"},

		{"zero timeout", "connections: { timeout: 0s }\n", "connections.timeout: must be positive, got 0s"},
		{"negative timeout", "connections: { timeout: -1s }\n", "connections.timeout: must be positive, got -1s"},
		{
			"handshake timeout longer than the connection timeout", "connections: { timeout: 2s, handshake_timeout: 5s }\n",
			"connections.handshake_timeout: must be positive and at most the connection timeout (2s), got 5s",
		},
		{"negative count", "connections: { max: -1 }\n", "connections.max: must be between 0 and 2147483647, got -1"},
		{"too large count", "log: { rate_limit: 2147483648 }\n", "log.rate_limit: must be between 0 and 2147483647, got 2147483648"},
		{"invalid log format", "log: { format: xml }\n", `log.format: must be text or json, got "xml"`},
		{"invalid log level", "log: { level: verbose }\n", `log.level: must be debug, info, warn or error, got "verbose"`},
		{"log level with an offset", "log: { level: info+2 }\n", `log.level: must be debug, info, warn or error, got "info+2"`},

		{
			"invalid trusted proxy", "listen: [{ address: \":25565\", proxy_protocol: true, trusted_proxies: [10.0.0.0/33] }]\n",
			`listen.0.trusted_proxies: "10.0.0.0/33" is not an IP address or CIDR prefix`,
		},
		{
			"short IPv4-mapped trusted proxy", "listen: [{ address: \":25565\", proxy_protocol: true, trusted_proxies: [\"::ffff:0.0.0.0/80\"] }]\n",
			`listen.0.trusted_proxies: "::ffff:0.0.0.0/80" is an IPv4-mapped prefix shorter than /96`,
		},
		{
			"zoned trusted proxy", "listen: [{ address: \":25565\", proxy_protocol: true, trusted_proxies: [\"fe80::1%eth0\"] }]\n",
			`listen.0.trusted_proxies: "fe80::1%eth0" has an IPv6 zone, which is not supported`,
		},
		{
			"trusted proxies without the PROXY protocol", "listen: [{ address: \":25565\", trusted_proxies: [10.0.0.0/8] }]\n",
			"listen.0.trusted_proxies: requires proxy_protocol",
		},
		{
			"PROXY protocol without trusted proxies", "listen: [{ address: \":25565\", proxy_protocol: true }]\n",
			"listen.0.trusted_proxies: must list the proxies allowed to send headers, such as 10.0.0.0/8, or 0.0.0.0/0 and ::/0 for any address",
		},
		{
			"trusted proxies of a Unix socket", "listen: [{ address: unix:/run/a.sock, proxy_protocol: true, trusted_proxies: [10.0.0.0/8] }]\n",
			"listen.0.trusted_proxies: does not apply to Unix sockets, whose peers have no address: proxy_protocol alone trusts them",
		},

		{"server with an invalid ID", "servers:\n  My-Server: { kick: Bye }\n", `servers.My-Server: the ID must be made of lowercase letters, digits and underscores, got "My-Server"`},
		{"empty server", "servers:\n  lobby: { client_protocol: never }\n", "servers.lobby: must have a status, an upstream or a kick message"},
		{"invalid client protocol", "servers:\n  lobby: { kick: Bye, client_protocol: sometimes }\n", `servers.lobby.client_protocol: must be auto, always or never, got "sometimes"`},
		{"number as kick message", "servers:\n  lobby: { kick: 5 }\n", "servers.lobby.kick: must be text or a text component, got 5"},
		{
			"invalid JSON kick message", "servers:\n  lobby: { kick: '{\"text\": \"Bye\",}' }\n",
			`servers.lobby.kick: invalid JSON at byte 16: invalid character '}' looking for beginning of object key string` +
				` (for plain text that starts with "{", write { text: "..." })`,
		},
		{
			"invalid JSON description", "servers:\n  lobby:\n    status: { description: '{\"text\": \"Hi\"' }\n",
			`servers.lobby.status.description: invalid JSON at byte 13: unexpected end of JSON input` +
				` (for plain text that starts with "{", write { text: "..." })`,
		},
		{"unsupported kick message", "servers:\n  default: { kick: [{ text: .inf }] }\n", "servers.default.kick: json: unsupported value: +Inf"},
		{
			"invalid protocol version", "servers:\n  default:\n    status: { version: { protocol: \"1.21\" } }\n",
			`servers.default.status.version.protocol: must be an integer between -2147483648 and 2147483647, got "1.21"`,
		},
		{"NaN", "servers:\n  default:\n    status: { values: [1, .nan] }\n", "servers.default.status: json: unsupported value: NaN"},
		{"favicon not a string", "servers:\n  default:\n    status: { favicon: 1 }\n", "servers.default.status.favicon: must be the path of a PNG file or a data URI, got 1"},
		{
			"oversized status", "servers:\n  default:\n    status: { favicon: \"data:image/png;base64," + strings.Repeat("A", 32767) + "\" }\n",
			"servers.default.status: too long: 32875 characters, the limit is 32767 (is the favicon too large?)",
		},
		{
			"oversized kick message", "servers:\n  lobby: { kick: " + strings.Repeat("a", 262144) + " }\n",
			"servers.lobby.kick: too long: 262155 characters, the limit is 262144",
		},

		{"rule without server", "rules: [{}]\n", "rules.0.server: must be set"},
		{"rule with an unknown server", "rules: [{ server: other }]\n", `rules.0.server: no server has the ID "other"`},
		{"invalid rule name", "rules: [{ name: Lobby, server: default }]\n", `rules.0.name: must be made of lowercase letters, digits and underscores, got "Lobby"`},
		{"rule name of digits", "rules: [{ name: \"1\", server: default }]\n", `rules.0.name: must not be only digits, which name the rules without a name, got "1"`},
		{"rule named default", "rules: [{ name: default, server: default }]\n", `rules.0.name: "default" is the name of the implicit last rule`},
		{"duplicate rule name", "rules: [{ name: a, server: default }, { name: a, server: default }]\n", "rules.1.name: rule 0 has the same name"},
		{"empty host list", "rules: [{ host: [], server: default }]\n", "rules.0.host: must list at least one host"},
		{"empty host", "rules: [{ host: [\"\"], server: default }]\n", "rules.0.host: empty host"},
		{"host with a space", "rules: [{ host: [\"a .com\"], server: default }]\n", `rules.0.host: invalid host "a .com": must not contain spaces or control characters`},
		{"wildcard inside a host", "rules: [{ host: [\"a.*.com\"], server: default }]\n", `rules.0.host: invalid host "a.*.com": a wildcard is only allowed as "*." followed by a domain`},
		{"wildcard alone", "rules: [{ host: [\"*\"], server: default }]\n", `rules.0.host: invalid host "*": a wildcard is only allowed as "*." followed by a domain`},
		{"invalid condition", "rules: [{ when: 'protcol < 47', server: default }]\n", "rules.0.when: 1:1: undefined: protcol (did you mean protocol?)"},
		{"condition failing with a sample", "rules: [{ when: host, server: default }]\n", `rules.0.when: fails for a modern ping (host "mc.example.com"): must return True or False, got string`},
		{"wildcard without domain", "rules: [{ host: [\"*..a.com\"], server: default }]\n", `rules.0.host: invalid host "*..a.com": "*." must be followed by a domain`},
		{"empty label in a host", "rules: [{ host: [a..com], server: default }]\n", `rules.0.host: invalid host "a..com": empty label`},
		{"leading dot without domain", "rules: [{ host: [..a.com], server: default }]\n", `rules.0.host: invalid host "..a.com": "." must be followed by a domain`},

		{"server with an unknown upstream", "servers:\n  lobby: { upstream: other, kick: Bye }\n", `servers.lobby.upstream: no upstream has the ID "other"`},
		{
			"server without its upstream", "upstreams:\n  lobby: { address: lobby.example.com }\nservers:\n  lobby: { upstream: \"\" }\n",
			"servers.lobby: must have a status, an upstream or a kick message",
		},

		{"upstream with an invalid ID", "upstreams:\n  My-Lobby: { address: lobby.example.com }\n", `upstreams.My-Lobby: the ID must be made of lowercase letters, digits and underscores, got "My-Lobby"`},
		{"upstream without address", "upstreams:\n  lobby: { interval: 1s }\n", "upstreams.lobby.address: must be set"},
		{
			"upstream with invalid handshake port", "upstreams:\n  lobby: { address: lobby.example.com, handshake: { port: 65536 } }\n",
			"upstreams.lobby.handshake.port: must be between 1 and 65535, got 65536",
		},
		{
			"upstream with unbracketed IPv6 address", "upstreams:\n  lobby: { address: \"2001:db8::1\" }\n",
			`upstreams.lobby.address: "2001:db8::1" must be host or host:port`,
		},
		{
			"upstream with invalid port", "upstreams:\n  lobby: { address: lobby.example.com:0 }\n",
			`upstreams.lobby.address: "lobby.example.com:0" has an invalid port, must be between 1 and 65535`,
		},
		{
			"upstream with negative duration", "upstreams:\n  lobby: { address: lobby.example.com, interval: -1s }\n",
			"upstreams.lobby.interval: must be positive, got -1s",
		},
		{
			"upstream with short max_age", "upstreams:\n  lobby: { address: lobby.example.com, max_age: 12s }\n",
			"upstreams.lobby.max_age: must be at least interval + timeout (15s), the longest time between two successful polls, got 12s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadFile(t, tc.content, nil); err == nil || err.Error() != tc.want {
				t.Errorf("Load() error = %v, want %s", err, tc.want)
			}
		})
	}
}

// All the mistakes are reported at once.
func TestCheckReportsAllErrors(t *testing.T) {
	_, err := loadFile(t, `
connections:
  timeout: -1s
  max: -1
log:
  level: verbose
upstreams:
  lobby: { interval: 0s }
`, nil)
	want := []string{
		"connections.timeout: must be positive, got -1s",
		"connections.max: must be between 0 and 2147483647, got -1",
		`log.level: must be debug, info, warn or error, got "verbose"`,
		"upstreams.lobby.address: must be set",
		"upstreams.lobby.interval: must be positive, got 0s",
	}
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Load() error = %v, want %s", err, strings.Join(want, "\n"))
	}
}

func TestFaviconErrors(t *testing.T) {
	path := writeConfigFile(t, "servers:\n  missing: { status: { favicon: missing.png } }\n  config: { status: { favicon: config.yaml } }\n")
	dir := filepath.Dir(path)
	_, err := loadEnv(map[string]string{"CONFIG_FILE": path})
	want := "servers.config.status.favicon: " + filepath.Join(dir, "config.yaml") + " is not a PNG file\n" +
		"servers.missing.status.favicon: open " + filepath.Join(dir, "missing.png") + ": no such file or directory"
	if err == nil || err.Error() != want {
		t.Errorf("Load() error = %v, want %s", err, want)
	}
}

// hostConditions returns the conditions of a rule with hosts, as Load makes
// them.
func hostConditions(t *testing.T, patterns ...string) []condition.Condition {
	t.Helper()
	hosts, err := condition.Hosts(patterns)
	if err != nil {
		t.Fatalf("Hosts(%q) error: %v", patterns, err)
	}
	return []condition.Condition{hosts}
}

// Conditions are Starlark expressions, which may span several lines.
func TestRuleConditions(t *testing.T) {
	cfg, err := loadFile(t, `
rules:
  - when: |
      in_network(ip, "10.0.0.0/8")
        and protocol >= 767
    server: default
`, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	rules := cfg.Server.Rules
	if len(rules) != 2 || len(rules[0].Conditions) != 1 || len(rules[1].Conditions) != 0 {
		t.Fatalf("rules = %+v, want a condition, then the implicit last rule", rules)
	}
	for client, want := range map[*condition.Client]bool{
		{IP: "10.1.2.3", Protocol: 767}:  true,
		{IP: "192.0.2.1", Protocol: 767}: false,
		{IP: "10.1.2.3", Protocol: 47}:   false,
	} {
		if got, err := rules[0].Conditions[0].Match(client); err != nil || got != want {
			t.Errorf("condition with %+v = %v, %v, want %v", client, got, err, want)
		}
	}

	cfg, err = loadEnv(map[string]string{"RULES": `[{ when: 'event == "join"', server: default }]`})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got, err := cfg.Server.Rules[0].Conditions[0].Match(&condition.Client{Event: condition.EventJoin}); err != nil || !got {
		t.Errorf("condition of RULES = %v, %v, want a match", got, err)
	}
}
