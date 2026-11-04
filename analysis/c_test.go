package analysis

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-embedded-c/c/cell"
)

func TestAnalyzeCCell(t *testing.T) {
	for name, tc := range map[string]struct {
		src                          string
		defines, funcs, consts, uses string
	}{
		"variables":  {"int a = 1;\nint b;", "a,b", "", "", ""},
		"function":   {"int k;\nint f(int x) { return x + k; }", "k,f", "f", "", ""},
		"typedef":    {"typedef int myint;", "myint", "", "myint", ""},
		"tags":       {"struct s { int a; };\nunion u { int a; };\nenum e { A, B };", "struct s,union u,enum e,A,B", "", "struct s,union u,enum e,A,B", ""},
		"macro":      {"#define N 3\n#define SQ(x) ((x)*(x))", "N,SQ", "", "N,SQ", ""},
		"redeclared": {"int a;\nint a;", "a", "", "", ""},
		"empty":      {"", "", "", "", ""},
		"comment":    {"/* nothing */", "", "", "", ""},
		"statements": {"int a = 1;\na = a + 1;", "a", "", "", ""},
	} {
		c := AnalyzeCCell(2, tc.src)
		if len(c.Errors) != 0 {
			t.Errorf("%s: errors %+v", name, c.Errors)
			continue
		}
		if c.Lang != C || c.Index != 2 || c.Source != tc.src || names(c.Defines) != tc.defines || names(c.Funcs) != tc.funcs || names(c.Consts) != tc.consts || names(c.Uses) != tc.uses {
			t.Errorf("%s: defines %q funcs %q consts %q uses %q", name, names(c.Defines), names(c.Funcs), names(c.Consts), names(c.Uses))
		}
		if c.Dynamic {
			t.Errorf("%s: a C cell is never dynamic", name)
		}
	}
}

func TestCDiagnostics(t *testing.T) {
	// cc/v4's message, at the byte the line and column point to.
	c := AnalyzeCCell(3, "int a;\nint y = ;")
	if len(c.Errors) < 1 || c.Errors[0] != (Error{Cell: 3, Pos: 15, Msg: "unexpected ';', expected unary expression"}) {
		t.Errorf("%+v", c.Errors)
	}
	// An error outside the cell has Offset -1: position 0, and says so.
	var outside *Error
	for i, e := range c.Errors {
		if strings.HasPrefix(e.Msg, "outside the cell (in a header): ") {
			outside = &c.Errors[i]
		}
	}
	if outside == nil || outside.Pos != 0 || outside.Cell != 3 {
		t.Errorf("no error outside the cell: %+v", c.Errors)
	}
	if len(c.Defines) != 0 {
		t.Errorf("a cell with errors defines %v", c.Defines)
	}
	// A missing header is an error of the cell.
	if c := AnalyzeCCell(0, "#include <nonexistent.h>\n"); len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "no such header") {
		t.Errorf("header: %+v", c.Errors)
	}
}

func TestCLimits(t *testing.T) {
	big := AnalyzeCCell(0, "int a;\n"+strings.Repeat("/* pad */\n", cLimits.MaxBytes/10+1))
	deep := AnalyzeCCell(0, "int a = "+strings.Repeat("(", 300)+"1"+strings.Repeat(")", 300)+";")
	for name, c := range map[string]*Cell{"big": big, "deep": deep} {
		if len(c.Errors) == 0 || len(c.Defines) != 0 {
			t.Errorf("%s: %+v", name, c)
		}
	}
	if !strings.Contains(big.Errors[0].Msg, "limit") {
		t.Errorf("big: %+v", big.Errors)
	}
}

