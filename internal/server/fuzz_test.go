package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/script"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// FuzzServer sends anything to a real server, which must neither panic nor
// hang.
func FuzzServer(f *testing.F) {
	f.Add(append(modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 773, ServerAddress: "a.com", ServerAddressExtra: "FML3\x00", ServerPort: 25565, Intent: modernproto.IntentStatus}),
		append(modernproto.AppendPacket(nil, 0, nil), modernproto.AppendPacket(nil, 1, make([]byte, 8))...)...), false)
	f.Add(modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 773, ServerAddress: "a.com", ServerPort: 25565, Intent: modernproto.IntentLogin}), false)
	f.Add(legacyPing16, false)
	f.Add(legacyLogin13, false)
	f.Add(append(proxyHeader("192.0.2.1:56324"), legacyPing16...), true)
	cfg := withServer(testConfig, func(*VirtualServer) {})
	cfg.ConnectionTimeout, cfg.HandshakeTimeout = 300*time.Millisecond, 200*time.Millisecond
	when, err := script.NewScope(nil, nil).CompileCondition(`fms.matches(client.host, client.host_extra or "^$") or fms.in_network(client.ip, "10.0.0.0/8")`)
	if err != nil {
		f.Fatal(err)
	}
	hosts, _ := script.Hosts([]string{".a.com"})
	cfg.Rules = []Rule{{Name: "a", Conditions: []script.Condition{hosts, when}, Server: testServerID}, {Name: "0", Server: testServerID}}
	addrs := map[bool]string{}
	for proxy, options := range map[bool]ListenerOptions{false: {}, true: testProxyProtocol} {
		addrs[proxy], _ = startServerWithOptions(f, cfg, nil, slog.New(slog.DiscardHandler), options)
	}
	f.Fuzz(func(t *testing.T, data []byte, proxy bool) {
		conn, err := net.Dial("tcp", addrs[proxy])
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write(data)
		_ = conn.(*net.TCPConn).CloseWrite()
		if _, err := io.Copy(io.Discard, conn); errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("server did not close the connection")
		}
	})
}

// fuzzUpstream counts the pollers of each ID that run at once, which must
// never exceed one.
type fuzzUpstream struct {
	running  *atomic.Int32
	stopped  chan struct{}
	t        *testing.T
	id       string
	previous *fuzzUpstream
}

func (u *fuzzUpstream) Run(ctx context.Context) {
	if n := u.running.Add(1); n != 1 {
		u.t.Errorf("upstream %s: %d pollers running", u.id, n)
	}
	<-ctx.Done()
	u.running.Add(-1)
	close(u.stopped)
}

func (u *fuzzUpstream) State() upstream.State { return upstream.State{} }

type fuzzListener struct {
	Listener
	addr string
}

// fuzzGeneration is the configuration of generation g, made from b: its
// upstreams, limits, and whether it is invalid. Its rule, server and status
// are named after g, so that an answer mixing two generations would panic or
// show.
func fuzzGeneration(g int, b byte) (Config, bool) {
	cfg := testConfig
	server := "s" + strconv.Itoa(g)
	st, err := status.Parse([]byte(`{"description":"g` + strconv.Itoa(g) + `"}`))
	if err != nil {
		panic(err)
	}
	vs := VirtualServer{Status: st, ClientProtocol: status.ClientProtocolNever, Kick: json.RawMessage(`"k` + strconv.Itoa(g) + `"`)}
	cfg.Upstreams = map[string]upstream.Config{}
	for i, id := range []string{"a", "b", "c"} {
		if b&(1<<i) != 0 {
			cfg.Upstreams[id] = upstream.Config{Host: id, Port: uint16(b>>3&1) + 1}
			vs.Upstream = id
		}
	}
	limits := []int{0, 1, 2, 50}
	cfg.MaxConnections = limits[b>>4&3]
	cfg.MaxConnectionsPerIP = limits[b>>6&3]
	invalid := b&0x0f == 0x0f
	if invalid {
		vs.Upstream = "missing"
	}
	cfg.Servers = map[string]VirtualServer{server: vs}
	cfg.Rules = []Rule{{Name: "r" + strconv.Itoa(g), Server: server}}
	return cfg, invalid
}

