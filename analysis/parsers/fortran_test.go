package parsers

import (
	"strings"
	"testing"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
	"github.com/go-notebook/notebook/analysis"
)

func positionOf(line, col int) syntax.Position { return syntax.Position{Line: line, Col: col} }

func TestAnalyzeFortranCell(t *testing.T) {
	for name, tc := range map[string]struct {
		src                          string
		defines, funcs, consts, uses string
	}{
		"variables":  {"integer :: a = 1\nreal(8) :: b", "a,b", "", "", ""},
		"statements": {"integer :: a\na = 2\nprint *, a", "a", "", "", ""},
		"parameter":  {"integer, parameter :: n = 3\nreal :: x", "n,x", "", "n", ""},
		"subroutine": {"integer :: k = 1\nsubroutine f(x)\n  integer :: x\n  x = x + k\nend subroutine", "k,f", "f", "", ""},
		"function":   {"function g(x) result(r)\n  integer :: x, r\n  r = x\nend function", "g", "g", "", ""},
		"type":       {"type :: point\n  real :: x, y\nend type", "point", "", "point", ""},
		"module":     {"module m\n  integer :: v = 1\nend module", "m", "", "m", ""},
		"case":       {"INTEGER :: Mixed", "mixed", "", "", ""},
		"empty":      {"", "", "", "", ""},
		"comment":    {"! nothing", "", "", "", ""},
	} {
		c := analyzeFortranAlone(2, tc.src)
		if len(c.Errors) != 0 {
			t.Errorf("%s: errors %+v", name, c.Errors)
			continue
		}
		if c.Lang != analysis.Fortran || c.Index != 2 || c.Source != tc.src || names(c.Defines) != tc.defines || names(c.Funcs) != tc.funcs || names(c.Consts) != tc.consts || names(c.Uses) != tc.uses {
			t.Errorf("%s: defines %q funcs %q consts %q uses %q", name, names(c.Defines), names(c.Funcs), names(c.Consts), names(c.Uses))
		}
		if c.Dynamic {
			t.Errorf("%s: a Fortran cell is never dynamic", name)
		}
	}
}

// Cells are not independent: a later cell sees the earlier ones' variables,
// procedures, types and modules through the session; an error leaves the
// state as it was.
func TestFortranSequence(t *testing.T) {
	c0, s := analyzeFortranCell(0, "integer :: n = 3\ntype :: p\n  integer :: i\nend type", nil)
	c1, s1 := analyzeFortranCell(1, "integer :: m\nm = n + 1", s)
	if len(c0.Errors)+len(c1.Errors) != 0 || names(c1.Defines) != "m" || names(c1.Uses) != "n" {
		t.Fatalf("%+v %+v", c0, c1)
	}
	c2, s2 := analyzeFortranCell(2, "type(p) :: q\nq%i = n", s1)
	if len(c2.Errors) != 0 || names(c2.Defines) != "q" || names(c2.Uses) != "n" { // a type reference is not in the front end's Uses yet
		t.Fatalf("%+v", c2)
	}
	// Alone, the same cell is an error: n and p are undefined.
	if alone := analyzeFortranAlone(2, "type(p) :: q\nq%i = n"); len(alone.Errors) == 0 {
		t.Error("the cell is fine without the session")
	}
	// A cell with an error leaves the state as it was.
	bad, sb := analyzeFortranCell(3, "integer :: z\nz = undefined_name", s2)
	if len(bad.Errors) == 0 || len(bad.Defines) != 0 || sb != s2 {
		t.Fatalf("%+v", bad)
	}
	if ok, _ := analyzeFortranCell(4, "integer :: w\nw = m", sb); len(ok.Errors) != 0 {
		t.Errorf("%+v", ok.Errors)
	}
}

