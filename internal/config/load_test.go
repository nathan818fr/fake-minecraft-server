package config

import (
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// Every setting has a variable named after its key, and every upstream and
// server one named after its ID.
func TestVariables(t *testing.T) {
	want := []string{
		"FMS_LISTEN",
		"FMS_CONNECTIONS_TIMEOUT",
		"FMS_CONNECTIONS_HANDSHAKE_TIMEOUT",
		"FMS_CONNECTIONS_MAX",
		"FMS_CONNECTIONS_MAX_PER_IP",
		"FMS_CONNECTIONS_RATE_LIMIT_PER_IP",
		"FMS_LOG_FORMAT",
		"FMS_LOG_LEVEL",
		"FMS_LOG_RATE_LIMIT",
		"FMS_NO_SOFTWARE_SIGNATURE",
		"FMS_UPSTREAMS_Creative_1",
		"FMS_UPSTREAMS_LOBBY",
		"FMS_SERVERS_DEFAULT",
		"FMS_SERVERS_LOBBY",
		"FMS_RULES",
		"FMS_SCRIPT",
		"FMS_SCRIPT_FILE",
	}
	// Variables without the prefix are ignored, such as the ones Kubernetes
	// sets for a service named servers.
	env := map[string]string{"LOG": "", "LOG_LEVEL": "", "PATH": "", "SERVERS_PORT": ""}
	for _, name := range want {
		env[name] = ""
	}
	var got []string
	for _, v := range variables(&settings{}, env) {
		got = append(got, v.name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("variables() = %q, want %q", got, want)
	}
}

func TestVariableValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.png"), pngSignature, 0o600); err != nil {
		t.Fatal(err)
	}
	// The favicons of variables are relative to the working directory.
	t.Chdir(dir)
	cfg, err := loadEnv(map[string]string{
		"FMS_LISTEN":                        `["[::1]:25566", { address: unix:/run/fake-minecraft-server.sock, proxy_protocol: true }]`,
		"FMS_CONNECTIONS_TIMEOUT":           "500ms",
		"FMS_CONNECTIONS_HANDSHAKE_TIMEOUT": "200ms",
		"FMS_CONNECTIONS_MAX":               "0",
		"FMS_CONNECTIONS_MAX_PER_IP":        "2",
		"FMS_CONNECTIONS_RATE_LIMIT_PER_IP": "3",

		"FMS_LOG_FORMAT":     "json",
		"FMS_LOG_LEVEL":      "debug",
		"FMS_LOG_RATE_LIMIT": "0",

		"FMS_NO_SOFTWARE_SIGNATURE": "true",

		"FMS_SERVERS_DEFAULT":     `{"status":{"description":"Hello","favicon":"icon.png","players":{"max":100,"online":2}},"kick":{"text":"Bye","color":"red"}}`,
		"FMS_SERVERS_MAINTENANCE": "{ status: { description: Maintenance, version: { name: Fake, protocol: -1 } }, client_protocol: never, kick: Closed }",
		"FMS_UPSTREAMS_LOBBY":     "{ address: lobby.example.com, interval: 1m }",
		"FMS_RULES":               "[{ name: lobby, host: [lobby.example.com], server: lobby }, { server: maintenance }]",
	})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []Listener{
		{Network: "tcp", Address: "[::1]:25566"},
		{Network: "unix", Address: "/run/fake-minecraft-server.sock", Options: server.ListenerOptions{ProxyProtocol: true}},
	}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	s := cfg.Server
	if s.ConnectionTimeout != 500*time.Millisecond || s.HandshakeTimeout != 200*time.Millisecond ||
		s.MaxConnections != 0 || s.MaxConnectionsPerIP != 2 || s.ConnectionRateLimitPerIP != 3 || s.LogRateLimit != 0 || s.SoftwareSignature {
		t.Errorf("unexpected server config: %+v", s)
	}
	if cfg.LogFormat != logging.FormatJSON || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("logFormat = %q, logLevel = %v, want json and DEBUG", cfg.LogFormat, cfg.LogLevel)
	}
	// The status of a variable is the whole status, as in the file.
	want := `{"description":"Hello","favicon":"data:image/png;base64,iVBORw0KGgo=",` +
		`"players":{"max":100,"online":2}}`
	if got := statusJSON(t, cfg, "default"); got != want {
		t.Errorf("default status = %s, want %s", got, want)
	}
	if got, want := statusJSON(t, cfg, "maintenance"), `{"description":"Maintenance","version":{"name":"Fake","protocol":-1}}`; got != want {
		t.Errorf("maintenance status = %s, want %s", got, want)
	}
	if want := []server.Rule{
		{Name: "lobby", Conditions: hostConditions(t, "lobby.example.com"), Server: "lobby"},
		{Name: "1", Server: "maintenance"},
		{Name: "default", Server: "default"},
	}; !reflect.DeepEqual(cfg.Server.Rules, want) {
		t.Errorf("Rules = %v, want %v", cfg.Server.Rules, want)
	}
	if vs := cfg.Server.Servers["maintenance"]; vs.ClientProtocol != status.ClientProtocolNever || string(vs.Kick) != `{"text":"Closed"}` {
		t.Errorf("maintenance server = %+v, want its client protocol and kick message", vs)
	}
	// The lobby server, without a kick message, gets the built-in one, never
	// the one of the default server.
	for id, want := range map[string]string{"default": `{"color":"red","text":"Bye"}`, "lobby": `{"text":"§cNot available"}`} {
		if got := string(cfg.Server.Servers[id].Kick); got != want {
			t.Errorf("server %s: kick message = %s, want %s", id, got, want)
		}
	}
	if got := cfg.Server.Upstreams["lobby"]; got.Host != "lobby.example.com" || got.Interval != time.Minute {
		t.Errorf("lobby upstream = %+v, want lobby.example.com polled every minute", got)
	}
}