// A macro-expansion bomb is refused, in well under a second: 2^40 tokens
// would not fit in memory.
func TestCExpansionBomb(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("#define D0(a) ((a)+(a))\n")
	for i := 1; i <= 40; i++ {
		sb.WriteString("#define D" + strconv.Itoa(i) + "(a) D" + strconv.Itoa(i-1) + "(D" + strconv.Itoa(i-1) + "(a))\n")
	}
	sb.WriteString("int x = D40(1);\n")
	src := sb.String()
	done := make(chan *Cell, 1)
	go func() { done <- AnalyzeCCell(0, src) }()
	select {
	case c := <-done:
		if len(c.Errors) == 0 || !strings.Contains(c.Errors[0].Msg, "macro expansion exceeds") || len(c.Defines) != 0 {
			t.Errorf("not refused: %+v", c.Errors)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the expansion bomb was not refused in 30 s")
	}
}

func TestCFrontEndPanic(t *testing.T) {
	old := cAnalyze
	t.Cleanup(func() { cAnalyze = old })
	cAnalyze = func([]byte, cell.Context, cell.Limits) *cell.Analysis { panic("boom") }
	c, st := analyzeCCell(1, "int a;", "state")
	if len(c.Errors) != 1 || c.Errors[0] != (Error{Cell: 1, Msg: "the C parser failed on this cell; please report it"}) || st != "state" {
		t.Errorf("%+v %v", c.Errors, st)
	}
}

// A cell sees the typedefs and macros of the C cells before it, and a cell
// with an error leaves nothing behind.
func TestCSequence(t *testing.T) {
	one := "typedef int myint;\n#define N 3\nint base = 1;"
	two := "myint v = N + base;"
	if c := AnalyzeCCell(1, two); len(c.Errors) == 0 {
		t.Fatal("cell 2 alone has no error")
	}
	g := AnalyzeSources([]Source{
		{Lang: C, Source: one},
		{Lang: C, Source: two},
		{Lang: C, Source: "myint broken = ;"}, // an error: defines nothing, extends nothing
		{Lang: C, Source: "myint w = v;"},
	})
	if len(g.Errors) == 0 || g.Errors[0].Cell != 2 {
		t.Fatalf("errors %+v", g.Errors)
	}
	for _, e := range g.Errors {
		if e.Cell != 2 {
			t.Errorf("error outside cell 2: %+v", e)
		}
	}
	if c := g.Cells[1]; len(c.Errors) != 0 || names(c.Defines) != "v" || names(c.Uses) != "N,base,myint" {
		t.Errorf("cell 1: %+v", c)
	}
	if c := g.Cells[3]; len(c.Errors) != 0 || names(c.Uses) != "myint,v" {
		t.Errorf("cell 3: %+v", c)
	}
	if !slices.Equal(g.Deps[1], []int{0}) || !slices.Equal(g.Deps[3], []int{0, 1}) || len(g.Deps[2]) != 0 {
		t.Errorf("deps %v", g.Deps)
	}
	// The same cells split by a cell of another language are the same.
	g = AnalyzeSources([]Source{{Lang: C, Source: one}, {Lang: Go, Source: "x := 1"}, {Lang: C, Source: two}})
	if len(g.Errors) != 0 || !slices.Equal(g.Deps[2], []int{0}) {
		t.Errorf("interleaved: %+v %v", g.Errors, g.Deps)
	}
	// A name is defined by one cell, here a prototype and its definition.
	g = AnalyzeSources([]Source{{Lang: C, Source: "int f(int);"}, {Lang: C, Source: "int f(int a) { return a; }"}})
	if len(g.Errors) != 1 || !strings.Contains(g.Errors[0].Msg, "f is already defined by cell 0") {
		t.Errorf("redefinition: %+v", g.Errors)
	}
}

func TestCGraph(t *testing.T) {
	g := AnalyzeSources([]Source{
		{Lang: C, Source: "int x = 1;\nint f(int a) { return a; }\ntypedef int myint;\n#define N 2"},
		{Lang: Go, Source: "z := x"},        // reads the C variable
		{Lang: Ruby, Source: "w = f(1)"},    // f is a C function: not shared
		{Lang: Julia, Source: "v = myint"},  // a typedef: not shared
		{Lang: R, Source: "u <- N"},         // a macro: not shared
		{Lang: C, Source: "int y = x + N;"}, // the same language sees everything
	})
	if len(g.Errors) != 0 {
		t.Errorf("errors %+v", g.Errors)
	}
	if !slices.Equal(g.Deps[1], []int{0}) || len(g.Deps[2]) != 0 || len(g.Deps[3]) != 0 || len(g.Deps[4]) != 0 || !slices.Equal(g.Deps[5], []int{0}) {
		t.Errorf("deps %v", g.Deps)
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
	// A C variable named like a Go keyword stays out of Go's sight.
	g = AnalyzeSources([]Source{{Lang: C, Source: "int type;"}, {Lang: Go, Source: "_ = 1"}})
	if !Shared(g.Cells[0], &Cell{Lang: Ruby}, "type") || Shared(g.Cells[0], g.Cells[1], "type") {
		t.Error("type crosses to Go")
	}
	if h, ok := Lookup(C).(Highlighter); !ok || h.Lexer() != "c" {
		t.Error("C is highlighted with go-rouge's c lexer")
	}
	// Language.Analyze is a cell alone, as the first C cell.
	n, diags, err := Lookup(C).Analyze("int a;", DefaultLimits)
	if err != nil || len(diags) != 0 || !slices.Equal(n.Defines, []string{"a"}) {
		t.Errorf("Analyze: %+v %v %v", n, diags, err)
	}
}
