package modernproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Intent int32

// The intents of handshakes. Transfers (1.20.5+) are logins of players sent by
// another server.
const (
	IntentStatus   Intent = 1
	IntentLogin    Intent = 2
	IntentTransfer Intent = 3
)

// maxServerAddressBytes limits the server address to 255 bytes. Vanilla limits
// it to 255 UTF-16 code units, which can take up to 765 bytes, but addresses
// are ASCII in practice (host names and IP addresses): counting bytes is
// simpler, and only rejects non-ASCII addresses close to the limit.
const maxServerAddressBytes = 255

const serverPortLength = 2

// DefaultPort is the port of servers whose address has none, nor an SRV
// record.
const DefaultPort = 25565

// MaxHandshakePayloadLength is the length of a handshake payload with the
// longest fields: protocol version, server address (after its 2-byte length),
// port and intent (1 byte).
const MaxHandshakePayloadLength = maxVarIntBytes + 2 + maxServerAddressBytes + serverPortLength + 1

var ErrInvalidHandshake = errors.New("invalid handshake")

type Handshake struct {
	ProtocolVersion int32
	// ServerAddress is the host that the client connected to, and
	// ServerAddressExtra what follows it after a NUL byte, added by modded
	// clients and by proxies, such as "FML\x00" for Forge clients.
	ServerAddress      string
	ServerAddressExtra string
	ServerPort         uint16
	Intent             Intent
}

// ParseHandshake parses the payload of a handshake packet. It rejects unknown
// intents and trailing bytes, so that a valid handshake identifies a Minecraft
// client.
func ParseHandshake(payload []byte) (Handshake, error) {
	r := bytes.NewReader(payload)
	var h Handshake
	var err error

	if h.ProtocolVersion, err = ReadVarInt(r); err != nil {
		return Handshake{}, fmt.Errorf("%w: protocol version: %w", ErrInvalidHandshake, noEOF(err))
	}

	address, err := readString(r, maxServerAddressBytes)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: server address: %w", ErrInvalidHandshake, err)
	}
	h.ServerAddress, h.ServerAddressExtra, _ = strings.Cut(address, "\x00")

	var port [serverPortLength]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return Handshake{}, fmt.Errorf("%w: server port: %w", ErrInvalidHandshake, noEOF(err))
	}
	h.ServerPort = binary.BigEndian.Uint16(port[:])

	intent, err := ReadVarInt(r)
	if err != nil {
		return Handshake{}, fmt.Errorf("%w: intent: %w", ErrInvalidHandshake, noEOF(err))
	}
	h.Intent = Intent(intent)
	switch h.Intent {
	case IntentStatus, IntentLogin, IntentTransfer:
	default:
		return Handshake{}, fmt.Errorf("%w: unknown intent %d", ErrInvalidHandshake, intent)
	}

	if r.Len() != 0 {
		return Handshake{}, fmt.Errorf("%w: %d trailing bytes", ErrInvalidHandshake, r.Len())
	}
	return h, nil
}

func AppendHandshake(b []byte, h Handshake) []byte {
	var payload []byte
	payload = AppendVarInt(payload, h.ProtocolVersion)
	address := h.ServerAddress
	if h.ServerAddressExtra != "" {
		address += "\x00" + h.ServerAddressExtra
	}
	payload = AppendString(payload, address)
	payload = binary.BigEndian.AppendUint16(payload, h.ServerPort)
	payload = AppendVarInt(payload, int32(h.Intent))
	return AppendPacket(b, HandshakeID, payload)
}
