package script

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// sampleBase is the most common client, which the other samples change.
var sampleBase = Client{Request: RequestPing, Host: "mc.example.com", Port: 25565, Protocol: 773, IP: "203.0.113.5", Listener: "[::]:25565"}

// sampleClient is a sample client, named in the errors of the scripts that
// fail with it.
type sampleClient struct {
	name   string
	client Client
}

// samples are the clients that scripts run with when compiled, to report most
// mistakes at startup rather than when clients connect: the most common one,
// then ones that differ from it by what other clients, bad clients and
// libraries send. It is a quick check, not a proof: conditions that fail with
// other clients do not apply to them, and hooks that fail answer as without
// them.
var samples = []sampleClient{
	{"a modern ping", sampleBase},
	{"a player joining", sample(join)},
	{"a transferred player", sample(join, func(c *Client) { c.Transferred = true })},
	{"a modded client", sample(func(c *Client) { c.HostExtra = "FML3\x00" })},
	{"a client on IPv6", sample(func(c *Client) { c.IP = "2001:db8::1" })},
	{"a client on a Unix socket", sample(func(c *Client) { c.IP, c.Listener = "", "unix:/run/server.sock" })},
	{"a ping without host, from a bad client or library", sample(func(c *Client) { c.Host = "" })},
	{"a ping to a host without dots", sample(func(c *Client) { c.Host = "localhost" })},
	{"a ping to port 0", sample(func(c *Client) { c.Port = 0 })},
	{"a ping with an unknown protocol", sample(func(c *Client) { c.Protocol = -1 })},
	{"a player joining without username, from a bad client or library", sample(join, func(c *Client) { c.Username, c.UUID = "", "" })},
	{"a player joining with a long username", sample(join, func(c *Client) { c.Username = "Abcdefghijklmnop" })},
	{"a legacy ping from Beta 1.8 to 1.3", legacyPingWithoutHost("beta-1.3")},
	{"a legacy ping from 1.4 or 1.5", legacyPingWithoutHost("1.4-1.5")},
	{"a legacy ping from 1.6", sample(func(c *Client) { c.Protocol, c.Legacy, c.LegacyPingVersion = 78, true, "1.6" })},
	{"a legacy login", sample(join, func(c *Client) { c.Protocol, c.Legacy, c.UUID = 78, true, "" })},
}

// join makes a sample a player joining, who sends a username and a UUID.
func join(c *Client) {
	c.Request = RequestJoin
	c.Username = "Notch"
	c.UUID = "069a79f4-44e9-4726-a5be-fca90e38aaf5"
}

func sample(changes ...func(*Client)) Client {
	c := sampleBase
	for _, change := range changes {
		change(&c)
	}
	return c
}

// legacyPingWithoutHost returns a sample legacy ping in a format that has no
// host, port nor protocol.
func legacyPingWithoutHost(version string) Client {
	return sample(func(c *Client) {
		c.Host = ""
		c.Port = 0
		c.Protocol = -1
		c.Legacy = true
		c.LegacyPingVersion = version
	})
}

// samplesOf returns the samples whose request is request: hooks only run for
// their request.
func samplesOf(request string) []sampleClient {
	var found []sampleClient
	for _, s := range samples {
		if s.client.Request == request {
			found = append(found, s)
		}
	}
	return found
}

// sampleStatus is the status of the upstreams while available, for the samples:
// the fields that clients show.
var sampleStatus = func() *status.Status {
	st, err := status.Parse([]byte(`{"description":{"text":"A server"},"players":{"max":20,"online":3,` +
		`"sample":[{"name":"Notch","id":"069a79f4-44e9-4726-a5be-fca90e38aaf5"}]},"version":{"name":"1.21.10","protocol":773}}`))
	if err != nil {
		panic(err)
	}
	return st
}()

// sampleUpstreams are the states of the upstreams that each sample runs with.
var sampleUpstreams = []struct {
	name  string
	state upstream.State
}{
	{"unavailable", upstream.State{}},
	{"available", upstream.State{Status: sampleStatus, Polled: true, Latency: 12 * time.Millisecond, Age: 3 * time.Second}},
}

// checkSamples runs run with each of clients, with each state of the
// upstreams, and returns the first error, which tells the sample, the state of
// the upstreams if run read one, and the fields of the client that run read,
// if its error is an *Error.
func checkSamples(clients []sampleClient, run func(req *Request) error) error {
	for _, s := range clients {
		for _, upstreams := range sampleUpstreams {
			client := s.client
			readUpstream := false
			req := &Request{Client: &client, Upstream: func(string) upstream.State {
				readUpstream = true
				return upstreams.state
			}}
			if err := run(req); err != nil {
				var fields []Field
				var scriptErr *Error
				if errors.As(err, &scriptErr) {
					fields, err = scriptErr.Fields, scriptErr.Err
				}
				state := ""
				if readUpstream {
					state = ", with the upstreams " + upstreams.name
				}
				return fmt.Errorf("fails for %s%s%s: %w", s.name, state, describe(fields), err)
			}
		}
	}
	return nil
}

// describe returns fields as " (client.name value, ...)", or "" if there are
// none.
func describe(fields []Field) string {
	if len(fields) == 0 {
		return ""
	}
	texts := make([]string, len(fields))
	for i, f := range fields {
		texts[i] = f.String()
	}
	return " (" + strings.Join(texts, ", ") + ")"
}
