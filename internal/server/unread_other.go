//go:build !unix || aix

package server

import "net"

// hasUnreadData reports true: without a way to look at the receive queue,
// connections are closed gracefully.
func hasUnreadData(net.Conn) bool {
	return true
}
