package status

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/modernproto"
)

func mustParse(t testing.TB, data string) *Status {
	t.Helper()
	s, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse(%s) error: %v", data, err)
	}
	return s
}

func TestParse(t *testing.T) {
	s := mustParse(t, `{
		"description": {"text": "Hello"},
		"players": {"max": 100, "online": 3, "sample": [{"name": "Notch", "id": "069a79f4-44e9-4726-a5be-fca90e38aaf5"}]},
		"version": {"name": "Paper 1.21.10", "protocol": 773},
		"favicon": "data:image/png;base64,AAAA",
		"enforcesSecureChat": true,
		"forgeData": {"fmlNetworkVersion": 3, "d": "abc", "mods": [], "x": null}
	}`)
	want := Object{
		{"description", Object{{"text", "Hello"}}},
		{"players", Object{
			{"max", json.Number("100")},
			{"online", json.Number("3")},
			{"sample", []any{Object{{"name", "Notch"}, {"id", "069a79f4-44e9-4726-a5be-fca90e38aaf5"}}}},
		}},
		{"version", Object{{"name", "Paper 1.21.10"}, {"protocol", json.Number("773")}}},
		// Kept encoded.
		{"favicon", json.RawMessage(`"data:image/png;base64,AAAA"`)},
		{"enforcesSecureChat", true},
		{"forgeData", Object{{"fmlNetworkVersion", json.Number("3")}, {"d", "abc"}, {"mods", []any{}}, {"x", nil}}},
	}
	if !reflect.DeepEqual(s.Object, want) {
		t.Errorf("Parse() = %+v, want %+v", s.Object, want)
	}

	if got, want := s.Version(), (Version{Name: "Paper 1.21.10", Protocol: 773}); got != want {
		t.Errorf("Version() = %+v, want %+v", got, want)
	}
	if got, want := s.Players(), (Players{Max: 100, Online: 3}); got != want {
		t.Errorf("Players() = %+v, want %+v", got, want)
	}
	if got, want := string(AppendValue(nil, s.Description())), `{"text":"Hello"}`; got != want {
		t.Errorf("Description() = %s, want %s", got, want)
	}
}

// New encodes a favicon, unless it already is, so that callers can pass an
// encoded one on without encoding it again.
func TestNew(t *testing.T) {
	encoded := json.RawMessage(`"data:image/png;base64,AAAA"`)
	for _, favicon := range []any{"data:image/png;base64,AAAA", encoded} {
		s, err := New(Object{{"favicon", favicon}})
		if err != nil {
			t.Fatalf("New() error: %v", err)
		}
		got, _ := s.Get(faviconPath)
		if data, ok := got.(json.RawMessage); !ok || string(data) != string(encoded) {
			t.Errorf("New(%#v) favicon = %#v, want %s", favicon, got, encoded)
		}
		if _, ok := favicon.(json.RawMessage); ok && &got.(json.RawMessage)[0] != &encoded[0] {
			t.Errorf("New() encoded the favicon again")
		}
	}
}

func TestParseWithoutKnownFields(t *testing.T) {
	s := mustParse(t, `{"custom":1}`)
	if s.Version() != (Version{}) || s.Players() != (Players{}) || s.Description() != nil {
		t.Errorf("Version() = %+v, Players() = %+v, Description() = %v, want zero values", s.Version(), s.Players(), s.Description())
	}
}

// Every field is passed on, in order.
func TestAppendJSON(t *testing.T) {
	for _, data := range []string{
		`{}`,
		`{"custom":1,"description":"Hi","players":{"online":1,"hidden":true,"max":2},"version":{"protocol":773,"name":"Fake","build":7}}`,
		`{"z":{"b":[1.50,-2e3,12345678901234567890,true,false,null,"",{},[]],"a":"§\"\\\n"}}`,
	} {
		if got := string(mustParse(t, data).AppendJSON(nil)); got != data {
			t.Errorf("AppendJSON() = %s, want %s", got, data)
		}
	}
}

