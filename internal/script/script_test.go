package script

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

var (
	modernJoin = Client{
		Request: RequestJoin, Host: "Play.A.com.", HostExtra: "FML3\x00", Port: 25566, Protocol: 767,
		IP: "10.1.2.3", Listener: "[::]:25566",
	}
	legacyPing = Client{Request: RequestPing, Protocol: -1, Legacy: true, LegacyPingVersion: "1.4-1.5", IP: "2001:db8::1", Listener: "[::]:25565"}
)

func TestCompile(t *testing.T) {
	tests := []struct {
		src    string
		client Client
		want   bool
	}{
		{`client.request == "join" and not client.transferred`, modernJoin, true},
		{`client.request == "ping"`, legacyPing, true},
		// Values are as clients sent them.
		{`client.host == "Play.A.com."`, modernJoin, true},
		{`client.host == "play.a.com"`, modernJoin, false},
		{`"FML" in client.host_extra`, modernJoin, true},
		{`client.port == 25566 and client.protocol >= 767`, modernJoin, true},
		{`client.protocol >= 0 and client.protocol < 47`, legacyPing, false},
		{`client.legacy and client.legacy_ping_version == "1.4-1.5"`, legacyPing, true},
		{`client.legacy_ping_version == ""`, modernJoin, true},
		{`client.listener == "[::]:25566"`, modernJoin, true},
		{"(\n  fms.match_host(client.host, \".a.com\")  # Any case.\n  and client.protocol >= 767\n)", modernJoin, true},
		{"fms.match_host(client.host, \".a.com\") and\n  client.protocol >= 767", modernJoin, true},
		{`fms.match_host(client.host, "*.b.com", "a.com")`, modernJoin, false},
		{`fms.match_host(client.host, ".a.com")`, legacyPing, false},
		{`fms.in_network(client.ip, "10.0.0.0/8")`, modernJoin, true},
		{`fms.in_network(client.ip, "192.168.0.0/16", "10.1.2.3")`, modernJoin, true},
		{`fms.in_network(client.ip, "2001:db8::/32")`, legacyPing, true},
		{`fms.in_network(client.ip, "::ffff:10.0.0.0/104")`, modernJoin, true},
		{`fms.in_network(client.ip, "10.0.0.0/8")`, Client{Protocol: -1}, false},
		{`fms.matches(client.host_extra, "^FML[23]\x00")`, modernJoin, true},
		{`fms.matches(client.host, "(?i)^play\\.a\\.com\\.?$")`, modernJoin, true},
		{`fms.matches(client.host, "^" + "play")`, modernJoin, false},
		// The dialect has sets.
		{`client.ip in set(["10.1.2.3", "10.1.2.4"])`, modernJoin, true},
		{`"host_extra" in dir(client) and type(client) == "client"`, modernJoin, true},
	}
	for _, tc := range tests {
		c, err := NewScope(nil, nil).CompileCondition(tc.src)
		if err != nil {
			t.Errorf("CompileCondition(%q) error: %v", tc.src, err)
			continue
		}
		if got, err := c.Match(&Request{Client: &tc.client}, nil); err != nil || got != tc.want {
			t.Errorf("%q: Match() = %v, %v, want %v", tc.src, got, err, tc.want)
		}
	}
}

