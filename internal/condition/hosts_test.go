package condition

import "testing"

func TestParseHostPattern(t *testing.T) {
	for text, want := range map[string]string{
		"Play.Example.com.": "play.example.com",
		"*.Example.com":     "*.example.com",
		".Example.com.":     ".example.com",
		"203.0.113.5":       "203.0.113.5",
		"2001:db8::1":       "2001:db8::1",
	} {
		if got, err := parseHostPattern(text); err != nil || got != want {
			t.Errorf("parseHostPattern(%q) = %q, %v, want %q", text, got, err, want)
		}
	}
	for _, text := range []string{"", ".", "..", "..com", ".*.com", "*", "*.", "*..com", "a.*.com", "*a.com", "a b", "a\x00", "a..com", "a.com..", "0..", "*.a..com"} {
		if got, err := parseHostPattern(text); err == nil {
			t.Errorf("parseHostPattern(%q) = %q, want an error", text, got)
		}
	}
}

func TestHosts(t *testing.T) {
	c, err := Hosts([]string{"play.a.com", "*.b.com", ".c.com"})
	if err != nil {
		t.Fatalf("Hosts() error: %v", err)
	}
	for host, want := range map[string]bool{
		"play.a.com":         true,
		"PLAY.A.com.":        true,
		"x.play.a.com":       false,
		"x.b.com":            true,
		"y.x.b.com":          true,
		"b.com":              false,
		"c.com":              true,
		"x.c.com":            true,
		"xc.com":             false,
		"play.a.com.example": false,
		"":                   false,
	} {
		if got := c.Match(&Client{Host: host}); got != want {
			t.Errorf("Match(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestHostsErrors(t *testing.T) {
	for _, patterns := range [][]string{nil, {}, {"a.com", "a.*.com"}} {
		if _, err := Hosts(patterns); err == nil {
			t.Errorf("Hosts(%q) succeeded, want an error", patterns)
		}
	}
}
