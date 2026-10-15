package script

import (
	"errors"
	"fmt"

	"go.starlark.net/starlark"
)

// Condition is what a rule requires of clients: host patterns (see Hosts) or
// an expression (see Scope.CompileCondition).
type Condition interface {
	// Match reports whether the client of req matches the condition, or why
	// it failed, as an *Error. A condition that fails does not apply. What it
	// prints goes to printer, or nowhere if nil.
	Match(req *Request, printer func(msg string)) (bool, error)
}

// expression is a condition written in Starlark.
type expression struct {
	scope *Scope
	fn    *starlark.Function
}

// CompileCondition returns the condition written in src, a Starlark expression
// that returns True or False, such as `client.protocol < 47`, which can use
// the globals of the module of s. It also checks the fields of client and the
// literal arguments of the functions, and runs the condition with sample
// clients.
func (s *Scope) CompileCondition(src string) (Condition, error) {
	fn, err := s.compileExpr(src, "client", "ctx")
	if err != nil {
		return nil, err
	}
	c := &expression{scope: s, fn: fn}
	if err := checkSamples(samples, func(req *Request) error {
		_, err := c.Match(req, nil)
		return err
	}); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *expression) Match(req *Request, printer func(msg string)) (bool, error) {
	v := clientKind.new(req.Client)
	result, err := starlark.Call(newThread(printer), c.fn, starlark.Tuple{v, c.scope.ctxValue(req)}, nil)
	if err != nil {
		return false, &Error{Err: errors.New(evalMessage(err)), Fields: clientFields(v)}
	}
	ok, isBool := result.(starlark.Bool)
	if !isBool {
		return false, &Error{Err: fmt.Errorf("must return True or False, got %s", result.Type()), Fields: clientFields(v)}
	}
	return bool(ok), nil
}
