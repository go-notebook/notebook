package parsers

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-embedded-c/c/cell"
	"github.com/go-notebook/notebook/analysis"
)

func names(l []string) string { return strings.Join(l, ",") }

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
		c := analyzeAlone(2, tc.src)
		if len(c.Errors) != 0 {
			t.Errorf("%s: errors %+v", name, c.Errors)
			continue
		}
		if c.Lang != analysis.C || c.Index != 2 || c.Source != tc.src || names(c.Defines) != tc.defines || names(c.Funcs) != tc.funcs || names(c.Consts) != tc.consts || names(c.Uses) != tc.uses {
			t.Errorf("%s: defines %q funcs %q consts %q uses %q", name, names(c.Defines), names(c.Funcs), names(c.Consts), names(c.Uses))
		}
		if c.Dynamic {
			t.Errorf("%s: a C cell is never dynamic", name)
		}
	}
}

func TestCDiagnostics(t *testing.T) {
	// cc/v4's message, at the byte the line and column point to.
	c := analyzeAlone(3, "int a;\nint y = ;")
	if len(c.Errors) < 1 || c.Errors[0] != (analysis.Error{Cell: 3, Pos: 15, Msg: "unexpected ';', expected unary expression"}) {
		t.Errorf("%+v", c.Errors)
	}
	// An error outside the cell has Offset -1: position 0, and says so.
	var outside *analysis.Error
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
	if c := analyzeAlone(0, "#include <nonexistent.h>\n"); len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "no such header") {
		t.Errorf("header: %+v", c.Errors)
	}
}

