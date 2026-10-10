package modernproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func handshakePayload(t *testing.T, h Handshake) []byte {
	t.Helper()
	payload, err := ReadPacket(newReader(AppendHandshake(nil, h)), HandshakeID, make([]byte, MaxHandshakePayloadLength))
	if err != nil {
		t.Fatalf("ReadPacket() error: %v", err)
	}
	return payload
}

func TestParseHandshake(t *testing.T) {
	protocolVersions := map[string]int32{
		"1.7.2":            4,
		"1.21.4":           769,
		"26.3":             777,
		"26.1 snapshot 1":  0x4000011f,
		"negative version": -1,
	}
	intents := []Intent{IntentStatus, IntentLogin, IntentTransfer}

	for name, version := range protocolVersions {
		for _, intent := range intents {
			want := Handshake{ProtocolVersion: version, ServerAddress: "mc.example.com", ServerPort: 25565, Intent: intent}
			got, err := ParseHandshake(handshakePayload(t, want))
			if err != nil {
				t.Fatalf("%s, intent %d: ParseHandshake() error: %v", name, intent, err)
			}
			if got != want {
				t.Errorf("%s, intent %d: ParseHandshake() = %+v, want %+v", name, intent, got, want)
			}
		}
	}
}

func TestParseHandshakeKnownBytes(t *testing.T) {
	payload := []byte{
		0xdd, 0xc7, 0x01, // protocol version 25565
		0x09, 'l', 'o', 'c', 'a', 'l', 'h', 'o', 's', 't',
		0x63, 0xdd, // port 25565
		0x01, // intent
	}
	got, err := ParseHandshake(payload)
	if err != nil {
		t.Fatalf("ParseHandshake() error: %v", err)
	}
	want := Handshake{ProtocolVersion: 25565, ServerAddress: "localhost", ServerPort: 25565, Intent: IntentStatus}
	if got != want {
		t.Errorf("ParseHandshake() = %+v, want %+v", got, want)
	}
}

func TestParseHandshakeLongestFields(t *testing.T) {
	want := Handshake{
		ProtocolVersion: -1,
		ServerAddress:   strings.Repeat("a", maxServerAddressBytes),
		ServerPort:      65535,
		Intent:          IntentLogin,
	}
	payload := handshakePayload(t, want)
	if len(payload) != MaxHandshakePayloadLength {
		t.Errorf("payload length = %d, want MaxHandshakePayloadLength (%d)", len(payload), MaxHandshakePayloadLength)
	}
	got, err := ParseHandshake(payload)
	if err != nil {
		t.Fatalf("ParseHandshake() error: %v", err)
	}
	if got != want {
		t.Errorf("ParseHandshake() = %+v, want %+v", got, want)
	}
}

// Modded clients and proxies add data to the server address, after a NUL byte.
func TestParseHandshakeServerAddressExtra(t *testing.T) {
	want := Handshake{ProtocolVersion: 763, ServerAddress: "localhost", ServerAddressExtra: "FML3\x00", ServerPort: 25565, Intent: IntentLogin}
	payload := handshakePayload(t, want)
	if !bytes.Contains(payload, []byte("localhost\x00FML3\x00")) {
		t.Fatalf("payload = %q, want the extra data after a NUL byte", payload)
	}
	got, err := ParseHandshake(payload)
	if err != nil {
		t.Fatalf("ParseHandshake() error: %v", err)
	}
	if got != want {
		t.Errorf("ParseHandshake() = %+v, want %+v", got, want)
	}
}

func TestParseHandshakeErrors(t *testing.T) {
	valid := handshakePayload(t, Handshake{ProtocolVersion: 773, ServerAddress: "localhost", ServerPort: 25565, Intent: IntentStatus})
	withIntent := func(intent Intent) []byte {
		return handshakePayload(t, Handshake{ProtocolVersion: 773, ServerAddress: "localhost", Intent: intent})
	}
	withAddress := func(address string) []byte {
		b := AppendVarInt(nil, 773)
		b = AppendString(b, address)
		b = binary.BigEndian.AppendUint16(b, 25565)
		return AppendVarInt(b, int32(IntentStatus))
	}

	tests := []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"truncated address", valid[:5]},
		{"truncated port", valid[:12]},
		{"missing intent", valid[:14]},
		{"trailing data", append(valid[:len(valid):len(valid)], 0x00)},
		{"intent 0", withIntent(0)},
		{"intent 4", withIntent(4)},
		{"address too long", withAddress(strings.Repeat("a", maxServerAddressBytes+1))},
		// Within the vanilla limit of 255 characters, but not of 255 bytes.
		{"non-ASCII address too long", withAddress(strings.Repeat("€", 86))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseHandshake(tc.payload); !errors.Is(err, ErrInvalidHandshake) {
				t.Errorf("got error %v, want ErrInvalidHandshake", err)
			}
		})
	}
}
