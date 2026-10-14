// Package script implements what users write in Starlark, and the conditions
// of rules, which tell the clients that rules apply to: host patterns, and
// expressions (see Compile), which see the client and the functions of the fms
// module.
package script

// Condition is what a rule requires of clients: host patterns (see Hosts) or
// an expression (see Compile).
type Condition interface {
	// Match reports whether the client of req matches the condition, or why
	// it failed, as an *Error. A condition that fails does not apply.
	Match(req *Request) (bool, error)
}

// Request is a request of a client: what scripts know about it, and where
// what they print during it goes.
type Request struct {
	Client *Client
	// Print receives what scripts print, or is nil to drop it.
	Print func(msg string)
}

// Error is the error of a condition that failed with a client. Fields are the
// fields of the client that the condition read, to tell which values it failed
// with.
type Error struct {
	Err    error
	Fields []Field
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// Field is a field of the client variable of conditions and its value: a
// string, an int or a bool.
type Field struct {
	Name  string
	Value any
}

// String returns f as in conditions, such as `client.host "mc.example.com"`.
func (f Field) String() string {
	return "client." + f.Name + " " + starlarkValue(f.Value).String()
}

// The requests of Client.Request.
const (
	RequestPing = "ping"
	RequestJoin = "join"
)

// Client is what conditions know about a client, as the fields of their client
// variable, named like the fields of logs (see clientValue). Unknown values are
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
}
