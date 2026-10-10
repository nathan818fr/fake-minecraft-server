package legacyproto

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestReadLogin(t *testing.T) {
	tests := []struct {
		name string
		data string
		want Login
	}{
		{
			name: "1.3 to 1.6",
			// Protocol 78, Notch, localhost:25565.
			data: "02 4e 0005 004e006f0074006300680009 006c006f00630061006c0068006f00730074 000063dd",
			want: Login{ProtocolVersion: 78, Username: "Notch", HasHost: true, ServerAddress: "localhost", ServerPort: 25565},
		},
		{
			name: "before 1.3",
			data: "02 0015 004e006f007400630068003b006c006f00630061006c0068006f00730074003a00320035003500360035",
			want: Login{ProtocolVersion: -1, Username: "Notch", HasHost: true, ServerAddress: "localhost", ServerPort: 25565},
		},
		{
			name: "before 1.3, IPv6",
			data: "02 000d 004e006f007400630068003b005b003a003a0031005d003a0031",
			want: Login{ProtocolVersion: -1, Username: "Notch", HasHost: true, ServerAddress: "[::1]", ServerPort: 1},
		},
		{
			name: "before 1.3, username only",
			data: "02 0005 004e006f00740063 0068",
			want: Login{ProtocolVersion: -1, Username: "Notch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(append(mustHex(t, tc.data), "next"...)))
			got, ok, err := ReadLogin(r)
			if err != nil || !ok || got != tc.want {
				t.Fatalf("ReadLogin() = %+v, %v, %v, want %+v", got, ok, err, tc.want)
			}
			if rest, _ := io.ReadAll(r); string(rest) != "next" {
				t.Errorf("left %q unread, want %q", rest, "next")
			}
		})
	}
}

func TestReadLoginIgnoresOtherPackets(t *testing.T) {
	r := bufio.NewReader(bytes.NewReader([]byte{0x10, 0x00}))
	if _, ok, err := ReadLogin(r); ok || err != nil {
		t.Errorf("ReadLogin() = %v, %v, want not a legacy login", ok, err)
	}
	if r.Buffered() != 2 {
		t.Errorf("ReadLogin() consumed data")
	}
}

func TestReadLoginErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
		want error
	}{
		{"username too long", "02 4e 0011 0061006100610061006100610061006100610061006100610061006100610061 0061", ErrInvalidLogin},
		{"non-ASCII username too long", "02 4e 0009 00e900e900e900e900e900e900e900e900e9", ErrInvalidLogin},
		{"port out of range", "02 4e 0001 0061 0001 0061 00010000", ErrInvalidLogin},
		{"truncated", "02 4e 0005 004e006f", io.ErrUnexpectedEOF},
		{"before 1.3, missing port", "02 0007 004e003b006c006f00630061006c", ErrInvalidLogin},
		{"before 1.3, invalid port", "02 0004 004e003b0061003a", ErrInvalidLogin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := ReadLogin(bufio.NewReader(bytes.NewReader(mustHex(t, tc.data))))
			if !ok || !errors.Is(err, tc.want) {
				t.Errorf("ReadLogin() = %v, %v, want an error %v", ok, err, tc.want)
			}
		})
	}
}