// Mistakes are reported when compiling, with their position in the
// expression, or the first sample client they fail with and the values of the
// fields that they read.
func TestCompileErrors(t *testing.T) {
	tests := []struct {
		src, want string
	}{
		{`client.protocol <`, "1:18: got end of file, want primary expression"},
		{"client.protocol >= 0 and\n  client.protcol < 47", "2:10: client has no .protcol field or method (did you mean .protocol?)"},
		{"client.protocol >= 0 or\n  client.protcol < 47", "2:10: client has no .protcol field or method (did you mean .protocol?)"},
		{`client.port > 0 or ctx.upstreem("lobby")`, "1:24: ctx has no .upstreem field or method (did you mean .upstream?)"},
		{`protocol < 47`, "1:1: undefined: protocol"},
		{`True) or (False`, "1:5: unexpected ')'"},
		{`x = 1`, "1:4: got '=' after expression, want EOF"},
		{"client.protocol >= 0 and\n  client.protocol <", "2:20: incomplete expression"},
		{`client.host`, `fails for a modern ping (client.host "mc.example.com"): must return True or False, got string`},
		{`client.protocol > "47"`, "fails for a modern ping (client.protocol 773): int > string not implemented"},
		{`client.host.split(".")[1] == "example"`, `fails for a ping without host, from a bad client or library (client.host ""): list index 1 out of range [-1:0]`},
		{`not client.legacy and client.host.split(".")[1] == "example"`, `fails for a ping without host, from a bad client or library (client.host "", client.legacy False): list index 1 out of range [-1:0]`},
		{`int(client.ip.split(".")[0]) == 10`, `fails for a client on IPv6 (client.ip "2001:db8::1"): int: invalid literal with base 10: 2001:db8::1`},
		{`client.port != 25565 and 25565 // client.port > 1`, "fails for a ping to port 0 (client.port 0): floored division by zero"},
		{`client.legacy and client.host[0] == "m"`, `fails for a legacy ping from Beta 1.8 to 1.3 (client.host "", client.legacy True): index 0 out of range: empty string`},
		{`len([x for x in range(100000)]) > 0`, "fails for a modern ping: Starlark computation cancelled: too many steps"},
		{`fms.match_host(client.host, "a.*.com")`, `1:29: fms.match_host: invalid host "a.*.com": a wildcard is only allowed as "*." followed by a domain`},
		{`fms.in_network(client.ip, "10.0.0.0/33")`, `1:27: fms.in_network: "10.0.0.0/33" is not an IP address or CIDR prefix`},
		{`fms.in_network(client.ip, "fe80::1%eth0")`, `1:27: fms.in_network: "fe80::1%eth0" has an IPv6 zone, which is not supported`},
		{"client.protocol > 0 and\n  fms.matches(client.host, \"(\")", "2:28: fms.matches: error parsing regexp: missing closing ): `(`"},
		{`fms.match_host(client.host)`, `fails for a modern ping (client.host "mc.example.com"): fms.match_host: got 1 arguments, want at least 2`},
		{`fms.in_network(client.ip, 10)`, `fails for a modern ping (client.ip "203.0.113.5"): fms.in_network: argument 2 must be a string, got int`},
		{`client.port > 0 or fms.matche(client.host, "a")`, "1:24: module has no .matche field or method (did you mean .matches?)"},
	}
	for _, tc := range tests {
		if _, err := NewScope(nil, nil).CompileCondition(tc.src); err == nil || err.Error() != tc.want {
			t.Errorf("CompileCondition(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// Conditions that fail when clients connect report why, such as patterns
// computed from what clients sent, and the values of the fields that they
// read.
func TestMatchErrors(t *testing.T) {
	c, err := NewScope(nil, nil).CompileCondition(`fms.matches(client.host, client.host_extra or "^$")`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	_, err = c.Match(&Request{Client: &Client{Host: "a.com", HostExtra: "("}}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "fms.matches: error parsing regexp") {
		t.Errorf("Match() error = %v, want an invalid regexp", err)
	}
	var condErr *Error
	want := []Field{{"host", "a.com"}, {"host_extra", "("}}
	if !errors.As(err, &condErr) || !slices.Equal(condErr.Fields, want) {
		t.Errorf("Match() error = %#v, want the fields %v", err, want)
	}
}

// The fields read are recorded wherever they are read, such as by a function
// that the client is passed to, and only those.
func TestMatchErrorsFieldsRead(t *testing.T) {
	c, err := NewScope(nil, nil).CompileCondition(`[f(client) for f in [lambda c: c.port >= 0]][0] and fms.matches(client.host, client.host_extra or "^$")`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	_, err = c.Match(&Request{Client: &Client{Host: "a.com", HostExtra: "(", Port: 1}}, nil)
	var condErr *Error
	want := []Field{{"host", "a.com"}, {"host_extra", "("}, {"port", 1}}
	if !errors.As(err, &condErr) || !slices.Equal(condErr.Fields, want) {
		t.Errorf("Match() error = %#v, want the fields %v", err, want)
	}

	// Writing the client reads all its fields.
	c, err = NewScope(nil, nil).CompileCondition(`str(client) != "" and fms.matches(client.host, client.host_extra or "^$")`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	_, err = c.Match(&Request{Client: &Client{HostExtra: "("}}, nil)
	if !errors.As(err, &condErr) || len(condErr.Fields) != len(clientKind.attrs) {
		t.Errorf("Match() error = %#v, want all the fields", err)
	}
}

// print returns None, and what it prints goes nowhere without a printer.
func TestPrint(t *testing.T) {
	c, err := NewScope(nil, nil).CompileCondition(`print(client) == None`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	if got, err := c.Match(&Request{Client: &modernJoin}, nil); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

func BenchmarkMatch(b *testing.B) {
	c, err := NewScope(nil, nil).CompileCondition(`fms.match_host(client.host, ".a.com") and client.protocol >= 767 and not fms.in_network(client.ip, "192.168.0.0/16")`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Match(&Request{Client: &modernJoin}, nil); err != nil {
			b.Fatal(err)
		}
	}
}