// Variables take the same values as the file, except for text and lists.
func TestVariablesReadLikeFile(t *testing.T) {
	cfg, err := loadFile(t, "connections: { max_per_ip: 2 }\nservers:\n  default: { kick: Bye }\n", map[string]string{
		"FMS_SERVERS_DEFAULT":        "{ kick: null }",
		"FMS_CONNECTIONS_MAX":        "0x10",
		"FMS_CONNECTIONS_MAX_PER_IP": "null",
		"FMS_LOG_RATE_LIMIT":         "1_000",
	})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	s := cfg.Server
	// Null unsets the setting of the file.
	if got := string(s.Servers["default"].Kick); got != `{"text":"§cNot available"}` {
		t.Errorf("default kick message = %s, want the built-in one", got)
	}
	if s.MaxConnections != 16 || s.MaxConnectionsPerIP != 8 || s.LogRateLimit != 1000 {
		t.Errorf("MaxConnections = %d, MaxConnectionsPerIP = %d, LogRateLimit = %d, want 16, the default and 1000", s.MaxConnections, s.MaxConnectionsPerIP, s.LogRateLimit)
	}
}

func TestVariablesEmptyAreUnset(t *testing.T) {
	cfg, err := loadFile(t, "connections: { max: 10 }\nupstreams:\n  lobby: { address: lobby.example.com }\n",
		map[string]string{"FMS_LISTEN": "", "FMS_CONNECTIONS_MAX": "", "FMS_UPSTREAMS_LOBBY": ""})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []Listener{{Network: "tcp", Address: ":25565"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	if cfg.Server.MaxConnections != 10 || len(cfg.Server.Upstreams) != 1 {
		t.Errorf("MaxConnections = %d, upstreams = %v, want the ones of the file", cfg.Server.MaxConnections, cfg.Server.Upstreams)
	}
}

// Variables override the settings of the file, and are merged into its
// entries.
func TestVariablesOverrideFile(t *testing.T) {
	path := writeConfigFile(t, `
listen: [":25566", { address: ":25567", proxy_protocol: true, trusted_proxies: [10.0.0.0/8] }]
connections: { max: 10, max_per_ip: 2 }
servers:
  default:
    status:
      description: Hello
      favicon: icon.png
      players: { max: 100, online: 2, sample: [{ name: Notch, id: 069a79f4-44e9-4726-a5be-fca90e38aaf5 }] }
      version: { name: "1.21.10", protocol: 773 }
  lobby:
    kick: Lobby down
upstreams:
  lobby: { address: lobby.example.com, interval: 1m, handshake: { host: play.example.com } }
  creative: { address: creative.example.com }
rules: [{ server: lobby }, { server: default }]
`)
	// The favicon of the file stays relative to the file.
	t.Chdir(t.TempDir())
	cfg, err := loadEnv(map[string]string{
		"FMS_CONFIG_FILE":         path,
		"FMS_LISTEN":              ":25568",
		"FMS_RULES":               "[{ server: creative }]",
		"FMS_CONNECTIONS_MAX":     "20",
		"FMS_SERVERS_DEFAULT":     `{"status":{"players":{"online":5,"sample":null},"version":{"name":"Fake"}},"client_protocol":"always"}`,
		"FMS_SERVERS_MAINTENANCE": "{ status: { description: Maintenance } }",
		"FMS_UPSTREAMS_LOBBY":     "{ address: lobby2.example.com, handshake: { port: 25566 } }",
		"FMS_UPSTREAMS_HUB":       "{ address: hub.example.com }",
	})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if s := cfg.Server; s.MaxConnections != 20 || s.MaxConnectionsPerIP != 2 {
		t.Errorf("MaxConnections = %d, MaxConnectionsPerIP = %d, want 20 from the variables, and 2 from the file",
			s.MaxConnections, s.MaxConnectionsPerIP)
	}
	// Lists replace the ones of the file, with their options.
	if want := []Listener{{Network: "tcp", Address: ":25568"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	if want := []server.Rule{{Name: "0", Server: "creative"}, {Name: "default", Server: "default"}}; !reflect.DeepEqual(cfg.Server.Rules, want) {
		t.Errorf("Rules = %v, want %v", cfg.Server.Rules, want)
	}
	// The options of a server replace the ones of the file, and the others
	// are kept.
	if vs := cfg.Server.Servers["default"]; vs.ClientProtocol != status.ClientProtocolAlways {
		t.Errorf("default server = %+v, want the client protocol of the variable", vs)
	}
	if vs := cfg.Server.Servers["lobby"]; string(vs.Kick) != `{"text":"Lobby down"}` {
		t.Errorf("lobby server = %+v, want the kick message of the file", vs)
	}

	want := `{"description":"Hello","favicon":"data:image/png;base64,iVBORw0KGgo=",` +
		`"players":{"max":100,"online":5},"version":{"name":"Fake","protocol":773}}`
	if got := statusJSON(t, cfg, "default"); got != want {
		t.Errorf("default status = %s, want %s", got, want)
	}
	if got, want := statusJSON(t, cfg, "maintenance"), `{"description":"Maintenance"}`; got != want {
		t.Errorf("maintenance status = %s, want %s", got, want)
	}

	lobby := cfg.Server.Upstreams["lobby"]
	if lobby.Host != "lobby2.example.com" || lobby.ServerAddress != "play.example.com" || lobby.ServerPort != 25566 || lobby.Interval != time.Minute {
		t.Errorf("lobby upstream = %+v, want lobby2.example.com, with the handshake port of the variable, the handshake host"+
			" and interval of the file", lobby)
	}
	if got := slices.Sorted(maps.Keys(cfg.Server.Upstreams)); !slices.Equal(got, []string{"creative", "hub", "lobby"}) {
		t.Errorf("upstreams = %v, want creative, hub and lobby", got)
	}
}

// Errors must tell what is expected, with the same words for variables and
// for the file. Entries written on a single line need no line numbers.
func TestVariableErrors(t *testing.T) {
	tests := []struct {
		name, value, want string
	}{
		{"FMS_CONNECTIONS_MAX", "many", "must be an integer, got `many`"},
		{"FMS_CONNECTIONS_MAX", "5.5", "must be an integer, got `5.5`"},
		{"FMS_CONNECTIONS_TIMEOUT", "3", "must be a duration such as \"3s\" or \"500ms\", got `3`"},
		{"FMS_LISTEN", " , ", `must list at least one value, got " , "`},
		{"FMS_LISTEN", `[{ address: ":25565", proxy_protocol: 1 }]`, "must be true or false, got `1`"},
		{"FMS_LISTEN", `[{ address: ":25565", proxy: true }]`, "unknown key proxy"},
		{"FMS_SERVERS_DEFAULT", `{"status":`, "did not find expected node content"},
		{"FMS_SERVERS_DEFAULT", "status: { description: Hello }\nkick: [\n", "line 2: did not find expected node content"},
		{"FMS_SERVERS_DEFAULT", "Hello", "must be a mapping, got `Hello`"},
		{"FMS_SERVERS_DEFAULT", "{ kick: Bye } }", "unexpected content after the value"},
		{"FMS_SERVERS_DEFAULT", "{\n  kick: Bye\n}\n}\n", "line 4: unexpected content after the value"},
		{"FMS_SERVERS_DEFAULT", "kick: Bye\n---\nkick: Hi\n", "line 2: only one YAML document is allowed"},
		{"FMS_RULES", "[{ server: default }]]", "unexpected content after the value"},
		{"FMS_SERVERS_LOBBY", "{ upstream: { address: lobby.example.com } }", "must be text, got a mapping"},
		{"FMS_UPSTREAMS_LOBBY", "lobby.example.com", "must be a mapping, got `lobby.e...`"},
		{"FMS_UPSTREAMS_LOBBY", "{ address: lobby.example.com, port: 25565 }", "unknown key port"},
		{"FMS_UPSTREAMS_LOBBY", "{ address: lobby.example.com, interval: 10 }", "must be a duration such as \"3s\" or \"500ms\", got `10`"},
	}
	for _, tc := range tests {
		if _, err := loadEnv(map[string]string{tc.name: tc.value}); err == nil || err.Error() != tc.name+": "+tc.want {
			t.Errorf("%s=%q: error = %v, want %s: %s", tc.name, tc.value, err, tc.name, tc.want)
		}
	}
}

// All the mistakes are reported at once: the ones of the variables that
// cannot be parsed, whose settings are left as in the file, then the ones of
// the settings.
func TestVariablesReportAllErrors(t *testing.T) {
	_, err := loadFile(t, "connections: { max_per_ip: -1 }\nupstreams:\n  lobby: { address: lobby.example.com:0 }\n", map[string]string{
		"FMS_LISTEN": "unix:",

		"FMS_CONNECTIONS_TIMEOUT":           "3",
		"FMS_CONNECTIONS_HANDSHAKE_TIMEOUT": "-1s",
		"FMS_CONNECTIONS_MAX":               "-1",
		"FMS_CONNECTIONS_MAX_PER_IP":        "x",

		"FMS_LOG_FORMAT":     "xml",
		"FMS_LOG_LEVEL":      "verbose",
		"FMS_LOG_RATE_LIMIT": "-1",

		"FMS_SERVERS_DEFAULT": "{ status: { players: 1 } }",
		"FMS_UPSTREAMS_LOBBY": "{ timeout: 1 }",
		"FMS_RULES":           "{ server: default }",
	})
	want := []string{
		"FMS_CONNECTIONS_TIMEOUT: must be a duration such as \"3s\" or \"500ms\", got `3`",
		"FMS_CONNECTIONS_MAX_PER_IP: must be an integer, got `x`",
		"FMS_UPSTREAMS_LOBBY: must be a duration such as \"3s\" or \"500ms\", got `1`",
		"FMS_RULES: must be a list, got a mapping",
		`listen.0: "unix:" has no socket path`,
		"connections.handshake_timeout: must be positive and at most the connection timeout (3s), got -1s",
		"connections.max: must be between 0 and 2147483647, got -1",
		"connections.max_per_ip: must be between 0 and 2147483647, got -1",
		`log.format: must be text or json, got "xml"`,
		`log.level: must be debug, info, warn or error, got "verbose"`,
		"log.rate_limit: must be between 0 and 2147483647, got -1",
		`upstreams.lobby.address: "lobby.example.com:0" has an invalid port, must be between 1 and 65535`,
		"servers.default.status.players: must be a JSON object, got 1",
	}
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Load() error = %v, want %s", err, strings.Join(want, "\n"))
	}
}

// The configuration is returned with its mistakes, so that its log settings
// apply to them.
func TestLogSettingsWithErrors(t *testing.T) {
	cfg, err := loadEnv(map[string]string{"FMS_LOG_FORMAT": "json", "FMS_LOG_LEVEL": "debug", "FMS_CONNECTIONS_MAX": "-1"})
	if err == nil || cfg.LogFormat != logging.FormatJSON || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("logFormat = %q, logLevel = %v, %v, want json, DEBUG and an error", cfg.LogFormat, cfg.LogLevel, err)
	}
	if cfg, err := loadEnv(map[string]string{"FMS_LOG_FORMAT": "xml"}); err == nil || cfg.LogFormat != "" {
		t.Errorf("logFormat = %q, %v, want none and an error", cfg.LogFormat, err)
	}
}

func TestFileYAML(t *testing.T) {
	cfg, err := loadFile(t, `
# Null values are unset.
listen:
connections:
  max:
  max_per_ip: &limit 4
log:
  rate_limit: *limit
servers:
  maintenance:
    status: &maintenance
      description: Maintenance
      version: { name: Fake, protocol: 0x1F }
  modded:
    status:
      <<: *maintenance
      description: Modded
  copy:
    status: *maintenance
`, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if want := []Listener{{Network: "tcp", Address: ":25565"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("Listeners = %v, want %v", cfg.Listeners, want)
	}
	if s := cfg.Server; s.MaxConnections != 10000 || s.MaxConnectionsPerIP != 4 || s.LogRateLimit != 4 {
		t.Errorf("MaxConnections = %d, MaxConnectionsPerIP = %d, LogRateLimit = %d, want the default, 4 and 4",
			s.MaxConnections, s.MaxConnectionsPerIP, s.LogRateLimit)
	}
	for id, want := range map[string]string{
		"maintenance": `{"description":"Maintenance","version":{"name":"Fake","protocol":31}}`,
		"modded":      `{"description":"Modded","version":{"name":"Fake","protocol":31}}`,
		"copy":        `{"description":"Maintenance","version":{"name":"Fake","protocol":31}}`,
	} {
		if got := statusJSON(t, cfg, id); got != want {
			t.Errorf("%s status = %s, want %s", id, got, want)
		}
	}
}

func TestFileEmpty(t *testing.T) {
	for _, content := range []string{"", "# Nothing set yet.\n"} {
		cfg, err := loadFile(t, content, nil)
		if err != nil || len(cfg.Server.Upstreams) != 0 || cfg.Server.MaxConnections != 10000 {
			t.Errorf("Load() with %q = %+v, %v, want the defaults", content, cfg, err)
		}
	}
}

// Errors found when decoding the file tell their line.
func TestFileErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"not a mapping", "- listen\n", "line 1: must be a mapping, got a list"},
		{"content after the value", "{ log: { level: info } }\n}\n", "line 2: unexpected content after the value"},
		{"several documents", "log: { level: info }\n---\nlog: { level: debug }\n", "line 2: only one YAML document is allowed"},
		{"duplicate key", "log:\n  level: info\n  level: debug\n", `line 3: mapping key "level" already defined at line 2`},
		{"unknown key", "listen: [\":25565\"]\nother: 1\n", "line 2: unknown key other"},
		{"unknown nested key", "connections:\n  timout: 5s\n", "line 2: unknown key timout"},
		{"variable name", "FMS_LOG_LEVEL: info\n", "line 1: unknown key FMS_LOG_LEVEL"},
		{"status variable", "motd: Hi\n", "line 1: unknown key motd"},
		{"invalid integer", "connections: { max: many }\n", "line 1: must be an integer, got `many`"},
		{"float as integer", "connections: { max: 5.5 }\n", "line 1: must be an integer, got `5.5`"},
		{"invalid duration", "connections:\n  timeout: 3\n", "line 2: must be a duration such as \"3s\" or \"500ms\", got `3`"},
		{"invalid boolean", "listen: [{ address: \":25565\", proxy_protocol: maybe }]\n", "line 1: must be true or false, got `maybe`"},
		{"unknown listener key", "listen:\n  - { address: \":25565\", proxy: true }\n", "line 2: unknown key proxy"},
		{"list as a single value", "listen: \":25565\"\n", "line 1: must be a list, got `:25565`"},
		{"single value as a list", "connections: { max: [1] }\n", "line 1: must be an integer, got a list"},
		{"section as a single value", "connections: 5\n", "line 1: must be a mapping, got `5`"},
		{"mapping as text", "log: { level: { name: info } }\n", "line 1: must be text, got a mapping"},
		{"servers not a mapping", "servers: [a]\n", "line 1: must be a mapping, got a list"},
		{"server not a mapping", "servers:\n  default: Hello\n", "line 2: must be a mapping, got `Hello`"},
		{"status not a mapping", "servers:\n  default: { status: Hello }\n", "line 2: must be a mapping, got `Hello`"},
		{"server unknown option", "servers:\n  lobby: { kick: Bye, port: 25565 }\n", "line 2: unknown key port"},
		{"rules not a list", "rules: { server: default }\n", "line 1: must be a list, got a mapping"},
		{"rule unknown option", "rules: [{ server: default, port: 25565 }]\n", "line 1: unknown key port"},
		{"host as a single value", "rules: [{ host: a.example.com, server: default }]\n", "line 1: must be a list, got `a.examp...`"},
		{"upstream unknown option", "upstreams:\n  lobby: { address: lobby.example.com, port: 25565 }\n", "line 2: unknown key port"},
		{"invalid protocol version", "upstreams:\n  lobby:\n    address: lobby.example.com\n    handshake: { protocol: new }\n", "line 4: must be an integer, got `new`"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadFile(t, tc.content, nil); err == nil || err.Error() != "FMS_CONFIG_FILE: "+tc.want {
				t.Errorf("Load() error = %v, want FMS_CONFIG_FILE: %s", err, tc.want)
			}
		})
	}
}

