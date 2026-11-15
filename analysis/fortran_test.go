package analysis

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// analysis must not import go-embedded-fortran: a program that does not import
// analysis/parsers must not carry it (v0.9.0's opt-in rule).
func TestNoEmbeddedFortranImport(t *testing.T) {
	if testing.Short() {
		t.Skip("needs the go command")
	}
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/go-embedded-fortran") {
			t.Errorf("analysis depends on %s", p)
		}
	}
}

// With a front end set, Fortran cells are analysed in sequence through it,
// with the state it returns; Language.Analyze is the first Fortran cell alone.
func TestFortranLang(t *testing.T) {
	old := fortranParser
	t.Cleanup(func() { fortranParser = old })
	var states []any
	SetFortranParser(func(index int, src string, state any) (*Cell, any) {
		states = append(states, state)
		return &Cell{Index: index, Source: src, Lang: Fortran, Defines: []string{src}, Imports: map[string]string{}}, src
	})
	g := AnalyzeSources([]Source{{Lang: Fortran, Source: "a"}, {Lang: Go, Source: "x := 1"}, {Lang: Fortran, Source: "b"}})
	if len(states) != 2 || states[0] != nil || states[1] != "a" || !slices.Equal(g.Cells[2].Defines, []string{"b"}) {
		t.Errorf("states %v, cells %+v", states, g.Cells)
	}
	n, diags, err := Lookup(Fortran).Analyze("c", DefaultLimits)
	if err != nil || len(diags) != 0 || !slices.Equal(n.Defines, []string{"c"}) {
		t.Errorf("Analyze: %+v %v %v", n, diags, err)
	}
	if run := RunOf(Fortran); !run.Runs {
		t.Errorf("Fortran cells run: %+v", run)
	}
	if h, ok := Lookup(Fortran).(Highlighter); !ok || h.Lexer() != "fortran" {
		t.Error("Fortran is highlighted with go-rouge's fortran lexer")
	}
	if rt, ok := Lookup(Fortran).(Runtime); !ok || rt.Runtime().Path != FortranPath || rt.Runtime().Module != "github.com/go-notebook/notebook" {
		t.Error("Fortran cells run on this module's fcells")
	}
	// The variables live in the session, which no other language can reach: no
	// name crosses, in either direction, and the language says so (Isolated).
	owner := &Cell{Lang: Fortran, Funcs: []string{"f"}, Consts: []string{"T"}}
	if Lookup(Fortran).Crosses(owner, &Cell{Lang: Go}, "v") || !isolated(Fortran) || Shared(owner, &Cell{Lang: Go}, "v") || Shared(owner, &Cell{Lang: Ruby}, "v") ||
		Shared(&Cell{Lang: Go}, &Cell{Lang: Fortran}, "v") || !Shared(owner, &Cell{Lang: Fortran}, "v") {
		t.Error("crossing")
	}
}

// Without the front end the language stays registered and its cells fail
// clearly.
func TestFortranParserMissing(t *testing.T) {
	old := fortranParser
	t.Cleanup(func() { fortranParser = old })
	fortranParser = nil
	c := AnalyzeSources([]Source{{Lang: Fortran, Source: "integer :: a"}}).Cells[0]
	if len(c.Errors) != 1 || c.Errors[0].Msg != ParserMissing("Fortran") || len(c.Defines) != 0 {
		t.Errorf("%+v", c)
	}
}
