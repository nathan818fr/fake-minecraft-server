package legacyproto

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// colorNames are the names of the legacy colors in JSON text components, by
// code: "§0" is black, "§f" white.
var colorNames = [16]string{
	"black", "dark_blue", "dark_green", "dark_aqua", "dark_red", "dark_purple", "gold", "gray",
	"dark_gray", "blue", "green", "aqua", "red", "light_purple", "yellow", "white",
}

// colorRGB are the RGB values of the legacy colors, by code.
var colorRGB = [16][3]int{
	{0x00, 0x00, 0x00}, {0x00, 0x00, 0xaa}, {0x00, 0xaa, 0x00}, {0x00, 0xaa, 0xaa},
	{0xaa, 0x00, 0x00}, {0xaa, 0x00, 0xaa}, {0xff, 0xaa, 0x00}, {0xaa, 0xaa, 0xaa},
	{0x55, 0x55, 0x55}, {0x55, 0x55, 0xff}, {0x55, 0xff, 0x55}, {0x55, 0xff, 0xff},
	{0xff, 0x55, 0x55}, {0xff, 0x55, 0xff}, {0xff, 0xff, 0x55}, {0xff, 0xff, 0xff},
}

const colorCodes = "0123456789abcdef"

// decorationCodes are the codes of the decorations, and decorationNames their
// names in JSON text components, in the order of the bits of
// style.decorations.
const decorationCodes = "klmno"

var decorationNames = [len(decorationCodes)]string{"obfuscated", "bold", "strikethrough", "underlined", "italic"}

// maxComponentDepth bounds the nesting of the components that are converted,
// which JSON allows up to 10,000 levels: deeper components have no text. Real
// components nest a few levels.
const maxComponentDepth = 64

type style struct {
	// color is the index of its color in colorNames, or -1 for none.
	color       int
	decorations uint8
}

var plainStyle = style{color: -1}

