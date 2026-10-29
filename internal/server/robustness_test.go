package server

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

// crashPayloads are the opening bytes that have crashed or stalled Minecraft
// servers over the years, and the traffic that reaches a public port by
// accident: HTTP and TLS from scanners and browsers, oversized or unterminated
// lengths, and lengths that promise far more than the sender sends. The server
// must reject each one and free its slot by the handshake deadline, while still
// serving others.
func crashPayloads() map[string][]byte {
	oversized := func(length int32) []byte { return modernproto.AppendVarInt(nil, length) }
	return map[string][]byte{
		// A browser or an HTTP health check aimed at the port. Its first byte,
		// 'G', is a packet length of 71, so the server waits for a 71-byte
		// packet it will never parse as a handshake.
		"http GET": []byte("GET / HTTP/1.1\r\nHost: x\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\n\r\n"),
		// An HTTPS client or TLS scanner: a TLS 1.2 ClientHello record. Its
		// first byte, 0x16, is a packet length of 22.
		"tls hello": append([]byte{0x16, 0x03, 0x01, 0x00, 0x2e, 0x01, 0x00, 0x00, 0x2a, 0x03, 0x03},
			make([]byte, 0x2e-6)...),
		// A packet length of five 0x80 bytes: a VarInt that never ends. The
		// length reader must stop and reject it, not read forever.
		"varint never ends": {0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80},
		// A packet length with the sign bit set, decoded as a large negative
		// int32 (over 3 bytes, vanilla's limit), rejected as invalid.
		"negative length": {0xff, 0xff, 0xff, 0xff, 0x0f},
		// A packet that claims 2 MiB, then sends nothing: the server must not
		// buffer 2 MiB waiting for it.
		"2 MiB length, nothing": oversized(2 << 20),
		// The largest length the 3-byte reader accepts, then nothing.
		"huge length, nothing": {0xff, 0xff, 0x7f},
		// A handshake whose server-address string claims 32767 bytes but sends
		// none: readString must bound what it reads to the address limit.
		"huge address length": func() []byte {
			p := modernproto.AppendVarInt(nil, 767)                                           // Protocol.
			p = modernproto.AppendVarInt(p, 32767)                                            // String length, over the 255-byte limit.
			return modernproto.AppendPacketHeader(nil, modernproto.HandshakeID, 1<<20+len(p)) // Length promises far more.
		}(),
		// A valid status handshake, then a Login Start that claims a 2 MiB
		// username: the username reader must reject it by its 16-byte limit.
		"huge username": func() []byte {
			b := modernproto.AppendHandshake(nil, modernproto.Handshake{ProtocolVersion: 767, ServerAddress: "localhost", ServerPort: 25565, Intent: modernproto.IntentLogin})
			b = modernproto.AppendPacketHeader(b, modernproto.LoginStartID, 2<<20)
			return append(b, modernproto.AppendVarInt(nil, 2<<20)...)
		}(),
		// A zero-length packet: length 0 leaves no room for even the packet ID.
		"zero length": {0x00},
		// A legacy 1.6 ping whose MC|PingHost length promises far more than it
		// sends, to see the server bound its wait.
		"legacy overlong host": {0xfe, 0x01, 0xfa, 0x7f, 0xff, 0x00, 0x00},
		// All zero bytes: a run of empty packets (length 0), as some stress
		// tools send. Each is rejected; the server must not spin on them.
		"zero flood": make([]byte, 4096),
		// A handshake that parses but with an intent the server does not know,
		// then silence.
		"unknown intent": func() []byte {
			p := modernproto.AppendVarInt(nil, 767)
			p = modernproto.AppendString(p, "localhost")
			p = binary.BigEndian.AppendUint16(p, 25565)
			p = modernproto.AppendVarInt(p, 99) // Not status, login or transfer.
			return modernproto.AppendPacket(nil, modernproto.HandshakeID, p)
		}(),
	}
}

func TestCrashPayloads(t *testing.T) {
	cfg := testConfig
	// Short deadlines so the test can assert that a bad connection is freed by
	// the handshake deadline, as it would be under a real flood.
	cfg.ConnectionTimeout = 600 * time.Millisecond
	cfg.HandshakeTimeout = 300 * time.Millisecond
	addr, stop := startServerWithLogger(t, cfg, slog.New(slog.DiscardHandler))
	defer stop()

	// A legitimate client keeps pinging throughout, from its own goroutine, to
	// check that the payloads never stop the server from serving.
	var served, failed atomic.Int64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if err := tryStatus(addr); err != nil {
				failed.Add(1)
			} else {
				served.Add(1)
			}
		}
	}()

	for name, payload := range crashPayloads() {
		t.Run(name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", addr, clientTimeout)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = conn.Close() }()

			// The server must free the slot by the handshake deadline: give it
			// that plus a margin, well under the connection timeout, and read
			// until it closes. A payload that bought a longer-lived slot (read
			// held open to the connection timeout) would miss this deadline.
			_ = conn.SetDeadline(time.Now().Add(cfg.HandshakeTimeout + 200*time.Millisecond))
			if _, err := conn.Write(payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := io.Copy(io.Discard, conn); os.IsTimeout(err) {
				t.Fatal("server did not close the connection within the handshake deadline")
			}
		})
	}

	close(done)
	wg.Wait()
	if served.Load() == 0 {
		t.Fatalf("the legitimate client was never served (%d failures)", failed.Load())
	}
}

// TestShutdownBounded checks that stopping the server with many connections
// held open (clients that got their status and never ping, nor close) finishes
// within the connection timeout: in-flight connections are not interrupted by
// shutdown, so a client cannot stall it past that bound. The accept loops and
// upstreams stop at once; only the held connections keep Serve waiting, until
// their own deadline.
func TestShutdownBounded(t *testing.T) {
	const held = 500
	cfg := testConfig
	cfg.ConnectionTimeout = 500 * time.Millisecond
	cfg.HandshakeTimeout = 200 * time.Millisecond
	cfg.MaxConnections = 0

	ln := listenTCP(t)
	addr := ln.Addr().String()
	srv := newServerWithUpstreams(t, cfg, nil, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, Listener{Listener: ln}) }()

	for range held {
		holdConnection(t, addr) // Status exchanged, then waits for a ping it never sends.
	}

	start := time.Now()
	cancel()
	select {
	case err := <-served:
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
		if elapsed > cfg.ConnectionTimeout+500*time.Millisecond {
			t.Fatalf("shutdown took %v, longer than the connection timeout %v", elapsed, cfg.ConnectionTimeout)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not finish: held connections stalled it")
	}
}