// All the mistakes of the file are reported at once.
func TestFileReportsAllErrors(t *testing.T) {
	_, err := loadFile(t, `
connections:
  timeout: 3
  max: -1
log:
  level: verbose
  other: 1
`, nil)
	want := []string{
		"FMS_CONFIG_FILE: line 3: must be a duration such as \"3s\" or \"500ms\", got `3`",
		"FMS_CONFIG_FILE: line 7: unknown key other",
		"connections.max: must be between 0 and 2147483647, got -1",
		`log.level: must be debug, info, warn or error, got "verbose"`,
	}
	if err == nil || err.Error() != strings.Join(want, "\n") {
		t.Errorf("Load() error = %v, want %s", err, strings.Join(want, "\n"))
	}
}

func TestFileSyntaxError(t *testing.T) {
	_, err := loadFile(t, "log: { level: info }\nlisten: [\":25565\"\n", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "FMS_CONFIG_FILE: line ") {
		t.Errorf("Load() error = %v, want a FMS_CONFIG_FILE error starting with its line", err)
	}
}

func TestFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	_, err := loadEnv(map[string]string{"FMS_CONFIG_FILE": path})
	if want := "FMS_CONFIG_FILE: open " + path + ": no such file or directory"; err == nil || err.Error() != want {
		t.Errorf("Load() error = %v, want %s", err, want)
	}
}
