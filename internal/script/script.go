package script

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// dialect is the Starlark dialect of scripts: the language of the
// specification, plus set(), while loops and recursion, all bounded by step
// limits. Reassigning globals stays an error, as it is usually a mistake.
var dialect = &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, Recursion: true}

// positioned returns err with the positions of its errors moved up by lines,
// and without file name, as "line:column: message".
func positioned(err error, lines int32) error {
	if err == nil {
		return nil
	}
	format := func(pos syntax.Position, msg string) string {
		// Some messages hold positions too, with the empty file name first.
		msg = strings.ReplaceAll(msg, " at :", " at ")
		return fmt.Sprintf("%d:%d: %s", pos.Line-lines, pos.Col, msg)
	}
	var syntaxErr syntax.Error
	var resolveErrs resolve.ErrorList
	switch {
	case errors.As(err, &syntaxErr):
		return errors.New(format(syntaxErr.Pos, syntaxErr.Msg))
	case errors.As(err, &resolveErrs):
		msgs := make([]string, len(resolveErrs))
		for i, e := range resolveErrs {
			msgs[i] = format(e.Pos, e.Msg)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return err
}

// checkLiterals checks the string literals given to the functions of the fms
// module in n. It compiles the regular expressions of fms.matches for good (see
// compilePattern). The positions of n are lineOffset lines down.
func checkLiterals(n syntax.Node, lineOffset int32) error {
	var err error
	syntax.Walk(n, func(n syntax.Node) bool {
		if err != nil {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		fn := fmsFunction(call.Fn)
		if fn == "" || len(call.Args) < 2 {
			return true
		}
		for _, arg := range call.Args[1:] {
			lit, ok := arg.(*syntax.Literal)
			if !ok || lit.Token != syntax.STRING {
				continue
			}
			text := lit.Value.(string)
			var argErr error
			switch fn {
			case "fms.match_host":
				_, argErr = parseHostPattern(text)
			case "fms.in_network":
				_, argErr = ParseNetwork(text)
			case "fms.matches":
				_, argErr = compilePattern(text, true)
			}
			if argErr != nil {
				pos := lit.TokenPos
				err = fmt.Errorf("%d:%d: %s: %w", pos.Line-lineOffset, pos.Col, fn, argErr)
				return false
			}
		}
		return true
	})
	return err
}

// fmsFunction returns the name of the function of the fms module that fn
// names, such as "fms.matches", or "" if it names none.
func fmsFunction(fn syntax.Expr) string {
	dot, ok := fn.(*syntax.DotExpr)
	if !ok {
		return ""
	}
	if x, ok := dot.X.(*syntax.Ident); !ok || x.Name != "fms" {
		return ""
	}
	return "fms." + dot.Name.Name
}

// fmsModule returns the fms module, which holds the functions of conditions, so
// that their names never clash with the ones of scripts.
func fmsModule() *starlarkstruct.Module {
	return &starlarkstruct.Module{Name: "fms", Members: starlark.StringDict{
		"match_host": starlark.NewBuiltin("fms.match_host", matchHostBuiltin),
		"in_network": starlark.NewBuiltin("fms.in_network", inNetworkBuiltin),
		"matches":    starlark.NewBuiltin("fms.matches", matchesBuiltin),
	}}
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
