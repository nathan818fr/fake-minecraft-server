package script

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

func mustParseStatus(t *testing.T, data string) *status.Status {
	t.Helper()
	st, err := status.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// testScope returns a scope with the upstreams survival and creative, and the
// server lobby, whose upstream is survival and client protocol always.
func testScope(t *testing.T) *Scope {
	t.Helper()
	return NewScope(
		[]UpstreamInfo{{ID: "survival", Address: "survival.example.com"}, {ID: "creative", Address: "creative.example.com:25566"}},
		[]ServerInfo{{
			ID:             "lobby",
			StaticStatus:   mustParseStatus(t, `{"description":"Down","players":{"max":0,"online":0}}`),
			Upstream:       "survival",
			ClientProtocol: status.ClientProtocolAlways,
			Kick:           json.RawMessage(`{"text":"Bye","color":"red"}`),
		}},
	)
}

func noCheckKick(json.RawMessage) error { return nil }

func noCheckStatus(*status.Status, status.ClientProtocol) error { return nil }

// upstreams returns a Request.Upstream that gives states from states, and
// unavailable states for the others.
func upstreams(states map[string]upstream.State) func(string) upstream.State {
	return func(id string) upstream.State { return states[id] }
}

func TestJoinHook(t *testing.T) {
	tests := []struct {
		src  string
		want string // "" for None.
	}{
		{`None`, ``},
		{`{"kick": "Sorry " + client.username}`, `{"text":"Sorry Notch"}`},
		// Text is never parsed as JSON.
		{`{"kick": '{"text": "x"}'}`, `{"text":"{\"text\": \"x\"}"}`},
		{`{"kick": {"text": "Sorry", "color": "red", "extra": [server.kick]}}`, `{"text":"Sorry","color":"red","extra":[{"text":"Bye","color":"red"}]}`},
		{`{"kick": ["a", ("b",)]}`, `["a",["b"]]`},
		{`{"kick": ctx.server("lobby").kick}`, `{"text":"Bye","color":"red"}`},
	}
	for _, tc := range tests {
		h, err := testScope(t).CompileJoinHook(tc.src, "lobby", noCheckKick)
		if err != nil {
			t.Errorf("CompileJoinHook(%q) error: %v", tc.src, err)
			continue
		}
		client := sample(join)
		if got, err := h.Run(&Request{Client: &client}, nil); err != nil || string(got) != tc.want {
			t.Errorf("%q: Run() = %s, %v, want %s", tc.src, got, err, tc.want)
		}
	}
}

func TestPingHook(t *testing.T) {
	live := mustParseStatus(t, `{"description":"Live","players":{"max":20,"online":3}}`)
	other := mustParseStatus(t, `{"players":{"max":10,"online":2}}`)
	tests := []struct {
		src            string
		status         string // "" for None.
		clientProtocol status.ClientProtocol
	}{
		{`None`, ``, status.ClientProtocolAuto},
		// The status of the server is the one of its upstream while available,
		// answered with its client protocol unless the hook returns one.
		{`{"status": server.status}`, `{"description":"Live","players":{"max":20,"online":3}}`, status.ClientProtocolAlways},
		{`{"status": server.static_status, "client_protocol": "never"}`, `{"description":"Down","players":{"max":0,"online":0}}`, status.ClientProtocolNever},
		{
			`{"status": fms.merge(server.status, {"players": {"online": server.status["players"]["online"] + (ctx.upstream("creative").status or {"players": {"online": 0}})["players"]["online"], "sample": None}})}`,
			`{"description":"Live","players":{"max":20,"online":5}}`, status.ClientProtocolAlways,
		},
		{`{"status": {"description": fms.sanitize("§cHi\n" + client.host), "favicon": "data:image/png;base64,AAAA"}}`, `{"description":"Himc.example.com","favicon":"data:image/png;base64,AAAA"}`, status.ClientProtocolAlways},
	}
	for _, tc := range tests {
		h, err := testScope(t).CompilePingHook(tc.src, "lobby", noCheckStatus)
		if err != nil {
			t.Errorf("CompilePingHook(%q) error: %v", tc.src, err)
			continue
		}
		req := &Request{Client: &sampleBase, Upstream: upstreams(map[string]upstream.State{"survival": {Status: live, Polled: true}, "creative": {Status: other, Polled: true}})}
		answer, err := h.Run(req, nil)
		if err != nil {
			t.Errorf("%q: Run() error: %v", tc.src, err)
			continue
		}
		var got string
		var gotProtocol status.ClientProtocol
		if answer != nil {
			got, gotProtocol = string(answer.Status.AppendJSON(nil)), answer.ClientProtocol
		}
		if got != tc.status || gotProtocol != tc.clientProtocol {
			t.Errorf("%q: Run() = %s, %d, want %s, %d", tc.src, got, gotProtocol, tc.status, tc.clientProtocol)
		}
	}
}

// A ping hook that returns unchanged the favicon of a status reuses its JSON
// rather than encoding it again at each ping.
func TestPingHookFavicons(t *testing.T) {
	static := mustParseStatus(t, `{"favicon":"data:image/png;base64,c3RhdGlj"}`)
	live := mustParseStatus(t, `{"favicon":"data:image/png;base64,bGl2ZQ=="}`)
	s := NewScope([]UpstreamInfo{{ID: "survival"}}, []ServerInfo{{ID: "lobby", StaticStatus: static, Upstream: "survival"}})
	tests := []struct {
		src    string
		reused bool
	}{
		{`server.static_status["favicon"]`, true},
		// The sample status of upstreams has no favicon.
		{`server.status.get("favicon", "data:")`, true},
		{`"data:image/png;base64," + "bmV3"`, false},
	}
	for _, tc := range tests {
		h, err := s.CompilePingHook(`{"status": {"favicon": `+tc.src+`}}`, "lobby", noCheckStatus)
		if err != nil {
			t.Fatalf("CompilePingHook(%s) error: %v", tc.src, err)
		}
		favicon := func() json.RawMessage {
			req := &Request{Client: &sampleBase, Upstream: upstreams(map[string]upstream.State{"survival": {Status: live, Polled: true}})}
			answer, err := h.Run(req, nil)
			if err != nil {
				t.Fatalf("%s: Run() error: %v", tc.src, err)
			}
			data, _ := answer.Status.Get(faviconPath)
			return data.(json.RawMessage)
		}
		first, again := favicon(), favicon()
		if reused := &first[0] == &again[0]; reused != tc.reused || string(first) != string(again) {
			t.Errorf("%s: favicons %s and %s, reused %v, want %v", tc.src, first, again, reused, tc.reused)
		}
	}
}

// Hooks are checked at startup with the samples of their request, with the
// upstreams unavailable, then available.
func TestHookErrors(t *testing.T) {
	join := []struct {
		src, want string
	}{
		{`"Bye"`, `fails for a player joining: must return None or {"kick": message}, got string`},
		{`{"kick": "a", "status": "b"}`, `fails for a player joining: unknown key "status" in the result, want {"kick": message}`},
		{`{"kick": 1}`, `fails for a player joining: "kick" must be text or a text component, got int`},
		{`{"kick": {"text": float("nan")}}`, `fails for a player joining: "kick": "text": nan is not a JSON number`},
		{`{"kick": {1: "a"}}`, `fails for a player joining: "kick": dict keys must be strings, got int`},
		{`{"kick": client.username[0]}`, `fails for a player joining without username, from a bad client or library (client.username ""): index 0 out of range: empty string`},
		{`{"kick": ctx.upstream("survival").status["description"]}`, `fails for a player joining, with the upstreams unavailable: unhandled index operation NoneType[string]`},
		{`{"kick": ctx.upstream("survivl")}`, `1:23: ctx.upstream: no upstream has the ID "survivl"`},
		{`{"kick": ctx.server("lobbi")}`, `1:21: ctx.server: no server has the ID "lobbi"`},
		// Even in a branch that no sample client takes.
		{`{"kick": server.kik} if client.host == "x.test" else None`, `1:17: server has no .kik field or method (did you mean .kick?)`},
	}
	for _, tc := range join {
		if _, err := testScope(t).CompileJoinHook(tc.src, "lobby", noCheckKick); err == nil || err.Error() != tc.want {
			t.Errorf("CompileJoinHook(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
	if _, err := testScope(t).CompileJoinHook(`{"kick": "x"}`, "lobby", func(json.RawMessage) error { return errors.New("too long") }); err == nil || err.Error() != `fails for a player joining: "kick": too long` {
		t.Errorf("CompileJoinHook() error = %v, want the error of the check", err)
	}

	ping := []struct {
		src, want string
	}{
		{`{"status": "x"}`, `fails for a modern ping: "status" must be a dict, got string`},
		{`{"client_protocol": "never"}`, `fails for a modern ping: the result has no "status"`},
		{`{"status": {}, "client_protocol": "sometimes"}`, `fails for a modern ping: "client_protocol" must be "auto", "always" or "never", got "sometimes"`},
		{`{"status": {"favicon": "icon.png"}}`, `fails for a modern ping: "status": favicon must be a data URI ("data:image/png;base64,..."), got "icon.png"`},
		{`{"status": {"players": {"max": "many"}}}`, `fails for a modern ping: "status": players.max: must be an integer between -2147483648 and 2147483647, got "many"`},
		{`{"status": fms.merge(server.status, {"players": {"online": ctx.upstream("survival").status["players"]["online"]}})}`, `fails for a modern ping, with the upstreams unavailable: unhandled index operation NoneType[string]`},
	}
	for _, tc := range ping {
		if _, err := testScope(t).CompilePingHook(tc.src, "lobby", noCheckStatus); err == nil || err.Error() != tc.want {
			t.Errorf("CompilePingHook(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// Errors when a hook runs tell the fields of the client that it read.
func TestHookRunErrors(t *testing.T) {
	h, err := testScope(t).CompileJoinHook(`{"kick": "x" if fms.matches(client.host, client.host_extra or "^$") else "y"}`, "lobby", noCheckKick)
	if err != nil {
		t.Fatalf("CompileJoinHook() error: %v", err)
	}
	_, err = h.Run(&Request{Client: &Client{Host: "a.com", HostExtra: "("}}, nil)
	var hookErr *Error
	if !errors.As(err, &hookErr) || len(hookErr.Fields) != 2 || !strings.HasPrefix(err.Error(), "fms.matches: error parsing regexp") {
		t.Errorf("Run() error = %#v, want an invalid regexp, with the fields read", err)
	}
}

func TestUpstreamMethod(t *testing.T) {
	c, err := testScope(t).CompileCondition(`ctx.upstream("creative").address == "creative.example.com:25566"` +
		` and ctx.upstream("creative").latency_ms == 12.5 and ctx.upstream("creative").age_ms == 3000.0` +
		` and ctx.upstream("survival").status == None and ctx.upstream("survival").latency_ms == None`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	states := map[string]upstream.State{"creative": {Polled: true, Latency: 12500 * time.Microsecond, Age: 3 * time.Second}}
	if got, err := c.Match(&Request{Client: &sampleBase, Upstream: upstreams(states)}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}

	// Upstreams change with requests, and the module runs once, without ctx.
	if err := testScope(t).LoadModule(`X = ctx.upstream("survival")`); err == nil || err.Error() != "1:5: undefined: ctx" {
		t.Errorf("LoadModule() error = %v, want ctx undefined", err)
	}
	if err := testScope(t).LoadModule("def f(ctx):\n    return ctx.upstreams(\"survival\")\n"); err == nil ||
		err.Error() != "2:16: ctx has no .upstreams field or method (did you mean .upstream?)" {
		t.Errorf("LoadModule() error = %v, want the method that does not exist", err)
	}
}

// The variables of scripts and the objects of the configuration have their
// type, a short form, and their attributes.
func TestObjects(t *testing.T) {
	c, err := testScope(t).CompileCondition(`type(client) == "client" and str(client).startswith('client(request = "ping", transferred = False, ')` +
		` and type(ctx) == "ctx" and str(ctx) == "ctx" and dir(ctx) == ["server", "upstream"]` +
		` and str(ctx.server("lobby")) == 'server(id = "lobby")' and dir(ctx.server("lobby")) == ["id", "kick", "static_status", "status", "upstream"]`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &sampleBase}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

// Joins see the username and UUID of the player.
func TestLoginFields(t *testing.T) {
	c, err := NewScope(nil, nil).CompileCondition(`client.request == "join" and client.username == "Notch" and client.uuid.startswith("069a79f4")`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	client := sample(join)
	if got, err := c.Match(&Request{Client: &client}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

// A ping hook that changes a status with a favicon of 30 KB.
func BenchmarkPingHook(b *testing.B) {
	st, err := status.Parse([]byte(`{"description":"Down","players":{"max":0,"online":0},"favicon":"data:image/png;base64,` + strings.Repeat("A", 30000) + `"}`))
	if err != nil {
		b.Fatal(err)
	}
	h, err := NewScope(nil, []ServerInfo{{ID: "lobby", StaticStatus: st}}).CompilePingHook(`{"status": fms.merge(server.status, {"players": {"online": 5}})}`, "lobby", noCheckStatus)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.Run(&Request{Client: &sampleBase}, nil); err != nil {
			b.Fatal(err)
		}
	}
}
