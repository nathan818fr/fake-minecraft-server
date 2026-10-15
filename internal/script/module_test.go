package script

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Conditions use the functions and constants of the module, which runs once,
// with the dialect of conditions.
func TestModule(t *testing.T) {
	s := NewScope(nil, nil)
	err := s.LoadModule(`
STAFF = set(["10.1.2.3"])
PORTS = {}
for port in [25565, 25566]:
    PORTS[port] = True

def is_staff(client):
    return client.ip in STAFF and fms.match_host(client.host, ".a.com")

def countdown(n):
    while n > 0:
        n -= 1
    return n

def depth(n):
    return 0 if n == 0 else 1 + depth(n - 1)

print("loaded", len(PORTS))
`)
	if err != nil {
		t.Fatalf("LoadModule() error: %v", err)
	}
	if want := []string{"loaded 2"}; !slices.Equal(s.Prints(), want) {
		t.Errorf("Prints() = %q, want %q", s.Prints(), want)
	}
	c, err := s.CompileCondition(`is_staff(client) and client.port in PORTS and countdown(3) == 0 and depth(3) == 3`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	for client, want := range map[*Client]bool{&modernJoin: true, &legacyPing: false} {
		if got, err := c.Match(&Request{Client: client}, nil); err != nil || got != want {
			t.Errorf("Match(%+v) = %v, %v, want %v", client, got, err, want)
		}
	}
}

// Mistakes are reported with their position in the module.
func TestLoadModuleErrors(t *testing.T) {
	tests := []struct {
		src, want string
	}{
		{"x = 1\nx = 2\n", "2:1: cannot reassign global x declared at 1:1"},
		{"load(\"other.star\", \"x\")\n", "1:1: load is not supported: a configuration has a single script"},
		{"client = 1\n", "1:1: client is reserved for the variable that scripts see"},
		{"ctx = 1\n", "1:1: ctx is reserved for the variable that scripts see"},
		{"def fms():\n    pass\n", "1:5: fms is reserved for the variable that scripts see"},
		{"for server in []:\n    pass\n", "1:5: server is reserved for the variable that scripts see"},
		{"def f(client):\n    return client.protcol\n", "2:19: client has no .protcol field or method (did you mean .protocol?)"},
		{"def f(client):\n    return fms.matches(client.host, \"(\")\n", "2:37: fms.matches: error parsing regexp: missing closing ): `(`"},
		{"X = [1][2]\n", "1:8: list index 2 out of range [-1:0]"},
		{"def f():\n    return 1 // 0\nX = f()\n", "2:14: floored division by zero"},
		{"X = len([x for x in range(2000000)])\n", "1:10: Starlark computation cancelled: too many steps"},
		{"X = (\n", "2:1: got end of file, want primary expression"},
	}
	for _, tc := range tests {
		if err := NewScope(nil, nil).LoadModule(tc.src); err == nil || err.Error() != tc.want {
			t.Errorf("LoadModule(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// The names of a module that failed to load are not reported as typos.
func TestModuleFailed(t *testing.T) {
	s := NewScope(nil, nil)
	if err := s.LoadModule("def is_staff(client):\n    return (\n"); err == nil {
		t.Fatal("LoadModule() succeeded, want an error")
	}
	if _, err := s.CompileCondition(`is_staff(client)`); err == nil || err.Error() != "1:1: undefined: is_staff (the script failed to load)" {
		t.Errorf("CompileCondition() error = %v, want is_staff undefined, as the script failed to load", err)
	}

	// A file that cannot be read fails the module too.
	s = NewScope(nil, nil)
	if err := s.LoadModuleFile(filepath.Join(t.TempDir(), "missing.star")); err == nil {
		t.Fatal("LoadModuleFile() succeeded, want an error")
	}
	if _, err := s.CompileCondition(`is_staff(client)`); err == nil || !strings.HasSuffix(err.Error(), "(the script failed to load)") {
		t.Errorf("CompileCondition() error = %v, want is_staff undefined, as the script failed to load", err)
	}

	// What it printed before failing is kept, as it may tell why.
	s = NewScope(nil, nil)
	if err := s.LoadModule("print(\"loading\")\nX = 1 // 0\n"); err == nil {
		t.Fatal("LoadModule() succeeded, want an error")
	}
	if want := []string{"loading"}; !slices.Equal(s.Prints(), want) {
		t.Errorf("Prints() = %q, want %q", s.Prints(), want)
	}
}

// The top level of a module has a larger budget than each evaluation.
func TestLoadModuleSteps(t *testing.T) {
	s := NewScope(nil, nil)
	if err := s.LoadModule("TABLE = {i: i * i for i in range(20000)}\ndef square(n):\n    return TABLE[n]\n"); err != nil {
		t.Fatalf("LoadModule() error: %v", err)
	}
	if _, err := s.CompileCondition(`square(client.port % 1000) >= 0`); err != nil {
		t.Errorf("CompileCondition() error: %v", err)
	}
}

// What conditions print during a request goes to the printer of Match.
func TestRequestPrint(t *testing.T) {
	s := NewScope(nil, nil)
	if err := s.LoadModule("def log(client):\n    print(\"from\", client.ip)\n    return True\n"); err != nil {
		t.Fatalf("LoadModule() error: %v", err)
	}
	c, err := s.CompileCondition(`log(client)`)
	if err != nil {
		t.Fatalf("CompileCondition() error: %v", err)
	}
	var printed []string
	printer := func(msg string) { printed = append(printed, msg) }
	if got, err := c.Match(&Request{Client: &modernJoin}, printer); err != nil || !got {
		t.Errorf("Match() = %v, %v, want true", got, err)
	}
	if want := []string{"from 10.1.2.3"}; !slices.Equal(printed, want) {
		t.Errorf("printed %q, want %q", printed, want)
	}
}

// Patterns are compiled once. Literal ones are kept for good, and at most
// maxCompiledPatterns others.
func TestCompilePattern(t *testing.T) {
	literal, err := compilePattern("^lit$", true)
	if err != nil {
		t.Fatal(err)
	}
	first, err := compilePattern("^a+$", false)
	if again, _ := compilePattern("^a+$", false); err != nil || first != again {
		t.Errorf("compilePattern() = %p, %v, then %p, want the same regexp", first, err, again)
	}
	for i := range 2 * maxCompiledPatterns {
		if _, err := compilePattern("^"+strconv.Itoa(i)+"$", false); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(compiledPatterns.others); n > maxCompiledPatterns {
		t.Errorf("%d patterns kept, want at most %d", n, maxCompiledPatterns)
	}
	if again, _ := compilePattern("^lit$", false); again != literal {
		t.Error("the literal pattern was compiled again")
	}
}
