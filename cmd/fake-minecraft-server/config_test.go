package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
)

func lookupMap(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := vars[key]
		return value, ok
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(lookupMap(nil))
	if err != nil {
		t.Fatalf("loadConfig() error: %v", err)
	}
	if want := []listenAddr{{"tcp", ":25565"}}; !slices.Equal(cfg.listenAddrs, want) || len(cfg.warnings) != 0 {
		t.Errorf("listenAddrs = %v, warnings = %q, want %v and no warnings", cfg.listenAddrs, cfg.warnings, want)
	}
	if got := string(cfg.server.KickMessage); got != `{"text":"§cNot available"}` {
		t.Errorf("KickMessage = %s", got)
	}
	if got := string(cfg.server.MOTD); got != `{"text":"§eHello World!"}` {
		t.Errorf("MOTD = %s", got)
	}
	if cfg.server.MaxPlayers != 0 || cfg.server.OnlinePlayers != 0 || cfg.server.ProtocolVersion != 0 ||
		cfg.server.MirrorProtocolVersion || cfg.server.VersionName != "" || cfg.server.Favicon != "" {
		t.Errorf("unexpected non-default server config: %+v", cfg.server)
	}
	if cfg.server.ConnectionTimeout != 3*time.Second || cfg.server.HandshakeTimeout != time.Second {
		t.Errorf("ConnectionTimeout = %v, HandshakeTimeout = %v", cfg.server.ConnectionTimeout, cfg.server.HandshakeTimeout)
	}
	if cfg.logFormat != logging.FormatText {
		t.Errorf("logFormat = %q, want text", cfg.logFormat)
	}
	if cfg.logLevel != slog.LevelInfo || cfg.server.LogRateLimit != 1000 {
		t.Errorf("logLevel = %v, LogRateLimit = %d, want INFO and 1000", cfg.logLevel, cfg.server.LogRateLimit)
	}
}

func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig(lookupMap(map[string]string{
		"LISTEN_ADDR":      "[::1]:25566, unix:/run/fake-minecraft-server.sock",
		"KICK_MESSAGE":     "Bye",
		"MOTD":             `{"text":"Hi","color":"gold"}`,
		"FAVICON":          "data:image/png;base64,AAAA",
		"MAX_PLAYERS":      "100",
		"ONLINE_PLAYERS":   "-5",
		"PROTOCOL_NAME":    "Maintenance",
		"PROTOCOL_VERSION": "773",

		"CONNECTION_TIMEOUT": "500ms",
		"HANDSHAKE_TIMEOUT":  "200ms",
		"LOG_FORMAT":         "json",
		"LOG_LEVEL":          "debug",
		"LOG_RATE_LIMIT":     "0",
	}))
	if err != nil {
		t.Fatalf("loadConfig() error: %v", err)
	}
	if want := []listenAddr{{"tcp", "[::1]:25566"}, {"unix", "/run/fake-minecraft-server.sock"}}; !slices.Equal(cfg.listenAddrs, want) {
		t.Errorf("listenAddrs = %v, want %v", cfg.listenAddrs, want)
	}
	s := cfg.server
	if string(s.KickMessage) != `{"text":"Bye"}` || string(s.MOTD) != `{"text":"Hi","color":"gold"}` {
		t.Errorf("KickMessage = %s, MOTD = %s", s.KickMessage, s.MOTD)
	}
	if s.Favicon != "data:image/png;base64,AAAA" || s.MaxPlayers != 100 || s.OnlinePlayers != -5 ||
		s.VersionName != "Maintenance" || s.ProtocolVersion != 773 || s.MirrorProtocolVersion {
		t.Errorf("unexpected server config: %+v", s)
	}
	if cfg.logFormat != logging.FormatJSON {
		t.Errorf("logFormat = %q, want json", cfg.logFormat)
	}
	if s.ConnectionTimeout != 500*time.Millisecond || s.HandshakeTimeout != 200*time.Millisecond ||
		cfg.logLevel != slog.LevelDebug || s.LogRateLimit != 0 {
		t.Errorf("ConnectionTimeout = %v, HandshakeTimeout = %v, logLevel = %v, LogRateLimit = %d",
			s.ConnectionTimeout, s.HandshakeTimeout, cfg.logLevel, s.LogRateLimit)
	}
}

