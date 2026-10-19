package script

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/nathan818fr/fake-minecraft-server/v2/internal/status"
)

// maxLoadSteps bounds the work of the top level of a module, which runs once,
// so that it can build large tables, but never hangs the startup.
const maxLoadSteps = 1_000_000

// scriptVariables are the variables that scripts see besides fms: client and
// ctx in conditions and hooks, and server in hooks. The functions of a module
// take them as parameters of the same name, whose attributes are checked like
// theirs (see checkFields).
var scriptVariables = []string{"client", "ctx", "server"}

// reservedNames are the names of the variables that scripts see, which modules
// must not define, so that they never hide them.
var reservedNames = append([]string{"fms"}, scriptVariables...)

// module is the script module of a configuration: the functions and constants
// that its scripts can use. Its globals are frozen, so that requests share
// them.
type module struct {
	globals starlark.StringDict
}

// moduleLoading is what fms.read_favicon needs while a module loads, held by
// its thread under moduleLoadingKey: the directory that the paths are relative
// to, and the favicons read (see encodedFavicon).
type moduleLoading struct {
	dir      string
	favicons []encodedFavicon
}

const moduleLoadingKey = "fms.module_loading"

// LoadModule runs src, a Starlark file, once, and gives its globals to the
// scripts compiled with s from then on. The paths that it reads are relative
// to dir. It checks, like CompileCondition, the fields of client and the
// literal arguments of the functions, and reports the mistakes of src with
// their positions in it.
func (s *Scope) LoadModule(src, dir string) (err error) {
	defer func() { s.moduleFailed = err != nil }()
	file, err := dialect.Parse("", src, 0)
	if err != nil {
		return positioned(err, 0)
	}
	if err := checkModuleStmts(file); err != nil {
		return err
	}
	if err := checkFields(file, 0, scriptVariables); err != nil {
		return err
	}
	if err := s.checkLiterals(file, 0, true); err != nil {
		return err
	}

	predeclared := starlark.StringDict{"fms": fmsModule()}
	program, err := starlark.FileProgram(file, predeclared.Has)
	if err != nil {
		return positioned(err, 0)
	}
	m := &module{}
	thread := &starlark.Thread{Print: func(_ *starlark.Thread, msg string) { s.prints = append(s.prints, msg) }}
	thread.SetMaxExecutionSteps(maxLoadSteps)
	loading := &moduleLoading{dir: dir}
	thread.SetLocal(moduleLoadingKey, loading)
	globals, err := program.Init(thread, predeclared)
	if err != nil {
		return loadError(err)
	}
	globals.Freeze()
	m.globals = globals
	s.module = m
	s.favicons = append(s.favicons, loading.favicons...)
	return nil
}

// LoadModuleFile is LoadModule for the source in the file at path, whose
// paths are relative to the directory of the file. A file that cannot be read
// fails the module too (see Scope.moduleFailed).
func (s *Scope) LoadModuleFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		s.moduleFailed = true
		return err
	}
	return s.LoadModule(string(data), filepath.Dir(path))
}

// Prints returns what the top level of the module of s printed when loaded,
// even if it then failed, which may tell why.
func (s *Scope) Prints() []string {
	return s.prints
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

// readFaviconBuiltin implements fms.read_favicon(path), the PNG file at path as
// a favicon (see status.ReadFavicon). It only works while the module loads, so
// that requests never read files: modules keep favicons in globals.
func readFaviconBuiltin(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var path string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &path); err != nil {
		return nil, err
	}
	loading, ok := thread.Local(moduleLoadingKey).(*moduleLoading)
	if !ok {
		return nil, errReadFaviconAfterLoad
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(loading.dir, path)
	}
	uri, err := status.ReadFavicon(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	loading.favicons = append(loading.favicons, encodedFavicon{text: uri, json: status.AppendValue(nil, uri)})
	return starlark.String(uri), nil
}

var errReadFaviconAfterLoad = errors.New("fms.read_favicon: only works while the script loads: keep favicons in globals")
