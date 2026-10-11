package legacyproto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// pingHost16 is the 1.6 ping of the protocol documentation: protocol 73,
// localhost:25565.
const pingHost16 = "fe01fa 000b 004d0043007c00500069006e00670048006f00730074 0019 49 0009 006c006f00630061006c0068006f00730074 000063dd"

func TestReadPing(t *testing.T) {
	// Only a complete 1.6 ping can be followed by other data: the others are
	// recognized by the absence of more data.
	tests := []struct {
		name string
		data string
		next string
		want Ping
	}{
		{"beta to 1.3", "fe", "", Ping{Version: PingBeta, ProtocolVersion: -1}},
		{"1.4 to 1.5", "fe01", "", Ping{Version: Ping14, ProtocolVersion: -1}},
		{"1.6", pingHost16, "next", Ping{Version: Ping16, HasHost: true, ProtocolVersion: 73, ServerAddress: "localhost", ServerPort: 25565}},
		{"1.6 without MC|PingHost", "fe01fa", "", Ping{Version: Ping16, ProtocolVersion: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(append(mustHex(t, tc.data), tc.next...)))
			got, ok, err := ReadPing(r)
			if err != nil || !ok || got != tc.want {
				t.Fatalf("ReadPing() = %+v, %v, %v, want %+v", got, ok, err, tc.want)
			}
			if rest, _ := io.ReadAll(r); string(rest) != tc.next {
				t.Errorf("left %q unread, want %q", rest, tc.next)
			}
		})
	}
}

// Modern packets, including handshakes of 254 bytes that start like a 1.4 ping
// (fe 01), must be left untouched.
func TestReadPingIgnoresModernPackets(t *testing.T) {
	for _, data := range []string{"1000", "fe0100"} {
		r := bufio.NewReader(bytes.NewReader(mustHex(t, data)))
		if _, ok, err := ReadPing(r); ok || err != nil {
			t.Errorf("ReadPing(%s) = %v, %v, want not a legacy ping", data, ok, err)
		}
		if rest, _ := io.ReadAll(r); hex.EncodeToString(rest) != data {
			t.Errorf("ReadPing(%s) consumed data, left %x", data, rest)
		}
	}
}

func TestIncomplete(t *testing.T) {
	for data, want := range map[string]bool{"": false, "fe": true, "fe01": true, "fe01fa": true, "fe01fa00": false, "fe0100": false, "fe02": false, "10": false} {
		r := bufio.NewReader(bytes.NewReader(mustHex(t, data)))
		_, _ = r.Peek(1) // Buffers what was received.
		if got := Incomplete(r); got != want {
			t.Errorf("Incomplete(%s) = %v, want %v", data, got, want)
		}
	}
}

func TestReadPingErrors(t *testing.T) {
	ping := mustHex(t, pingHost16)
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"wrong payload", mustHex(t, "fe02"), ErrInvalidPing},
		{"wrong packet after the ping", mustHex(t, "fe0105"), ErrInvalidPing},
		{"wrong channel", bytes.Replace(ping, mustHex(t, "0048006f00730074"), mustHex(t, "0048006f00730075"), 1), ErrInvalidPing},
		{"wrong data length", bytes.Replace(ping, mustHex(t, "001949"), mustHex(t, "001a49"), 1), ErrInvalidPing},
		{"server address too long", append(mustHex(t, "fe01fa000b004d0043007c00500069006e00670048006f00730074ffff4901"), make([]byte, 100)...), ErrInvalidPing},
		{"truncated channel", ping[:10], io.ErrUnexpectedEOF},
		{"truncated", ping[:len(ping)-2], io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := ReadPing(bufio.NewReader(bytes.NewReader(tc.data)))
			if !ok || !errors.Is(err, tc.want) {
				t.Errorf("ReadPing() = %v, %v, want an error %v", ok, err, tc.want)
			}
		})
	}
}

func TestAppendPingResponse(t *testing.T) {
	status := Status{ProtocolVersion: 47, VersionName: "1.4.2", MOTD: "A Minecraft Server", MaxPlayers: 20}
	tests := []struct {
		name    string
		version PingVersion
		status  Status
		want    string
	}{
		{
			name:    "1.4 to 1.6",
			version: Ping14,
			status:  status,
			want:    "ff0023 00a700310000 003400370000 0031002e0034002e00320000 00410020004d0069006e0065006300720061006600740020005300650072007600650072 0000 00300000 00320030",
		},
		{
			name:    "beta to 1.3",
			version: PingBeta,
			status:  Status{MOTD: "A Minecraft Server", MaxPlayers: 10},
			want:    "ff0017 00410020004d0069006e0065006300720061006600740020005300650072007600650072 00a70030 00a700310030",
		},
		{
			name:    "beta to 1.3, without formatting codes",
			version: PingBeta,
			status:  Status{MOTD: "§eHi", MaxPlayers: 1},
			want:    "ff0006 00480069 00a70030 00a70031",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hex.EncodeToString(AppendPingResponse(nil, tc.version, tc.status)); got != strings.ReplaceAll(tc.want, " ", "") {
				t.Errorf("AppendPingResponse() = %s, want %s", got, strings.ReplaceAll(tc.want, " ", ""))
			}
		})
	}
}

// The version name and the MOTD are shortened to fit the length limit, rather
// than the player counts, which clients need to parse the response. The
// version name keeps up to half of the room, or what the MOTD leaves.
func TestAppendPingResponseShortensFields(t *testing.T) {
	long := strings.Repeat("a", 300)
	// Without "§1\x000\x00" before them, "\x00" between them, and "\x000\x000"
	// after them.
	const room = maxKickLength - 10
	for _, tc := range []struct {
		name              string
		status            Status
		versionName, motd int
	}{
		{"long MOTD", Status{VersionName: "x", MOTD: long}, 1, room - 1},
		{"long version name", Status{VersionName: long, MOTD: "x"}, room - 1, 1},
		{"both long", Status{VersionName: long, MOTD: long}, room / 2, room - room/2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := AppendPingResponse(nil, Ping16, tc.status)
			if length := int(response[1])<<8 | int(response[2]); length != maxKickLength {
				t.Errorf("response of %d code units, want %d", length, maxKickLength)
			}
			if !bytes.HasSuffix(response, mustHex(t, "0000 00300000 0030")) {
				t.Errorf("response does not end with the player counts: %x", response[len(response)-16:])
			}
			fields := strings.Split(kickText(response), "\x00")
			if len(fields[2]) != tc.versionName || len(fields[3]) != tc.motd {
				t.Errorf("version name of %d characters and MOTD of %d, want %d and %d", len(fields[2]), len(fields[3]), tc.versionName, tc.motd)
			}
		})
	}
}

// kickText returns the text of a kick packet.
func kickText(packet []byte) string {
	units := make([]uint16, (len(packet)-3)/2)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(packet[3+2*i:])
	}
	return string(utf16.Decode(units))
}

func TestMOTDFromComponent(t *testing.T) {
	if got := MOTDFromComponent([]byte(`{"text":"§eFirst\n§dSecond"}`)); got != "§eFirst" {
		t.Errorf("MOTDFromComponent() = %q, want the first line only", got)
	}
}