func TestLoadConfigEmptyValuesUseDefaults(t *testing.T) {
	cfg, err := loadConfig(lookupMap(map[string]string{"LISTEN_ADDR": "", "LISTEN_PORT": "", "MOTD": ""}))
	if err != nil {
		t.Fatalf("loadConfig() error: %v", err)
	}
	if want := []listenAddr{{"tcp", ":25565"}}; !slices.Equal(cfg.listenAddrs, want) || string(cfg.server.MOTD) != `{"text":"§eHello World!"}` {
		t.Errorf("listenAddrs = %v, MOTD = %s", cfg.listenAddrs, cfg.server.MOTD)
	}
}

func TestLoadConfigListenAddrs(t *testing.T) {
	tests := []struct {
		value string
		want  []listenAddr
	}{
		{":25565", []listenAddr{{"tcp", ":25565"}}},
		{"0.0.0.0:25565, [::]:25566", []listenAddr{{"tcp", "0.0.0.0:25565"}, {"tcp", "[::]:25566"}}},
		{"unix:/run/a.sock,unix:@abstract", []listenAddr{{"unix", "/run/a.sock"}, {"unix", "@abstract"}}},
		{":25565,,", []listenAddr{{"tcp", ":25565"}}},
	}
	for _, tc := range tests {
		cfg, err := loadConfig(lookupMap(map[string]string{"LISTEN_ADDR": tc.value}))
		if err != nil || !slices.Equal(cfg.listenAddrs, tc.want) {
			t.Errorf("LISTEN_ADDR=%q: listenAddrs = %v, %v, want %v", tc.value, cfg.listenAddrs, err, tc.want)
		}
	}
}

func TestLoadConfigListenAddrErrors(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"0.0.0.0", `LISTEN_ADDR: "0.0.0.0" must be host:port or unix:path`},
		{"[::1]", `LISTEN_ADDR: "[::1]" must be host:port or unix:path`},
		{":0", `LISTEN_ADDR: ":0" has an invalid port, must be between 1 and 65535`},
		{":70000", `LISTEN_ADDR: ":70000" has an invalid port, must be between 1 and 65535`},
		{":minecraft", `LISTEN_ADDR: ":minecraft" has an invalid port, must be between 1 and 65535`},
		{"unix:", `LISTEN_ADDR: "unix:" has no socket path`},
		{":25565, unix:", `LISTEN_ADDR: "unix:" has no socket path`},
		{"unix:/run/a.sock, :25565, unix:/run/a.sock", `LISTEN_ADDR: lists "unix:/run/a.sock" twice`},
		{" , ", `LISTEN_ADDR: must list at least one address, got " , "`},
	}
	for _, tc := range tests {
		if _, err := loadConfig(lookupMap(map[string]string{"LISTEN_ADDR": tc.value})); err == nil || err.Error() != tc.want {
			t.Errorf("LISTEN_ADDR=%q: error = %v, want %s", tc.value, err, tc.want)
		}
	}
}

func TestLoadConfigDeprecatedListenHostPort(t *testing.T) {
	cfg, err := loadConfig(lookupMap(map[string]string{"LISTEN_HOST": "::1", "LISTEN_PORT": "25566"}))
	if want := []listenAddr{{"tcp", "[::1]:25566"}}; err != nil || !slices.Equal(cfg.listenAddrs, want) {
		t.Errorf("listenAddrs = %v, %v, want %v", cfg.listenAddrs, err, want)
	}
	wantWarnings := []string{"LISTEN_HOST and LISTEN_PORT are deprecated, use LISTEN_ADDR=[::1]:25566 instead"}
	if !slices.Equal(cfg.warnings, wantWarnings) {
		t.Errorf("warnings = %q, want %q", cfg.warnings, wantWarnings)
	}

	_, err = loadConfig(lookupMap(map[string]string{"LISTEN_PORT": "70000"}))
	if err == nil || !strings.Contains(err.Error(), "LISTEN_PORT:") {
		t.Errorf("loadConfig() error = %v, want a LISTEN_PORT error", err)
	}

	_, err = loadConfig(lookupMap(map[string]string{"LISTEN_ADDR": ":25565", "LISTEN_PORT": "25566"}))
	if want := "LISTEN_ADDR: cannot be combined with the deprecated LISTEN_HOST and LISTEN_PORT"; err == nil || err.Error() != want {
		t.Errorf("loadConfig() error = %v, want %s", err, want)
	}
}

func TestLoadConfigClientProtocolVersion(t *testing.T) {
	cfg, err := loadConfig(lookupMap(map[string]string{"PROTOCOL_VERSION": "client"}))
	if err != nil {
		t.Fatalf("loadConfig() error: %v", err)
	}
	if !cfg.server.MirrorProtocolVersion {
		t.Error("MirrorProtocolVersion = false, want true")
	}
}

