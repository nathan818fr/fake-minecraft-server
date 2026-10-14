package script

import (
	"errors"
	"fmt"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// maxLoadSteps bounds the work of the top level of a module, which runs once,
// so that it can build large tables, but never hangs the startup.
const maxLoadSteps = 1_000_000

// reservedNames are the names of the variables that scripts see, which modules
// must not define, so that they never hide them.
var reservedNames = []string{"client", "fms"}

// Module is the script of a configuration: the functions and constants that
// conditions can use. Its globals are frozen, so that requests share them.
type Module struct {
	globals starlark.StringDict
	prints  []string
}

// LoadModule runs src, a Starlark file, once, and returns its globals. It
// checks, like Compile, the fields of client and the literal arguments of the
// functions, and reports the mistakes of src with their positions in it.
func LoadModule(src string) (*Module, error) {
	file, err := dialect.Parse("", src, 0)
	if err != nil {
		return nil, positioned(err, 0)
	}
	if err := checkModuleStmts(file); err != nil {
		return nil, err
	}
	if err := checkFields(file, 0); err != nil {
		return nil, err
	}
	if err := checkLiterals(file, 0); err != nil {
		return nil, err
	}

	predeclared := starlark.StringDict{"fms": fmsModule()}
	program, err := starlark.FileProgram(file, predeclared.Has)
	if err != nil {
		return nil, positioned(err, 0)
	}
	m := &Module{}
	thread := &starlark.Thread{Print: func(_ *starlark.Thread, msg string) { m.prints = append(m.prints, msg) }}
	thread.SetMaxExecutionSteps(maxLoadSteps)
	globals, err := program.Init(thread, predeclared)
	if err != nil {
		return nil, loadError(err)
	}
	globals.Freeze()
	m.globals = globals
	return m, nil
}

// Prints returns what the top level of m printed when loaded.
func (m *Module) Prints() []string {
	return m.prints
}

// checkModuleStmts checks the top-level statements of file: no load, as a
// configuration has a single module, and no definition of a reserved name.
func checkModuleStmts(file *syntax.File) error {
	for _, stmt := range file.Stmts {
		if load, ok := stmt.(*syntax.LoadStmt); ok {
			pos := load.Load
			return fmt.Errorf("%d:%d: load is not supported: a configuration has a single script", pos.Line, pos.Col)
		}
		for _, id := range definedNames(stmt) {
			if slices.Contains(reservedNames, id.Name) {
				return fmt.Errorf("%d:%d: %s is reserved for the variable that scripts see", id.NamePos.Line, id.NamePos.Col, id.Name)
			}
		}
	}
	return nil
}

// definedNames returns the global names that stmt, a top-level statement,
// defines.
func definedNames(stmt syntax.Stmt) []*syntax.Ident {
	var ids []*syntax.Ident
	var targets func(e syntax.Expr)
	targets = func(e syntax.Expr) {
		switch e := e.(type) {
		case *syntax.Ident:
			ids = append(ids, e)
		case *syntax.TupleExpr:
			for _, x := range e.List {
				targets(x)
			}
		case *syntax.ListExpr:
			for _, x := range e.List {
				targets(x)
			}
		case *syntax.ParenExpr:
			targets(e.X)
		}
	}
	switch stmt := stmt.(type) {
	case *syntax.DefStmt:
		ids = append(ids, stmt.Name)
	case *syntax.AssignStmt:
		targets(stmt.LHS)
	case *syntax.ForStmt:
		targets(stmt.Vars)
		for _, s := range stmt.Body {
			ids = append(ids, definedNames(s)...)
		}
	case *syntax.IfStmt:
		for _, s := range append(slices.Clone(stmt.True), stmt.False...) {
			ids = append(ids, definedNames(s)...)
		}
	case *syntax.WhileStmt:
		for _, s := range stmt.Body {
			ids = append(ids, definedNames(s)...)
		}
	}
	return ids
}

// loadError returns err, from running a module, with the position in the
// module where it happened, rather than in a built-in function, whose frames
// have no position.
func loadError(err error) error {
	var evalErr *starlark.EvalError
	if !errors.As(err, &evalErr) {
		return positioned(err, 0)
	}
	for i := range evalErr.CallStack {
		if pos := evalErr.CallStack.At(i).Pos; pos.Line > 0 {
			return fmt.Errorf("%d:%d: %s", pos.Line, pos.Col, evalErr.Msg)
		}
	}
	return errors.New(evalErr.Msg)
}
