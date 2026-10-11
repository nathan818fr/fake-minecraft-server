package modernproto

import (
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// Maximum lengths, in UTF-16 code units, of the strings sent to clients.
const (
	MaxStatusResponseLength   = 32767
	MaxDisconnectReasonLength = 262144
)

// UTF16Length returns the length of s in UTF-16 code units, which is how the
// protocol measures string lengths.
func UTF16Length(s []byte) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRune(s)
		s = s[size:]
		n += max(utf16.RuneLen(r), 1)
	}
	return n
}

// readString reads a string of at most maxBytes bytes in UTF-8.
func readString(r Reader, maxBytes int) (string, error) {
	length, err := ReadVarInt(r)
	if err != nil {
		return "", noEOF(err)
	}
	if length < 0 || int(length) > maxBytes {
		return "", fmt.Errorf("string length %d out of range [0, %d]", length, maxBytes)
	}
	s := make([]byte, length)
	if _, err := io.ReadFull(r, s); err != nil {
		return "", noEOF(err)
	}
	return string(s), nil
}

// AppendString appends s prefixed with its length in bytes.
func AppendString(b []byte, s string) []byte {
	b = AppendVarInt(b, int32(len(s)))
	return append(b, s...)
}
