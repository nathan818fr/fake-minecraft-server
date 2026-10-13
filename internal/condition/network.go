package condition

import (
	"fmt"
	"net/netip"
	"strings"
)

// ipv4MappedPrefixBits is the length of the prefix of IPv4-mapped IPv6
// addresses (::ffff:0:0/96), which hold the IPv4 address in their last 32 bits.
const ipv4MappedPrefixBits = 96

// ParseNetwork parses an IP address, as a prefix of its full length, or a CIDR
// prefix, as written in in_network and in the trusted proxies.
func ParseNetwork(s string) (netip.Prefix, error) {
	var prefix netip.Prefix
	var err error
	if strings.Contains(s, "/") {
		prefix, err = netip.ParsePrefix(s)
	} else {
		var addr netip.Addr
		addr, err = netip.ParseAddr(s)
		// Client addresses with a zone never match a prefix.
		if addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("%q has an IPv6 zone, which is not supported", s)
		}
		prefix = netip.PrefixFrom(addr, addr.BitLen())
	}
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address or CIDR prefix", s)
	}
	// Client addresses are unmapped, so IPv4-mapped prefixes must be too.
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < ipv4MappedPrefixBits {
			return netip.Prefix{}, fmt.Errorf("%q is an IPv4-mapped prefix shorter than /%d", s, ipv4MappedPrefixBits)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-ipv4MappedPrefixBits)
	}
	return prefix.Masked(), nil
}
