package legacyproto

import (
	"strings"
	"testing"
)

func TestStripFormatting(t *testing.T) {
	tests := []struct{ in, want string }{
		{"§cRed §lbold§r", "Red bold"},
		{"a§§bc§", "abc"},
		{"100%§", "100%"},
	}
	for _, tc := range tests {
		if got := StripFormatting(tc.in); got != tc.want {
			t.Errorf("StripFormatting(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTextFromComponent(t *testing.T) {
	tests := []struct {
		component string
		want      string
	}{
		{`{"text":"§eHello\n§dWorld"}`, "§eHello\n§dWorld"},
		{`"plain"`, "plain"},
		{" \n" + `{"text":"spaced"}`, "spaced"},
		{`{"text":"invalid"`, ""},
		{`42`, ""},
		{``, ""},
		// Styles are inherited, and written only when they change: removing
		// one takes a color code or a reset.
		{`{"text":"A","extra":[{"text":"B","bold":true},"C",["D"]]}`, "A§lB§rCD"},
		{`{"text":"A","color":"gold","extra":[{"text":"B","bold":true},{"text":"C","italic":true,"bold":false}]}`, "§6A§lB§6§oC"},
		{`{"text":"","color":"red","bold":true,"extra":["A",{"text":"B","color":"red"},{"text":"C","color":"blue"}]}`, "§c§lAB§9§lC"},
		// The first component of a list is the parent of the others.
		{`[{"text":"A","color":"green"},"B",{"text":"C","color":"reset"}]`, "§aABC"},
		// Hex colors become the closest legacy color.
		{`{"text":"A","color":"#ff8800"}`, "§6A"},
		{`{"text":"A","color":"#FFFFFE"}`, "§fA"},
		{`{"text":"A","color":"#12345"}`, "A"},
		// After codes in a text, the next style is written whole.
		{`{"text":"","color":"red","extra":["§lA","B"]}`, "§c§lA§cB"},
		{`["§cA","B"]`, "§cA§rB"},
		// Only text parts have text.
		{`{"translate":"multiplayer.disconnect.kicked","extra":[{"text":"!","color":"red"}]}`, "§c!"},
	}
	for _, tc := range tests {
		if got := TextFromComponent([]byte(tc.component)); got != tc.want {
			t.Errorf("TextFromComponent(%s) = %q, want %q", tc.component, got, tc.want)
		}
	}
}

func TestComponentFromText(t *testing.T) {
	tests := []struct {
		text string
		char rune
		want string
	}{
		{"", '§', `{"text":""}`},
		{"Hello", '§', `{"text":"Hello"}`},
		{"§cRed", '§', `{"text":"Red","color":"red"}`},
		// A color resets the decorations, and r the whole style.
		{"&l&6Gold &lbold&r plain", '&', `{"text":"","extra":[{"text":"Gold ","color":"gold"},{"text":"bold","color":"gold","bold":true},{"text":" plain"}]}`},
		{"&#FF8800Orange &kx", '&', `{"text":"","extra":[{"text":"Orange ","color":"#ff8800"},{"text":"x","color":"#ff8800","obfuscated":true}]}`},
		// Codes that change nothing, unknown codes and other prefixes stay
		// as they are.
		{"&c&cA&cB", '&', `{"text":"AB","color":"red"}`},
		{"&zA&#12345 §cB&", '&', `{"text":"&zA&#12345 §cB&"}`},
	}
	for _, tc := range tests {
		if got := string(ComponentFromText(tc.text, tc.char)); got != tc.want {
			t.Errorf("ComponentFromText(%q, %q) = %s, want %s", tc.text, tc.char, got, tc.want)
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

// Shortened fields never end with a "§" without its code, which clients show.
func TestWithoutCutCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"§cA§", "§cA"},
		{"§§", "§§"},
		{"A§§§", "A§§"},
		{strings.Repeat("a", 3), "aaa"},
	}
	for _, tc := range tests {
		if got := withoutCutCode(tc.in); got != tc.want {
			t.Errorf("withoutCutCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
