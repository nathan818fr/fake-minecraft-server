package script

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"

	"go.starlark.net/starlark"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// maxJSONDepth bounds the nesting of the values that scripts return or merge,
// which can hold themselves (a = []; a.append(a)).
const maxJSONDepth = 64

var errNestedTooDeeply = errors.New("nested too deeply")

// jsonValue returns v, a JSON value as held by status.Object, as a Starlark
// value: objects become dicts that keep the order of their members, arrays
// lists, integers ints and other numbers floats, so that toJSON gives the
// same values back.
func jsonValue(v any) starlark.Value {
	switch v := v.(type) {
	case bool:
		return starlark.Bool(v)
	case json.Number:
		return numberValue(v)
	case string:
		return starlark.String(v)
	case []any:
		elems := make([]starlark.Value, len(v))
		for i, e := range v {
			elems[i] = jsonValue(e)
		}
		return starlark.NewList(elems)
	case status.Object:
		d := starlark.NewDict(len(v))
		for _, m := range v {
			_ = d.SetKey(starlark.String(m.Name), jsonValue(m.Value)) // d is not frozen.
		}
		return d
	case json.RawMessage:
		// Values kept encoded, such as favicons, are decoded on demand.
		var decoded any
		if err := json.Unmarshal(v, &decoded); err == nil {
			if s, ok := decoded.(string); ok {
				return starlark.String(s)
			}
		}
		return starlark.String(string(v))
	}
	return starlark.None
}

func numberValue(n json.Number) starlark.Value {
	text := string(n)
	if !strings.ContainsAny(text, ".eE") {
		if i, err := strconv.ParseInt(text, 10, 64); err == nil {
			return starlark.MakeInt64(i)
		}
		if b, ok := new(big.Int).SetString(text, 10); ok {
			return starlark.MakeBigInt(b)
		}
	}
	f, _ := strconv.ParseFloat(text, 64) // Valid JSON numbers parse.
	return starlark.Float(f)
}

// statusValue returns st as a frozen Starlark dict, or None if st is nil.
func statusValue(st *status.Status) starlark.Value {
	if st == nil {
		return starlark.None
	}
	v := jsonValue(st.Object)
	v.Freeze()
	return v
}

// statusSlot holds the Starlark value of the last status of a source, such as
// an upstream, so that each status is converted once, whatever the number of
// requests that read it.
type statusSlot struct {
	last atomic.Pointer[convertedStatus]
}

type convertedStatus struct {
	status  *status.Status
	value   starlark.Value
	favicon encodedFavicon
}

// value returns the Starlark value of st (see statusValue).
func (s *statusSlot) value(st *status.Status) starlark.Value {
	if st == nil {
		return starlark.None
	}
	if c := s.last.Load(); c != nil && c.status == st {
		return c.value
	}
	v := statusValue(st)
	s.last.Store(&convertedStatus{status: st, value: v, favicon: faviconOf(st, v)})
	return v
}

var faviconPath = status.Path{"favicon"}

// encodedFavicon is a favicon that scripts can read, as they see it and in
// JSON, so that a ping hook that returns it unchanged need not encode it again,
// which would cost more than the rest of its answer.
type encodedFavicon struct {
	text string
	json json.RawMessage
}

// faviconOf returns the favicon of st, whose Starlark value is v (see
// statusValue), or a zero encodedFavicon if it has none.
func faviconOf(st *status.Status, v starlark.Value) encodedFavicon {
	data, _ := st.Get(faviconPath)
	encoded, ok := data.(json.RawMessage) // See status.Parse.
	if !ok {
		return encodedFavicon{}
	}
	value, _, _ := v.(*starlark.Dict).Get(starlark.String("favicon"))
	text, ok := value.(starlark.String)
	if !ok {
		return encodedFavicon{}
	}
	return encodedFavicon{text: string(text), json: encoded}
}