func TestFortranDiagnostics(t *testing.T) {
	// The byte offset of the line and column the front end reports.
	src := "integer :: a\ninteger :: b = ;\n"
	c := analyzeFortranAlone(3, src)
	if len(c.Errors) == 0 || c.Errors[0].Cell != 3 || c.Errors[0].Pos < strings.Index(src, "\ninteger :: b")+1 || c.Errors[0].Pos > len(src) {
		t.Fatalf("%+v", c.Errors)
	}
	if len(c.Defines) != 0 {
		t.Errorf("a cell with errors defines %v", c.Defines)
	}
	for _, tc := range []struct {
		src  string
		line int
		col  int
		want int
	}{{"ab\ncd", 1, 2, 1}, {"ab\ncd", 2, 2, 4}, {"ab\ncd", 9, 1, 5}, {"ab", 0, 0, 0}, {"é\nx", 2, 1, 3}} {
		got := fortranOffset(tc.src, positionOf(tc.line, tc.col))
		if got != tc.want {
			t.Errorf("%q %d:%d = %d, want %d", tc.src, tc.line, tc.col, got, tc.want)
		}
	}
	// INCLUDE reads no file: it is refused.
	if c := analyzeFortranAlone(0, "include 'x.inc'\n"); len(c.Errors) == 0 || !strings.Contains(c.Errors[0].Msg, "include") {
		t.Errorf("include: %+v", c.Errors)
	}
}

func TestFortranLimits(t *testing.T) {
	big := analyzeFortranAlone(0, "integer :: a\n"+strings.Repeat("! pad\n", fortranMaxBytes/6+1))
	deep := analyzeFortranAlone(0, "integer :: a = "+strings.Repeat("(", 5000)+"1"+strings.Repeat(")", 5000))
	for name, c := range map[string]*analysis.Cell{"big": big, "deep": deep} {
		if len(c.Errors) == 0 || len(c.Defines) != 0 {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if !strings.Contains(big.Errors[0].Msg, "limit") {
		t.Errorf("big: %+v", big.Errors)
	}
}

// A panic of the front end is an error of the cell and leaves the state.
func TestFortranPanic(t *testing.T) {
	old := fortranAnalyze
	t.Cleanup(func() { fortranAnalyze = old })
	fortranAnalyze = func(*sema.Session, any, int, string) (*sema.CellInfo, any) { panic("boom") }
	state := any("state")
	c, next := analyzeFortranCell(1, "integer :: a", state)
	if len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "Fortran parser failed") || next != state {
		t.Errorf("%+v", c)
	}
}

// The registration: the language, its run capability, and which names cross.
func TestFortranRegistered(t *testing.T) {
	g := analysis.AnalyzeSources([]analysis.Source{
		{Lang: analysis.Fortran, Source: "integer :: n = 2\ninteger, parameter :: k = 1\nsubroutine s()\nend subroutine"},
		{Lang: analysis.Fortran, Source: "integer :: m\nm = n"},
		{Lang: analysis.Go, Source: "x := 1"},
	})
	for _, c := range g.Cells {
		if len(c.Errors) != 0 {
			t.Fatalf("%+v", c.Errors)
		}
	}
	owner := g.Cells[0]
	if names(owner.Defines) != "k,n,s" || names(owner.Funcs) != "s" || names(owner.Consts) != "k" {
		t.Fatalf("%+v", owner)
	}
	// Variables cross; procedures and parameters stay Fortran's.
	goCell := &analysis.Cell{Lang: analysis.Go}
	if !analysis.Shared(owner, goCell, "n") || analysis.Shared(owner, goCell, "s") || analysis.Shared(owner, goCell, "k") {
		t.Error("crossing")
	}
	if len(g.Cells[1].Uses) != 1 || g.Cells[1].Uses[0] != "n" {
		t.Errorf("uses %v", g.Cells[1].Uses)
	}
	// One definition per name, as for every language of the notebook.
	dup := analysis.AnalyzeSources([]analysis.Source{{Lang: analysis.Fortran, Source: "integer :: n"}, {Lang: analysis.Fortran, Source: "integer :: n"}})
	if len(dup.Errors) != 1 || !strings.Contains(dup.Errors[0].Msg, "n is already defined by cell 0") {
		t.Error("a second definition of n is accepted")
	}
	run := analysis.RunOf(analysis.Fortran)
	if run.Runs || run.NotRun != analysis.FortranNotRun || run.Blocked(1) != analysis.FortranBlocked(1) {
		t.Errorf("run %+v", run)
	}
	if h, ok := analysis.Lookup(analysis.Fortran).(analysis.Highlighter); !ok || h.Lexer() != "fortran" {
		t.Error("Fortran is highlighted with go-rouge's fortran lexer")
	}
}
