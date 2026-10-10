package legacyproto

import (
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

func TestAppendKick(t *testing.T) {
	if got := hex.EncodeToString(AppendKick(nil, "§cNo")); got != "ff000400a70063004e006f" {
		t.Errorf("AppendKick() = %s", got)
	}
	kick := AppendKick(nil, strings.Repeat("a", 300))
	if length := int(kick[1])<<8 | int(kick[2]); length != maxKickLength || len(kick) != 3+2*length {
		t.Errorf("kick of %d code units in %d bytes, want %d code units", length, len(kick), maxKickLength)
	}
}

// Text cut to the length limit must not end with half of a surrogate pair.
func TestEncodeUTF16(t *testing.T) {
	want := encodeUTF16("😀😀", 4)
	for _, maxLength := range []int{4, 5} {
		if units := encodeUTF16("😀😀😀", maxLength); !slices.Equal(units, want) {
			t.Errorf("encodeUTF16(%d) = %x, want %x", maxLength, units, want)
		}
	}
}

// Components nested deeper than maxComponentDepth have no text, so that
// statuses made to be costly to convert are not.
func TestTextFromComponentDepth(t *testing.T) {
	nested := func(depth int) []byte {
		return []byte(strings.Repeat("[", depth) + `"x"` + strings.Repeat("]", depth))
	}
	if got := TextFromComponent(nested(maxComponentDepth)); got != "x" {
		t.Errorf("TextFromComponent() at the maximum depth = %q, want %q", got, "x")
	}
	if got := TextFromComponent(nested(maxComponentDepth + 1)); got != "" {
		t.Errorf("TextFromComponent() deeper = %q, want no text", got)
	}
}

func TestTextFromComponent(t *testing.T) {
	tests := []struct {
		component string
		want      string
	}{
		{`{"text":"§eHello\n§dWorld"}`, "§eHello\n§dWorld"},
		{`{"text":"A","extra":[{"text":"B","bold":true},"C",["D"]]}`, "ABCD"},
		{`"plain"`, "plain"},
	}
	for _, tc := range tests {
		if got := TextFromComponent([]byte(tc.component)); got != tc.want {
			t.Errorf("TextFromComponent(%s) = %q, want %q", tc.component, got, tc.want)
		}
	}
}
