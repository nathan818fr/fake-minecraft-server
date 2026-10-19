package script

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"regexp"
	"sync"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/legacyproto"
)

// fmsModule returns the fms module, which holds the native functions of
// scripts, all pure, so that they work anywhere (but fms.read_favicon, which
// only works while the module loads), and under a name of their own, so that
// they never clash with the ones of scripts.
func fmsModule() *starlarkstruct.Module {
	members := starlark.StringDict{
		"match_host":      starlark.NewBuiltin("fms.match_host", matchHostBuiltin),
		"in_network":      starlark.NewBuiltin("fms.in_network", inNetworkBuiltin),
		"matches":         starlark.NewBuiltin("fms.matches", matchesBuiltin),
		"bucket":          starlark.NewBuiltin("fms.bucket", bucketBuiltin),
		"merge":           starlark.NewBuiltin("fms.merge", mergeBuiltin),
		"strip_invisible": newTextBuiltin("fms.strip_invisible", stripInvisible),
		"sanitize":        newTextBuiltin("fms.sanitize", sanitize),
		"to_legacy":       newComponentBuiltin("fms.to_legacy", legacyproto.TextFromValue),
		"plain_text":      newComponentBuiltin("fms.plain_text", plainText),
		"from_legacy":     starlark.NewBuiltin("fms.from_legacy", fromLegacyBuiltin),
		"format_number":   starlark.NewBuiltin("fms.format_number", formatNumberBuiltin),
		"split_duration":  starlark.NewBuiltin("fms.split_duration", splitDurationBuiltin),
		"read_favicon":    starlark.NewBuiltin("fms.read_favicon", readFaviconBuiltin),
		"from_timestamp":  starlark.NewBuiltin("fms.from_timestamp", fromTimestampBuiltin),
	}
	for _, name := range timeFunctions {
		members[name] = newTimeBuiltin(name)
	}
	return &starlarkstruct.Module{Name: "fms", Members: members}
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
	"read_favicon": {outsideModule: errReadFaviconAfterLoad},
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

// bucketBuiltin implements fms.bucket(value, n), the bucket of value among n,
// from 0 to n - 1: the same for a value across requests, restarts and
// versions, and spread evenly. SHA-256, not hash, which is Java's
// String.hashCode: with n = 2, the bucket of hash("a:" + s) follows from the
// one of hash(s), so that two splits named by a prefix would pick the same
// clients.
func bucketBuiltin(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value string
	var n int
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &value, &n); err != nil {
		return nil, err
	}
	if n < 1 {
		return nil, fmt.Errorf("%s: n must be at least 1, got %d", b.Name(), n)
	}
	sum := sha256.Sum256([]byte(value))
	return starlark.MakeUint64(binary.BigEndian.Uint64(sum[:8]) % uint64(n)), nil
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
