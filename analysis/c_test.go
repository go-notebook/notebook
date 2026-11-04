package analysis

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// analysis must not import go-embedded-c: a program that does not import
// analysis/parsers must not carry it (v0.9.0's opt-in rule).
func TestNoEmbeddedCImport(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the go command")
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/go-embedded-c") || strings.HasPrefix(p, "modernc.org/cc") {
			t.Errorf("analysis depends on %s", p)
		}
	}
}

// With a front end set, C cells are analysed in sequence through it, with the
// state it returns; Language.Analyze is the first C cell alone.
func TestCLang(t *testing.T) {
	old := cParser
	t.Cleanup(func() { cParser = old })
	var states []any
	SetCParser(func(index int, src string, state any) (*Cell, any) {
		states = append(states, state)
		return &Cell{Index: index, Source: src, Lang: C, Defines: []string{src}, Imports: map[string]string{}}, state
	})
	g := AnalyzeSources([]Source{{Lang: C, Source: "a"}, {Lang: Go, Source: "x := 1"}, {Lang: C, Source: "b"}})
	if len(states) != 2 || states[0] != nil || states[1] != nil || !slices.Equal(g.Cells[2].Defines, []string{"b"}) {
		t.Errorf("states %v, cells %+v", states, g.Cells)
	}
	n, diags, err := Lookup(C).Analyze("c", DefaultLimits)
	if err != nil || len(diags) != 0 || !slices.Equal(n.Defines, []string{"c"}) {
		t.Errorf("Analyze: %+v %v %v", n, diags, err)
	}
	if CBlocked(4) != "blocked: it depends on cell 4, a C cell, and C cells are not run yet" {
		t.Error(CBlocked(4))
	}
	run := RunOf(C)
	if run.Runs || run.NotRun != "C cells are not run yet (go-embedded-c M2: code generation)" || run.Blocked(1) != CBlocked(1) ||
		run.Reads(2) != "not run: it reads cell 2, a C cell, and C cells are not run yet" ||
		run.ReadsBlocked(3) != "not run: it reads cell 3, which is blocked by a C cell" {
		t.Errorf("run %+v", run)
	}
	if h, ok := Lookup(C).(Highlighter); !ok || h.Lexer() != "c" {
		t.Error("C is highlighted with go-rouge's c lexer")
	}
	// What is not a variable stays C's; a variable named like a Go keyword
	// stays out of Go's sight.
	owner := &Cell{Lang: C, Funcs: []string{"f"}, Consts: []string{"T"}}
	if Shared(owner, &Cell{Lang: Go}, "f") || Shared(owner, &Cell{Lang: Ruby}, "T") || !Shared(owner, &Cell{Lang: Ruby}, "v") || Shared(owner, &Cell{Lang: Go}, "type") {
		t.Error("crossing")
	}
}
