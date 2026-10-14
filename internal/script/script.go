package script

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

// dialect is the Starlark dialect of conditions: the language of the
// specification, plus set(), while loops and recursion, all bounded by
// maxSteps. Reassigning globals stays an error, as it is usually a mistake.
var dialect = &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, Recursion: true}

// maxSteps bounds the work of each evaluation, such as comprehensions over
// large ranges. Conditions take a few dozen steps.
const maxSteps = 10_000

// sampleBase is the most common client, which the other samples change.
var sampleBase = Client{Request: RequestPing, Host: "mc.example.com", Port: 25565, Protocol: 773, IP: "203.0.113.5", Listener: "[::]:25565"}

// samples are the clients that conditions run with when compiled, to report
// most mistakes at startup rather than when clients connect: the most common
// one, then ones that differ from it by what other clients, bad clients and
// libraries send. It is a quick check, not a proof: conditions that fail with
// other clients do not apply to them.
var samples = []struct {
	name   string
	client Client
}{
	{"a modern ping", sampleBase},
	{"a player joining", sample(func(c *Client) { c.Request = RequestJoin })},
	{"a transferred player", sample(func(c *Client) { c.Request, c.Transferred = RequestJoin, true })},
	{"a modded client", sample(func(c *Client) { c.HostExtra = "FML3\x00" })},
	{"a client on IPv6", sample(func(c *Client) { c.IP = "2001:db8::1" })},
	{"a client on a Unix socket", sample(func(c *Client) { c.IP, c.Listener = "", "unix:/run/server.sock" })},
	{"a ping without host, from a bad client or library", sample(func(c *Client) { c.Host = "" })},
	{"a ping to a host without dots", sample(func(c *Client) { c.Host = "localhost" })},
	{"a ping to port 0", sample(func(c *Client) { c.Port = 0 })},
	{"a ping with an unknown protocol", sample(func(c *Client) { c.Protocol = -1 })},
	{"a legacy ping from Beta 1.8 to 1.3", legacyPingWithoutHost("beta-1.3")},
	{"a legacy ping from 1.4 or 1.5", legacyPingWithoutHost("1.4-1.5")},
	{"a legacy ping from 1.6", sample(func(c *Client) { c.Protocol, c.Legacy, c.LegacyPingVersion = 78, true, "1.6" })},
	{"a legacy login", sample(func(c *Client) { c.Request, c.Protocol, c.Legacy = RequestJoin, 78, true })},
}

func sample(change func(*Client)) Client {
	c := sampleBase
	change(&c)
	return c
}

// legacyPingWithoutHost returns a sample legacy ping in a format that has no
// host, port nor protocol.
func legacyPingWithoutHost(version string) Client {
	return sample(func(c *Client) {
		c.Host, c.Port, c.Protocol, c.Legacy, c.LegacyPingVersion = "", 0, -1, true, version
	})
}

// expression is a condition written in Starlark.
//
// See https://github.com/bazelbuild/starlark/blob/master/spec.md
type expression struct {
	fn *starlark.Function
}

// Compile returns the condition written in src, a Starlark expression that
// returns True or False, such as `client.protocol < 47`, which can use the
// globals of m, if not nil. It also checks the fields of client and the
// literal arguments of the functions, and runs the condition with sample
// clients.
func Compile(src string, m *Module) (Condition, error) {
	expr, lineOffset, err := parseExpr(dialect, src)
	if err != nil {
		return nil, err
	}
	if err := checkFields(expr, lineOffset); err != nil {
		return nil, err
	}
	if err := checkLiterals(expr, lineOffset); err != nil {
		return nil, err
	}

	// The expression is compiled once, as what a function returns, which runs
	// with the client as argument. Its lines are two lines down.
	wrapper := "def when(client):\n  return (\n" + src + "\n)\n"
	predeclared := starlark.StringDict{"fms": fmsModule()}
	if m != nil {
		maps.Copy(predeclared, m.globals)
	}
	globals, err := starlark.ExecFileOptions(dialect, newThread(nil), "", wrapper, predeclared)
	if err != nil {
		return nil, positioned(err, 2)
	}
	c := &expression{fn: globals["when"].(*starlark.Function)}

	for _, sample := range samples {
		if _, fields, err := c.match(&Request{Client: &sample.client}); err != nil {
			return nil, fmt.Errorf("fails for %s%s: %w", sample.name, describe(fields), err)
		}
	}
	return c, nil
}

