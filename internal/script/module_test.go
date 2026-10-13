package script

import (
	"slices"
	"strconv"
	"testing"

	"go.starlark.net/starlark"
)

// A module runs once, with the dialect of scripts and the fms functions, and
// keeps its globals frozen.
func TestLoadModule(t *testing.T) {
	m, err := LoadModule(`
OFFICE = set(["10.0.0.0/8"])
PORTS = {}
for port in [25565, 25566]:
    PORTS[port] = True

def countdown(n):
    while n > 0:
        n -= 1
    return n

def depth(n):
    return 0 if n == 0 else 1 + depth(n - 1)

CHECKS = [
    fms.in_network("10.1.2.3", *OFFICE),
    fms.match_host("Play.A.com.", ".a.com"),
    fms.matches("play1.a.com", "^play[0-9]+"),
    not fms.in_network("", "10.0.0.0/8"),
    countdown(3) == 0,
    depth(3) == 3,
]
print("loaded", len(PORTS))
`)
	if err != nil {
		t.Fatalf("LoadModule() error: %v", err)
	}
	if want := []string{"loaded 2"}; !slices.Equal(m.Prints(), want) {
		t.Errorf("Prints() = %q, want %q", m.Prints(), want)
	}
	checks := m.globals["CHECKS"].(*starlark.List)
	for i := range checks.Len() {
		if checks.Index(i) != starlark.True {
			t.Errorf("CHECKS[%d] = %s, want True", i, checks.Index(i))
		}
	}
	if err := m.globals["PORTS"].(*starlark.Dict).SetKey(starlark.MakeInt(1), starlark.True); err == nil {
		t.Error("PORTS can be changed, want it frozen")
	}
}

// Mistakes are reported with their position in the module.
func TestLoadModuleErrors(t *testing.T) {
	tests := []struct {
		src, want string
	}{
		{"x = 1\nx = 2\n", "2:1: cannot reassign global x declared at 1:1"},
		{"load(\"other.star\", \"x\")\n", "1:1: load is not supported: a configuration has a single script"},
		{"def fms():\n    pass\n", "1:5: fms is reserved for the variable that scripts see"},
		{"for fms in []:\n    pass\n", "1:5: fms is reserved for the variable that scripts see"},
		{"X = fms.matches(\"a\", \"(\")\n", "1:22: fms.matches: error parsing regexp: missing closing ): `(`"},
		{"X = fms.in_network(\"10.0.0.1\", \"10.0.0.0/33\")\n", `1:32: fms.in_network: "10.0.0.0/33" is not an IP address or CIDR prefix`},
		{"X = fms.match_host(\"a.com\", \"a.*.com\")\n", `1:29: fms.match_host: invalid host "a.*.com": a wildcard is only allowed as "*." followed by a domain`},
		{"X = [1][2]\n", "1:8: list index 2 out of range [-1:0]"},
		{"def f():\n    return 1 // 0\nX = f()\n", "2:14: floored division by zero"},
		{"X = fms.in_network(\"x\", \"10.0.0.0/8\")\n", "1:19: fms.in_network: ParseAddr(\"x\"): unable to parse IP"},
		{"X = len([x for x in range(2000000)])\n", "1:10: Starlark computation cancelled: too many steps"},
		{"X = (\n", "2:1: got end of file, want primary expression"},
	}
	for _, tc := range tests {
		if _, err := LoadModule(tc.src); err == nil || err.Error() != tc.want {
			t.Errorf("LoadModule(%q) error = %v, want %s", tc.src, err, tc.want)
		}
	}
}

// The top level of a module can take many steps, to build tables.
func TestLoadModuleSteps(t *testing.T) {
	m, err := LoadModule("TABLE = {i: i * i for i in range(20000)}\n")
	if err != nil {
		t.Fatalf("LoadModule() error: %v", err)
	}
	if n := m.globals["TABLE"].(*starlark.Dict).Len(); n != 20000 {
		t.Errorf("TABLE has %d entries, want 20000", n)
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
