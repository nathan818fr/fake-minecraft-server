package modernproto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

var notchUUID = []byte{0x06, 0x9a, 0x79, 0xf4, 0x44, 0xe9, 0x47, 0x26, 0xa5, 0xbe, 0xfc, 0xa9, 0x0e, 0x38, 0xaa, 0xf5}

const notchUUIDString = "069a79f4-44e9-4726-a5be-fca90e38aaf5"

func TestReadLoginStart(t *testing.T) {
	name := AppendString(nil, "Notch")
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	tests := []struct {
		name            string
		protocolVersion int32
		payload         []byte
		want            LoginStart
	}{
		{"1.8: username only", 47, name, LoginStart{Username: "Notch"}},
		{"1.19: username and signature data", 759, join(name, []byte{0x00}), LoginStart{Username: "Notch"}},
		{"1.19.1: UUID after signature data is not read", 760, join(name, []byte{0x00, 0x01}, notchUUID), LoginStart{Username: "Notch"}},
		{"1.19.3: optional UUID present", 761, join(name, []byte{0x01}, notchUUID), LoginStart{Username: "Notch", UUID: notchUUIDString}},
		{"1.20.1: optional UUID absent", 763, join(name, []byte{0x00}), LoginStart{Username: "Notch"}},
		{"1.20.2: mandatory UUID", 764, join(name, notchUUID), LoginStart{Username: "Notch", UUID: notchUUIDString}},
		{"26.3: mandatory UUID", 777, join(name, notchUUID), LoginStart{Username: "Notch", UUID: notchUUIDString}},
		{"recent snapshot", snapshotProtocolVersionBase + 400, join(name, notchUUID), LoginStart{Username: "Notch", UUID: notchUUIDString}},
		{"old snapshot", snapshotProtocolVersionBase + 100, join(name, notchUUID), LoginStart{Username: "Notch"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadLoginStart(newReader(AppendPacket(nil, LoginStartID, tc.payload)), tc.protocolVersion)
			if err != nil || got != tc.want {
				t.Errorf("ReadLoginStart() = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}

// A packet too short for the expected fields must not make ReadLoginStart read
// the next packet.
func TestReadLoginStartStaysWithinPacket(t *testing.T) {
	name := AppendString(nil, "Notch")
	tests := []struct {
		name            string
		protocolVersion int32
		payload         []byte
	}{
		{"missing UUID", 777, name},
		{"truncated UUID", 777, append(name[:len(name):len(name)], notchUUID[:8]...)},
		{"missing optional UUID flag", 761, name},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := AppendPacket(nil, 0x7f, notchUUID)
			r := newReader(append(AppendPacket(nil, LoginStartID, tc.payload), next...))
			if got, err := ReadLoginStart(r, tc.protocolVersion); err != nil || got != (LoginStart{Username: "Notch"}) {
				t.Errorf("ReadLoginStart() = %+v, %v, want only the username", got, err)
			}
			want := append(bytes.Clone(tc.payload[len(name):]), next...)
			if rest, _ := io.ReadAll(r); !bytes.Equal(rest, want) {
				t.Errorf("left % x unread, want the rest of the packet and the next one: % x", rest, want)
			}
		})
	}
}

func TestReadLoginStartErrors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{"empty", nil, io.EOF},
		{"wrong id", AppendPacket(nil, 0x02, AppendString(nil, "Steve")), ErrUnexpectedPacket},
		{"username longer than the packet", []byte{0x03, LoginStartID, 0x05, 'S', 't', 'e', 'v', 'e'}, ErrInvalidPacketLength},
		{"length prefix of 4 bytes", []byte{0x87, 0x80, 0x80, 0x00, LoginStartID}, ErrVarIntTooLong},
		{"truncated username", []byte{0x07, LoginStartID, 0x05, 'S', 't'}, io.ErrUnexpectedEOF},
		{"missing username", []byte{0x01, LoginStartID}, io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadLoginStart(newReader(tc.input), 777); !errors.Is(err, tc.want) {
				t.Errorf("got error %v, want %v", err, tc.want)
			}
		})
	}

	// The second username is within the vanilla limit of 16 characters, but not
	// of 16 bytes.
	for _, name := range []string{strings.Repeat("a", maxUsernameBytes+1), strings.Repeat("é", 9)} {
		packet := AppendPacket(nil, LoginStartID, AppendString(nil, name))
		if _, err := ReadLoginStart(newReader(packet), 777); err == nil {
			t.Errorf("ReadLoginStart() accepted the username %q, longer than the limit", name)
		}
	}
	longest := strings.Repeat("a", maxUsernameBytes)
	packet := AppendPacket(nil, LoginStartID, AppendString(nil, longest))
	if login, err := ReadLoginStart(newReader(packet), 47); err != nil || login.Username != longest {
		t.Errorf("ReadLoginStart() = %+v, %v, want the longest username", login, err)
	}
}
