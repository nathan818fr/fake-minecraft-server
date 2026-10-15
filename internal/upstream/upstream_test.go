package upstream_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// upstreamStatus has fields that the poller does not read, which it must keep.
const upstreamStatus = `{"version":{"name":"Paper 1.21.10","protocol":773},"players":{"max":100,"online":12,"sample":[]},` +
	`"description":"Hello","enforcesSecureChat":true,"forgeData":{"fmlNetworkVersion":3}}`

func testConfig(addr string) upstream.Config {
	addrPort := netip.MustParseAddrPort(addr)
	return upstream.Config{
		Host:            addrPort.Addr().String(),
		Port:            addrPort.Port(),
		ServerAddress:   "mc.example.com",
		ServerPort:      25565,
		ProtocolVersion: -1,
		Interval:        20 * time.Millisecond,
		RetryInterval:   20 * time.Millisecond,
		Timeout:         time.Second,
		MaxAge:          300 * time.Millisecond,
	}
}

// lockedBuffer collects logs written by a poller while the test reads them.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startPoller(t *testing.T, cfg upstream.Config) (*upstream.Poller, *lockedBuffer, func()) {
	t.Helper()
	return startPollerAt(t, cfg, slog.LevelInfo)
}

// startPollerAt runs a poller, logging at level, until the returned function is
// called.
func startPollerAt(t *testing.T, cfg upstream.Config, level slog.Level) (*upstream.Poller, *lockedBuffer, func()) {
	t.Helper()
	logs := &lockedBuffer{}
	p := upstream.NewPoller("lobby", cfg, slog.New(logging.NewTextHandler(logs, level)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		<-done
	})
	t.Cleanup(stop)
	return p, logs, stop
}

// startUpstream starts a server answering statusJSON on ln, until the returned
// function is called.
func startUpstream(t *testing.T, ln net.Listener, statusJSON string, logs io.Writer) func() {
	t.Helper()
	st, err := status.Parse([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{
		Servers:           map[string]server.VirtualServer{"default": {Status: st, ClientProtocol: status.ClientProtocolNever, Kick: json.RawMessage(`"Bye"`)}},
		Rules:             []server.Rule{{Name: "default", Server: "default"}},
		ConnectionTimeout: time.Second,
		HandshakeTimeout:  time.Second,
	}, slog.New(logging.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, server.Listener{Listener: ln}) }()
	stop := sync.OnceFunc(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error: %v", err)
		}
	})
	t.Cleanup(stop)
	return stop
}

func listen(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPoller(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	upstreamLogs := &lockedBuffer{}
	stopUpstream := startUpstream(t, ln, upstreamStatus, upstreamLogs)
	p, logs, stop := startPoller(t, testConfig(ln.Addr().String()))

	waitFor(t, "a status", func() bool { return p.State().Status != nil })
	if got := string(p.State().Status.AppendJSON(nil)); got != upstreamStatus {
		t.Errorf("status = %s, want %s", got, upstreamStatus)
	}
	waitFor(t, "several polls", func() bool { return strings.Count(upstreamLogs.String(), "server list ping") >= 3 })
	stop()
	stopUpstream()

	// Logged once, although polled several times.
	want := regexp.MustCompile(`^\S+ INFO  upstream available: upstream=lobby address=127\.0\.0\.1:\d+ version="Paper 1\.21\.10" protocol=773 online=12 max=100 latency=\S+\n$`)
	if !want.MatchString(logs.String()) {
		t.Errorf("logs = %q, want them to match %s", logs, want)
	}
	// The handshake has the configured values, and the poller pings like
	// clients.
	if line := `server list ping: protocol=-1 host="mc.example.com" port=25565 rule=default server=default source=static ping_request=true`; !strings.Contains(upstreamLogs.String(), line) {
		t.Errorf("upstream logs = %q, want lines with %q", upstreamLogs, line)
	}
}

// The polls between changes of availability are logged at the debug level.
func TestPollerLogsPollsAtDebugLevel(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	startUpstream(t, ln, upstreamStatus, io.Discard)
	_, logs, stop := startPollerAt(t, testConfig(ln.Addr().String()), slog.LevelDebug)
	waitFor(t, "several polls", func() bool { return strings.Count(logs.String(), "upstream polled") >= 2 })
	stop()

	want := regexp.MustCompile(`^\S+ INFO  upstream available: upstream=lobby address=\S+ version="Paper 1\.21\.10" protocol=773 online=12 max=100 latency=\S+\n` +
		`(\S+ DEBUG upstream polled: upstream=lobby address=\S+ version="Paper 1\.21\.10" protocol=773 online=12 max=100 latency=\S+\n)+$`)
	if !want.MatchString(logs.String()) {
		t.Errorf("logs = %q, want them to match %s", logs, want)
	}
}

// Servers can send version names as long as their status, which are shortened
// in logs.
func TestPollerShortensLoggedVersion(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	startUpstream(t, ln, `{"version":{"name":"a`+strings.Repeat("§", 40)+`","protocol":773}}`, io.Discard)
	p, logs, stop := startPoller(t, testConfig(ln.Addr().String()))
	waitFor(t, "a status", func() bool { return p.State().Status != nil })
	stop()

	if want := `version="a` + strings.Repeat("§", 31) + `..."`; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want them to contain %s", logs, want)
	}
}

