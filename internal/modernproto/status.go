package modernproto

import (
	"fmt"
	"io"
)

// maxStatusResponseBytes is the most bytes that a status response takes in
// UTF-8: each of its MaxStatusResponseLength UTF-16 code units takes at most 3.
const maxStatusResponseBytes = 3 * MaxStatusResponseLength

// ReadStatusResponse reads the status response of a server, as a client, and
// returns the status JSON it holds.
func ReadStatusResponse(r Reader) ([]byte, error) {
	payloadLength, err := readPacketHeader(r, StatusResponseID, 0, maxVarIntBytes+maxStatusResponseBytes)
	if err != nil {
		return nil, err
	}
	length, err := ReadVarInt(r)
	if err != nil {
		return nil, noEOF(err)
	}
	if length < 0 || int(length) != payloadLength-VarIntSize(length) {
		return nil, fmt.Errorf("%w: string of %d bytes in a payload of %d", ErrInvalidPacketLength, length, payloadLength)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, noEOF(err)
	}
	if n := UTF16Length(data); n > MaxStatusResponseLength {
		return nil, fmt.Errorf("status length %d exceeds %d", n, MaxStatusResponseLength)
	}
	return data, nil
}
