// Package script implements what users write in Starlark, and the conditions
// of rules, which tell the clients that rules apply to: host patterns, and
// expressions. The scripts of a configuration (see Scope) are its conditions,
// the hooks of its servers (their ping_script and join_script) and its
// module. They see the client, the functions of the fms module, and the
// upstreams and servers of the configuration.
//
// See https://github.com/bazelbuild/starlark/blob/master/spec.md
package script

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/upstream"
)

// Request is a request of a client: what scripts know about it, shared by the
// scripts that run for it.
type Request struct {
	Client *Client
	// Upstream returns the state of the upstream with the ID id, the same at
	// each call during the request, so that every script of the request sees
	// the same one. If nil, no upstream is available.
	Upstream func(id string) upstream.State
	// Now returns the current time, and Rand is the source of the random
	// numbers of scripts, so that tests can fix them. If nil, they are the
	// clock and a random source.
	Now  func() time.Time
	Rand *rand.Rand

	// ctx is the ctx variable of the request, made when first needed, and now
	// the time of the request, read when first needed.
	ctx *ctxValue
	now time.Time
}

// upstreamState returns the state of the upstream with the ID id during req.
func (req *Request) upstreamState(id string) upstream.State {
	if req.Upstream == nil {
		return upstream.State{}
	}
	return req.Upstream(id)
}