func TestLoadConfigReportsAllErrors(t *testing.T) {
	_, err := loadConfig(lookupMap(map[string]string{
		"LISTEN_ADDR":      "unix:",
		"MAX_PLAYERS":      "many",
		"ONLINE_PLAYERS":   "3000000000",
		"PROTOCOL_VERSION": "1.21",
		"FAVICON":          filepath.Join(t.TempDir(), "missing.png"),

		"CONNECTION_TIMEOUT": "3",
		"HANDSHAKE_TIMEOUT":  "-1s",
		"LOG_FORMAT":         "xml",
		"LOG_LEVEL":          "verbose",
		"LOG_RATE_LIMIT":     "-1",
	}))
	if err == nil {
		t.Fatal("loadConfig() succeeded, want an error")
	}
	for _, key := range []string{
		"LISTEN_ADDR", "MAX_PLAYERS", "ONLINE_PLAYERS", "PROTOCOL_VERSION", "FAVICON",
		"CONNECTION_TIMEOUT", "HANDSHAKE_TIMEOUT", "LOG_FORMAT", "LOG_LEVEL", "LOG_RATE_LIMIT",
	} {
		if !strings.Contains(err.Error(), key+":") {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

// Errors must tell what is expected, in the terms of the variables.
func TestLoadConfigErrorMessages(t *testing.T) {
	notPNG := filepath.Join(t.TempDir(), "icon.jpg")
	if err := os.WriteFile(notPNG, []byte("\xff\xd8\xff"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"PROTOCOL_VERSION": "1.21"}, `PROTOCOL_VERSION: must be an integer or "client", got "1.21"`},
		{map[string]string{"LOG_LEVEL": "verbose"}, `LOG_LEVEL: must be debug, info, warn or error, got "verbose"`},
		{map[string]string{"FAVICON": notPNG}, "FAVICON: " + notPNG + " is not a PNG file"},
	}
	for _, tc := range tests {
		if _, err := loadConfig(lookupMap(tc.vars)); err == nil || err.Error() != tc.want {
			t.Errorf("loadConfig(%v) error = %v, want %s", tc.vars, err, tc.want)
		}
	}
}

func TestLoadConfigHandshakeTimeout(t *testing.T) {
	// The default handshake timeout must not exceed a short connection timeout.
	cfg, err := loadConfig(lookupMap(map[string]string{"CONNECTION_TIMEOUT": "500ms"}))
	if err != nil || cfg.server.HandshakeTimeout != 500*time.Millisecond {
		t.Errorf("HandshakeTimeout = %v, %v, want 500ms", cfg.server.HandshakeTimeout, err)
	}

	_, err = loadConfig(lookupMap(map[string]string{"CONNECTION_TIMEOUT": "2s", "HANDSHAKE_TIMEOUT": "5s"}))
	if err == nil || !strings.Contains(err.Error(), "HANDSHAKE_TIMEOUT:") {
		t.Errorf("loadConfig() error = %v, want a HANDSHAKE_TIMEOUT error", err)
	}
}

func TestTextComponent(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain", `{"text":"plain"}`},
		{`say "hi"`, `{"text":"say \"hi\""}`},
		{"§cRed\nLine", `{"text":"§cRed\nLine"}`},
		{`{"text":"json","bold":true}`, `{"text":"json","bold":true}`},
		{`{not json`, `{"text":"{not json"}`},
		{`["array"]`, `{"text":"[\"array\"]"}`},
	}
	for _, tc := range tests {
		got := textComponent(tc.input)
		if string(got) != tc.want {
			t.Errorf("textComponent(%q) = %s, want %s", tc.input, got, tc.want)
		}
		if !json.Valid(got) {
			t.Errorf("textComponent(%q) is not valid JSON", tc.input)
		}
	}
}

func TestLoadConfigFavicon(t *testing.T) {
	path := filepath.Join(t.TempDir(), "icon.png")
	if err := os.WriteFile(path, pngSignature, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"data:image/png;base64,AAAA", "data:image/png;base64,AAAA"},
		{path, "data:image/png;base64,iVBORw0KGgo="},
	}
	for _, tc := range tests {
		cfg, err := loadConfig(lookupMap(map[string]string{"FAVICON": tc.input}))
		if err != nil || cfg.server.Favicon != tc.want {
			t.Errorf("FAVICON=%q: Favicon = %q, %v, want %q", tc.input, cfg.server.Favicon, err, tc.want)
		}
	}
}
