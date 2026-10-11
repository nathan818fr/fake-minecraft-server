package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/internal/server"
)

func TestListenReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sock")
	// A listener that is closed without removing its file, as after a crash.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()

	listeners, err := listen(context.Background(), []listenAddr{{"unix", path}}, server.ListenerOptions{})
	if err != nil {
		t.Fatalf("listen() error: %v", err)
	}
	closeAll(listeners)
}

func TestListenKeepsSocketInUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sock")
	inUse, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inUse.Close() })

	if listeners, err := listen(context.Background(), []listenAddr{{"unix", path}}, server.ListenerOptions{}); err == nil {
		closeAll(listeners)
		t.Fatal("listen() succeeded, want an error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket file of the running listener was removed: %v", err)
	}
}

// The server starts on all its addresses or none.
func TestListenClosesAllOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sock")
	invalid := filepath.Join(t.TempDir(), "missing", "server.sock")

	if listeners, err := listen(context.Background(), []listenAddr{{"unix", path}, {"unix", invalid}}, server.ListenerOptions{}); err == nil {
		closeAll(listeners)
		t.Fatal("listen() succeeded, want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("first listener was not closed: stat error %v", err)
	}
}
