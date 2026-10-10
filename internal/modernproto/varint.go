package modernproto

import (
	"errors"
	"io"
)

const maxVarIntBytes = 5

// Each byte of a VarInt holds 7 bits of the value, and its high bit tells
// whether more bytes follow.
const (
	varIntValueBits   = 7
	varIntValueMask   = 0x7f
	varIntContinueBit = 0x80
)

var ErrVarIntTooLong = errors.New("varint is too long")

// ReadVarInt reads a VarInt: a two's complement int32 encoded in base 128,
// least significant group first, using at most 5 bytes.
//
// It returns io.EOF only if no byte could be read at all.
func ReadVarInt(r io.ByteReader) (int32, error) {
	return readVarInt(r, maxVarIntBytes)
}

func readVarInt(r io.ByteReader, maxBytes int) (int32, error) {
	var value uint32
	for i := range maxBytes {
		b, err := r.ReadByte()
		if err != nil {
			if i > 0 {
				err = noEOF(err)
			}
			return 0, err
		}
		value |= uint32(b&varIntValueMask) << (varIntValueBits * i)
		if b&varIntContinueBit == 0 {
			return int32(value), nil
		}
	}
	return 0, ErrVarIntTooLong
}

func AppendVarInt(b []byte, v int32) []byte {
	u := uint32(v)
	for u >= varIntContinueBit {
		b = append(b, byte(u)|varIntContinueBit)
		u >>= varIntValueBits
	}
	return append(b, byte(u))
}

func VarIntSize(v int32) int {
	n := 1
	for u := uint32(v); u >= varIntContinueBit; u >>= varIntValueBits {
		n++
	}
	return n
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
