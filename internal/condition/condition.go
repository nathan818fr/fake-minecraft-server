// Package condition implements the conditions of rules, which tell the
// clients that rules apply to.
package condition

// Condition is what a rule requires of clients: host patterns (see Hosts).
type Condition interface {
	// Match reports whether client matches the condition.
	Match(client *Client) bool
}

// Client is what conditions know about a client.
type Client struct {
	// Host is the host that the client connected to, as it sent it.
	Host string
}