// Strings escape only what JSON requires, in the shortest form.
func TestAppendString(t *testing.T) {
	tests := []struct {
		s    string
		want string
	}{
		{"", `""`},
		{"§eHello 日本 😀", `"§eHello 日本 😀"`},
		{`quote " backslash \`, `"quote \" backslash \\"`},
		{"\n\r\t\b\f\x00\x1f\x7f", `"\n\r\t\b\f\u0000\u001f` + "\x7f" + `"`},
		// Escaped by encoding/json, but valid in JSON.
		{"<b>&amp;</b>   ", `"<b>&amp;</b> ` + "  " + `"`},
		{"invalid \xff \xe2\x80", `"invalid ` + "� ��" + `"`},
	}
	for _, tc := range tests {
		if got := string(appendString(nil, tc.s)); got != tc.want {
			t.Errorf("appendString(%q) = %s, want %s", tc.s, got, tc.want)
		}
	}
}

// A status is never longer once encoded again than the JSON it was decoded
// from, in UTF-16 code units like the protocol limit, so that statuses received
// from servers fit in responses too. Nothing is lost either.
func FuzzParse(f *testing.F) {
	for _, data := range []string{
		`{}`,
		` { "description" : { "text" : "§eHi <b> \/ 😀  " } , "x" : [ 1.50 , -2e3 , true , null ] } `,
		`{"version":{"name":"\u0000\u001f\b\f\n\r\t\"\\","protocol":773},"players":{"max":1,"online":0}}`,
		`{"favicon":"data:image\/png;base64,AAAA","a":"\ud800 lone surrogate"}`,
	} {
		f.Add([]byte(data))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := Parse(data)
		if err != nil {
			return
		}
		encoded := s.AppendJSON(nil)
		if n, original := modernproto.UTF16Length(encoded), modernproto.UTF16Length(data); n > original {
			t.Errorf("AppendJSON() = %s, %d UTF-16 code units, longer than the %d of %s", encoded, n, original, data)
		}
		again, err := Parse(encoded)
		if err != nil || !reflect.DeepEqual(again, s) {
			t.Errorf("Parse(%s) = %+v, %v, want %+v", encoded, again, err, s)
		}
	})
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		data string
		want string
	}{
		{``, "unexpected EOF"},
		{`[]`, "must be a JSON object, got []"},
		{`{"a":1`, "unexpected end of JSON input"},
		{`{"a" 1}`, "invalid character '1' after object key"},
		{`{} {}`, "invalid data after the JSON value"},
		{`{"a":1,"a":2}`, `duplicate field "a"`},
		{`{"forgeData":{"mods":[{"id":1,"id":2}]}}`, `duplicate field "id"`},
		{
			`{"` + strings.Repeat("k", 100) + `":1,"` + strings.Repeat("k", 100) + `":2}`,
			`duplicate field "` + strings.Repeat("k", 63) + `...`,
		},
		{`{"version":[]}`, "version: must be a JSON object, got []"},
		{`{"players":"many"}`, `players: must be a JSON object, got "many"`},
		{`{"version":{"name":1}}`, "version.name: must be a string, got 1"},
		{`{"version":{"protocol":"773"}}`, `version.protocol: must be an integer between -2147483648 and 2147483647, got "773"`},
		{`{"version":{"protocol":3000000000}}`, "version.protocol: must be an integer between -2147483648 and 2147483647, got 3000000000"},
		{`{"players":{"online":1.5}}`, "players.online: must be an integer between -2147483648 and 2147483647, got 1.5"},
		// Long values are shortened, without cutting characters.
		{
			`{"version":{"name":["a` + strings.Repeat("§", 40) + `"]}}`,
			`version.name: must be a string, got ["a` + strings.Repeat("§", 30) + `...`,
		},
	}
	for _, tc := range tests {
		if _, err := Parse([]byte(tc.data)); err == nil || err.Error() != tc.want {
			t.Errorf("Parse(%s) error = %v, want %s", tc.data, err, tc.want)
		}
	}
}

func TestGet(t *testing.T) {
	s := mustParse(t, `{"forgeData":{"mods":[{"modId":"forge"},{"modId":"jei"}]},"favicon":"data:x"}`)
	tests := []struct {
		path Path
		want any
		ok   bool
	}{
		{Path{"forgeData", "mods", "1", "modId"}, "jei", true},
		{Path{"forgeData", "mods", "2"}, nil, false},
		{Path{"forgeData", "mods", "first"}, nil, false},
		// Indexes are written like in JSON Pointer only.
		{Path{"forgeData", "mods", "+1"}, nil, false},
		{Path{"forgeData", "mods", "01"}, nil, false},
		{Path{"forgeData", "mods", "-0"}, nil, false},
		{Path{"forgeData", "missing"}, nil, false},
		{Path{"forgeData", "mods", "0", "modId", "x"}, nil, false},
		// Encoded values are not looked into.
		{Path{"favicon", "0"}, nil, false},
	}
	for _, tc := range tests {
		if got, ok := s.Get(tc.path); got != tc.want || ok != tc.ok {
			t.Errorf("Get(%q) = %v, %v, want %v, %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
	if got, ok := s.Get(Path{"forgeData", "mods", "0"}); !ok || !reflect.DeepEqual(got, Object{{"modId", "forge"}}) {
		t.Errorf("Get() = %v, %v, want the first mod", got, ok)
	}
}

func TestWith(t *testing.T) {
	const data = `{"version":{"name":"Fake","protocol":773},"players":{"sample":[{"name":"Notch"},{"name":"jeb_"}]},"description":"Hi"}`
	s := mustParse(t, data)
	tests := []struct {
		path  Path
		value any
		want  string
	}{
		{
			Path{"version", "protocol"}, json.Number("5"),
			`{"version":{"name":"Fake","protocol":5},"players":{"sample":[{"name":"Notch"},{"name":"jeb_"}]},"description":"Hi"}`,
		},
		{
			Path{"players", "sample", "1", "name"}, "Dinnerbone",
			`{"version":{"name":"Fake","protocol":773},"players":{"sample":[{"name":"Notch"},{"name":"Dinnerbone"}]},"description":"Hi"}`,
		},
		// The objects missing on the way are created.
		{
			Path{"a", "b"}, "c",
			`{"version":{"name":"Fake","protocol":773},"players":{"sample":[{"name":"Notch"},{"name":"jeb_"}]},"description":"Hi","a":{"b":"c"}}`,
		},
		// The values on the way that are neither objects nor arrays are
		// replaced.
		{
			Path{"description", "text"}, "Hello",
			`{"version":{"name":"Fake","protocol":773},"players":{"sample":[{"name":"Notch"},{"name":"jeb_"}]},"description":{"text":"Hello"}}`,
		},
	}
	for _, tc := range tests {
		got, err := s.With(tc.path, tc.value)
		if err != nil || string(got.AppendJSON(nil)) != tc.want {
			t.Errorf("With(%q, %v) = %s, %v, want %s", tc.path, tc.value, got.AppendJSON(nil), err, tc.want)
		}
	}
	// The original is left as is.
	if got := string(s.AppendJSON(nil)); got != data {
		t.Errorf("original = %s, want %s", got, data)
	}
}

// In arrays, path elements must be the indexes of existing elements.
func TestWithErrors(t *testing.T) {
	s := mustParse(t, `{"players":{"sample":[{"name":"Notch"},{"name":"jeb_"}]}}`)
	tests := []struct {
		path Path
		want string
	}{
		{Path{"players", "sample", "2", "name"}, "players.sample.2: no such element, the array has 2"},
		{Path{"players", "sample", "first"}, "players.sample.first: no such element, the array has 2"},
		{Path{"players", "sample", "01"}, "players.sample.01: no such element, the array has 2"},
	}
	for _, tc := range tests {
		if _, err := s.With(tc.path, "x"); err == nil || err.Error() != tc.want {
			t.Errorf("With(%q) error = %v, want %s", tc.path, err, tc.want)
		}
	}
}

func TestWithProtocolVersion(t *testing.T) {
	tests := []struct {
		data string
		want string
	}{
		{`{"version":{"name":"Fake","protocol":47},"description":"Hi"}`, `{"version":{"name":"Fake","protocol":773},"description":"Hi"}`},
		// Clients ignore versions without a name.
		{`{"version":{"protocol":47}}`, `{"version":{"protocol":773,"name":""}}`},
		{`{"description":"Hi"}`, `{"description":"Hi","version":{"name":"","protocol":773}}`},
	}
	for _, tc := range tests {
		if got := string(mustParse(t, tc.data).WithProtocolVersion(773).AppendJSON(nil)); got != tc.want {
			t.Errorf("WithProtocolVersion(%s) = %s, want %s", tc.data, got, tc.want)
		}
	}
}

// Copies of the same object do not share the members they add.
func TestWithIndependentCopies(t *testing.T) {
	o := make(Object, 0, 4) // Room for members to append.
	a, _ := o.With(Path{"a"}, true)
	b, _ := o.With(Path{"b"}, true)
	if got, want := string(a.AppendJSON(nil)), `{"a":true}`; got != want {
		t.Errorf("first copy = %s, want %s", got, want)
	}
	if got, want := string(b.AppendJSON(nil)), `{"b":true}`; got != want {
		t.Errorf("second copy = %s, want %s", got, want)
	}
}

func BenchmarkAppendJSON(b *testing.B) {
	s := mustParse(b, `{"version":{"name":"Fake 1.0","protocol":773},"players":{"max":20,"online":3,"sample":[]},`+
		`"description":{"text":"Hello"},"favicon":"data:image/png;base64,`+strings.Repeat("A", 16<<10)+`",`+
		`"enforcesSecureChat":true,"forgeData":{"fmlNetworkVersion":3,"mods":[{"modId":"forge","modmarker":"ANY"}]}}`)
	buf := s.AppendJSON(nil)
	b.ReportAllocs()
	for b.Loop() {
		buf = s.AppendJSON(buf[:0])
	}
}
