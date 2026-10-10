package server

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

// readBufferSize fits the handshake and status request that clients send
// together in their first segment.
const readBufferSize = 512

// maxDrainedBytes bounds what closeGracefully reads. It exceeds what clients
// may still send by then, such as a 1.19 Login Start with its signature data.
const maxDrainedBytes = 8 << 10

// connBuffers holds the buffers of a connection, and its session, which
// escapes to the heap. They are pooled, so that floods of connections do not
// allocate them over and over.
type connBuffers struct {
	head    headRecorder
	reader  *bufio.Reader
	payload [modernproto.MaxHandshakePayloadLength]byte
	session session
}

var connBuffersPool = sync.Pool{
	New: func() any {
		buffers := &connBuffers{}
		buffers.reader = bufio.NewReaderSize(&buffers.head, readBufferSize)
		return buffers
	},
}

// peerAddr returns the address of the peer of conn, or an invalid address for
// Unix sockets, whose peers have none.
func peerAddr(conn net.Conn) netip.AddrPort {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}
	return unmapAddrPort(addr.AddrPort())
}

// isUnixPeer reports whether addr, made by peerAddr, stands for a Unix socket
// peer, which has no address.
func isUnixPeer(addr netip.AddrPort) bool {
	return !addr.IsValid()
}

// unmapAddrPort turns an IPv4-mapped IPv6 address (::ffff:192.0.2.1) into
// IPv4. Dual-stack listeners can report IPv4 clients either way, and logs must
// identify them the same way.
func unmapAddrPort(addr netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
}

// abort closes conn with a TCP reset, which releases it immediately instead
// of leaving it in the TIME_WAIT state.
func abort(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

// closeGracefully half-closes conn and drains it until deadline before closing
// it. Closing a TCP socket with unread data makes the kernel send a reset, on
// which some systems (Windows) drop the response that the client did not read
// yet.
func closeGracefully(conn net.Conn, deadline time.Time) {
	_ = conn.SetReadDeadline(deadline)
	// Implemented by TCP and Unix sockets.
	if hc, ok := conn.(interface{ CloseWrite() error }); ok && hc.CloseWrite() == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(conn, maxDrainedBytes))
	}
	_ = conn.Close()
}

// closeAnswered closes conn, whose client was answered after the server read
// its whole request, and then sends nothing (see session.readWhole). Unless
// data came meanwhile, closing sends a FIN as the shutdown of closeGracefully
// does, so its drain, which waits for the client to close, is only needed
// then.
func closeAnswered(conn net.Conn, deadline time.Time) {
	if hasUnreadData(conn) {
		closeGracefully(conn, deadline)
		return
	}
	_ = conn.Close()
}
