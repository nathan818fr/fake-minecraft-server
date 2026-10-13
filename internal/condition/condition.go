// Package condition implements the conditions of rules, which tell the
// clients that rules apply to.
package condition

import (
	"strconv"

	"go.starlark.net/starlark"
)

// Condition is a condition of a rule.
type Condition interface {
	// Match reports whether client matches the condition, or why it failed,
	// as an *Error. A condition that fails does not apply.
	Match(client *Client) (bool, error)
}

// Error is the error of a condition that failed with a client. Vars are the
// variables of the client that the condition uses, to tell which values it
// failed with.
type Error struct {
	Err  error
	Vars []Var
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// Var is a variable of conditions and its value: a string, an int or a bool.
type Var struct {
	Name  string
	Value any
}

// String returns v as in conditions, such as `host "mc.example.com"`.
func (v Var) String() string {
	switch value := v.Value.(type) {
	case string:
		return v.Name + " " + starlark.String(value).String()
	case int:
		return v.Name + " " + strconv.Itoa(value)
	case bool:
		return v.Name + " " + starlark.Bool(value).String()
	}
	panic("condition: unsupported value") // See Var.
}

// The events of Client.Event.
const (
	EventPing = "ping"
	EventJoin = "join"
)

// Client is what conditions know about a client, as variables named like the
// fields of logs. Unknown values are empty or zero, and -1 for Protocol.
type Client struct {
	// Event is EventPing for status requests, and EventJoin for logins and
	// transfers.
	Event       string
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
}
