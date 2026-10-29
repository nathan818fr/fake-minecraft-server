package modernproto

import (
	"bytes"
	"testing"
)

func FuzzParseHandshake(f *testing.F) {
	f.Add(handshakeBytes(Handshake{ProtocolVersion: 773, ServerAddress: "mc.example.com", ServerAddressExtra: "FML3\x00", ServerPort: 25565, Intent: IntentStatus}))
	f.Add([]byte{0xdd, 0xc7, 0x01, 0x09, 'l', 'o', 'c', 'a', 'l', 'h', 'o', 's', 't', 0x63, 0xdd, 0x01})
	f.Fuzz(func(t *testing.T, payload []byte) {
		h, err := ParseHandshake(payload)
		if err != nil {
			return
		}
		again, err := ParseHandshake(handshakeBytes(h))
		if err != nil || again != h {
			t.Errorf("round trip of %+v = %+v, %v", h, again, err)
		}
	})
}

// handshakeBytes returns the payload of the handshake packet of h.
func handshakeBytes(h Handshake) []byte {
	r := bytes.NewReader(AppendHandshake(nil, h))
	payload, err := ReadPacket(r, HandshakeID, make([]byte, MaxHandshakePayloadLength+16))
	if err != nil {
		panic(err)
	}
	return payload
}

func FuzzReadLoginStart(f *testing.F) {
	f.Add([]byte{0x05, 'N', 'o', 't', 'c', 'h'}, int32(759))
	f.Fuzz(func(t *testing.T, payload []byte, protocol int32) {
		_, _ = ReadLoginStart(bytes.NewReader(AppendPacket(nil, LoginStartID, payload)), protocol)
	})
}

func FuzzReadPacket(f *testing.F) {
	f.Add([]byte{0x01, 0x00})
	f.Fuzz(func(t *testing.T, stream []byte) {
		_, _ = ReadPacket(bytes.NewReader(stream), 0, make([]byte, 512))
		_ = ReadPacketExact(bytes.NewReader(stream), 0, nil)
		_, _ = ReadStatusResponse(bytes.NewReader(stream))
	})
}
