package server

import (
	"net/netip"
	"testing"
)

func TestClientLimiter(t *testing.T) {
	l := newClientLimiter(2)
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
	l := newClientLimiter(1)
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
