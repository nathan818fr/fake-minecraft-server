package modernproto

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

var varIntCases = []struct {
	value   int32
	encoded []byte
}{
	{0, []byte{0x00}},
	{1, []byte{0x01}},
	{127, []byte{0x7f}},
	{128, []byte{0x80, 0x01}},
	{255, []byte{0xff, 0x01}},
	{25565, []byte{0xdd, 0xc7, 0x01}},
	{2097151, []byte{0xff, 0xff, 0x7f}},
	{0x4000011f, []byte{0x9f, 0x82, 0x80, 0x80, 0x04}}, // 26.1 snapshot 1 protocol version
	{math.MaxInt32, []byte{0xff, 0xff, 0xff, 0xff, 0x07}},
	{-1, []byte{0xff, 0xff, 0xff, 0xff, 0x0f}},
	{math.MinInt32, []byte{0x80, 0x80, 0x80, 0x80, 0x08}},
}

func TestAppendVarInt(t *testing.T) {
	for _, tc := range varIntCases {
		if got := AppendVarInt(nil, tc.value); !bytes.Equal(got, tc.encoded) {
			t.Errorf("AppendVarInt(%d) = % x, want % x", tc.value, got, tc.encoded)
		}
		if got := VarIntSize(tc.value); got != len(tc.encoded) {
			t.Errorf("VarIntSize(%d) = %d, want %d", tc.value, got, len(tc.encoded))
		}
	}
}

func TestReadVarInt(t *testing.T) {
	for _, tc := range varIntCases {
		r := bytes.NewReader(tc.encoded)
		got, err := ReadVarInt(r)
		if err != nil || got != tc.value {
			t.Errorf("ReadVarInt(% x) = %d, %v, want %d", tc.encoded, got, err, tc.value)
		}
		if r.Len() != 0 {
			t.Errorf("ReadVarInt(% x) left %d unread bytes", tc.encoded, r.Len())
		}
	}
}

func TestReadVarIntErrors(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  error
	}{
		{"empty", nil, io.EOF},
		{"truncated", []byte{0x80, 0x80}, io.ErrUnexpectedEOF},
		{"too long", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, ErrVarIntTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadVarInt(bytes.NewReader(tc.input)); !errors.Is(err, tc.want) {
				t.Errorf("got error %v, want %v", err, tc.want)
			}
		})
	}
}