// Error is the error of a script that failed for a request: a condition or a
// hook. Fields are the fields of the client that the script read, to tell
// which values it failed with.
type Error struct {
	Err    error
	Fields []Field
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// dialect is the Starlark dialect of scripts: the language of the
// specification, plus set(), while loops and recursion, all bounded by
// maxSteps. Reassigning globals stays an error, as it is usually a mistake.
var dialect = &syntax.FileOptions{Set: true, While: true, TopLevelControl: true, Recursion: true}

// maxSteps bounds the work of each evaluation, such as comprehensions over
// large ranges. Conditions take a few dozen steps.
const maxSteps = 10_000

// compileExpr compiles src, a Starlark expression, as what a function with the
// parameters params returns, with the fms module and the globals of the module
// of s, after checking the attributes that it reads of fms and of params (see
// checkFields) and the literal arguments of the functions.
func (s *Scope) compileExpr(src string, params ...string) (*starlark.Function, error) {
	expr, lineOffset, err := parseExpr(dialect, src)
	if err != nil {
		return nil, err
	}
	if err := checkFields(expr, lineOffset, params); err != nil {
		return nil, err
	}
	if err := s.checkLiterals(expr, lineOffset, false); err != nil {
		return nil, err
	}

	// The expression is compiled once, as what a function returns. The
	// function is built as a syntax tree around the parsed expression, so that
	// errors keep the positions of src.
	def := &syntax.DefStmt{Name: &syntax.Ident{Name: "expr"}, Body: []syntax.Stmt{&syntax.ReturnStmt{Result: expr}}}
	for _, name := range params {
		def.Params = append(def.Params, &syntax.Ident{Name: name})
	}
	predeclared := starlark.StringDict{"fms": fmsModule()}
	if s.module != nil {
		maps.Copy(predeclared, s.module.globals)
	}
	program, err := starlark.FileProgram(&syntax.File{Options: dialect, Stmts: []syntax.Stmt{def}}, predeclared.Has)
	if err != nil {
		var resolveErrs resolve.ErrorList // Shares its errors with err.
		if s.moduleFailed && errors.As(err, &resolveErrs) {
			for i, e := range resolveErrs {
				if strings.HasPrefix(e.Msg, "undefined: ") {
					resolveErrs[i].Msg += " (the script failed to load)"
				}
			}
		}
		return nil, positioned(err, lineOffset)
	}
	globals, err := program.Init(newThread(nil), predeclared)
	if err != nil {
		return nil, err // Defining a function does not fail.
	}
	return globals["expr"].(*starlark.Function), nil
}

// checkedVariables are the variables of scripts whose attributes checkFields
// checks, as samples: their attributes are the same for every value.
var checkedVariables = map[string]starlark.HasAttrs{
	"client": clientKind.new(&sampleBase),
	"ctx":    ctxKind.new(ctxView{}),
	"server": serverKind.new(serverView{}),
	"fms":    fmsModule(),
}

// checkFields checks the attributes of fms, and of the variables that n sees,
// that n reads by name, so that a typo is reported at startup, even in a
// branch that no sample client takes. The positions of n are lineOffset lines
// down.
func checkFields(n syntax.Node, lineOffset int32, variables []string) error {
	var err error
	syntax.Walk(n, func(n syntax.Node) bool {
		dot, ok := n.(*syntax.DotExpr)
		if err != nil || !ok {
			return err == nil
		}
		x, ok := dot.X.(*syntax.Ident)
		if !ok || x.Name != "fms" && !slices.Contains(variables, x.Name) {
			return true
		}
		v, ok := checkedVariables[x.Name]
		if ok && !slices.Contains(v.AttrNames(), dot.Name.Name) {
			// Starlark's own message, with its spelling hint.
			_, getErr := starlark.EvalOptions(dialect, newThread(nil), "", x.Name+"."+dot.Name.Name, starlark.StringDict{x.Name: v})
			pos := dot.Name.NamePos
			err = fmt.Errorf("%d:%d: %s", pos.Line-lineOffset, pos.Col, evalMessage(getErr))
		}
		return err == nil
	})
	return err
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

// evalMessage returns the message of err, an error of a Starlark evaluation,
// without its call stack.
func evalMessage(err error) string {
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) {
		return evalErr.Msg
	}
	return err.Error()
}

// newThread returns a thread that evaluates scripts, whose prints go to
// printer, or nowhere if nil.
func newThread(printer func(msg string)) *starlark.Thread {
	thread := &starlark.Thread{Print: dropPrint}
	if printer != nil {
		thread.Print = func(_ *starlark.Thread, msg string) { printer(msg) }
	}
	thread.SetMaxExecutionSteps(maxSteps)
	return thread
}

func dropPrint(*starlark.Thread, string) {}

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

// callCheck is what checkLiterals checks at startup in the calls to a
// function of fms or a method of ctx, so that mistakes are reported then, even
// in a branch that no sample client takes.
type callCheck struct {
	// literal checks the literal strings given as the argument at index arg,
	// or as every argument but the first if arg is argsAfterFirst.
	arg     int
	literal func(s *Scope, text string) error
	// outsideModule, if set, is the error of the calls outside the module.
	outsideModule error
}

// argsAfterFirst is the callCheck.arg of the functions that take a value, then
// what to compare it with, such as the networks of fms.in_network.
const argsAfterFirst = -1

// checksArg reports whether c checks the literal given as the argument at
// index i.
func (c callCheck) checksArg(i int) bool {
	switch {
	case c.literal == nil:
		return false
	case c.arg == argsAfterFirst:
		return i > 0
	default:
		return i == c.arg
	}
}

// checkedCall returns the name of the function of fms or the method of ctx
// that fn names, such as "fms.matches", and its checks, if it has any (see
// fmsCallChecks and ctxCallChecks).
func checkedCall(fn syntax.Expr) (string, callCheck, bool) {
	dot, ok := fn.(*syntax.DotExpr)
	if !ok {
		return "", callCheck{}, false
	}
	x, ok := dot.X.(*syntax.Ident)
	if !ok {
		return "", callCheck{}, false
	}
	var check callCheck
	switch x.Name {
	case "fms":
		check, ok = fmsCallChecks[dot.Name.Name]
	case "ctx":
		check, ok = ctxCallChecks[dot.Name.Name]
	default:
		ok = false
	}
	return x.Name + "." + dot.Name.Name, check, ok
}

// checkLiterals checks the calls to the functions of fms and to the methods of
// ctx in n (see callCheck), in a module if inModule. The positions of n are
// lineOffset lines down.
func (s *Scope) checkLiterals(n syntax.Node, lineOffset int32, inModule bool) error {
	var err error
	syntax.Walk(n, func(n syntax.Node) bool {
		if err != nil {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		fn, check, ok := checkedCall(call.Fn)
		if !ok {
			return true
		}
		if check.outsideModule != nil && !inModule {
			pos, _ := call.Span()
			err = fmt.Errorf("%d:%d: %w", pos.Line-lineOffset, pos.Col, check.outsideModule)
			return false
		}
		for i, arg := range call.Args {
			if !check.checksArg(i) {
				continue
			}
			lit, ok := arg.(*syntax.Literal)
			if !ok || lit.Token != syntax.STRING {
				continue
			}
			if argErr := check.literal(s, lit.Value.(string)); argErr != nil {
				pos := lit.TokenPos
				err = fmt.Errorf("%d:%d: %s: %w", pos.Line-lineOffset, pos.Col, fn, argErr)
				return false
			}
		}
		return true
	})
	return err
}
