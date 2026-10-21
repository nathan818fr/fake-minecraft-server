package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/logging"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// openListeners opens addrs, closed at the end of the test, as listeners of a
// reloader.
func openListeners(t *testing.T, addrs ...config.Listener) []openListener {
	t.Helper()
	listeners, err := listen(context.Background(), addrs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAll(listeners) })
	open := make([]openListener, len(listeners))
	for i, ln := range listeners {
		open[i] = openListener{settings: addrs[i], Listener: ln}
	}
	return open
}

// Listeners with the same address stay open, with their new options.
func TestReloaderListen(t *testing.T) {
	dir := t.TempDir()
	kept := config.Listener{Network: "unix", Address: filepath.Join(dir, "kept.sock")}
	removed := config.Listener{Network: "unix", Address: filepath.Join(dir, "removed.sock")}
	r := reloader{listeners: openListeners(t, kept, removed)}

	kept.Options = server.ListenerOptions{ProxyProtocol: true}
	added := config.Listener{Network: "unix", Address: filepath.Join(dir, "added.sock")}
	listeners, opened, err := r.listen(context.Background(), []config.Listener{kept, added})
	if err != nil {
		t.Fatalf("listen() error: %v", err)
	}
	t.Cleanup(func() { closeAll(opened) })

	if len(listeners) != 2 || len(opened) != 1 {
		t.Fatalf("listen() = %v, opened %v, want 2 listeners, 1 opened", listeners, opened)
	}
	if got := listeners[0]; got.Listener.Listener != r.listeners[0].Listener.Listener || !got.Options.ProxyProtocol {
		t.Errorf("kept listener = %+v, want the open one, with the PROXY protocol", got)
	}
	if got := listeners[1]; got.Listener.Listener != opened[0].Listener || got.settings.Address != added.Address {
		t.Errorf("added listener = %+v, want the opened one, at %s", got, added.Address)
	}
}

// An address that a removed listener holds can only be replaced by a restart,
// which the error tells.
func TestReloaderListenOverlappingAddress(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("other systems may bind both addresses, as Go sets SO_REUSEADDR")
	}
	old := openListeners(t, config.Listener{Network: "tcp", Address: "127.0.0.1:0"})
	port := old[0].Addr().(*net.TCPAddr).Port
	old[0].settings.Address = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	r := reloader{listeners: old}

	_, _, err := r.listen(context.Background(), []config.Listener{{Network: "tcp", Address: net.JoinHostPort("0.0.0.0", strconv.Itoa(port))}})
	if err == nil || !strings.Contains(err.Error(), "restart") {
		t.Errorf("listen() error = %v, want one telling to restart", err)
	}
}

// lockedBuffer collects logs written by the server while the test reads them.
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

func writeConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// statusDescription returns the description of the status that the server on
// the Unix socket at path answers.
func statusDescription(t *testing.T, path string) string {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	request := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 777, ServerAddress: "localhost", ServerPort: 25565, Intent: modernproto.IntentStatus})
	if _, err := conn.Write(modernproto.AppendPacket(request, modernproto.StatusRequestID, nil)); err != nil {
		t.Fatal(err)
	}
	data, err := modernproto.ReadStatusResponse(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	var st struct{ Description json.RawMessage }
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	return string(st.Description)
}

// A reload applies the configuration file again, listeners and log level
// included, or keeps the running one if the file is invalid.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	oldSocket, newSocket := filepath.Join(dir, "old.sock"), filepath.Join(dir, "new.sock")
	writeConfig(t, path, "listen: [unix:"+oldSocket+"]\nservers: {default: {status: {description: Old}}}\n")
	t.Setenv("FMS_CONFIG_FILE", path)

	cfg, err := config.Load(os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	level := new(slog.LevelVar)
	logger := slog.New(logging.NewTextHandler(logs, level))
	srv, err := server.New(cfg.Server, logger)
	if err != nil {
		t.Fatal(err)
	}
	r := reloader{srv: srv, logger: logger, level: level, format: cfg.LogFormat, listeners: openListeners(t, cfg.Listeners...)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, r.listeners[0].Listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve() error: %v", err)
		}
	})

	writeConfig(t, path, "listen: [unix:"+newSocket+"]\nlog: {level: debug}\nscript: print('loaded')\nservers: {default: {status: {description: New}}}\n")
	r.reload(ctx)
	// Prints at the new level.
	if want := "DEBUG script print: hook=load text=\"loaded\"\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("logs = %q, want %q", logs, want)
	}
	if !strings.Contains(logs.String(), "INFO  reloaded\n") {
		t.Fatalf("logs = %q, want a reloaded line", logs)
	}
	if got := statusDescription(t, newSocket); got != `"New"` {
		t.Errorf("description = %s, want \"New\"", got)
	}
	if _, err := os.Stat(oldSocket); !os.IsNotExist(err) {
		t.Errorf("old socket not removed: stat error %v", err)
	}
	if level.Level() != slog.LevelDebug {
		t.Errorf("log level = %v, want debug", level.Level())
	}

	writeConfig(t, path, "listen: [unix:"+newSocket+"]\nconnections: {max: -1}\nservers: {default: {status: {description: Invalid}}}\n")
	r.reload(ctx)
	if !strings.Contains(logs.String(), "ERROR reload failed: error=") {
		t.Errorf("logs = %q, want a reload failed line", logs)
	}
	if got := statusDescription(t, newSocket); got != `"New"` {
		t.Errorf("description after the invalid reload = %s, want \"New\"", got)
	}
}