// The last status stays in use for MaxAge after the upstream stops answering,
// until it answers again.
func TestPollerAvailability(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	addr := ln.Addr().String()
	stopUpstream := startUpstream(t, ln, upstreamStatus, io.Discard)
	cfg := testConfig(addr)
	p, logs, _ := startPoller(t, cfg)

	waitFor(t, "a status", func() bool { return p.State().Status != nil })
	stopUpstream()
	stoppedAt := time.Now()
	waitFor(t, "the status to expire", func() bool { return p.State().Status == nil })
	if elapsed := time.Since(stoppedAt); elapsed < cfg.MaxAge/2 {
		t.Errorf("status expired %v after the upstream stopped, want about %v", elapsed, cfg.MaxAge)
	}
	// The state still tells about the last successful poll.
	if st := p.State(); !st.Polled || st.Latency <= 0 || st.Age < cfg.MaxAge {
		t.Errorf("State() = %+v, want the last poll, older than MaxAge", st)
	}
	waitFor(t, "unavailability to be logged", func() bool { return strings.Contains(logs.String(), "upstream unavailable") })

	startUpstream(t, listen(t, addr), upstreamStatus, io.Discard)
	waitFor(t, "a status", func() bool { return p.State().Status != nil })
	waitFor(t, "availability to be logged", func() bool { return strings.Count(logs.String(), "upstream available") == 2 })
	if n := strings.Count(logs.String(), "upstream unavailable"); n != 1 {
		t.Errorf("unavailability logged %d times, want once: %s", n, logs)
	}
}

func TestPollerFailures(t *testing.T) {
	invalidStatus := modernproto.AppendPacket(nil, modernproto.StatusResponseID, modernproto.AppendString(nil, `{"version":{"protocol":"client"}}`))
	tests := []struct {
		name    string
		answer  []byte
		wantErr string
	}{
		{"invalid status", invalidStatus, `invalid status: version.protocol: must be an integer between -2147483648 and 2147483647, got \\"client\\"`},
		{"no answer", nil, `reading status: read tcp 127.0.0.1:\d+->127.0.0.1:\d+: i/o timeout`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ln := listen(t, "127.0.0.1:0")
			t.Cleanup(func() { _ = ln.Close() })
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_, _ = conn.Write(tc.answer)
					go func() {
						_, _ = io.Copy(io.Discard, conn)
						_ = conn.Close()
					}()
				}
			}()
			cfg := testConfig(ln.Addr().String())
			cfg.Timeout = 100 * time.Millisecond
			p, logs, stop := startPoller(t, cfg)

			want := regexp.MustCompile(`^\S+ WARN  upstream unavailable: upstream=lobby address=127\.0\.0\.1:\d+ error="` + tc.wantErr + `"\n$`)
			waitFor(t, "unavailability to be logged", func() bool { return logs.String() != "" })
			stop()
			if !want.MatchString(logs.String()) {
				t.Errorf("logs = %q, want them to match %s", logs, want)
			}
			if p.State().Status != nil {
				t.Error("Status() is not nil")
			}
		})
	}
}

