package script

import (
	"errors"
	"math"
	"testing"

	"go.starlark.net/starlark"
)

// A status passed through scripts unchanged comes out identical, member order,
// large integers and favicon included.
func TestStatusValueRoundTrip(t *testing.T) {
	data := `{"version":{"name":"1.21.10","protocol":773},"description":{"text":"Hi","extra":[{"text":"!","bold":true}]},` +
		`"players":{"max":100,"online":2,"sample":[]},"favicon":"data:image/png;base64,AAAA","big":123456789012345678901234567890,` +
		`"ratio":0.5,"none":null,"enforcesSecureChat":false}`
	v := statusValue(mustParseStatus(t, data))
	got, err := marshalJSON(v)
	if err != nil || string(got) != data {
		t.Errorf("marshalJSON() = %s, %v, want %s", got, err, data)
	}
}

func TestMarshalJSONErrors(t *testing.T) {
	looped := starlark.NewList(nil)
	_ = looped.Append(looped)
	tests := []struct {
		value starlark.Value
		want  string
	}{
		{starlark.NewSet(0), "set is not a JSON value"},
		{starlark.Float(math.Inf(1)), "+inf is not a JSON number"},
		{looped, "nested too deeply"},
	}
	for _, tc := range tests {
		if _, err := marshalJSON(tc.value); err == nil || err.Error() != tc.want {
			t.Errorf("marshalJSON(%s) error = %v, want %s", tc.value.Type(), err, tc.want)
		}
	}
}

// fms.merge merges like the variables of servers merge statuses (RFC 7396).
func TestMerge(t *testing.T) {
	c, err := NewScope(nil, nil).CompileCondition(`fms.merge({"a": 1, "b": {"c": 2, "d": 3}}, {"b": {"c": None, "e": 4}, "f": [5]}) == {"a": 1, "b": {"d": 3, "e": 4}, "f": [5]}` +
		` and fms.merge({"a": 1}, "x") == "x" and fms.merge("x", {"a": None}) == {}`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &sampleBase}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

func TestTextFunctions(t *testing.T) {
	tests := []struct {
		fn   func(string) string
		in   string
		want string
	}{
		{stripFormatting, "§cRed §lbold§r", "Red bold"},
		{stripFormatting, "a§§bc§", "abc"},
		{stripFormatting, "100%§", "100%"},
		{stripInvisible, "a\nb\tc\x00d\u202ee\u200bf\xffg", "abcdefg"},
		{stripInvisible, "§cNotch é 名前", "§cNotch é 名前"},
	}
	for _, tc := range tests {
		if got := tc.fn(tc.in); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
	c, err := NewScope(nil, nil).CompileCondition(`fms.sanitize("§c§\nNotch\u202e") == "Notch"`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &sampleBase}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

// A patch that holds itself fails, rather than recursing until the stack
// overflows, which would crash the server.
func TestMergeLooped(t *testing.T) {
	looped := starlark.NewDict(1)
	_ = looped.SetKey(starlark.String("a"), looped)
	if _, err := merge(starlark.None, looped, 0); !errors.Is(err, errNestedTooDeeply) {
		t.Errorf("merge() error = %v, want %v", err, errNestedTooDeeply)
	}
}
