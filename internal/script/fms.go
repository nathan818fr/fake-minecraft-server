package script

import (
	"fmt"
	"net/netip"
	"regexp"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// fmsModule returns the fms module, which holds the native functions of
// scripts, all pure, so that they work anywhere, and under a name of their own,
// so that they never clash with the ones of scripts.
func fmsModule() *starlarkstruct.Module {
	return &starlarkstruct.Module{Name: "fms", Members: starlark.StringDict{
		"match_host":      starlark.NewBuiltin("fms.match_host", matchHostBuiltin),
		"in_network":      starlark.NewBuiltin("fms.in_network", inNetworkBuiltin),
		"matches":         starlark.NewBuiltin("fms.matches", matchesBuiltin),
		"merge":           starlark.NewBuiltin("fms.merge", mergeBuiltin),
		"strip_invisible": newTextBuiltin("fms.strip_invisible", stripInvisible),
		"sanitize":        newTextBuiltin("fms.sanitize", sanitize),
	}}
}

// newTextBuiltin returns the function name, which returns the text it is given
// transformed by transform.
func newTextBuiltin(name string, transform func(string) string) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var text string
		if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &text); err != nil {
			return nil, err
		}
		return starlark.String(transform(text)), nil
	})
}

// fmsCallChecks are the checks of the calls to the functions of fms (see
// callCheck).
var fmsCallChecks = map[string]callCheck{
	"match_host": {arg: argsAfterFirst, literal: func(_ *Scope, text string) error {
		_, err := parseHostPattern(text)
		return err
	}},
	"in_network": {arg: argsAfterFirst, literal: func(_ *Scope, text string) error {
		_, err := ParseNetwork(text)
		return err
	}},
	// Compiled for good (see compilePattern).
	"matches": {arg: 1, literal: func(_ *Scope, text string) error {
		_, err := compilePattern(text, true)
		return err
	}},
}

// matchHostBuiltin implements fms.match_host(host, *patterns), which matches
// host like the condition of Hosts.
func matchHostBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	host, texts, err := unpackStrings(b, args, kwargs)
	if err != nil {
		return nil, err
	}
	patterns := make([]string, len(texts))
	for i, text := range texts {
		if patterns[i], err = parseHostPattern(text); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
	}
	return starlark.Bool(matchHost(patterns, host)), nil
}

// inNetworkBuiltin implements fms.in_network(ip, *networks), which reports
// whether ip is in one of the networks (see ParseNetwork). No address is in no
// network.
func inNetworkBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	ip, texts, err := unpackStrings(b, args, kwargs)
	if err != nil {
		return nil, err
	}
	var addr netip.Addr
	if ip != "" {
		if addr, err = netip.ParseAddr(ip); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		addr = addr.WithZone("").Unmap()
	}
	found := false
	for _, text := range texts {
		prefix, err := ParseNetwork(text)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
		found = found || addr.IsValid() && prefix.Contains(addr)
	}
	return starlark.Bool(found), nil
}

// matchesBuiltin implements fms.matches(s, pattern), which reports whether the
// regular expression pattern matches s (see compilePattern).
func matchesBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s, pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &s, &pattern); err != nil {
		return nil, err
	}
	re, err := compilePattern(pattern, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	return starlark.Bool(re.MatchString(s)), nil
}

// maxCompiledPatterns bounds the patterns that are not literals in
// compiledPatterns, which start over when full, so that patterns made of
// client values cannot grow it.
const maxCompiledPatterns = 256

// compiledPatterns holds the regular expressions of fms.matches, so that each
// pattern is compiled once: the literal ones, compiled when scripts are
// checked and kept for good, and the others, such as constants of the module
// or patterns built by its functions, which would otherwise be compiled at
// each call, about ten times the cost of a condition.
var compiledPatterns struct {
	mu       sync.RWMutex
	literals map[string]*regexp.Regexp
	others   map[string]*regexp.Regexp
}

// compilePattern returns the regular expression of pattern, a literal of a
// script if literal, from compiledPatterns if compiled before.
func compilePattern(pattern string, literal bool) (*regexp.Regexp, error) {
	c := &compiledPatterns
	c.mu.RLock()
	re, ok := c.literals[pattern]
	if !ok && !literal {
		re, ok = c.others[pattern]
	}
	c.mu.RUnlock()
	if ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if literal {
		if c.literals == nil {
			c.literals = make(map[string]*regexp.Regexp)
		}
		c.literals[pattern] = re
		return re, nil
	}
	if c.others == nil || len(c.others) >= maxCompiledPatterns {
		c.others = make(map[string]*regexp.Regexp, maxCompiledPatterns)
	}
	c.others[pattern] = re
	return re, nil
}

// unpackStrings unpacks the arguments of a function that takes a string, then
// at least one other.
func unpackStrings(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (string, []string, error) {
	if len(kwargs) != 0 {
		return "", nil, fmt.Errorf("%s: unexpected keyword arguments", b.Name())
	}
	if len(args) < 2 {
		return "", nil, fmt.Errorf("%s: got %d arguments, want at least 2", b.Name(), len(args))
	}
	values := make([]string, len(args))
	for i, arg := range args {
		s, ok := starlark.AsString(arg)
		if !ok {
			return "", nil, fmt.Errorf("%s: argument %d must be a string, got %s", b.Name(), i+1, arg.Type())
		}
		values[i] = s
	}
	return values[0], values[1:], nil
}
