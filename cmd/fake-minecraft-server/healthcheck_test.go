package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// The health check passes against a running server, and fails where none
// answers.
func TestHealthcheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.sock")
	cfg, err := config.Load([]string{"FMS_LISTEN=unix:" + path})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(cfg.Server, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	listeners, err := listen(context.Background(), cfg.Listeners)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(listeners) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, listeners...) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error: %v", err)
		}
	})

	if err := healthcheck("unix:" + path); err != nil {
		t.Errorf("healthcheck() of the server error: %v", err)
	}
	if err := healthcheck("unix:" + filepath.Join(dir, "none.sock")); err == nil {
		t.Error("healthcheck() of no server = nil, want an error")
	}
}
