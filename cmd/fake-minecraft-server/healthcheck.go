package main

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// defaultHealthcheckAddr is the default listener, seen from the machine or the
// container of the server.
const defaultHealthcheckAddr = "127.0.0.1:25565"

// healthcheck polls the server at addr, host:port or unix:path, like an
// upstream, for the health checks of containers: the image has no other
// program to connect to it.
func healthcheck(addr string) error {
	// The default timeout of upstreams.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	network := "tcp"
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		network, addr = "unix", path
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	_, _, err = upstream.Poll(conn, upstream.Config{
		ServerAddress:   "localhost",
		ServerPort:      modernproto.DefaultPort,
		ProtocolVersion: -1,
	})
	return err
}
