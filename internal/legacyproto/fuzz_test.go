package legacyproto

import (
	"bufio"
	"bytes"
	"testing"
	"unicode/utf8"
)

func FuzzReadPing(f *testing.F) {
	f.Add([]byte("\xfe\x01\xfa\x00\x0b\x00M\x00C\x00|\x00P\x00i\x00n\x00g\x00H\x00o\x00s\x00t\x00\x19\x49\x00\x09\x00l\x00o\x00c\x00a\x00l\x00h\x00o\x00s\x00t\x00\x00\x63\xdd"))
	f.Add([]byte{0xfe, 0x01})
	f.Add([]byte{0xfe})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReader(bytes.NewReader(data))
		_ = Incomplete(r)
		_, _, _ = ReadPing(r)
	})
}

func FuzzReadLogin(f *testing.F) {
	f.Add([]byte("\x02\x4e\x00\x05\x00N\x00o\x00t\x00c\x00h\x00\x09\x00l\x00o\x00c\x00a\x00l\x00h\x00o\x00s\x00t\x00\x00\x63\xdd"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = ReadLogin(bufio.NewReader(bytes.NewReader(data)))
	})
}

func FuzzTextFromComponent(f *testing.F) {
	f.Add([]byte(`{"text":"A","extra":[{"text":"B","bold":true},"C",["D"]]}`))
	f.Add([]byte(`"plain"`))
	f.Fuzz(func(t *testing.T, component []byte) {
		text := TextFromComponent(component)
		if !utf8.ValidString(text) && utf8.Valid(component) {
			t.Errorf("TextFromComponent(%q) = %q, invalid UTF-8", component, text)
		}
		_ = AppendKick(nil, text)
	})
}
