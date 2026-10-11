//go:build unix

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/internal/server"
)

// A second server cannot take the socket of a running one, only take it over
// once it stops.
func TestListenLocksSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.sock")
	addrs := []listenAddr{{"unix", path}}
	first, err := listen(context.Background(), addrs, server.ListenerOptions{})
	if err != nil {
		t.Fatalf("listen() error: %v", err)
	}

	if listeners, err := listen(context.Background(), addrs, server.ListenerOptions{}); err == nil || !strings.Contains(err.Error(), "another server uses it") {
		closeAll(listeners)
		t.Fatalf("second listen() error = %v, want the socket locked", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("socket of the first server was removed: %v", err)
	}

	closeAll(first)
	again, err := listen(context.Background(), addrs, server.ListenerOptions{})
	if err != nil {
		t.Fatalf("listen() after the first server stopped: %v", err)
	}
	closeAll(again)
}

// A symlink in place of the lock file is not followed, so that the server
// neither opens nor creates the file that it points to.
func TestListenRefusesSymlinkedLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.sock")
	target := filepath.Join(dir, "elsewhere")
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}

	addrs := []listenAddr{{"unix", path}}
	if listeners, err := listen(context.Background(), addrs, server.ListenerOptions{}); err == nil {
		closeAll(listeners)
		t.Fatal("listen() succeeded, want an error")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("the target of the symlink was created: lstat error %v", err)
	}
}
