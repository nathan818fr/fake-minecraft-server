package modernproto

import (
	"bytes"
	"testing"
)

func TestAppendString(t *testing.T) {
	got := AppendString(nil, "§a")
	want := []byte{0x03, 0xc2, 0xa7, 'a'}
	if !bytes.Equal(got, want) {
		t.Errorf("AppendString() = % x, want % x", got, want)
	}
}

func TestUTF16Length(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"abc", 3},
		{"§c", 2},
		{"日本", 2},
		{"😀", 2},
		{"\xff", 1},
	}
	for _, tc := range tests {
		if got := UTF16Length([]byte(tc.input)); got != tc.want {
			t.Errorf("UTF16Length(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
