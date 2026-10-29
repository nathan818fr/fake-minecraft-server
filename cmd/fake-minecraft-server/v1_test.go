package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// loadV1 loads the configuration of env once its v1 variables are replaced.
func loadV1(t *testing.T, env ...string) (config.Config, v1Setup) {
	t.Helper()
	environ, v := fromV1(env)
	cfg, err := config.Load(environ)
	if err != nil {
		t.Fatalf("%v, with %q", err, v.replacement)
	}
	return cfg, v
}

// v1Component returns the text component that v1 sent for text.
func v1Component(t *testing.T, text string) any {
	t.Helper()
	var component any
	if strings.HasPrefix(text, "{") && json.Unmarshal([]byte(text), &component) == nil {
		return component
	}
	return map[string]any{"text": text}
}

// component returns data, JSON, as a text component, with plain text as a
// component of text alone, which clients show the same.
func component(t *testing.T, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if text, ok := v.(string); ok {
		return map[string]any{"text": text}
	}
	return v
}

func description(t *testing.T, st *status.Status) any {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(st.AppendJSON(nil), &fields); err != nil {
		t.Fatal(err)
	}
	return component(t, fields["description"])
}

func TestFromV1(t *testing.T) {
	cfg, v := loadV1(t,
		"LISTEN_HOST=::1",
		"LISTEN_PORT=25566",
		"MOTD=§eWelcome to My Server!\n§dJoin now!",
		"PROTOCOL_NAME=§cMaintenance",
		"PROTOCOL_VERSION=-1",
		"MAX_PLAYERS=100",
		"ONLINE_PLAYERS= 42 ",
		`KICK_MESSAGE={"text":"Sorry","color":"red"}`,
		"PATH=/bin",
	)
	if v.errs != nil {
		t.Errorf("errors: %v", v.errs)
	}
	if want := []config.Listener{{Network: "tcp", Address: "[::1]:25566"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("listeners: got %v, want %v", cfg.Listeners, want)
	}
	vs := cfg.Server.Servers["default"]
	if got, want := vs.Status.Version(), (status.Version{Name: "§cMaintenance", Protocol: -1}); got != want {
		t.Errorf("version: got %v, want %v", got, want)
	}
	if got, want := vs.Status.Players(), (status.Players{Max: 100, Online: 42}); got != want {
		t.Errorf("players: got %v, want %v", got, want)
	}
	if got, want := description(t, vs.Status), v1Component(t, "§eWelcome to My Server!\n§dJoin now!"); !reflect.DeepEqual(got, want) {
		t.Errorf("description: got %v, want %v", got, want)
	}
	if got, want := component(t, vs.Kick), v1Component(t, `{"text":"Sorry","color":"red"}`); !reflect.DeepEqual(got, want) {
		t.Errorf("kick: got %v, want %v", got, want)
	}
}

func TestFromV1Defaults(t *testing.T) {
	cfg, v := loadV1(t, "LISTEN_BACKLOG=511")
	if len(v.errs) != 1 || !strings.HasPrefix(v.errs[0].Error(), "LISTEN_BACKLOG: ") {
		t.Errorf("errors: got %v, want one about LISTEN_BACKLOG", v.errs)
	}
	if want := []config.Listener{{Network: "tcp", Address: ":25565"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("listeners: got %v, want %v", cfg.Listeners, want)
	}
	vs := cfg.Server.Servers["default"]
	if got := vs.Status.Version(); got != (status.Version{}) {
		t.Errorf("version: got %v, want v1's empty name and protocol 0", got)
	}
	if got, want := description(t, vs.Status), v1Component(t, "§eHello World!"); !reflect.DeepEqual(got, want) {
		t.Errorf("description: got %v, want %v", got, want)
	}
	if got, want := component(t, vs.Kick), v1Component(t, "§cNot available"); !reflect.DeepEqual(got, want) {
		t.Errorf("kick: got %v, want %v", got, want)
	}
}

// TestFromV1Text checks that texts reach the status and the kick message as v1
// sent them, through the YAML of the replacement.
func TestFromV1Text(t *testing.T) {
	for _, text := range []string{
		"plain", "a: b", "a, b", "#hash", "- dash", "yes", "null", "123", "'quoted'",
		`"double" \ back`, "trailing ", "\ttab", "line\nbreak", "{not json", `{"text":"json","bold":true}`,
		`["a",{"text":"b"}]`, "§", "日本語",
	} {
		cfg, _ := loadV1(t, "MOTD="+text, "KICK_MESSAGE="+text)
		vs := cfg.Server.Servers["default"]
		if got, want := description(t, vs.Status), v1Component(t, text); !reflect.DeepEqual(got, want) {
			t.Errorf("description of %q: got %v, want %v", text, got, want)
		}
		if got, want := component(t, vs.Kick), v1Component(t, text); !reflect.DeepEqual(got, want) {
			t.Errorf("kick of %q: got %v, want %v", text, got, want)
		}
	}
}

func TestFromV1Invalid(t *testing.T) {
	cfg, v := loadV1(t, "LISTEN_PORT=70000", "MAX_PLAYERS=many", "FAVICON=missing.png")
	if len(v.errs) != 3 {
		t.Errorf("errors: got %v, want 3", v.errs)
	}
	if want := []config.Listener{{Network: "tcp", Address: ":25565"}}; !reflect.DeepEqual(cfg.Listeners, want) {
		t.Errorf("listeners: got %v, want %v", cfg.Listeners, want)
	}
	vs := cfg.Server.Servers["default"]
	if got := vs.Status.Players(); got != (status.Players{}) {
		t.Errorf("players: got %v, want 0", got)
	}
	if _, ok := vs.Status.Get(status.Path{"favicon"}); ok {
		t.Error("the favicon that cannot be read is kept")
	}
}

func TestFromV1Favicon(t *testing.T) {
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewGray(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "icon.png")
	if err := os.WriteFile(path, icon.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, v := loadV1(t, "FAVICON="+path)
	if v.errs != nil {
		t.Errorf("errors: %v", v.errs)
	}
	if _, ok := cfg.Server.Servers["default"].Status.Get(status.Path{"favicon"}); !ok {
		t.Error("no favicon")
	}
}

func TestFromV1Ignored(t *testing.T) {
	for _, env := range [][]string{
		{"MOTD=Hello", "FMS_LOG_LEVEL=debug"},
		{"MOTD=Hello", "FMS_CONFIG_FILE=/etc/fms.yaml"},
		{"PATH=/bin", "LISTEN_PORT="},
	} {
		environ, v := fromV1(env)
		if !reflect.DeepEqual(environ, env) {
			t.Errorf("%q: got %q, want it unchanged", env, environ)
		}
		if v.replacement != nil {
			t.Errorf("%q: replaced by %q", env, v.replacement)
		}
	}
}

// TestV1Report checks that the replacement, as printed, gives the variables
// that the server read when pasted into a shell.
func TestV1Report(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip(err)
	}
	_, v := fromV1([]string{"LISTEN_HOST=::1", `MOTD=it's "quoted" $HOME \ back`, "KICK_MESSAGE=§cSorry, come back later."})
	var out bytes.Buffer
	v.report(&out, nil)
	var printed []string
	for line := range strings.Lines(out.String()) {
		if line, ok := strings.CutPrefix(line, "  FMS_"); ok {
			name, quoted, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "=")
			value, err := exec.Command(sh, "-c", "printf %s "+quoted).Output()
			if err != nil {
				t.Fatalf("%s: %v", quoted, err)
			}
			printed = append(printed, "FMS_"+name+"="+string(value))
		}
	}
	if !reflect.DeepEqual(printed, v.replacement) {
		t.Errorf("printed:\n%s\ngot %q, want %q", out.String(), printed, v.replacement)
	}
}
