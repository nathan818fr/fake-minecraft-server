package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/config"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/server"
)

// listen opens a listener on each address. If one fails, it closes the
// others, so that the server starts on all its addresses or none.
func listen(ctx context.Context, addrs []config.Listener) ([]server.Listener, error) {
	listenConfig := net.ListenConfig{
		// Connections are short-lived and bounded by the connection timeout,
		// so TCP keep-alive would only cost timers.
		KeepAlive: -1,
	}
	listeners := make([]server.Listener, 0, len(addrs))
	for _, addr := range addrs {
		var ln net.Listener
		var err error
		if addr.Network == "unix" {
			ln, err = listenUnix(ctx, &listenConfig, addr.Address)
		} else {
			ln, err = listenConfig.Listen(ctx, addr.Network, addr.Address)
		}
		if err != nil {
			closeAll(listeners)
			return nil, err
		}
		listeners = append(listeners, server.Listener{Listener: ln, Options: addr.Options})
	}
	return listeners, nil
}

// listenUnix listens on the Unix socket at path, whose file it claims first
// (see claimSocket).
func listenUnix(ctx context.Context, listenConfig *net.ListenConfig, path string) (net.Listener, error) {
	if strings.HasPrefix(path, "@") {
		return listenConfig.Listen(ctx, "unix", path) // Abstract socket (Linux), without a file.
	}
	claim, err := claimSocket(ctx, path)
	if err != nil {
		return nil, err
	}
	ln, err := listenConfig.Listen(ctx, "unix", path)
	if err != nil {
		_ = claim.Close()
		return nil, err
	}
	return claimedListener{Listener: ln, claim: claim}, nil
}

// claimedListener releases the claim on its socket file when closed, once the
// file is removed, so that the next server cannot recreate it before.
type claimedListener struct {
	net.Listener
	claim io.Closer
}

func (l claimedListener) Close() error {
	err := l.Listener.Close()
	_ = l.claim.Close()
	return err
}

// claimSocket makes this server the only one to use the Unix socket file at
// path until the returned claim is closed: it locks path.lock (see
// lockSocket), then removes the socket file if a crashed server left it.
func claimSocket(ctx context.Context, path string) (io.Closer, error) {
	lock, err := lockSocket(path)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", path, err)
	}
	if err := removeStaleSocket(ctx, path); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func closeAll(listeners []server.Listener) {
	for _, ln := range listeners {
		_ = ln.Close()
	}
}

// removeStaleSocket removes the Unix socket file at path if it was left by a
// crashed server and nothing listens on it anymore. Anything else at path is
// left for Listen to report.
func removeStaleSocket(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != fs.ModeSocket {
		return nil
	}
	// Even claimed, the socket may belong to another program.
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return nil
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return nil
	}
	return os.Remove(path)
}
