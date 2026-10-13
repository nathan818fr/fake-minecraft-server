package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
)

func listenUnix(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(t.TempDir(), "server.sock"))
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// With several listeners, each line tells which one the client connected to.
func TestServeMultipleListeners(t *testing.T) {
	tcp, unix := listenTCP(t), listenUnix(t)
	var logs bytes.Buffer
	stop := serve(t, testConfig, nil, slog.New(logging.NewTextHandler(&logs, nil)), Listener{Listener: tcp}, Listener{Listener: unix})

	for _, c := range []*client{dial(t, tcp.Addr().String()), dialNetwork(t, "unix", unix.Addr().String())} {
		c.statusExchange(777)
		_ = c.conn.Close()
	}
	stop()

	for _, want := range []string{
		`INFO  \[127\.0\.0\.1\]:\d+ server list ping: listener=127\.0\.0\.1:\d+ protocol=777 `,
		`INFO  unix server list ping: listener=unix:\S+server\.sock protocol=777 `,
	} {
		if !regexp.MustCompile(`(?m)^\[\S+\] ` + want).MatchString(logs.String()) {
			t.Errorf("logs = %q, want a line matching %q", logs.String(), want)
		}
	}
}

// Unix socket peers have no address: the per-client limit does not apply to
// them, and a Unix socket with the PROXY protocol trusts them.
func TestUnixSocketPeers(t *testing.T) {
	t.Run("not limited per client", func(t *testing.T) {
		cfg := testConfig
		cfg.MaxConnectionsPerIP = 1
		ln := listenUnix(t)
		serve(t, cfg, nil, slog.New(slog.DiscardHandler), Listener{Listener: ln})

		idle := dialNetwork(t, "unix", ln.Addr().String())
		idle.write([]byte{0x10})
		time.Sleep(50 * time.Millisecond)
		dialNetwork(t, "unix", ln.Addr().String()).statusExchange(777)
	})

	t.Run("trusted proxy", func(t *testing.T) {
		ln := listenUnix(t)
		var logs bytes.Buffer
		stop := serve(t, testConfig, nil, slog.New(logging.NewTextHandler(&logs, nil)), Listener{Listener: ln, Options: ListenerOptions{ProxyProtocol: true}})

		c := dialNetwork(t, "unix", ln.Addr().String())
		c.write(proxyHeader("192.0.2.1:56324"))
		c.statusExchange(777)
		_ = c.conn.Close()
		stop()

		want := `INFO  \[192\.0\.2\.1\]:56324 server list ping: proxy=unix protocol=777 `
		if !regexp.MustCompile(`(?m)^\[\S+\] ` + want).MatchString(logs.String()) {
			t.Errorf("logs = %q, want a line matching %q", logs.String(), want)
		}
	})
}

// If a listener fails, Serve stops the others, so that the server does not
// keep running on part of its addresses.
func TestServeStopsAllListenersOnError(t *testing.T) {
	failing := listenUnix(t)
	other := listenUnix(t)
	srv, err := New(testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), Listener{Listener: failing}, Listener{Listener: other})
	}()

	_ = failing.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Serve() returned nil, want the error of the failed listener")
		}
	case <-time.After(clientTimeout):
		t.Fatal("Serve() did not return")
	}
	if conn, err := net.Dial("unix", other.Addr().String()); err == nil {
		_ = conn.Close()
		t.Error("the other listener still accepts connections")
	}
}
