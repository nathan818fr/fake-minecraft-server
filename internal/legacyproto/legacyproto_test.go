package legacyproto

import (
	"encoding/hex"
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
	// Cut between "§" and its code, the "§" goes too.
	kick = AppendKick(nil, strings.Repeat("a", maxKickLength-1)+"§c")
	if length := int(kick[1])<<8 | int(kick[2]); length != maxKickLength-1 {
		t.Errorf("kick of %d code units, want %d, without the cut code", length, maxKickLength-1)
	}
}

// Text cut to the length limit must not end with half of a surrogate pair.
func TestShortenUTF16(t *testing.T) {
	for _, maxLength := range []int{4, 5} {
		if got := shortenUTF16("😀😀😀", maxLength); got != "😀😀" {
			t.Errorf("shortenUTF16(%d) = %q, want %q", maxLength, got, "😀😀")
		}
	}
}
