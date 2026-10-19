package script

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// matchCtx compiles src with the scope of testScope, and runs it with req.
func matchCtx(t *testing.T, src string, req *Request) {
	t.Helper()
	c, err := testScope(t).CompileCondition(src)
	if err != nil {
		t.Fatalf("CompileCondition(%s) error: %v", src, err)
	}
	if req.Client == nil {
		req.Client = &sampleBase
	}
	if got, err := c.Match(req, nil); err != nil || !got {
		t.Errorf("Match(%s) = %v, %v, want true", src, got, err)
	}
}

// ctx.now() is the time of the request in UTC, read once.
func TestNow(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	now := func() time.Time {
		calls++
		return time.Date(2026, 12, 1, 18, calls, 0, 0, paris)
	}
	matchCtx(t, `ctx.now() == ctx.now() and ctx.now().hour == 17 and ctx.now().minute == 1`+
		` and ctx.now().format("Z07:00") == "Z" and ctx.now().in_location("Europe/Paris").hour == 18`+
		` and fms.split_duration(fms.parse_time("2026-12-02T00:00:00Z") - ctx.now()).hours == 6`, &Request{Now: now})
	if calls != 1 {
		t.Errorf("the clock was read %d times, want once", calls)
	}
}

// Random numbers come from Request.Rand when set, so that tests can fix them.
func TestRandom(t *testing.T) {
	c, err := testScope(t).CompileCondition(`print([ctx.randint(1, 1000000) for _ in range(8)], ctx.choice(("a", "b", "c", "d", "e", "f", "g", "h")), ctx.random()) == None` +
		` and ctx.randint(3, 3) == 3 and ctx.randint(-9223372036854775808, 9223372036854775807) != 0.5`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	draw := func(r *rand.Rand) string {
		var printed string
		if got, err := c.Match(&Request{Client: &sampleBase, Rand: r}, func(msg string) { printed = msg }); err != nil || !got {
			t.Errorf("Match() = %v, %v, want true", got, err)
		}
		return printed
	}
	first, again, other := draw(rand.New(rand.NewPCG(1, 2))), draw(rand.New(rand.NewPCG(1, 2))), draw(rand.New(rand.NewPCG(3, 4)))
	if first != again || first == other {
		t.Errorf("draws %q, %q, %q, want the first two only alike", first, again, other)
	}
	if random := draw(nil); random == first || random == "" {
		t.Errorf("draw without Rand = %q, want another one", random)
	}

	tests := []struct{ src, want string }{
		{`ctx.randint(2, 1) == 1`, "ctx.randint: empty range: 2 > 1"},
		{`ctx.choice([]) == 1`, "ctx.choice: empty list"},
		{`ctx.choice({}) == 1`, "ctx.choice: got dict, want a list or a tuple"},
		{`ctx.choice("ab") == 1`, "ctx.choice: got string, want a list or a tuple"},
	}
	for _, tc := range tests {
		// Never run with the sample clients.
		c, err := testScope(t).CompileCondition(`client.host == "x.test" and ` + tc.src)
		if err != nil {
			t.Fatalf("CompileCondition(%s) error: %v", tc.src, err)
		}
		if _, err := c.Match(&Request{Client: &Client{Host: "x.test"}}, nil); err == nil || err.Error() != tc.want {
			t.Errorf("Match(%s) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// ctx.upstreams() lists the upstreams by ID, and reads their states only when
// used.
func TestUpstreams(t *testing.T) {
	read := 0
	states := map[string]upstream.State{"creative": {Status: sampleStatus, Polled: true}}
	request := func() *Request {
		return &Request{Upstream: func(id string) upstream.State {
			read++
			return states[id]
		}}
	}
	matchCtx(t, `[u.id for u in ctx.upstreams()] == ["creative", "survival"] and str(ctx.upstreams()[0]) == 'upstream(id = "creative")'`, request())
	if read != 0 {
		t.Errorf("upstreams read %d times, want none", read)
	}
	matchCtx(t, `[u.status["players"]["online"] for u in ctx.upstreams() if u.status] == [3]`+
		` and [u.id for u in ctx.upstreams() if u.latency_ms == None] == ["survival"]`, request())
}

// ctx.servers() lists the servers by ID.
func TestServers(t *testing.T) {
	scope := NewScope(nil, []ServerInfo{{ID: "lobby"}, {ID: "hub"}})
	c, err := scope.CompileCondition(`[s.id for s in ctx.servers()] == ["hub", "lobby"] and str(ctx.servers()[0]) == 'server(id = "hub")'`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &sampleBase}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}
