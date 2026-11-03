package analysis

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-embedded-r/r/syntax"
)

func TestAnalyzeRCell(t *testing.T) {
	for name, tc := range map[string]struct {
		src                  string
		defines, uses, funcs string
		dynamic              bool
	}{
		"assignments":      {"x <- 1\ny = 2\n3 -> z", "x,y,z", "", "", false},
		"reads":            {"total <- sum(x) + n", "total", "n,sum,x", "", false},
		"function":         {"f <- function(a, b = k) a + b + g\nh = \\(v) v", "f,h", "g,k", "f,h", false},
		"function value":   {"f <- function(a) a\nf <- 1", "f", "", "f", false},
		"not a function":   {"f <- 1\ng <- f\nx$y <- 2", "f,g,x", "x", "", false}, // x$y <- 2 modifies an x
		"for loop":         {"for (i in seq_len(n)) s <- s + i", "i,s", "n,s,seq_len", "", false},
		"local scope":      {"f <- function() { tmp <- 1; tmp }", "f", "", "f", false},
		"super assignment": {"x <<- 1", "", "", "", true},
		// library, assign and replacement functions are what the names pass
		// reads as R's binding forms, not as names the cell uses.
		"library":          {"library(stats)\ny <- 1", "y", "", "", true},
		"computed assign":  {"assign(paste0(\"v\", 1), 2)", "", "paste0", "", true},
		"literal assign":   {"assign(\"v\", 2)", "v", "", "", false},
		"assign in a call": {"names(x) <- c(\"a\")", "x", "c,x", "", false},
		"empty":            {"", "", "", "", false},
		"comment only":     {"# nothing", "", "", "", false},
	} {
		c := AnalyzeRCell(2, tc.src)
		if len(c.Errors) != 0 {
			t.Errorf("%s: errors %+v", name, c.Errors)
			continue
		}
		if c.Lang != R || c.Index != 2 || names(c.Defines) != tc.defines || names(c.Uses) != tc.uses || names(c.Funcs) != tc.funcs || c.Dynamic != tc.dynamic {
			t.Errorf("%s: defines %q uses %q funcs %q dynamic %v", name, names(c.Defines), names(c.Uses), names(c.Funcs), c.Dynamic)
		}
	}
}

func TestRErrors(t *testing.T) {
	// R's own message, at the byte the line and column point to.
	c := AnalyzeRCell(3, "y <- 2\nx <- y y")
	if len(c.Errors) != 1 || c.Errors[0].Cell != 3 || c.Errors[0].Pos != 14 || c.Errors[0].Msg != "syntax error: unexpected symbol" {
		t.Errorf("%+v", c.Errors)
	}
	big := AnalyzeRCell(0, strings.Repeat("x <- 1\n", rLimits.MaxBytes/7+1))
	deep := AnalyzeRCell(0, strings.Repeat("-", 5000)+"1")
	for name, c := range map[string]*Cell{"big": big, "deep": deep} {
		if len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "too large or too deeply nested") {
			t.Errorf("%s: %+v", name, c.Errors)
		}
	}
	// Brackets nest only as deep as R's own context stack: R's message.
	if c := AnalyzeRCell(0, strings.Repeat("(", 5000)); len(c.Errors) != 1 || c.Errors[0].Msg != "syntax error: contextstack overflow" {
		t.Errorf("brackets: %+v", c.Errors)
	}
}

func TestRParserSeams(t *testing.T) {
	old := rParse
	t.Cleanup(func() { rParse = old })
	rParse = func(string, syntax.Limits) (*syntax.File, error) { panic("boom") }
	if c := AnalyzeRCell(0, "x"); len(c.Errors) != 1 || c.Errors[0].Msg != "the R parser failed on this cell; please report it" {
		t.Errorf("panic: %+v", c.Errors)
	}
	rParse = func(string, syntax.Limits) (*syntax.File, error) { return nil, errors.New("other") }
	if c := AnalyzeRCell(0, "x"); len(c.Errors) != 1 || c.Errors[0].Msg != "other" {
		t.Errorf("other error: %+v", c.Errors)
	}
}

// Top-level shapes that are not f <- function: none is a function definition.
func TestRFuncDef(t *testing.T) {
	f, err := syntax.ParseAll("f\nf(1, 2)\nf + function() 1\nf[1] <- function() 1\nf <- 1\n`<-`(f)")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Exprs {
		if name, ok := rFuncDef(e); ok {
			t.Errorf("%s is a function definition", name)
		}
	}
}

func TestROffset(t *testing.T) {
	src := "ab\n\tcé d\n"
	for _, tc := range []struct{ line, col, want int }{
		{1, 1, 0}, {1, 2, 1}, {2, 0, 9}, {2, 8, 3}, {2, 9, 4}, {2, 10, 5}, {2, 11, 7}, {2, 12, 8}, {2, 99, 9}, {9, 1, 10},
	} {
		if got := rOffset(src, tc.line, tc.col); got != tc.want {
			t.Errorf("%d:%d = %d, want %d", tc.line, tc.col, got, tc.want)
		}
	}
	if got := rOffset("no newline", 1, 0); got != 10 {
		t.Errorf("end of a last line = %d", got)
	}
}

func TestRGraph(t *testing.T) {
	g := AnalyzeSources([]Source{
		{Lang: R, Source: "x <- 1\nf <- function(y) y"},
		{Lang: Go, Source: "z := x"},       // reads the R variable
		{Lang: Ruby, Source: "w = f(1)"},   // f is an R function: not shared
		{Lang: R, Source: "v <- z + w"},    // reads the Go and Ruby variables
		{Lang: R, Source: "assign(nm, 1)"}, // dynamic: depends on every earlier cell
		{Lang: Go, Source: "q := 1"},
	})
	if len(g.Errors) != 0 {
		t.Errorf("errors %+v", g.Errors)
	}
	if !slices.Equal(g.Deps[1], []int{0}) || len(g.Deps[2]) != 0 || !slices.Equal(g.Deps[3], []int{1, 2}) || !slices.Equal(g.Deps[4], []int{0, 1, 2, 3}) {
		t.Errorf("deps %v", g.Deps)
	}
	if RBlocked(4) != "blocked: it depends on cell 4, an R cell, and R cells are not run yet" {
		t.Error(RBlocked(4))
	}
	run := RunOf(R)
	if run.Runs || run.NotRun != RNotRun || run.Blocked(1) != RBlocked(1) ||
		run.Reads(2) != "not run: it reads cell 2, an R cell, and R cells are not run yet" ||
		run.ReadsBlocked(3) != "not run: it reads cell 3, which is blocked by an R cell" {
		t.Errorf("run %+v", run)
	}
	// An R variable named like a Go keyword stays out of Go's sight.
	g = AnalyzeSources([]Source{{Lang: R, Source: "type <- 1"}, {Lang: Go, Source: "_ = 1"}})
	if !Shared(g.Cells[0], &Cell{Lang: Ruby}, "type") || Shared(g.Cells[0], g.Cells[1], "type") {
		t.Error("type crosses to Go")
	}
	if _, ok := Lookup(R).(Highlighter); ok {
		t.Error("R is a Highlighter, but go-rouge has no R lexer")
	}
}
