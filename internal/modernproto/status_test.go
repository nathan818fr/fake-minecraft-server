package modernproto

import (
	"errors"
	"strings"
	"testing"
)

func TestReadStatusResponse(t *testing.T) {
	const json = `{"description":"§aHello"}`
	got, err := ReadStatusResponse(newReader(AppendPacket(nil, StatusResponseID, AppendString(nil, json))))
	if err != nil || string(got) != json {
		t.Errorf("ReadStatusResponse() = %s, %v, want %s", got, err, json)
	}
}

func TestReadStatusResponseErrors(t *testing.T) {
	// The longest status in UTF-8 that fits the limit in UTF-16 code units.
	longest := strings.Repeat("日", MaxStatusResponseLength)
	if _, err := ReadStatusResponse(newReader(AppendPacket(nil, StatusResponseID, AppendString(nil, longest)))); err != nil {
		t.Fatalf("ReadStatusResponse() error for the longest status: %v", err)
	}

	tests := []struct {
		name    string
		payload []byte
		wantErr error
	}{
		{"too long", AppendString(nil, longest+"a"), nil},
		{"trailing bytes", append(AppendString(nil, "{}"), 0), ErrInvalidPacketLength},
		{"truncated string", AppendVarInt(nil, 10), ErrInvalidPacketLength},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadStatusResponse(newReader(AppendPacket(nil, StatusResponseID, tc.payload)))
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Errorf("ReadStatusResponse() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
