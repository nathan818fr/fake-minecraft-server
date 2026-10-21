package server

import (
	"net/netip"
	"testing"
	"time"
)

func TestClientLimiter(t *testing.T) {
	var l clientLimiter
	l.setLimit(2)
	a := netip.MustParseAddr("192.0.2.1")
	b := netip.MustParseAddr("192.0.2.2")

	for range 2 {
		if !l.acquire(a) {
			t.Fatal("acquire() refused a connection under the limit")
		}
	}
	if l.acquire(a) {
		t.Error("acquire() accepted a connection over the limit")
	}
	if !l.acquire(b) {
		t.Error("acquire() refused another client")
	}

	l.release(a)
	if !l.acquire(a) {
		t.Error("acquire() refused a connection after a release")
	}

	l.release(a)
	l.release(a)
	l.release(b)
	if len(l.counts) != 0 {
		t.Errorf("limiter not empty after releasing everything: %v", l.counts)
	}
}

func TestClientLimiterGroupsClients(t *testing.T) {
	var l clientLimiter
	l.setLimit(1)
	if !l.acquire(netip.MustParseAddr("2001:db8:1:2::1")) {
		t.Fatal("acquire() refused the first connection")
	}
	if l.acquire(netip.MustParseAddr("2001:db8:1:2:ffff::1")) {
		t.Error("acquire() did not group IPv6 addresses by /64")
	}
	if !l.acquire(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Error("acquire() grouped distinct IPv6 /64 prefixes")
	}

	if !l.acquire(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("acquire() refused an IPv4 client")
	}
	if l.acquire(netip.MustParseAddr("::ffff:192.0.2.1")) {
		t.Error("acquire() did not treat an IPv4-mapped address as IPv4")
	}
}

func TestRateLimiter(t *testing.T) {
	var l rateLimiter
	l.setRate(10)
	a := netip.MustParseAddr("192.0.2.1")
	now := time.Now()

	// A burst of twice the rate, then one connection every 1/rate seconds.
	for i := range 20 {
		if !l.allow(a, now) {
			t.Fatalf("allow() refused connection %d of the burst", i+1)
		}
	}
	if l.allow(a, now) {
		t.Error("allow() accepted a connection over the burst")
	}
	if !l.allow(netip.MustParseAddr("192.0.2.2"), now) {
		t.Error("allow() refused another client")
	}
	// Refusals do not count: the next connection is allowed on time.
	for range 5 {
		l.allow(a, now.Add(50*time.Millisecond))
	}
	if !l.allow(a, now.Add(100*time.Millisecond)) {
		t.Error("allow() refused a connection after 1/rate seconds")
	}
	if l.allow(a, now.Add(150*time.Millisecond)) {
		t.Error("allow() accepted a second connection within 1/rate seconds")
	}

	l.setRate(0)
	if !l.allow(a, now.Add(150*time.Millisecond)) {
		t.Error("allow() refused a connection without a rate")
	}
	l.setRate(10)
	if !l.allow(netip.Addr{}, now) {
		t.Error("allow() limited a client without an address")
	}
}

func TestRateLimiterGroupsClients(t *testing.T) {
	var l rateLimiter
	l.setRate(1)
	now := time.Now()
	for _, s := range []string{"2001:db8:1:2::1", "2001:db8:1:2:ffff::1"} {
		l.allow(netip.MustParseAddr(s), now)
	}
	if l.allow(netip.MustParseAddr("2001:db8:1:2::2"), now) {
		t.Error("allow() did not group IPv6 addresses by /64")
	}
	if !l.allow(netip.MustParseAddr("2001:db8:1:3::1"), now) {
		t.Error("allow() grouped distinct IPv6 /64 prefixes")
	}
}

// The clients tracked are bounded: expired ones are removed, and past the
// bound, new clients are not limited.
func TestRateLimiterBoundsClients(t *testing.T) {
	var l rateLimiter
	l.setRate(1)
	now := time.Now()
	client := func(i int) netip.Addr {
		return netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
	}
	for i := range maxRateClients {
		l.allow(client(i), now)
	}
	if len(l.next) != maxRateClients {
		t.Fatalf("tracked %d clients, want %d", len(l.next), maxRateClients)
	}
	extra := client(maxRateClients)
	for range 5 {
		if !l.allow(extra, now) {
			t.Fatal("allow() limited a client past the bound")
		}
	}

	later := now.Add(rateBurst)
	if !l.allow(extra, later) {
		t.Fatal("allow() refused a new client once the others expired")
	}
	if len(l.next) != 1 {
		t.Errorf("tracked %d clients after they expired, want 1", len(l.next))
	}
}
