//go:build unix && !aix

package server

import (
	"errors"
	"net"
	"syscall"
)

// hasUnreadData reports whether data that conn did not read waits in its
// receive queue, without waiting for any. It reports true when it cannot tell.
func hasUnreadData(conn net.Conn) bool {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return true
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return true
	}
	unread := true
	_ = raw.Control(func(fd uintptr) {
		var b [1]byte
		// No byte and no error: the client closed, leaving nothing.
		n, _, err := syscall.Recvfrom(int(fd), b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		unread = n > 0 || (err != nil && !errors.Is(err, syscall.EAGAIN))
	})
	return unread
}
