package server

import (
	"net/netip"
	"sync"
)

// ipv6ClientPrefixBits is the length of the prefix that identifies IPv6
// clients, as a single IPv6 host usually controls a whole /64.
const ipv6ClientPrefixBits = 64

// clientLimiter bounds the number of concurrent connections per client.
// Clients are identified by their IPv4 address or IPv6 /64 prefix (see
// ipv6ClientPrefixBits). Clients without an address (Unix socket peers) are not
// limited. A limit of 0 disables it.
type clientLimiter struct {
	limit int

	mu     sync.Mutex
	counts map[netip.Prefix]int
}

func newClientLimiter(limit int) *clientLimiter {
	return &clientLimiter{limit: limit, counts: make(map[netip.Prefix]int)}
}

func (l *clientLimiter) acquire(addr netip.Addr) bool {
	if l.limit <= 0 || !addr.IsValid() {
		return true
	}
	client := clientPrefix(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[client] >= l.limit {
		return false
	}
	l.counts[client]++
	return true
}

func (l *clientLimiter) release(addr netip.Addr) {
	if l.limit <= 0 || !addr.IsValid() {
		return
	}
	client := clientPrefix(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[client] <= 1 {
		delete(l.counts, client)
	} else {
		l.counts[client]--
	}
}

func clientPrefix(addr netip.Addr) netip.Prefix {
	addr = addr.Unmap()
	bits := addr.BitLen()
	if addr.Is6() {
		bits = ipv6ClientPrefixBits
	}
	prefix, _ := addr.Prefix(bits)
	return prefix
}

// semaphore bounds the number of concurrent connections. A nil semaphore is
// unlimited.
type semaphore chan struct{}

func newSemaphore(size int) semaphore {
	if size <= 0 {
		return nil
	}
	return make(semaphore, size)
}

func (s semaphore) tryAcquire() bool {
	if s == nil {
		return true
	}
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s semaphore) release() {
	if s != nil {
		<-s
	}
}
