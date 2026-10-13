package condition

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

var (
	modernJoin = Client{
		Event: EventJoin, Host: "Play.A.com.", HostExtra: "FML3\x00", Port: 25566, Protocol: 767,
		IP: "10.1.2.3", Listener: "[::]:25566",
	}
	legacyPing = Client{Event: EventPing, Protocol: -1, Legacy: true, LegacyPingVersion: "1.4-1.5", IP: "2001:db8::1", Listener: "[::]:25565"}
)

func TestCompile(t *testing.T) {
	tests := []struct {
		src    string
		client Client
		want   bool
	}{
		{`event == "join" and not transferred`, modernJoin, true},
		{`event == "ping"`, legacyPing, true},
		// Values are as clients sent them.
		{`host == "Play.A.com."`, modernJoin, true},
		{`host == "play.a.com"`, modernJoin, false},
		{`"FML" in host_extra`, modernJoin, true},
		{`port == 25566 and protocol >= 767`, modernJoin, true},
		{`protocol >= 0 and protocol < 47`, legacyPing, false},
		{`legacy and legacy_ping_version == "1.4-1.5"`, legacyPing, true},
		{`legacy_ping_version == ""`, modernJoin, true},
		{`listener == "[::]:25566"`, modernJoin, true},
		{"(\n  match_host(host, \".a.com\")  # Any case.\n  and protocol >= 767\n)", modernJoin, true},
		{"match_host(host, \".a.com\") and\n  protocol >= 767", modernJoin, true},
		{`match_host(host, "*.b.com", "a.com")`, modernJoin, false},
		{`match_host(host, ".a.com")`, legacyPing, false},
		{`in_network(ip, "10.0.0.0/8")`, modernJoin, true},
		{`in_network(ip, "192.168.0.0/16", "10.1.2.3")`, modernJoin, true},
		{`in_network(ip, "2001:db8::/32")`, legacyPing, true},
		{`in_network(ip, "::ffff:10.0.0.0/104")`, modernJoin, true},
		{`in_network(ip, "10.0.0.0/8")`, Client{Protocol: -1}, false},
		{`matches(host_extra, "^FML[23]\x00")`, modernJoin, true},
		{`matches(host, "(?i)^play\\.a\\.com\\.?$")`, modernJoin, true},
		{`matches(host, "^" + "play")`, modernJoin, false},
	}
	for _, tc := range tests {
		c, err := Compile(tc.src)
		if err != nil {
			t.Errorf("Compile(%q) error: %v", tc.src, err)
			continue
		}
		if got, err := c.Match(&tc.client); err != nil || got != tc.want {
			t.Errorf("%q: Match() = %v, %v, want %v", tc.src, got, err, tc.want)
		}
	}
}

// Mistakes are reported when compiling, with their position in the
// expression, or the first sample client they fail with and the values of the
// variables that they use.
func TestCompileErrors(t *testing.T) {
	tests := []struct {
		src, want string
	}{
		{`protocol <`, "1:11: got end of file, want primary expression"},
		{"protocol >= 0 and\n  protcol < 47", "2:3: undefined: protcol (did you mean protocol?)"},
		{`True) or (False`, "1:5: unexpected ')'"},
		{`x = 1`, "1:4: got '=' after expression, want EOF"},
		{"protocol >= 0 and\n  protocol <", "2:13: incomplete expression"},
		{`host`, `fails for a modern ping (host "mc.example.com"): must return True or False, got string`},
		{`protocol > "47"`, "fails for a modern ping (protocol 773): int > string not implemented"},
		{`host.split(".")[1] == "example"`, `fails for a ping without host, from a bad client or library (host ""): list index 1 out of range [-1:0]`},
		{`not legacy and host.split(".")[1] == "example"`, `fails for a ping without host, from a bad client or library (host "", legacy False): list index 1 out of range [-1:0]`},
		{`int(ip.split(".")[0]) == 10`, `fails for a client on IPv6 (ip "2001:db8::1"): int: invalid literal with base 10: 2001:db8::1`},
		{`port != 25565 and 25565 // port > 1`, "fails for a ping to port 0 (port 0): floored division by zero"},
		{`legacy and host[0] == "m"`, `fails for a legacy ping from Beta 1.8 to 1.3 (host "", legacy True): index 0 out of range: empty string`},
		{`len([x for x in range(100000)]) > 0`, "fails for a modern ping: Starlark computation cancelled: too many steps"},
		{`match_host(host, "a.*.com")`, `1:18: match_host: invalid host "a.*.com": a wildcard is only allowed as "*." followed by a domain`},
		{`in_network(ip, "10.0.0.0/33")`, `1:16: in_network: "10.0.0.0/33" is not an IP address or CIDR prefix`},
		{`in_network(ip, "fe80::1%eth0")`, `1:16: in_network: "fe80::1%eth0" has an IPv6 zone, which is not supported`},
		{"protocol > 0 and\n  matches(host, \"(\")", "2:17: matches: error parsing regexp: missing closing ): `(`"},
		{`match_host(host)`, `fails for a modern ping (host "mc.example.com"): match_host: got 1 arguments, want at least 2`},
		{`in_network(ip, 10)`, `fails for a modern ping (ip "203.0.113.5"): in_network: argument 2 must be a string, got int`},
	}
	for _, tc := range tests {
		if _, err := Compile(tc.src); err == nil || err.Error() != tc.want {
			t.Errorf("Compile(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// Conditions that fail when clients connect report why, such as patterns
// computed from what clients sent, and the values of the variables that they
// use.
func TestMatchErrors(t *testing.T) {
	c, err := Compile(`matches(host, host_extra or "^$")`)
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	_, err = c.Match(&Client{Host: "a.com", HostExtra: "("})
	if err == nil || !strings.HasPrefix(err.Error(), "matches: error parsing regexp") {
		t.Errorf("Match() error = %v, want an invalid regexp", err)
	}
	var condErr *Error
	want := []Var{{"host", "a.com"}, {"host_extra", "("}}
	if !errors.As(err, &condErr) || !slices.Equal(condErr.Vars, want) {
		t.Errorf("Match() error = %#v, want the variables %v", err, want)
	}
}

// print is ignored.
func TestPrint(t *testing.T) {
	c, err := Compile(`print(host) == None`)
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}
	if got, err := c.Match(&modernJoin); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
}

func BenchmarkMatch(b *testing.B) {
	c, err := Compile(`match_host(host, ".a.com") and protocol >= 767 and not in_network(ip, "192.168.0.0/16")`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Match(&modernJoin); err != nil {
			b.Fatal(err)
		}
	}
}
