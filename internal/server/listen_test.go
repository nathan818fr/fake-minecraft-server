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

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
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
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unix := listenUnix(t)
	var logs bytes.Buffer
	stop := serve(t, testConfig, slog.New(logging.NewTextHandler(&logs, nil)), tcp, unix)

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
	go func() { done <- srv.Serve(context.Background(), failing, other) }()

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