// is reports whether text is the favicon f. Strings are compared by address,
// at once: a favicon returned unchanged is the same string, and any other is
// simply encoded again.
func (f encodedFavicon) is(text string) bool {
	return f.json != nil && len(f.text) == len(text) && unsafe.StringData(f.text) == unsafe.StringData(text)
}

// toJSON returns v, a value that a script returned, as a JSON value as held by
// status.Object. Only the values of JSON are accepted: dicts with string keys,
// lists and tuples, strings, ints, finite floats, bools and None.
func toJSON(v starlark.Value, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errNestedTooDeeply
	}
	switch v := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(v), nil
	case starlark.Int:
		return json.Number(v.String()), nil
	case starlark.Float:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%v is not a JSON number", v)
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	case starlark.String:
		return string(v), nil
	case *starlark.Dict:
		o := make(status.Object, 0, v.Len())
		for _, item := range v.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings, got %s", item[0].Type())
			}
			value, err := toJSON(item[1], depth+1)
			if err != nil {
				return nil, withPath(key.String(), err)
			}
			o = append(o, status.Member{Name: string(key), Value: value})
		}
		return o, nil
	case *starlark.List, starlark.Tuple:
		elems := v.(starlark.Indexable)
		values := make([]any, elems.Len())
		for i := range values {
			var err error
			if values[i], err = toJSON(elems.Index(i), depth+1); err != nil {
				return nil, withPath(strconv.Itoa(i), err)
			}
		}
		return values, nil
	}
	return nil, fmt.Errorf("%s is not a JSON value", v.Type())
}

// marshalJSON returns v, a value that a script returned, in JSON (see toJSON).
func marshalJSON(v starlark.Value) (json.RawMessage, error) {
	value, err := toJSON(v, 0)
	if err != nil {
		return nil, err
	}
	return status.AppendValue(nil, value), nil
}

// withPath returns err prefixed with where it happened, unless it is about
// nesting, whose path would be as long as the limit.
func withPath(path string, err error) error {
	if errors.Is(err, errNestedTooDeeply) {
		return err
	}
	return fmt.Errorf("%s: %w", path, err)
}

// mergeBuiltin implements fms.merge(base, patch) (see merge).
func mergeBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var base, patch starlark.Value
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &base, &patch); err != nil {
		return nil, err
	}
	merged, err := merge(base, patch, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return merged, nil
}

// merge returns patch merged into target, like a JSON merge patch (RFC 7396),
// as variables merge statuses: dicts merge, None removes a member, other
// values replace. target is left as is. depth is the nesting of patch in the
// patch given to fms.merge.
func merge(target, patch starlark.Value, depth int) (starlark.Value, error) {
	if depth > maxJSONDepth {
		return nil, errNestedTooDeeply
	}
	patchDict, ok := patch.(*starlark.Dict)
	if !ok {
		return patch, nil
	}
	merged := starlark.NewDict(patchDict.Len())
	if targetDict, ok := target.(*starlark.Dict); ok {
		for _, item := range targetDict.Items() {
			_ = merged.SetKey(item[0], item[1]) // merged is not frozen.
		}
	}
	for _, item := range patchDict.Items() {
		key, value := item[0], item[1]
		if value == starlark.None {
			if _, _, err := merged.Delete(key); err != nil {
				return nil, err
			}
			continue
		}
		current, _, err := merged.Get(key)
		if err != nil {
			return nil, err
		}
		if value, err = merge(current, value, depth+1); err != nil {
			return nil, err
		}
		if err := merged.SetKey(key, value); err != nil {
			return nil, err
		}
	}
	return merged, nil
}

// stripFormatting removes the formatting codes of Minecraft from s: "§" and
// the character after it, whatever it is, as the client reads them, so that
// no "§" is left.
func stripFormatting(s string) string {
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

// sanitize implements fms.sanitize: the codes first, so that "§" takes the
// invisible character after it, rather than the next visible one.
func sanitize(text string) string {
	return stripInvisible(stripFormatting(text))
}

// stripInvisible removes from s the characters that logs escape: line breaks,
// tabs, other control characters, zero-width and bidirectional characters,
// and invalid UTF-8.
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
