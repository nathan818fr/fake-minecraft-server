package script

import (
	"errors"
	"fmt"
	"strings"
)

// hosts is the condition of Hosts: patterns made by parseHostPattern.
type hosts []string

// Hosts returns the condition on the host that clients connected to, which
// must match one of patterns (see parseHostPattern).
func Hosts(patterns []string) (Condition, error) {
	if len(patterns) == 0 {
		return nil, errors.New("must list at least one host")
	}
	c := make(hosts, len(patterns))
	for i, text := range patterns {
		var err error
		if c[i], err = parseHostPattern(text); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c hosts) Match(req *Request) (bool, error) {
	return matchHost(c, req.Client.Host), nil
}

// parseHostPattern returns the host pattern made of text, in lowercase and
// without a trailing dot. Like the server names of nginx, it is a host name or
// an address, "*." and a domain, which matches the subdomains of the domain at
// any depth, or "." and a domain, which matches the domain too.
func parseHostPattern(text string) (string, error) {
	if strings.ContainsFunc(text, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", fmt.Errorf("invalid host %q: must not contain spaces or control characters", text)
	}
	pattern := strings.TrimSuffix(strings.ToLower(text), ".")
	prefix := ""
	if strings.HasPrefix(pattern, "*.") {
		prefix = "*."
	} else if strings.HasPrefix(pattern, ".") {
		prefix = "."
	}
	domain := pattern[len(prefix):]
	switch {
	case pattern == "":
		return "", errors.New("empty host")
	case strings.Contains(domain, "*"):
		return "", fmt.Errorf("invalid host %q: a wildcard is only allowed as \"*.\" followed by a domain", text)
	case prefix != "" && (domain == "" || strings.HasPrefix(domain, ".")):
		return "", fmt.Errorf("invalid host %q: %q must be followed by a domain", text, prefix)
	case strings.Contains(domain, "..") || strings.HasSuffix(domain, "."):
		return "", fmt.Errorf("invalid host %q: empty label", text)
	}
	return pattern, nil
}

// matchHost reports whether host matches one of patterns (see
// parseHostPattern), in any case and with or without a trailing dot. No host
// matches no pattern.
func matchHost(patterns []string, host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	for _, pattern := range patterns {
		var matches bool
		switch {
		case strings.HasPrefix(pattern, "*"):
			suffix := pattern[1:]
			matches = len(host) > len(suffix) && strings.HasSuffix(host, suffix)
		case strings.HasPrefix(pattern, "."):
			matches = host == pattern[1:] || strings.HasSuffix(host, pattern)
		default:
			matches = host == pattern
		}
		if matches {
			return true
		}
	}
	return false
}
