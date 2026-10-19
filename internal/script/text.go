package script

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.starlark.net/starlark"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/legacyproto"
)

// newTextBuiltin returns the function name, which returns the text it is given
// transformed by transform.
func newTextBuiltin(name string, transform func(string) string) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var text string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &text); err != nil {
			return nil, err
		}
		return starlark.String(transform(text)), nil
	})
}

// sanitize implements fms.sanitize: the codes first, so that "§" takes the
// invisible character after it, rather than the next visible one.
func sanitize(text string) string {
	return stripInvisible(legacyproto.StripFormatting(text))
}

// stripInvisible removes from s the characters that logs escape (see
// strconv.IsPrint): line breaks, tabs, other control characters, spaces other
// than the ASCII one, zero-width and bidirectional characters, and invalid
// UTF-8.
func stripInvisible(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size == 1 {
				continue
			}
		}
		if strconv.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// newComponentBuiltin returns the function name, which returns the text that
// convert makes of the text component it is given, written as text, dicts and
// lists (see legacyproto.TextFromValue).
func newComponentBuiltin(name string, convert func(component any) string) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var component starlark.Value
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &component); err != nil {
			return nil, err
		}
		switch component.(type) {
		case starlark.String, *starlark.Dict, *starlark.List, starlark.Tuple:
		default:
			return nil, fmt.Errorf("%s: got %s, want text or a text component", b.Name(), component.Type())
		}
		value, err := toJSON(component, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		return starlark.String(convert(value)), nil
	})
}

// plainText implements fms.plain_text: the legacy text of component without
// its codes.
func plainText(component any) string {
	return legacyproto.StripFormatting(legacyproto.TextFromValue(component))
}

// fromLegacyBuiltin implements fms.from_legacy(text, char="§"), the text
// component of legacy text, whose codes start with char (see
// legacyproto.ComponentFromText).
func fromLegacyBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var text string
	char := "§"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "text", &text, "char?", &char); err != nil {
		return nil, err
	}
	r, size := utf8.DecodeRuneInString(char)
	if size == 0 || size != len(char) || r == utf8.RuneError {
		return nil, fmt.Errorf("%s: char must be one character, got %q", b.Name(), char)
	}
	return componentValue(legacyproto.ComponentFromText(text, r)), nil
}
