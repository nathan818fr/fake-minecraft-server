package script

import (
	"go.starlark.net/starlark"
)

// The requests of Client.Request.
const (
	RequestPing = "ping"
	RequestJoin = "join"
)

// Client is what scripts know about a client, as the fields of their client
// variable, named like the fields of logs (see clientKind). Unknown values are
// empty or zero, and -1 for Protocol.
type Client struct {
	// Request is RequestPing for status requests, and RequestJoin for logins
	// and transfers.
	Request     string
	Transferred bool
	// Host is the host that the client connected to, as it sent it, and
	// HostExtra what modded clients and proxies add after it.
	Host      string
	HostExtra string
	Port      int
	Protocol  int
	Legacy    bool
	// LegacyPingVersion is the format of legacy pings, such as "1.6".
	LegacyPingVersion string
	// IP is the address of the client, empty for Unix socket clients.
	IP       string
	Listener string
	// Username and UUID are what the client sent when joining, UUID formatted
	// like "069a79f4-44e9-4726-a5be-fca90e38aaf5" (1.19.3+), or empty.
	Username string
	UUID     string
}

// Field is a field of the client variable of scripts and its value: a string,
// an int or a bool.
type Field struct {
	Name  string
	Value any
}

// String returns f as in scripts, such as `client.host "mc.example.com"`.
func (f Field) String() string {
	return "client." + f.Name + " " + starlarkValue(f.Value).String()
}

// clientValue is the client variable of scripts: a view of a Client, which
// records the fields read, even by the functions it is passed to, to tell
// which values a script failed with (see clientFields).
type clientValue = object[*Client]

// clientKind is the kind of clientValue: its fields, named like the fields of
// logs, are strings, ints or bools (see Field).
var clientKind = newObjectKind("client", nil, []objectAttr[*Client]{
	{name: "request", field: func(c *Client) starlark.Value { return starlark.String(c.Request) }},
	{name: "transferred", field: func(c *Client) starlark.Value { return starlark.Bool(c.Transferred) }},
	{name: "host", field: func(c *Client) starlark.Value { return starlark.String(c.Host) }},
	{name: "host_extra", field: func(c *Client) starlark.Value { return starlark.String(c.HostExtra) }},
	{name: "port", field: func(c *Client) starlark.Value { return starlark.MakeInt(c.Port) }},
	{name: "protocol", field: func(c *Client) starlark.Value { return starlark.MakeInt(c.Protocol) }},
	{name: "legacy", field: func(c *Client) starlark.Value { return starlark.Bool(c.Legacy) }},
	{name: "legacy_ping_version", field: func(c *Client) starlark.Value { return starlark.String(c.LegacyPingVersion) }},
	{name: "ip", field: func(c *Client) starlark.Value { return starlark.String(c.IP) }},
	{name: "listener", field: func(c *Client) starlark.Value { return starlark.String(c.Listener) }},
	{name: "username", field: func(c *Client) starlark.Value { return starlark.String(c.Username) }},
	{name: "uuid", field: func(c *Client) starlark.Value { return starlark.String(c.UUID) }},
})

// clientFields returns the fields of v that scripts read, in the order of
// clientKind.
func clientFields(v *clientValue) []Field {
	var fields []Field
	for i, a := range v.kind.attrs {
		if v.read&(1<<i) != 0 {
			fields = append(fields, Field{Name: a.name, Value: goValue(a.field(v.state))})
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
	panic("script: unsupported value") // See Field.
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
	panic("script: unsupported value") // See Field.
}
