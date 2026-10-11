// Package proxyproto reads PROXY protocol version 2 headers, which proxies
// send before the proxied data to pass on the client address. Version 1, a
// text format, is deliberately not supported.
//
// See https://www.haproxy.org/download/3.0/doc/proxy-protocol.txt
package proxyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
)

var ErrInvalidHeader = errors.New("invalid PROXY protocol header")

var signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// The header is the signature, the version and command, the address family
// and protocol, then the length of the body that follows it: the addresses,
// optionally followed by TLVs.
const (
	headerLength = 16

	commandOffset    = 12
	familyOffset     = 13
	bodyLengthOffset = 14
)

// maxBodyLength bounds the length of the body, as the specification allows.
// The addresses take at most 216 bytes (for Unix sockets), which leaves room
// for the TLVs of common proxies.
const maxBodyLength = 1024

const (
	commandLocal = 0x20
	commandProxy = 0x21

	familyUnspec = 0x00
	familyTCP4   = 0x11
	familyTCP6   = 0x21

	ipv4Length = 4
	ipv6Length = 16
	portLength = 2

	// Source and destination addresses, then ports.
	tcp4AddressesLength = 2*ipv4Length + 2*portLength
	tcp6AddressesLength = 2*ipv6Length + 2*portLength
)

// ReadHeader reads a PROXY protocol header and returns the source address of
// the proxied connection. It returns an invalid AddrPort for headers that do
// not carry one, such as the health checks of some proxies, in which case the
// address of the connection itself applies. TLVs are ignored.
func ReadHeader(r *bufio.Reader) (netip.AddrPort, error) {
	// Checking the first byte rejects most connections that bypass the proxy
	// without waiting for a whole header.
	first, err := r.Peek(1)
	if err != nil {
		return netip.AddrPort{}, err
	}
	if first[0] != signature[0] {
		return netip.AddrPort{}, fmt.Errorf("%w: missing signature", ErrInvalidHeader)
	}

	header, err := r.Peek(headerLength)
	if err != nil {
		return netip.AddrPort{}, noEOF(err)
	}
	if !bytes.Equal(header[:len(signature)], signature) {
		return netip.AddrPort{}, fmt.Errorf("%w: missing signature", ErrInvalidHeader)
	}
	command, family := header[commandOffset], header[familyOffset]
	bodyLength := int(binary.BigEndian.Uint16(header[bodyLengthOffset:]))
	_, _ = r.Discard(headerLength)

	if command != commandLocal && command != commandProxy {
		return netip.AddrPort{}, fmt.Errorf("%w: unsupported version and command %#x", ErrInvalidHeader, command)
	}
	if bodyLength > maxBodyLength {
		return netip.AddrPort{}, fmt.Errorf("%w: length %d exceeds %d", ErrInvalidHeader, bodyLength, maxBodyLength)
	}

	if command == commandLocal {
		// Sent by proxies for their own connections, such as health checks:
		// the addresses, whatever their family, are ignored.
		_, err := r.Discard(bodyLength)
		return netip.AddrPort{}, noEOF(err)
	}

	var addressesLength int
	switch family {
	case familyTCP4:
		addressesLength = tcp4AddressesLength
	case familyTCP6:
		addressesLength = tcp6AddressesLength
	case familyUnspec:
	default:
		return netip.AddrPort{}, fmt.Errorf("%w: unsupported address family and protocol %#x", ErrInvalidHeader, family)
	}
	if bodyLength < addressesLength {
		return netip.AddrPort{}, fmt.Errorf("%w: %d bytes are too short for the addresses", ErrInvalidHeader, bodyLength)
	}

	var addresses [tcp6AddressesLength]byte
	if _, err := io.ReadFull(r, addresses[:addressesLength]); err != nil {
		return netip.AddrPort{}, noEOF(err)
	}
	if _, err := r.Discard(bodyLength - addressesLength); err != nil {
		return netip.AddrPort{}, noEOF(err)
	}

	switch family {
	case familyTCP4:
		addr := netip.AddrFrom4([ipv4Length]byte(addresses[:ipv4Length]))
		return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(addresses[2*ipv4Length:])), nil
	case familyTCP6:
		addr := netip.AddrFrom16([ipv6Length]byte(addresses[:ipv6Length]))
		return netip.AddrPortFrom(addr, binary.BigEndian.Uint16(addresses[2*ipv6Length:])), nil
	default:
		return netip.AddrPort{}, nil // UNSPEC: no client address.
	}
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
