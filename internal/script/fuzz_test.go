package script

import "testing"

func FuzzCompile(f *testing.F) {
	for _, src := range []string{`client.protocol < 47`, `fms.match_host(client.host, ".a.com") and fms.in_network(client.ip, "10.0.0.0/8")`, `fms.matches(client.host_extra, "^FML")`, `[x for x in range(10)]`, "(\n client.host\n)"} {
		f.Add(src, "Play.A.com.", "FML3\x00", "10.1.2.3")
	}
	f.Fuzz(func(t *testing.T, src, host, extra, ip string) {
		c, err := NewScope(nil, nil).CompileCondition(src)
		if err != nil {
			return
		}
		_, _ = c.Match(&Request{Client: &Client{Host: host, HostExtra: extra, IP: ip, Protocol: 773}}, nil)
	})
}

func FuzzHosts(f *testing.F) {
	f.Add(".a.com", "x.a.com")
	f.Add("*.B.com.", "b.com")
	f.Fuzz(func(t *testing.T, text, host string) {
		pattern, err := parseHostPattern(text)
		if err != nil {
			return
		}
		if again, err := parseHostPattern(pattern); err != nil || again != pattern {
			t.Errorf("parseHostPattern(%q) = %q, then %q, %v", text, pattern, again, err)
		}
		_ = matchHost([]string{pattern}, host)
	})
}
