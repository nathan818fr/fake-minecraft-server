// Package modernproto implements the small subset of the Minecraft Java Edition
// protocol of clients since 1.7 needed to answer server list pings and refuse
// logins.
package modernproto

import (
	"errors"
	"fmt"
	"io"
)

// Packet IDs are scoped to a connection state, hence the shared values. They
// are all below 0x80, so their VarInt encoding is a single byte (see
// packetIDLength).
const (
	HandshakeID       byte = 0x00
	StatusRequestID   byte = 0x00
	StatusResponseID  byte = 0x00
	PingRequestID     byte = 0x01
	PongResponseID    byte = 0x01
	LoginStartID      byte = 0x00
	LoginDisconnectID byte = 0x00
)

// packetIDLength is the length of the encoded packet IDs, which packet lengths
// include.
const packetIDLength = 1

const PingPayloadLength = 8

// maxPacketLengthBytes is the most bytes a packet length takes, as vanilla
// limits packet lengths to 21 bits.
const maxPacketLengthBytes = 3

var (
	ErrInvalidPacketLength = errors.New("invalid packet length")
	ErrUnexpectedPacket    = errors.New("unexpected packet")
)

type Reader interface {
	io.Reader
	io.ByteReader
}

// ReadPacket reads an uncompressed packet that must have the given ID and a
// payload no longer than buf. It returns the payload, which aliases buf.
//
// The length and ID are checked before the payload is read, so that unrelated
// traffic is rejected after a couple of bytes. It returns io.EOF only if the
// stream ended cleanly before the packet.
func ReadPacket(r Reader, id byte, buf []byte) ([]byte, error) {
	return readPacket(r, id, 0, buf)
}

// ReadPacketExact is like ReadPacket, for packets with a fixed-length payload:
// it must have exactly len(payload) bytes.
func ReadPacketExact(r Reader, id byte, payload []byte) error {
	_, err := readPacket(r, id, len(payload), payload)
	return err
}

func readPacket(r Reader, id byte, minPayloadLength int, buf []byte) ([]byte, error) {
	payloadLength, err := readPacketHeader(r, id, minPayloadLength, len(buf))
	if err != nil {
		return nil, err
	}
	payload := buf[:payloadLength]
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, noEOF(err)
	}
	return payload, nil
}

// readPacketHeader reads the length and ID of a packet that must have the
// given ID and a payload of minPayloadLength to maxPayloadLength bytes, and
// returns the length of its payload.
func readPacketHeader(r Reader, id byte, minPayloadLength, maxPayloadLength int) (int, error) {
	length, err := readVarInt(r, maxPacketLengthBytes)
	if err != nil {
		return 0, err
	}
	payloadLength := int(length) - packetIDLength
	if payloadLength < minPayloadLength || payloadLength > maxPayloadLength {
		return 0, fmt.Errorf("%w: %d", ErrInvalidPacketLength, length)
	}
	gotID, err := r.ReadByte()
	if err != nil {
		return 0, noEOF(err)
	}
	if gotID != id {
		return 0, fmt.Errorf("%w: id %#x, want %#x", ErrUnexpectedPacket, gotID, id)
	}
	return payloadLength, nil
}

func AppendPacketHeader(b []byte, id byte, payloadLength int) []byte {
	b = AppendVarInt(b, int32(packetIDLength+payloadLength))
	return append(b, id)
}

func AppendPacket(b []byte, id byte, payload []byte) []byte {
	b = AppendPacketHeader(b, id, len(payload))
	return append(b, payload...)
}