// fakeResolver answers the lookups of SRV records with records and err, and
// records the names looked up.
type fakeResolver struct {
	records []*net.SRV
	err     error

	mu    sync.Mutex
	names []string
}

func (r *fakeResolver) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, "_"+service+"._"+proto+"."+name)
	return "", r.records, r.err
}

func (r *fakeResolver) lookups() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.names)
}

// The poller connects to the target of the SRV record of a host without port,
// looked up at each poll, but sends the host as written, as clients do in their
// server list.
func TestPollerFollowsSRVRecord(t *testing.T) {
	ln := listen(t, "127.0.0.1:0")
	upstreamLogs := &lockedBuffer{}
	startUpstream(t, ln, upstreamStatus, upstreamLogs)
	port := netip.MustParseAddrPort(ln.Addr().String()).Port()
	resolver := &fakeResolver{records: []*net.SRV{{Target: "127.0.0.1", Port: port}}}

	cfg := testConfig(ln.Addr().String())
	cfg.Host, cfg.Port = "mc.example.com", 0
	p := upstream.NewPollerWithResolver("lobby", cfg, slog.New(slog.DiscardHandler), resolver)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	waitFor(t, "several polls", func() bool { return strings.Count(upstreamLogs.String(), "server list ping") >= 2 })
	cancel()
	<-done

	if p.State().Status == nil {
		t.Error("Status() is nil, want the status of the upstream")
	}
	if names := resolver.lookups(); len(names) < 2 || names[0] != "_minecraft._tcp.mc.example.com" {
		t.Errorf("SRV lookups = %q, want _minecraft._tcp.mc.example.com at each poll", names)
	}
	if line := `server list ping: protocol=-1 host="mc.example.com" port=25565 `; !strings.Contains(upstreamLogs.String(), line) {
		t.Errorf("upstream logs = %q, want lines with %q", upstreamLogs, line)
	}
}

func TestPollerResolve(t *testing.T) {
	notFound := &net.DNSError{Err: "no such host", Name: "_minecraft._tcp.mc.example.com", IsNotFound: true}
	failure := &net.DNSError{Err: "server misbehaving", Name: "_minecraft._tcp.mc.example.com", IsTemporary: true}
	records := []*net.SRV{{Target: "node1.example.net.", Port: 25601, Priority: 1}, {Target: "node2.example.net.", Port: 25602, Priority: 2}}
	tests := []struct {
		name    string
		host    string
		port    uint16
		records []*net.SRV
		err     error
		lookup  bool
		want    string
		wantErr string
	}{
		{"port", "mc.example.com", 25566, records, nil, false, "mc.example.com:25566", ""},
		{"IPv4 address", "192.0.2.1", 0, records, nil, false, "192.0.2.1:25565", ""},
		{"IPv6 address", "2001:db8::1", 0, records, nil, false, "[2001:db8::1]:25565", ""},
		{"SRV record", "mc.example.com", 0, records, nil, true, "node1.example.net.:25601", ""},
		{"no SRV record", "mc.example.com", 0, nil, notFound, true, "mc.example.com:25565", ""},
		{"no records without error", "mc.example.com", 0, nil, nil, true, "mc.example.com:25565", ""},
		{
			"no service", "mc.example.com", 0, []*net.SRV{{Target: "."}}, nil, true,
			"", "the SRV record of mc.example.com says that it has no Minecraft server",
		},
		{
			"lookup failure", "mc.example.com", 0, nil, failure, true,
			"", "looking up SRV record: lookup _minecraft._tcp.mc.example.com: server misbehaving",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig("127.0.0.1:1")
			cfg.Host, cfg.Port = tc.host, tc.port
			resolver := &fakeResolver{records: tc.records, err: tc.err}
			p := upstream.NewPollerWithResolver("lobby", cfg, slog.New(slog.DiscardHandler), resolver)
			got, err := p.Resolve(context.Background())
			if got != tc.want || (err == nil) != (tc.wantErr == "") || (err != nil && err.Error() != tc.wantErr) {
				t.Errorf("Resolve() = %q, %v, want %q, %q", got, err, tc.want, tc.wantErr)
			}
			if names := resolver.lookups(); (len(names) != 0) != tc.lookup {
				t.Errorf("SRV lookups = %q, want a lookup: %v", names, tc.lookup)
			}
		})
	}
}