// checkedNames are the names of the fields of client and of the functions of
// fms, which checkFields checks.
var checkedNames = map[string][]string{"client": fieldNames, "fms": slices.Collect(maps.Keys(fmsModule().Members))}

// checkFields checks the fields of client and the functions of fms that n
// reads by name, so that a typo is reported at startup, even in a branch that
// no sample client takes. The positions of n are lineOffset lines down.
func checkFields(n syntax.Node, lineOffset int32) error {
	var err error
	syntax.Walk(n, func(n syntax.Node) bool {
		dot, ok := n.(*syntax.DotExpr)
		if err != nil || !ok {
			return err == nil
		}
		x, ok := dot.X.(*syntax.Ident)
		if ok && checkedNames[x.Name] != nil && !slices.Contains(checkedNames[x.Name], dot.Name.Name) {
			// Starlark's own message, with its spelling hint.
			variables := map[string]starlark.Value{"client": &clientValue{c: &sampleBase}, "fms": fmsModule()}
			_, getErr := starlark.EvalOptions(dialect, newThread(nil), "", x.Name+"."+dot.Name.Name, starlark.StringDict{x.Name: variables[x.Name]})
			var evalErr *starlark.EvalError
			msg := getErr.Error()
			if errors.As(getErr, &evalErr) {
				msg = evalErr.Msg
			}
			pos := dot.Name.NamePos
			err = fmt.Errorf("%d:%d: %s", pos.Line-lineOffset, pos.Col, msg)
		}
		return err == nil
	})
	return err
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

// parseExpr parses src, an expression that may span several lines, as if it
// were in parentheses. It returns how many lines down the positions of the
// expression are.
func parseExpr(opts *syntax.FileOptions, src string) (syntax.Expr, int32, error) {
	expr, err := opts.ParseExpr("", src, 0)
	if err == nil || !strings.Contains(src, "\n") {
		return expr, 0, positioned(err, 0)
	}
	// Inside parentheses, the lines keep their positions, one line down.
	expr, parenErr := opts.ParseExpr("", "(\n"+src+"\n)", 0)
	if parenErr == nil {
		return expr, 1, nil
	}
	// An error after the last line is at the closing parenthesis.
	lines := strings.Split(src, "\n")
	var syntaxErr syntax.Error
	if errors.As(parenErr, &syntaxErr) && int(syntaxErr.Pos.Line)-1 > len(lines) {
		return nil, 0, fmt.Errorf("%d:%d: incomplete expression", len(lines), len(lines[len(lines)-1])+1)
	}
	return nil, 0, positioned(parenErr, 1)
}

func (c *expression) Match(req *Request) (bool, error) {
	ok, fields, err := c.match(req)
	if err != nil {
		return false, &Error{Err: err, Fields: fields}
	}
	return ok, nil
}

// match runs c with the client of req, and returns its result, or its error
// with the fields of the client that it read.
func (c *expression) match(req *Request) (bool, []Field, error) {
	v := &clientValue{c: req.Client}
	result, err := starlark.Call(newThread(req.Print), c.fn, starlark.Tuple{v}, nil)
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			return false, v.fields(), errors.New(evalErr.Msg)
		}
		return false, v.fields(), err
	}
	ok, isBool := result.(starlark.Bool)
	if !isBool {
		return false, v.fields(), fmt.Errorf("must return True or False, got %s", result.Type())
	}
	return bool(ok), nil, nil
}

// newThread returns a thread for an evaluation, whose prints go to print, or
// nowhere if nil.
func newThread(print func(msg string)) *starlark.Thread {
	thread := &starlark.Thread{Print: func(_ *starlark.Thread, msg string) {
		if print != nil {
			print(msg)
		}
	}}
	thread.SetMaxExecutionSteps(maxSteps)
	return thread
}

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