// fuzzStatus returns the description that the listener answers, or an error.
func fuzzStatus(ln fuzzListener) (string, error) {
	conn, err := net.DialTimeout("tcp", ln.addr, time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	var request []byte
	if ln.Options.ProxyProtocol {
		request = proxyHeader("192.0.2.1:56324")
	}
	request = modernproto.AppendHandshake(request, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", ServerPort: 25565, Intent: modernproto.IntentStatus})
	request = modernproto.AppendPacket(request, modernproto.StatusRequestID, nil)
	if _, err := conn.Write(request); err != nil {
		return "", err
	}
	data, err := modernproto.ReadStatusResponse(bufio.NewReader(conn))
	if err != nil {
		return "", err
	}
	var st struct{ Description string }
	if err := json.Unmarshal(data, &st); err != nil {
		return "", err
	}
	return st.Description, nil
}

// FuzzReload reloads a server with the configurations and listeners that ops
// make, while clients connect, and checks after each reload that the new
// configuration answers on the new listeners only, that each upstream has one
// poller, and that the limits count no connection once all are closed.
func FuzzReload(f *testing.F) {
	f.Add([]byte{0x13, 0x01, 0x02, 0x00, 0x21, 0x07})
	f.Add([]byte{0x30, 0x0f, 0x11, 0xff, 0x01, 0x5a})
	f.Add([]byte{0x02, 0x99, 0x10, 0x46})
	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) > 16 {
			ops = ops[:16]
		}
		running := map[string]*atomic.Int32{"a": {}, "b": {}, "c": {}}
		var made []*fuzzUpstream
		var madeMu sync.Mutex
		makeUpstream := func(id string, _ upstream.Config, previous Upstream) Upstream {
			u := &fuzzUpstream{running: running[id], stopped: make(chan struct{}), t: t, id: id}
			if previous != nil {
				u.previous = previous.(*fuzzUpstream)
				if !isClosed(u.previous.stopped) {
					t.Errorf("upstream %s replaced while running", id)
				}
			}
			madeMu.Lock()
			made = append(made, u)
			madeMu.Unlock()
			return u
		}

		cfg, _ := fuzzGeneration(0, 0)
		srv, err := newServer(cfg, makeUpstream, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		newListener := func(proxy bool) fuzzListener {
			ln := listenTCP(t)
			l := fuzzListener{Listener: Listener{Listener: ln}, addr: ln.Addr().String()}
			if proxy {
				l.Options = testProxyProtocol
			}
			return l
		}
		current := []fuzzListener{newListener(false)}
		ctx, cancel := context.WithCancel(context.Background())
		served := make(chan error, 1)
		go func() { served <- srv.Serve(ctx, current[0].Listener) }()

		generation := 0
		for i := 0; i+1 < len(ops); i += 2 {
			listenOps, cfgOps := ops[i], ops[i+1]
			var next []fuzzListener
			var added, removed []fuzzListener
			for j, ln := range current {
				if listenOps&(1<<j) != 0 {
					ln.Options = ListenerOptions{}
					if listenOps&(0x10<<j) != 0 {
						ln.Options = testProxyProtocol
					}
					next = append(next, ln)
				} else {
					removed = append(removed, ln)
				}
			}
			for j := range int(listenOps>>6) % 3 {
				ln := newListener(listenOps&(0x08>>j) != 0)
				next, added = append(next, ln), append(added, ln)
			}
			if len(next) == 0 {
				ln := newListener(false)
				next, added = append(next, ln), append(added, ln)
			}
			cfg, invalid := fuzzGeneration(generation+1, cfgOps)

			// Clients during the reload, which must get the status of a
			// generation so far, on the listeners open throughout: the port
			// of a closed one can go to another server.
			stopClients := make(chan struct{})
			var clients sync.WaitGroup
			for k := range 4 {
				clients.Go(func() {
					for n := 0; ; n++ {
						select {
						case <-stopClients:
							return
						default:
						}
						description, err := fuzzStatus(next[(k+n)%len(next)])
						if err != nil {
							continue // Rejected by a limit, or a listener closed by the reload.
						}
						if g, err := strconv.Atoi(description[1:]); err != nil || description[0] != 'g' || g > generation+1 {
							t.Errorf("description %q during the reload to generation %d", description, generation+1)
						}
					}
				})
			}
			serverListeners := make([]Listener, len(next))
			for j, ln := range next {
				serverListeners[j] = ln.Listener
			}
			err := srv.Reload(cfg, serverListeners...)
			close(stopClients)
			clients.Wait()

			if invalid {
				if err == nil {
					t.Fatalf("Reload() of invalid generation %d succeeded", generation+1)
				}
				for _, ln := range added {
					_ = ln.Close()
				}
				removed = nil
			} else {
				if err != nil {
					t.Fatalf("Reload() error: %v", err)
				}
				generation++
				current = next
			}

			// Dialing could reach another listener given the same port.
			for _, ln := range removed {
				tcp := ln.Listener.Listener.(*net.TCPListener)
				_ = tcp.SetDeadline(time.Now())
				if _, err := tcp.Accept(); !errors.Is(err, net.ErrClosed) {
					t.Errorf("removed listener %s not closed: Accept() error %v", ln.addr, err)
				}
			}
			want := fmt.Sprintf("g%d", generation)
			for _, ln := range current {
				deadline := time.Now().Add(2 * time.Second)
				for {
					description, err := fuzzStatus(ln)
					if err == nil && description == want {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("listener %s (proxy %v): description %q, error %v, want %q", ln.addr, ln.Options.ProxyProtocol, description, err, want)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			upstreams := srv.state.Load().cfg.Upstreams
			for id, n := range running {
				_, want := upstreams[id]
				deadline := time.Now().Add(2 * time.Second)
				for (n.Load() == 1) != want {
					if time.Now().After(deadline) {
						t.Fatalf("upstream %s: %d pollers running, want it polled: %v", id, n.Load(), want)
					}
					time.Sleep(time.Millisecond)
				}
			}
		}

		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve() error: %v", err)
		}
		if n := srv.connLimiter.count.Load(); n != 0 {
			t.Errorf("connection limiter counts %d connections after Serve returned", n)
		}
		srv.clientLimiter.mu.Lock()
		if len(srv.clientLimiter.counts) != 0 {
			t.Errorf("client limiter counts %v after Serve returned", srv.clientLimiter.counts)
		}
		srv.clientLimiter.mu.Unlock()
		for _, u := range made {
			if u.running.Load() != 0 {
				t.Errorf("upstream %s still running after Serve returned", u.id)
			}
		}
	})
}
