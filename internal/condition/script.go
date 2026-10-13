package condition

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// params are the names of the variables of conditions, in the order of args.
var params = []string{
	"event", "transferred", "host", "host_extra", "port", "protocol",
	"legacy", "legacy_ping_version", "ip", "listener",
}

func (c *Client) args() starlark.Tuple {
	return starlark.Tuple{
		starlark.String(c.Event), starlark.Bool(c.Transferred),
		starlark.String(c.Host), starlark.String(c.HostExtra),
		starlark.MakeInt(c.Port), starlark.MakeInt(c.Protocol),
		starlark.Bool(c.Legacy), starlark.String(c.LegacyPingVersion),
		starlark.String(c.IP), starlark.String(c.Listener),
	}
}

// maxSteps bounds the work of each evaluation, such as comprehensions over
// large ranges. Conditions take a few dozen steps.
const maxSteps = 10_000

// sampleBase is the most common client, which the other samples change.
var sampleBase = Client{Event: EventPing, Host: "mc.example.com", Port: 25565, Protocol: 773, IP: "203.0.113.5", Listener: "[::]:25565"}

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
	{"a player joining", sample(func(c *Client) { c.Event = EventJoin })},
	{"a transferred player", sample(func(c *Client) { c.Event, c.Transferred = EventJoin, true })},
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
	{"a legacy login", sample(func(c *Client) { c.Event, c.Protocol, c.Legacy = EventJoin, 78, true })},
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

// script is a condition written in Starlark.
//
// See https://github.com/bazelbuild/starlark/blob/master/spec.md
type script struct {
	fn *starlark.Function
	// used are the indexes in params of the variables that the condition
	// uses.
	used []int
}

// Compile returns the condition written in src, a Starlark expression that
// returns True or False, such as `protocol < 47`. It also checks the literal
// arguments of the functions, and runs the condition with sample clients.
func Compile(src string) (Condition, error) {
	opts := &syntax.FileOptions{}
	expr, lineOffset, err := parseExpr(opts, src)
	if err != nil {
		return nil, err
	}
	regexps, err := checkLiterals(expr, lineOffset)
	if err != nil {
		return nil, err
	}

	// The expression is compiled once, as what a function returns, which runs
	// with the variables as arguments. Its lines are two lines down.
	wrapper := "def when(" + strings.Join(params, ", ") + "):\n  return (\n" + src + "\n)\n"
	globals, err := starlark.ExecFileOptions(opts, newThread(), "", wrapper, builtins(regexps))
	if err != nil {
		return nil, positioned(err, 2)
	}
	c := &script{fn: globals["when"].(*starlark.Function), used: usedVariables(expr)}

	for _, sample := range samples {
		if _, err := c.match(&sample.client); err != nil {
			return nil, fmt.Errorf("fails for %s%s: %w", sample.name, describe(c.vars(&sample.client)), err)
		}
	}
	return c, nil
}

// usedVariables returns the indexes in params of the variables that expr
// uses, in order.
func usedVariables(expr syntax.Expr) []int {
	var used []int
	syntax.Walk(expr, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if i := slices.Index(params, id.Name); i >= 0 && !slices.Contains(used, i) {
				used = append(used, i)
			}
		}
		return true
	})
	slices.Sort(used)
	return used
}

// describe returns vars as " (name value, ...)", or "" if there are none.
func describe(vars []Var) string {
	if len(vars) == 0 {
		return ""
	}
	texts := make([]string, len(vars))
	for i, v := range vars {
		texts[i] = v.String()
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

func (c *script) Match(client *Client) (bool, error) {
	ok, err := c.match(client)
	if err != nil {
		return false, &Error{Err: err, Vars: c.vars(client)}
	}
	return ok, nil
}

func (c *script) match(client *Client) (bool, error) {
	v, err := starlark.Call(newThread(), c.fn, client.args(), nil)
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			return false, errors.New(evalErr.Msg)
		}
		return false, err
	}
	result, ok := v.(starlark.Bool)
	if !ok {
		return false, fmt.Errorf("must return True or False, got %s", v.Type())
	}
	return bool(result), nil
}

// vars returns the variables of client that c uses.
func (c *script) vars(client *Client) []Var {
	args := client.args()
	vars := make([]Var, len(c.used))
	for j, i := range c.used {
		vars[j].Name = params[i]
		switch v := args[i].(type) {
		case starlark.String:
			vars[j].Value = string(v)
		case starlark.Bool:
			vars[j].Value = bool(v)
		case starlark.Int:
			n, _ := v.Int64() // Ports and protocol versions fit.
			vars[j].Value = int(n)
		}
	}
	return vars
}

func newThread() *starlark.Thread {
	thread := &starlark.Thread{Print: func(*starlark.Thread, string) {}}
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

// checkLiterals checks the string literals given to the functions of
// conditions, and returns the regular expressions of matches, compiled. The
// positions of expr are lineOffset lines down.
func checkLiterals(expr syntax.Expr, lineOffset int32) (map[string]*regexp.Regexp, error) {
	regexps := make(map[string]*regexp.Regexp)
	var err error
	syntax.Walk(expr, func(n syntax.Node) bool {
		if err != nil {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fn.(*syntax.Ident)
		if !ok || len(call.Args) < 2 {
			return true
		}
		for _, arg := range call.Args[1:] {
			lit, ok := arg.(*syntax.Literal)
			if !ok || lit.Token != syntax.STRING {
				continue
			}
			text := lit.Value.(string)
			var argErr error
			switch fn.Name {
			case "match_host":
				_, argErr = parseHostPattern(text)
			case "in_network":
				_, argErr = ParseNetwork(text)
			case "matches":
				var re *regexp.Regexp
				if re, argErr = regexp.Compile(text); argErr == nil {
					regexps[text] = re
				}
			}
			if argErr != nil {
				pos := lit.TokenPos
				err = fmt.Errorf("%d:%d: %s: %w", pos.Line-lineOffset, pos.Col, fn.Name, argErr)
				return false
			}
		}
		return true
	})
	return regexps, err
}

// builtins returns the functions of conditions. matches uses regexps, compiled
// at startup, for the regular expressions found in the condition.
func builtins(regexps map[string]*regexp.Regexp) starlark.StringDict {
	return starlark.StringDict{
		"match_host": starlark.NewBuiltin("match_host", matchHostBuiltin),
		"in_network": starlark.NewBuiltin("in_network", inNetwork),
		"matches": starlark.NewBuiltin("matches", func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			var s, pattern string
			if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &s, &pattern); err != nil {
				return nil, err
			}
			re, ok := regexps[pattern]
			if !ok {
				var err error
				if re, err = regexp.Compile(pattern); err != nil {
					return nil, fmt.Errorf("%s: %w", b.Name(), err)
				}
			}
			return starlark.Bool(re.MatchString(s)), nil
		}),
	}
}

// matchHostBuiltin implements match_host(host, *patterns), which matches host
// like the condition of Hosts.
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

// inNetwork implements in_network(ip, *networks), which reports whether ip is
// in one of the networks (see ParseNetwork). No address is in no network.
func inNetwork(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
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
