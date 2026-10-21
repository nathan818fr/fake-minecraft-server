package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// startReloadableServer serves cfg with upstreams on a listener, and returns
// the server with the listener, to reload them.
func startReloadableServer(t *testing.T, cfg Config, upstreams map[string]Upstream, logger *slog.Logger) (*Server, Listener) {
	t.Helper()
	ln := Listener{Listener: listenTCP(t)}
	srv := newServerWithUpstreams(t, cfg, upstreams, logger)
	serveServer(t, srv, ln)
	return srv, ln
}

func descriptionOf(t *testing.T, addr string) string {
	t.Helper()
	return string(dial(t, addr).statusExchange(777)["description"])
}

// blockingUpstream is a fake upstream whose first read of its state waits for
// release, once it closed reading.
type blockingUpstream struct {
	fakeUpstream
	once             sync.Once
	reading, release chan struct{}
}

func (u *blockingUpstream) State() upstream.State {
	u.once.Do(func() {
		close(u.reading)
		<-u.release
	})
	return u.fakeUpstream.State()
}

// A reload answers the connections that start after it, and the others with
// the configuration they started with, even in the middle of their request.
func TestReload(t *testing.T) {
	u := &blockingUpstream{reading: make(chan struct{}), release: make(chan struct{})}
	upstreams := map[string]Upstream{"live": u}
	oldCfg := withServer(withStatus(testConfig, `{"description":"Old"}`), func(server *VirtualServer) { server.Upstream = "live" })
	srv, ln := startReloadableServer(t, oldCfg, upstreams, slog.New(slog.DiscardHandler))
	addr := ln.Addr().String()

	started := dial(t, addr)
	started.handshake(777, modernproto.IntentStatus)
	started.write(modernproto.AppendPacket(nil, modernproto.StatusRequestID, nil))
	<-u.reading

	newCfg := withServer(withStatus(testConfig, `{"description":"New"}`), func(server *VirtualServer) { server.Upstream = "live" })
	if err := srv.Reload(withUpstreams(newCfg, upstreams), ln); err != nil {
		t.Fatalf("Reload() error: %v", err)
	}
	close(u.release)
	if got := string(started.readStatus()["description"]); got != `"Old"` {
		t.Errorf("description of the connection started before = %s, want \"Old\"", got)
	}
	if got := descriptionOf(t, addr); got != `"New"` {
		t.Errorf("description after = %s, want \"New\"", got)
	}
}

// An invalid configuration leaves the running one in place.
func TestReloadRejectsInvalidConfig(t *testing.T) {
	srv, ln := startReloadableServer(t, withStatus(testConfig, `{"description":"Old"}`), nil, slog.New(slog.DiscardHandler))
	cfg := withServer(withStatus(testConfig, `{"description":"New"}`), func(server *VirtualServer) { server.Upstream = "missing" })
	if err := srv.Reload(cfg, ln); err == nil {
		t.Error("Reload() succeeded, want an error")
	}
	if got := descriptionOf(t, ln.Addr().String()); got != `"Old"` {
		t.Errorf("description = %s, want \"Old\"", got)
	}
}

func TestReloadAfterServe(t *testing.T) {
	srv := newServerWithUpstreams(t, testConfig, nil, slog.New(slog.DiscardHandler))
	ln := Listener{Listener: listenTCP(t)}
	stop := serveServer(t, srv, ln)
	stop()

	done := make(chan error, 1)
	go func() { done <- srv.Reload(testConfig, ln) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Reload() succeeded after Serve returned, want an error")
		}
	case <-time.After(clientTimeout):
		t.Fatal("Reload() did not return")
	}
}

// reloadedUpstream is a fake upstream made by an upstreamMaker, which tells
// when it starts and stops running.
type reloadedUpstream struct {
	fakeUpstream
	cfg              upstream.Config
	previous         Upstream
	started, stopped chan struct{}
}

func (u *reloadedUpstream) Run(ctx context.Context) {
	close(u.started)
	<-ctx.Done()
	close(u.stopped)
}

