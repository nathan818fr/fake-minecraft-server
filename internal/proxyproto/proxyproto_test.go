package proxyproto

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"strings"
	"testing"
)

func header(command, family byte, body []byte) string {
	h := append([]byte{}, signature...)
	h = append(h, command, family)
	h = binary.BigEndian.AppendUint16(h, uint16(len(body)))
	return string(append(h, body...))
}

var (
	tcp4Body = []byte{
		192, 0, 2, 1, // source address
		192, 0, 2, 2, // destination address
		0xdc, 0x04, // source port 56324
		0x63, 0xdd, // destination port 25565
	}
	tcp6Body = []byte{
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, // source address
		0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x02, // destination address
		0xdc, 0x04, // source port 56324
		0x63, 0xdd, // destination port 25565
	}
	tlv = []byte{0x04, 0x00, 0x03, 'a', 'b', 'c'}
)

func TestReadHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   netip.AddrPort
	}{
		{"TCP4", header(commandProxy, familyTCP4, tcp4Body), netip.MustParseAddrPort("192.0.2.1:56324")},
		{"TCP6", header(commandProxy, familyTCP6, tcp6Body), netip.MustParseAddrPort("[2001:db8::1]:56324")},
		{"TCP4 with TLVs", header(commandProxy, familyTCP4, append(tcp4Body[:12:12], tlv...)), netip.MustParseAddrPort("192.0.2.1:56324")},
		{"UNSPEC", header(commandProxy, 0x00, nil), netip.AddrPort{}},
		{"LOCAL", header(commandLocal, 0x00, nil), netip.AddrPort{}},
		{"LOCAL with addresses", header(commandLocal, familyTCP4, tcp4Body), netip.AddrPort{}},
		{"LOCAL with unix addresses", header(commandLocal, 0x31, make([]byte, 216)), netip.AddrPort{}},
		{"longest", header(commandProxy, familyTCP4, append(tcp4Body[:12:12], make([]byte, maxBodyLength-12)...)), netip.MustParseAddrPort("192.0.2.1:56324")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tc.header + "data"))
			got, err := ReadHeader(r)
			if err != nil {
				t.Fatalf("ReadHeader() error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ReadHeader() = %v, want %v", got, tc.want)
			}
			if rest, _ := io.ReadAll(r); string(rest) != "data" {
				t.Errorf("data after the header = %q, want %q", rest, "data")
			}
		})
	}
}

func TestReadHeaderErrors(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   error
	}{
		{"no header", "\x10\x00\x89\x06\x09localhost", ErrInvalidHeader},
		{"empty", "", io.EOF},
		{"no header, first bytes only", "\x10\x00", ErrInvalidHeader},
		{"version 1 header", "PROXY TCP4 192.0.2.1 192.0.2.2 56324 25565\r\n", ErrInvalidHeader},
		{"corrupted signature", "\r\n\r\n\x00\r\nQUIT\r\x21\x00\x00\x00", ErrInvalidHeader},
		{"truncated signature", "\r\n\r\n", io.ErrUnexpectedEOF},
		{"unsupported version", header(0x11, familyTCP4, tcp4Body), ErrInvalidHeader},
		{"unknown command", header(0x22, familyTCP4, tcp4Body), ErrInvalidHeader},
		{"udp", header(commandProxy, 0x12, tcp4Body), ErrInvalidHeader},
		{"addresses too short", header(commandProxy, familyTCP6, tcp4Body), ErrInvalidHeader},
		{"too long", header(commandProxy, familyTCP4, append(tcp4Body[:12:12], make([]byte, maxBodyLength)...)), ErrInvalidHeader},
		{"truncated header", header(commandProxy, familyTCP4, tcp4Body)[:14], io.ErrUnexpectedEOF},
		{"truncated addresses", header(commandProxy, familyTCP4, tcp4Body)[:20], io.ErrUnexpectedEOF},
		{"truncated TLVs", header(commandProxy, familyTCP4, append(tcp4Body[:12:12], tlv...))[:30], io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadHeader(bufio.NewReader(strings.NewReader(tc.header))); !errors.Is(err, tc.want) {
				t.Errorf("got error %v, want %v", err, tc.want)
			}
		})
	}
}

func BenchmarkReadHeader(b *testing.B) {
	h := header(commandProxy, familyTCP4, tcp4Body)
	src := strings.NewReader(h)
	r := bufio.NewReader(src)
	b.ReportAllocs()
	for b.Loop() {
		src.Reset(h)
		r.Reset(src)
		if _, err := ReadHeader(r); err != nil {
			b.Fatal(err)
		}
	}
}
