package status

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nathan818fr/fake-minecraft-server/internal/logging"
)

// Object is a JSON object whose members keep their order. Its values are nil,
// bool, json.Number, string, []any, Object, or json.RawMessage for values kept
// encoded, so that they can be read, merged and transformed without losing
// anything, such as the digits of large numbers.
//
// Objects are not modified once built, so that they can be shared: With
// returns a modified copy.
type Object []Member

type Member struct {
	Name  string
	Value any
}

// Path locates a value in nested objects and arrays: each of its elements is
// the name of an object member, or the index of an array element, written in
// decimal without sign or leading zeros, as in JSON Pointer (RFC 6901).
type Path []string

// String returns path with its elements separated by dots, as in error
// messages.
func (p Path) String() string {
	return strings.Join(p, ".")
}

// Get returns the value at path, and whether there is one. Encoded values
// (json.RawMessage) are not looked into.
func (o Object) Get(path Path) (any, bool) {
	if len(path) == 0 {
		return o, true
	}
	// The first member is looked up in o itself: o in an interface would be
	// allocated at each call.
	i := o.index(path[0])
	if i < 0 {
		return nil, false
	}
	value := o[i].Value
	for _, key := range path[1:] {
		switch v := value.(type) {
		case Object:
			i := v.index(key)
			if i < 0 {
				return nil, false
			}
			value = v[i].Value
		case []any:
			i, ok := arrayIndex(v, key)
			if !ok {
				return nil, false
			}
			value = v[i]
		default:
			return nil, false
		}
	}
	return value, true
}

// With returns a copy of o with value at path, which must not be empty. It
// creates the objects missing on the way and, like a JSON merge patch
// (RFC 7396), replaces the values on the way that are neither objects nor
// arrays. In arrays, path elements must be the indexes of existing elements.
// The copy shares what it does not change with o.
func (o Object) With(path Path, value any) (Object, error) {
	updated, err := with(o, path, 0, value)
	if err != nil {
		return nil, err
	}
	return updated.(Object), nil
}

// with returns a copy of v, the value at path[:depth], with value at path.
func with(v any, path Path, depth int, value any) (any, error) {
	if depth == len(path) {
		return value, nil
	}
	key := path[depth]
	if array, ok := v.([]any); ok {
		i, ok := arrayIndex(array, key)
		if !ok {
			return nil, fmt.Errorf("%s: no such element, the array has %d", path[:depth+1], len(array))
		}
		element, err := with(array[i], path, depth+1, value)
		if err != nil {
			return nil, err
		}
		copied := slices.Clone(array)
		copied[i] = element
		return copied, nil
	}

	o, _ := v.(Object) // Other values are replaced.
	i := o.index(key)
	var member any
	if i >= 0 {
		member = o[i].Value
	}
	member, err := with(member, path, depth+1, value)
	if err != nil {
		return nil, err
	}
	if i < 0 {
		// Clipped, so that append copies o instead of extending it.
		return append(slices.Clip(o), Member{Name: key, Value: member}), nil
	}
	copied := slices.Clone(o)
	copied[i].Value = member
	return copied, nil
}

func arrayIndex(array []any, key string) (int, bool) {
	i, err := strconv.Atoi(key)
	if err != nil || i < 0 || i >= len(array) || strconv.Itoa(i) != key {
		return 0, false
	}
	return i, true
}

func (o Object) index(name string) int {
	return slices.IndexFunc(o, func(m Member) bool { return m.Name == name })
}

func (o Object) AppendJSON(b []byte) []byte {
	b = append(b, '{')
	for i, m := range o {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendString(b, m.Name)
		b = append(b, ':')
		b = appendValue(b, m.Value)
	}
	return append(b, '}')
}

// appendValue appends v, a value of an Object, in JSON. It panics on values of
// other types.
func appendValue(b []byte, v any) []byte {
	switch v := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		return strconv.AppendBool(b, v)
	case json.Number:
		return append(b, v...)
	case string:
		return appendString(b, v)
	case []any:
		b = append(b, '[')
		for i, item := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendValue(b, item)
		}
		return append(b, ']')
	case Object:
		return v.AppendJSON(b)
	case json.RawMessage:
		return append(b, v...)
	default:
		panic(fmt.Sprintf("status: unsupported value of type %T", v))
	}
}

// quote returns v in JSON for error messages, shortened like untrusted values
// in logs, as it may come from servers.
func quote(v any) string {
	return logging.Shorten(string(appendValue(nil, v)))
}

// appendString appends s as a JSON string, escaping only what JSON requires:
// quotation marks, backslashes and control characters, in their shortest form.
// Unlike with encoding/json, characters such as '<' or U+2028 are written as
// they are, which clients accept, so that a status is never longer than the
// JSON it was decoded from. Invalid UTF-8 is replaced with U+FFFD.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0 // Index of the first character not appended yet.
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				b = append(b, s[start:i]...)
				b = utf8.AppendRune(b, utf8.RuneError)
				start = i + size
			}
			i += size
			continue
		}
		if c >= ' ' && c != '"' && c != '\\' {
			i++
			continue
		}
		b = append(b, s[start:i]...)
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		default:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
		i++
		start = i
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

const hexDigits = "0123456789abcdef"

// decodeJSON decodes data, which holds a single JSON value, into the types of
// Object values. It rejects duplicate names in objects, which JSON parsers do
// not all handle the same way.
func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if errors.Is(err, io.EOF) {
		return nil, io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid data after the JSON value")
	}
	return value, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		var o Object
		names := make(map[string]bool)
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name := token.(string) // Object keys are strings.
			if names[name] {
				return nil, fmt.Errorf("duplicate field %s", quote(name))
			}
			names[name] = true
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			o = append(o, Member{Name: name, Value: value})
		}
		_, err := dec.Token() // The closing brace.
		return o, err
	case json.Delim('['):
		values := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		_, err := dec.Token() // The closing bracket.
		return values, err
	default:
		return token, nil
	}
}