func waitClosed(t *testing.T, what string, c chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(clientTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// Upstreams whose configuration is unchanged keep running, and the others
// stop, before their replacement starts from them.
func TestReloadUpstreams(t *testing.T) {
	made := map[string][]*reloadedUpstream{}
	makeUpstream := func(id string, cfg upstream.Config, previous Upstream) Upstream {
		if previous != nil && !isClosed(previous.(*reloadedUpstream).stopped) {
			t.Errorf("upstream %s replaced while running", id)
		}
		u := &reloadedUpstream{cfg: cfg, previous: previous, started: make(chan struct{}), stopped: make(chan struct{})}
		made[id] = append(made[id], u)
		return u
	}
	cfg := testConfig
	cfg.Upstreams = map[string]upstream.Config{"kept": {Host: "kept"}, "changed": {Host: "old"}, "removed": {Host: "removed"}}
	srv, err := newServer(cfg, makeUpstream, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ln := Listener{Listener: listenTCP(t)}
	serveServer(t, srv, ln)
	for id, upstreams := range made {
		waitClosed(t, id+" to start", upstreams[0].started)
	}

	cfg.Upstreams = map[string]upstream.Config{"kept": {Host: "kept"}, "changed": {Host: "new"}, "added": {Host: "added"}}
	if err := srv.Reload(cfg, ln); err != nil {
		t.Fatalf("Reload() error: %v", err)
	}

	if kept := made["kept"]; len(kept) != 1 || isClosed(kept[0].stopped) {
		t.Errorf("unchanged upstream made %d times, stopped: %v, want once, running", len(kept), isClosed(kept[0].stopped))
	}
	if removed := made["removed"][0]; !isClosed(removed.stopped) {
		t.Error("removed upstream still running")
	}
	changed := made["changed"]
	if len(changed) != 2 {
		t.Fatalf("changed upstream made %d times, want twice", len(changed))
	}
	if changed[1].cfg.Host != "new" || changed[1].previous != changed[0] {
		t.Errorf("replacement of the changed upstream has %+v from %p, want the new configuration from %p", changed[1].cfg, changed[1].previous, changed[0])
	}
	waitClosed(t, "the replacement to start", changed[1].started)
	if added := made["added"][0]; added.previous != nil {
		t.Errorf("added upstream made from %p, want from none", added.previous)
	} else {
		waitClosed(t, "the added upstream to start", added.started)
	}
}

// Kept listeners take their new options, new ones start, and the others
// close.
func TestReloadListeners(t *testing.T) {
	kept, removed, added := listenTCP(t), listenTCP(t), listenTCP(t)
	var logs bytes.Buffer
	srv := newServerWithUpstreams(t, testConfig, nil, slog.New(logging.NewTextHandler(&logs, nil)))
	stop := serveServer(t, srv, Listener{Listener: kept}, Listener{Listener: removed})

	if err := srv.Reload(testConfig, Listener{Listener: kept, Options: testProxyProtocol}, Listener{Listener: added}); err != nil {
		t.Fatalf("Reload() error: %v", err)
	}
	// Dialing could reach another listener given the same port.
	_ = removed.(*net.TCPListener).SetDeadline(time.Now())
	if _, err := removed.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the removed listener is not closed: Accept() error %v", err)
	}
	toAdded := dial(t, added.Addr().String())
	toAdded.statusExchange(777)
	proxied := dial(t, kept.Addr().String())
	proxied.write(proxyHeader("192.0.2.1:56324"))
	proxied.statusExchange(777)
	direct := dial(t, kept.Addr().String())
	direct.handshake(777, modernproto.IntentStatus)
	direct.expectClosed()
	_ = toAdded.conn.Close()
	_ = proxied.conn.Close()
	stop()

	for _, want := range []string{
		`INFO  stopped listening: listener=` + regexp.QuoteMeta(removed.Addr().String()),
		`INFO  listening: listener=` + regexp.QuoteMeta(added.Addr().String()),
	} {
		if !regexp.MustCompile(`(?m)^\[\S+\] ` + want + `$`).MatchString(logs.String()) {
			t.Errorf("logs = %q, want a line matching %q", logs.String(), want)
		}
	}
}

// New limits count the connections already open.
func TestReloadConnectionLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(*Config)
	}{
		{"global", func(cfg *Config) { cfg.MaxConnections = 3 }},
		{"per IP", func(cfg *Config) { cfg.MaxConnectionsPerIP = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ln := startReloadableServer(t, testConfig, nil, slog.New(slog.DiscardHandler))
			addr := ln.Addr().String()
			var held []*client
			for range 3 {
				held = append(held, holdConnection(t, addr))
			}

			cfg := testConfig
			tc.cfg(&cfg)
			if err := srv.Reload(cfg, ln); err != nil {
				t.Fatalf("Reload() error: %v", err)
			}
			expectRejected(t, addr)

			_ = held[0].conn.Close()
			expectAccepted(t, addr)
		})
	}
}
