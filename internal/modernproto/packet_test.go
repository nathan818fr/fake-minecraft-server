package modernproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func newReader(data []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(data))
}

func TestAppendPacket(t *testing.T) {
	got := AppendPacket(nil, 0x01, []byte{0xaa, 0xbb})
	want := []byte{0x03, 0x01, 0xaa, 0xbb}
	if !bytes.Equal(got, want) {
		t.Errorf("AppendPacket() = % x, want % x", got, want)
	}
}

func TestReadPacket(t *testing.T) {
	stream := AppendPacket(nil, 0x00, nil)
	stream = AppendPacket(stream, 0x01, []byte("payload"))
	r := newReader(stream)
	buf := make([]byte, 7)

	if payload, err := ReadPacket(r, 0x00, nil); err != nil || len(payload) != 0 {
		t.Errorf("ReadPacket() = % x, %v, want an empty payload", payload, err)
	}
	if payload, err := ReadPacket(r, 0x01, buf); err != nil || string(payload) != "payload" {
		t.Errorf("ReadPacket() = %q, %v, want %q", payload, err, "payload")
	}
	if _, err := ReadPacket(r, 0x00, buf); !errors.Is(err, io.EOF) {
		t.Errorf("ReadPacket() at end of stream: got error %v, want io.EOF", err)
	}
}

func TestReadPacketErrors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{"zero length", []byte{0x00}, ErrInvalidPacketLength},
		{"negative length", AppendVarInt(nil, -1), ErrVarIntTooLong},
		{"payload larger than buffer", AppendPacket(nil, 0x00, make([]byte, 5)), ErrInvalidPacketLength},
		{"wrong id", AppendPacket(nil, 0x02, nil), ErrUnexpectedPacket},
		{"non-minimal id", []byte{0x02, 0x80, 0x00}, ErrUnexpectedPacket},
		{"truncated length", []byte{0x80}, io.ErrUnexpectedEOF},
		{"missing id", []byte{0x01}, io.ErrUnexpectedEOF},
		{"truncated payload", []byte{0x05, 0x00, 0x01}, io.ErrUnexpectedEOF},
		// Vanilla rejects length prefixes longer than 3 bytes, even for small
		// values.
		{"length prefix of 4 bytes", []byte{0x81, 0x80, 0x80, 0x00, 0x00}, ErrVarIntTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadPacket(newReader(tc.input), 0x00, make([]byte, 4)); !errors.Is(err, tc.want) {
				t.Errorf("got error %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadPacketExact(t *testing.T) {
	stream := AppendPacket(nil, 0x00, nil)
	stream = AppendPacket(stream, 0x01, []byte("payload"))
	r := newReader(stream)

	if err := ReadPacketExact(r, 0x00, nil); err != nil {
		t.Errorf("ReadPacketExact() of an empty payload: %v", err)
	}
	payload := make([]byte, 7)
	if err := ReadPacketExact(r, 0x01, payload); err != nil || string(payload) != "payload" {
		t.Errorf("ReadPacketExact() = %q, %v, want %q", payload, err, "payload")
	}
}

func TestReadPacketExactErrors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{"shorter payload", AppendPacket(nil, 0x00, make([]byte, 3))},
		{"longer payload", AppendPacket(nil, 0x00, make([]byte, 5))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newReader(tc.input)
			if err := ReadPacketExact(r, 0x00, make([]byte, 4)); !errors.Is(err, ErrInvalidPacketLength) {
				t.Errorf("got error %v, want %v", err, ErrInvalidPacketLength)
			}
			// The length is checked before the payload is read.
			if r.Buffered() != len(tc.input)-1 {
				t.Errorf("%d bytes read, want only the length", len(tc.input)-r.Buffered())
			}
		})
	}
}

func BenchmarkReadHandshake(b *testing.B) {
	packet := AppendHandshake(nil, Handshake{ProtocolVersion: 777, ServerAddress: "mc.example.com", ServerPort: 25565, Intent: IntentStatus})
	src := bytes.NewReader(packet)
	r := bufio.NewReader(src)
	buf := make([]byte, MaxHandshakePayloadLength)
	b.ReportAllocs()
	for b.Loop() {
		src.Reset(packet)
		r.Reset(src)
		payload, err := ReadPacket(r, HandshakeID, buf)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := ParseHandshake(payload); err != nil {
			b.Fatal(err)
		}
	}
}