func TestCLimits(t *testing.T) {
	big := analyzeAlone(0, "int a;\n"+strings.Repeat("/* pad */\n", cLimits.MaxBytes/10+1))
	deep := analyzeAlone(0, "int a = "+strings.Repeat("(", 300)+"1"+strings.Repeat(")", 300)+";")
	for name, c := range map[string]*analysis.Cell{"big": big, "deep": deep} {
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
	done := make(chan *analysis.Cell, 1)
	go func() { done <- analyzeAlone(0, src) }()
	select {
	case c := <-done:
		if len(c.Errors) == 0 || !strings.Contains(c.Errors[0].Msg, "macro expansion exceeds") || len(c.Defines) != 0 {
			t.Errorf("not refused: %+v", c.Errors)
		}
	// The refusal is the assertion; the clock only stops a hang. Natively it
	// takes 0.43 s; under qemu riscv64 in CI it took over 30 s (#40, #42).
	case <-time.After(5 * time.Minute):
		t.Fatal("the expansion bomb was not refused in 5 min")
	}
}

// The invalid inputs go-embedded-c v0.2.1 fixed in its own front end (its
// FuzzSource corpus, c#15): hangs and panics on a few bytes. A notebook may
// come from anyone, so analysing one must end, with an error. Through the cell
// package they already ended on v0.2.0; this keeps it so.
func TestCShortCellsEnd(t *testing.T) {
	for _, src := range []string{
		"^", "\"\"\\", "0\n\\", "\"(0!0!\xd2\\", "0={#,", "#define M##\nM",
		"\"abc", "'a",
	} {
		done := make(chan *analysis.Cell, 1)
		go func() { done <- analyzeAlone(0, src) }()
		select {
		case c := <-done:
			if len(c.Errors) == 0 || len(c.Defines) != 0 {
				t.Errorf("%q: %+v", src, c)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%q: the C front end did not return in 10 s", src)
		}
	}
}

func TestCFrontEndPanic(t *testing.T) {
	old := cAnalyze
	t.Cleanup(func() { cAnalyze = old })
	cAnalyze = func([]byte, cell.Context, cell.Limits) *cell.Analysis { panic("boom") }
	c, st := analyzeCCell(1, "int a;", "state")
	if len(c.Errors) != 1 || c.Errors[0] != (analysis.Error{Cell: 1, Msg: "the C parser failed on this cell; please report it"}) || st != "state" {
		t.Errorf("%+v %v", c.Errors, st)
	}
}

// A cell sees the typedefs and macros of the C cells before it, and a cell
// with an error leaves nothing behind.
func TestCSequence(t *testing.T) {
	one := "typedef int myint;\n#define N 3\nint base = 1;"
	two := "myint v = N + base;"
	if c := analyzeAlone(1, two); len(c.Errors) == 0 {
		t.Fatal("cell 2 alone has no error")
	}
	g := analysis.AnalyzeSources([]analysis.Source{
		{Lang: analysis.C, Source: one},
		{Lang: analysis.C, Source: two},
		{Lang: analysis.C, Source: "myint broken = ;"}, // an error: defines nothing, extends nothing
		{Lang: analysis.C, Source: "myint w = v;"},
	})
	if len(g.Errors) == 0 {
		t.Fatal("no error")
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
	g = analysis.AnalyzeSources([]analysis.Source{{Lang: analysis.C, Source: one}, {Lang: analysis.Go, Source: "x := 1"}, {Lang: analysis.C, Source: two}})
	if len(g.Errors) != 0 || !slices.Equal(g.Deps[2], []int{0}) {
		t.Errorf("interleaved: %+v %v", g.Errors, g.Deps)
	}
}

func TestCGraph(t *testing.T) {
	g := analysis.AnalyzeSources([]analysis.Source{
		{Lang: analysis.C, Source: "int x = 1;\nint f(int a) { return a; }\ntypedef int myint;\n#define N 2"},
		{Lang: analysis.Go, Source: "z := x"},        // reads the C variable
		{Lang: analysis.Ruby, Source: "w = f(1)"},    // f is a C function: not shared
		{Lang: analysis.Julia, Source: "v = myint"},  // a typedef: not shared
		{Lang: analysis.R, Source: "u <- N"},         // a macro: not shared
		{Lang: analysis.C, Source: "int y = x + N;"}, // the same language sees everything
	})
	if len(g.Errors) != 0 {
		t.Errorf("errors %+v", g.Errors)
	}
	if !slices.Equal(g.Deps[1], []int{0}) || len(g.Deps[2]) != 0 || len(g.Deps[3]) != 0 || len(g.Deps[4]) != 0 || !slices.Equal(g.Deps[5], []int{0}) {
		t.Errorf("deps %v", g.Deps)
	}
	// A C variable named like a Go keyword stays out of Go's sight.
	g = analysis.AnalyzeSources([]analysis.Source{{Lang: analysis.C, Source: "int type;"}, {Lang: analysis.Go, Source: "_ = 1"}})
	if !analysis.Shared(g.Cells[0], &analysis.Cell{Lang: analysis.Ruby}, "type") || analysis.Shared(g.Cells[0], g.Cells[1], "type") {
		t.Error("type crosses to Go")
	}
	// Language.Analyze is a cell alone, as the first C cell.
	n, diags, err := analysis.Lookup(analysis.C).Analyze("int a;", analysis.DefaultLimits)
	if err != nil || len(diags) != 0 || !slices.Equal(n.Defines, []string{"a"}) {
		t.Errorf("Analyze: %+v %v %v", n, diags, err)
	}
}

// A name is defined by its definition, or failing that by its first tentative
// definition; a declaration (a prototype, an extern, a forward struct) reads
// the name, and is neither an owner nor in conflict. A second definition is
// go-notebook's rule, not C's.
func TestCDefinitions(t *testing.T) {
	for name, tc := range map[string]struct {
		cells    []string
		name     string
		owner    int
		readers  [][]int // Deps of each cell
		errCells []int
	}{
		"prototype then definition":      {[]string{"int f(int);", "int f(int a) { return a; }"}, "f", 1, [][]int{{1}, {}}, nil},
		"extern then definition":         {[]string{"extern int x;", "int x = 1;"}, "x", 1, [][]int{{1}, {}}, nil},
		"definition then extern":         {[]string{"int x = 1;", "extern int x;"}, "x", 0, [][]int{{}, {0}}, nil},
		"tentative then tentative":       {[]string{"int x;", "int x;"}, "x", 0, [][]int{{}, {}}, nil},
		"tentative then definition":      {[]string{"int x;", "int x = 1;"}, "x", 1, [][]int{{}, {}}, nil},
		"definition then tentative":      {[]string{"int x = 1;", "int x;"}, "x", 0, [][]int{{}, {}}, nil},
		"forward struct then struct":     {[]string{"struct s;", "struct s { int a; };"}, "struct s", 1, [][]int{{1}, {}}, nil},
		"second definition":              {[]string{"int x = 1;", "int x = 2;"}, "x", 0, [][]int{{}, {}}, []int{1}},
		"second function definition":     {[]string{"int f(void) { return 1; }", "int f(void) { return 2; }"}, "f", 0, [][]int{{}, {}}, []int{1}},
		"prototype after its definition": {[]string{"int f(int a) { return a; }", "int f(int);"}, "f", 0, [][]int{{}, {0}}, nil},
	} {
		srcs := make([]analysis.Source, len(tc.cells))
		for i, s := range tc.cells {
			srcs[i] = analysis.Source{Lang: analysis.C, Source: s}
		}
		g := analysis.AnalyzeSources(srcs)
		var errCells []int
		for _, e := range g.Errors {
			errCells = append(errCells, e.Cell)
			if !strings.Contains(e.Msg, "go-notebook's rule of one definition per name") {
				t.Errorf("%s: message %q", name, e.Msg)
			}
		}
		if !slices.Equal(errCells, tc.errCells) {
			t.Errorf("%s: errors %+v", name, g.Errors)
		}
		if g.Owner[tc.name] != tc.owner {
			t.Errorf("%s: %s is owned by cell %d, want %d", name, tc.name, g.Owner[tc.name], tc.owner)
		}
		for i, want := range tc.readers {
			if len(g.Deps[i]) != len(want) || (len(want) > 0 && !slices.Equal(g.Deps[i], want)) {
				t.Errorf("%s: deps of cell %d are %v, want %v", name, i, g.Deps[i], want)
			}
		}
	}
	// A prototype defines nothing; a tentative definition defines weakly.
	if c := analyzeAlone(0, "int f(int);\nint t;"); names(c.Defines) != "t" || names(c.Weak) != "t" || names(c.Uses) != "f" {
		t.Errorf("%+v", c)
	}
}
