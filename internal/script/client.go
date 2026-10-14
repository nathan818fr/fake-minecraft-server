package script

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"
)

// clientFields are the fields of the client variable, named like the fields
// of logs, in the order of their bits in clientValue.read. Their values are
// strings, ints or bools (see Field).
var clientFields = []struct {
	name  string
	value func(c *Client) starlark.Value
}{
	{"request", func(c *Client) starlark.Value { return starlark.String(c.Request) }},
	{"transferred", func(c *Client) starlark.Value { return starlark.Bool(c.Transferred) }},
	{"host", func(c *Client) starlark.Value { return starlark.String(c.Host) }},
	{"host_extra", func(c *Client) starlark.Value { return starlark.String(c.HostExtra) }},
	{"port", func(c *Client) starlark.Value { return starlark.MakeInt(c.Port) }},
	{"protocol", func(c *Client) starlark.Value { return starlark.MakeInt(c.Protocol) }},
	{"legacy", func(c *Client) starlark.Value { return starlark.Bool(c.Legacy) }},
	{"legacy_ping_version", func(c *Client) starlark.Value { return starlark.String(c.LegacyPingVersion) }},
	{"ip", func(c *Client) starlark.Value { return starlark.String(c.IP) }},
	{"listener", func(c *Client) starlark.Value { return starlark.String(c.Listener) }},
}

// fieldNames are the names of clientFields.
var fieldNames = func() []string {
	names := make([]string, len(clientFields))
	for i, f := range clientFields {
		names[i] = f.name
	}
	return names
}()

// clientValue is the client variable of conditions: a read-only view of a
// Client, which records the fields read, even by functions it is passed to, to
// tell which values a condition failed with.
type clientValue struct {
	c    *Client
	read uint32 // A bit for each of clientFields.
}

var _ starlark.HasAttrs = (*clientValue)(nil)

func (v *clientValue) String() string {
	var b strings.Builder
	b.WriteString("client(")
	for i, f := range clientFields {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(f.name + " = " + f.value(v.c).String())
	}
	b.WriteString(")")
	return b.String()
}

func (v *clientValue) Type() string         { return "client" }
func (v *clientValue) Freeze()              {}
func (v *clientValue) Truth() starlark.Bool { return true }

func (v *clientValue) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable: %s", v.Type()) }

func (v *clientValue) Attr(name string) (starlark.Value, error) {
	i := slices.Index(fieldNames, name)
	if i < 0 {
		return nil, nil // Starlark reports it, with a spelling hint.
	}
	v.read |= 1 << i
	return clientFields[i].value(v.c), nil
}

// AttrNames returns a copy, which callers may sort, as dir does.
func (v *clientValue) AttrNames() []string { return slices.Clone(fieldNames) }

// fields returns the fields read, in the order of clientFields.
func (v *clientValue) fields() []Field {
	var fields []Field
	for i, f := range clientFields {
		if v.read&(1<<i) != 0 {
			fields = append(fields, Field{Name: f.name, Value: goValue(f.value(v.c))})
		}
	}
	return fields
}

// goValue returns v, the value of a field, as a Go value, for Field.
func goValue(v starlark.Value) any {
	switch v := v.(type) {
	case starlark.String:
		return string(v)
	case starlark.Bool:
		return bool(v)
	case starlark.Int:
		n, _ := v.Int64() // Ports and protocol versions fit.
		return int(n)
	}
	panic("condition: unsupported value") // See Field.
}

// starlarkValue returns value, the value of a field, as a Starlark value.
func starlarkValue(value any) starlark.Value {
	switch value := value.(type) {
	case string:
		return starlark.String(value)
	case int:
		return starlark.MakeInt(value)
	case bool:
		return starlark.Bool(value)
	}
	panic("condition: unsupported value") // See Field.
}