// StripFormatting removes the formatting codes from s: "§" and the character
// after it, whatever it is, as clients read them, so that no "§" is left.
func StripFormatting(s string) string {
	var b strings.Builder
	skip := false
	for _, r := range s {
		switch {
		case skip:
			skip = false
		case r == '§':
			skip = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// withoutCutCode returns s without its last character if it is a "§" whose
// code character was cut, which clients would show.
func withoutCutCode(s string) string {
	code := false
	for _, r := range s {
		code = !code && r == '§'
	}
	if code {
		return s[:len(s)-len("§")]
	}
	return s
}

// TextFromComponent is TextFromValue for a text component written in JSON.
// Missing (nil) or invalid components have no text.
func TextFromComponent(component json.RawMessage) string {
	value, err := status.ParseValue(component)
	if err != nil {
		return ""
	}
	return TextFromValue(value)
}

// TextFromValue converts a text component, decoded like the values of a
// status.Object (text, []any or status.Object), to legacy text: its texts,
// with its colors and decorations as § codes, before the codes that the texts
// hold. Hex colors become the closest legacy color. Only text parts have text,
// not translations, keybinds, scores nor selectors.
func TextFromValue(component any) string {
	w := textWriter{style: plainStyle}
	w.component(component, plainStyle, 0)
	return w.b.String()
}

// textWriter writes legacy text, with the codes that change its style only
// when needed.
type textWriter struct {
	b     strings.Builder
	style style
	// unknown reports that the text written holds codes, so that its style is
	// unknown.
	unknown bool
}

// component writes c, a component at depth, which inherits parent, and
// returns its style, which its children inherit.
func (w *textWriter) component(c any, parent style, depth int) style {
	if depth > maxComponentDepth {
		return parent
	}
	switch c := c.(type) {
	case string:
		w.text(c, parent)
	case []any:
		// The first component of a list is the parent of the others.
		if len(c) > 0 {
			first := w.component(c[0], parent, depth+1)
			for _, child := range c[1:] {
				w.component(child, first, depth+1)
			}
		}
	case status.Object:
		o, ok := readObject(c, parent)
		if !ok {
			return parent
		}
		w.text(o.text, o.style)
		for _, child := range o.extra {
			w.component(child, o.style, depth+1)
		}
		return o.style
	}
	return parent
}

// componentObject is what legacy text has of a component object: its text,
// its style and its children.
type componentObject struct {
	text  string
	style style
	extra []any
}

// readObject reads o, a component object whose style inherits parent. It is
// invalid, and has no text, if one of the members that legacy text has is of
// another type.
func readObject(o status.Object, parent style) (componentObject, bool) {
	c := componentObject{style: parent}
	for _, m := range o {
		if m.Value == nil {
			continue // Like a missing member.
		}
		var ok bool
		switch m.Name {
		case "text":
			c.text, ok = m.Value.(string)
		case "extra":
			c.extra, ok = m.Value.([]any)
		case "color":
			var color string
			if color, ok = m.Value.(string); ok {
				if i, known := parseColor(color); known {
					c.style.color = i
				}
			}
		default:
			i := slices.Index(decorationNames[:], m.Name)
			if i < 0 {
				continue
			}
			var on bool
			if on, ok = m.Value.(bool); ok {
				c.style.decorations &^= 1 << i
				if on {
					c.style.decorations |= 1 << i
				}
			}
		}
		if !ok {
			return componentObject{}, false
		}
	}
	return c, true
}

// text writes text with the style st. Codes in text make the style unknown,
// so that the next text writes its style whole.
func (w *textWriter) text(text string, st style) {
	if text == "" {
		return
	}
	w.setStyle(st)
	w.b.WriteString(text)
	w.unknown = strings.ContainsRune(text, '§')
}

// setStyle writes the codes that change the style to st. Removing a color or a
// decoration needs a color code or "§r", which reset the decorations.
func (w *textWriter) setStyle(st style) {
	if !w.unknown && st == w.style {
		return
	}
	added := st.decorations
	if w.unknown || st.color != w.style.color || w.style.decorations&^st.decorations != 0 {
		if st.color >= 0 {
			w.b.WriteString("§" + colorCodes[st.color:st.color+1])
		} else {
			w.b.WriteString("§r")
		}
	} else {
		added &^= w.style.decorations
	}
	for i := range len(decorationCodes) {
		if added&(1<<i) != 0 {
			w.b.WriteString("§" + decorationCodes[i:i+1])
		}
	}
	w.style, w.unknown = st, false
}

// parseColor returns the index of the legacy color of color, a color of a JSON
// text component: its name, or the closest one to "#RRGGBB".
func parseColor(color string) (int, bool) {
	for i, name := range colorNames {
		if color == name {
			return i, true
		}
	}
	hex, ok := strings.CutPrefix(color, "#")
	if !ok || !isHexColor(hex) {
		return 0, false
	}
	var rgb [3]int
	for i := range len(hex) {
		rgb[i/2] = rgb[i/2]<<4 | strings.IndexByte(colorCodes, lower(hex[i]))
	}
	closest, closestDistance := 0, -1
	for i, c := range colorRGB {
		distance := 0
		for j := range c {
			distance += (c[j] - rgb[j]) * (c[j] - rgb[j])
		}
		if closestDistance < 0 || distance < closestDistance {
			closest, closestDistance = i, distance
		}
	}
	return closest, true
}

// isHexColor reports whether s is six hexadecimal digits.
func isHexColor(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := range len(s) {
		if strings.IndexByte(colorCodes, lower(s[i])) < 0 {
			return false
		}
	}
	return true
}

func lower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// textPart is a part of a JSON text component, with its style.
type textPart struct {
	Text          string `json:"text"`
	Color         string `json:"color,omitempty"`
	Obfuscated    bool   `json:"obfuscated,omitempty"`
	Bold          bool   `json:"bold,omitempty"`
	Strikethrough bool   `json:"strikethrough,omitempty"`
	Underlined    bool   `json:"underlined,omitempty"`
	Italic        bool   `json:"italic,omitempty"`
}

// ComponentFromText converts text, legacy text whose codes start with char,
// such as "§c" or "&c", to a JSON text component. Like in legacy text, a color
// resets the decorations, and "r" resets the style to the one around the
// component. Codes of hex colors, such as "§#ff8800", which clients only
// support in JSON, become hex colors. Unknown codes stay as text.
func ComponentFromText(text string, char rune) json.RawMessage {
	var parts []textPart
	var pending strings.Builder
	// color and decorations are the style that the codes read so far give to
	// the text after them, and pendingColor and pendingDecorations the style
	// of pending, the text of the next part. A part ends only when the style
	// changes, as consecutive codes may leave it as it was.
	color, decorations := "", uint8(0)
	pendingColor, pendingDecorations := color, decorations
	flush := func() {
		if pending.Len() == 0 {
			return
		}
		parts = append(parts, textPart{
			Text: pending.String(), Color: pendingColor,
			Obfuscated: pendingDecorations&1 != 0, Bold: pendingDecorations&2 != 0, Strikethrough: pendingDecorations&4 != 0,
			Underlined: pendingDecorations&8 != 0, Italic: pendingDecorations&16 != 0,
		})
		pending.Reset()
	}
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == char && i+size < len(text) {
			rest := text[i+size:]
			c := lower(rest[0])
			n := 1
			switch {
			case strings.IndexByte(colorCodes, c) >= 0:
				color, decorations = colorNames[strings.IndexByte(colorCodes, c)], 0
			case strings.IndexByte(decorationCodes, c) >= 0:
				decorations |= 1 << strings.IndexByte(decorationCodes, c)
			case c == 'r':
				color, decorations = "", 0
			case c == '#' && len(rest) >= 7 && isHexColor(rest[1:7]):
				color, decorations = "#"+strings.ToLower(rest[1:7]), 0
				n = 7
			default:
				n = 0
			}
			if n > 0 {
				i += size + n
				continue
			}
		}
		if color != pendingColor || decorations != pendingDecorations {
			flush()
			pendingColor, pendingDecorations = color, decorations
		}
		pending.WriteString(text[i : i+size])
		i += size
	}
	flush()

	var component any
	switch len(parts) {
	case 0:
		component = textPart{}
	case 1:
		component = parts[0]
	default:
		component = struct {
			Text  string     `json:"text"`
			Extra []textPart `json:"extra"`
		}{Extra: parts}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)  // Kept readable: components are not HTML.
	_ = enc.Encode(component) // Cannot fail: the parts hold strings and bools.
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
