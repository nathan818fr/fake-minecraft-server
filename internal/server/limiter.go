package server

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// ipv6ClientPrefixBits is the length of the prefix that identifies IPv6
// clients, as a single IPv6 host usually controls a whole /64.
const ipv6ClientPrefixBits = 64

// clientLimiter bounds the number of concurrent connections per client.
// Clients are identified by their IPv4 address or IPv6 /64 prefix (see
// ipv6ClientPrefixBits). Clients without an address (Unix socket peers) are not
// limited. A limit of 0 disables it. Connections are counted even without a
// limit, so that a limit set by a reload counts the ones already open. Its
// zero value is ready to use.
type clientLimiter struct {
	mu     sync.Mutex
	limit  int
	counts map[netip.Prefix]int
}

func (l *clientLimiter) setLimit(limit int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.limit = limit
}

func (l *clientLimiter) acquire(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	client := clientPrefix(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limit > 0 && l.counts[client] >= l.limit {
		return false
	}
	if l.counts == nil {
		l.counts = make(map[netip.Prefix]int)
	}
	l.counts[client]++
	return true
}

func (l *clientLimiter) release(addr netip.Addr) {
	if !addr.IsValid() {
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

// rateBurst is how far ahead of now the connections of a client may be
// counted, each worth 1/rate seconds: bursts reach twice the rate per second.
const rateBurst = 2 * time.Second

// maxRateClients bounds the clients that rateLimiter tracks, so that a flood
// from many addresses cannot grow its memory (a few MiB): past it, new clients
// are not limited until tracked ones expire.
const maxRateClients = 1 << 16

// rateLimiter bounds the rate of new connections per client, identified like
// clientLimiter, with the generic cell rate algorithm. Clients without an
// address (Unix socket peers) are not limited. A rate of 0 disables it. Its
// zero value is ready to use.
type rateLimiter struct {
	mu   sync.Mutex
	rate int
	// next holds, for each client, when its counted connections expire: a new
	// one is allowed while that stays within rateBurst of now.
	next map[netip.Prefix]time.Time
	// swept is when the expired clients were last removed, at most once per
	// rateBurst, so that sweeps cost little even with many clients.
	swept time.Time
}

func (l *rateLimiter) setRate(rate int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = rate
}

// allow reports whether the client at addr may open a new connection at now,
// and counts it if so.
func (l *rateLimiter) allow(addr netip.Addr, now time.Time) bool {
	if !addr.IsValid() {
		return true
	}
	client := clientPrefix(addr)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rate <= 0 {
		return true
	}
	last, tracked := l.next[client]
	next := now
	if tracked && last.After(now) {
		next = last
	}
	next = next.Add(time.Second / time.Duration(l.rate))
	if next.Sub(now) > rateBurst {
		return false
	}
	if !tracked {
		if now.Sub(l.swept) >= rateBurst {
			l.sweep(now)
		}
		if len(l.next) >= maxRateClients {
			return true
		}
		if l.next == nil {
			l.next = make(map[netip.Prefix]time.Time)
		}
	}
	l.next[client] = next
	return true
}

// sweep removes the clients whose connections expired.
func (l *rateLimiter) sweep(now time.Time) {
	for client, next := range l.next {
		if !next.After(now) {
			delete(l.next, client)
		}
	}
	l.swept = now
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

// connLimiter bounds the number of concurrent connections. A limit of 0
// disables it. Like clientLimiter, it counts connections even without a limit.
// Its zero value is ready to use.
type connLimiter struct {
	limit atomic.Int64
	count atomic.Int64
}

func (l *connLimiter) setLimit(limit int) {
	l.limit.Store(int64(limit))
}

func (l *connLimiter) tryAcquire() bool {
	count := l.count.Add(1)
	if limit := l.limit.Load(); limit > 0 && count > limit {
		l.count.Add(-1)
		return false
	}
	return true
}

func (l *connLimiter) release() {
	l.count.Add(-1)
}
