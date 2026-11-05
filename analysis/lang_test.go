package analysis

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeLang is a test language with no parser: each line of a cell is
// "def NAME", "use NAME", "dynamic", "bad" (a diagnostic) or "fail" (an error).
// It implements only Language, so the graph's generic path is the one tested.
type fakeLang struct {
	name   string
	crosse func(owner, user *Cell, name string) bool
	run    Run
}

func (f fakeLang) Name() string { return f.name }
func (f fakeLang) Run() Run     { return f.run }
func (f fakeLang) Crosses(owner, user *Cell, name string) bool {
	return f.crosse == nil || f.crosse(owner, user, name)
}

func (f fakeLang) Analyze(src string, lim Limits) (Names, []Diagnostic, error) {
	if len(src) > lim.MaxBytes {
		return Names{}, nil, errors.New("too big")
	}
	var n Names
	var d []Diagnostic
	for _, line := range strings.Split(src, "\n") {
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "def":
			n.Defines = append(n.Defines, arg)
		case "use":
			n.Uses = append(n.Uses, arg)
		case "dynamic":
			n.Dynamic = true
		case "bad":
			d = append(d, Diagnostic{Pos: 3, Msg: "bad line"})
		case "fail":
			return n, d, errors.New("cannot analyse")
		}
	}
	return n, d, nil
}

// The analysis tests analyse Julia and R cells, as a program importing
// analysis/parsers does; TestParserMissing takes the parsers away.
func init() { EnableParsers() }

// Without analysis/parsers, Julia, R and C stay registered, with their run
// capability (R runs, Julia and C do not yet), and their cells fail with ParserMissing; nothing else changes.
func TestParserMissing(t *testing.T) {
	oldC := cParser
	juliaParser, rParser, cParser = nil, nil, nil
	t.Cleanup(func() { EnableParsers(); cParser = oldC })
	if !slices.Contains(Languages(), Julia) || !slices.Contains(Languages(), R) || !slices.Contains(Languages(), C) || !RunOf(R).Runs || RunOf(C).Runs {
		t.Fatalf("languages %v", Languages())
	}
	g := AnalyzeSources([]Source{{Lang: Julia, Source: "x = 1"}, {Lang: R, Source: "y <- 1"}, {Lang: C, Source: "int w;"}, {Lang: Go, Source: "z := 1"}})
	for i, lang := range []string{"Julia", "R", "C"} {
		c := g.Cells[i]
		if len(c.Errors) != 1 || c.Errors[0].Msg != ParserMissing(lang) || c.Errors[0].Cell != i || len(c.Defines) != 0 {
			t.Errorf("%s cell %+v", lang, c)
		}
	}
	if len(g.Cells[3].Errors) != 0 || !slices.Equal(g.Cells[3].Defines, []string{"z"}) {
		t.Errorf("Go cell %+v", g.Cells[3])
	}
	if ParserMissing("R") != "this program has no R parser: it must import github.com/go-notebook/notebook/analysis/parsers" {
		t.Error(ParserMissing("R"))
	}
}

func register(t *testing.T, l Language) {
	t.Helper()
	Register(l)
	t.Cleanup(func() { Unregister(l.Name()) })
}

func TestRegistry(t *testing.T) {
	if got := Languages(); !reflect.DeepEqual(got, []string{"c", "go", "julia", "r", "ruby"}) {
		t.Fatalf("Languages = %v", got)
	}
	for _, n := range []string{"go", "ruby", "julia", "r", "c", ""} {
		if Lookup(n) == nil {
			t.Errorf("Lookup(%q) = nil", n)
		}
	}
	if Lookup("").Name() != "go" {
		t.Error(`"" is not Go`)
	}
	for _, n := range []string{"R", "C", "Go", "go ", "rb\x00", strings.Repeat("x", 1<<16), "\xff\xfe", "../etc", "ruby,julia"} {
		if Lookup(n) != nil {
			t.Errorf("Lookup(%q) found a language", n)
		}
	}
	register(t, fakeLang{name: "fake"})
	if Lookup("fake") == nil || len(Languages()) != 6 {
		t.Error("fake not registered")
	}
	mustPanic(t, "duplicate", func() { Register(fakeLang{name: "fake"}) })
	mustPanic(t, "empty", func() { Register(fakeLang{}) })
	Unregister("fake")
	if Lookup("fake") != nil {
		t.Error("Unregister did not remove it")
	}
}

func mustPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", what)
		}
	}()
	f()
}

func TestRunOf(t *testing.T) {
	if !RunOf("go").Runs || !RunOf("ruby").Runs || !RunOf("nonesuch").Runs {
		t.Error("Go, Ruby and an unknown language run")
	}
	j := RunOf("julia")
	if j.Runs || j.NotRun != JuliaNotRun || !strings.Contains(j.NotRun, "M3") {
		t.Errorf("Julia: %+v", j)
	}
	if got := j.Blocked(2); got != JuliaBlocked(2) {
		t.Errorf("Blocked = %q", got)
	}
	if got := j.Reads(2); got != "not run: it reads cell 2, a Julia cell, and Julia cells are not run yet" {
		t.Errorf("Reads = %q", got)
	}
	if got := j.ReadsBlocked(2); got != "not run: it reads cell 2, which is blocked by a Julia cell" {
		t.Errorf("ReadsBlocked = %q", got)
	}
}

func TestCapabilities(t *testing.T) {
	if !LearnsTypes("ruby") || LearnsTypes("go") || LearnsTypes("julia") || LearnsTypes("nonesuch") {
		t.Error("only Ruby learns its types")
	}
	if _, ok := Lookup("ruby").(Runtime); !ok {
		t.Error("Ruby links a package")
	}
	if m := Lookup("ruby").(Runtime).Runtime(); m.Path != RubyPath || m.Module != RubyModule || m.Version != DefaultRubyVersion {
		t.Errorf("module %+v", m)
	}
	for lang, lexer := range map[string]string{"go": "go", "ruby": "ruby", "c": "c"} {
		if h, ok := Lookup(lang).(Highlighter); !ok || h.Lexer() != lexer {
			t.Errorf("%s lexer", lang)
		}
	}
	if _, ok := Lookup("julia").(Highlighter); ok {
		t.Error("Julia has no lexer")
	}
}

func TestBuiltinAnalyze(t *testing.T) {
	n, diags, err := Lookup("ruby").Analyze("a = 1\nb = a\n", DefaultLimits)
	if err != nil || len(diags) != 0 || !reflect.DeepEqual(n.Defines, []string{"a", "b"}) || n.Dynamic {
		t.Errorf("ruby: %+v %v %v", n, diags, err)
	}
	_, diags, _ = Lookup("go").Analyze("x := ", DefaultLimits)
	if len(diags) == 0 {
		t.Error("a Go syntax error is a diagnostic")
	}
	if _, _, err = Lookup("julia").Analyze("xx", Limits{MaxBytes: 1}); err == nil {
		t.Error("the size limit is not enforced")
	}
}

func TestGenericLanguageInGraph(t *testing.T) {
	register(t, fakeLang{name: "fake"})
	g := AnalyzeSources([]Source{
		{Lang: "fake", Source: "def a\nuse z"},
		{Lang: Go, Source: "b := a"},
		{Lang: "fake", Source: "bad"},
		{Lang: "fake", Source: "fail"},
	})
	c := g.Cells[0]
	if c.Lang != "fake" || c.Index != 0 || !reflect.DeepEqual(c.Defines, []string{"a"}) || !reflect.DeepEqual(c.Uses, []string{"z"}) || c.Imports == nil {
		t.Errorf("cell 0: %+v", c)
	}
	if !reflect.DeepEqual(g.Deps[1], []int{0}) {
		t.Errorf("Go cell reads the fake cell: deps %v", g.Deps[1])
	}
	if len(g.Errors) != 2 || g.Errors[0] != (Error{Cell: 2, Pos: 3, Msg: "bad line"}) || g.Errors[1] != (Error{Cell: 3, Msg: "cannot analyse"}) {
		t.Errorf("errors: %v", g.Errors)
	}
}

func TestCrossingRuleIsTheOwners(t *testing.T) {
	register(t, fakeLang{name: "fake", crosse: func(_, user *Cell, name string) bool { return name != "hidden" && user.Lang == Go }})
	g := AnalyzeSources([]Source{
		{Lang: "fake", Source: "def seen\ndef hidden"},
		{Lang: Go, Source: "x := seen\ny := hidden"},
		{Lang: Ruby, Source: "z = seen"},
	})
	if !reflect.DeepEqual(g.Deps[1], []int{0}) {
		t.Errorf("Go sees seen: %v", g.Deps[1])
	}
	if len(g.Deps[2]) != 0 {
		t.Errorf("Ruby sees nothing of fake: %v", g.Deps[2])
	}
	// Same language: always shared. Nil owner language: never.
	if !Shared(&Cell{Lang: "fake"}, &Cell{Lang: "fake"}, "hidden") {
		t.Error("within a language every name is shared")
	}
	if Shared(&Cell{Lang: "nonesuch"}, &Cell{Lang: Go}, "a") {
		t.Error("an unknown owner shares nothing")
	}
	// A user of an unknown language has no name rules.
	if !Shared(&Cell{Lang: Ruby}, &Cell{Lang: "nonesuch"}, "len") {
		t.Error("an unknown user accepts a Ruby local")
	}
	if Shared(&Cell{Lang: Ruby}, &Cell{Lang: Go}, "len") || !Shared(&Cell{Lang: Ruby}, &Cell{Lang: Julia}, "len") {
		t.Error("Go names are checked, Julia's are not")
	}
}

// seqLang is a fake Sequential language: each cell's state is the list of the
// names defined by the cells before it. A line is "def NAME", "use NAME" (an
// error unless an earlier cell defined NAME) or "bad" (an error).
type seqLang struct {
	fakeLang
	begun *int
}

func (s seqLang) Begin() any {
	*s.begun++
	return []string{}
}

func (s seqLang) AnalyzeNext(state any, index int, src string) (*Cell, any) {
	known := state.([]string)
	c := &Cell{Index: index, Source: src, Lang: s.name, Imports: map[string]string{}}
	next := slices.Clone(known)
	for _, line := range strings.Split(src, "\n") {
		verb, arg, _ := strings.Cut(line, " ")
		switch verb {
		case "def":
			c.Defines = append(c.Defines, arg)
			next = append(next, arg)
		case "use":
			if !slices.Contains(known, arg) {
				c.Errors = append(c.Errors, Error{Cell: index, Msg: arg + " is not defined"})
			}
			c.Uses = append(c.Uses, arg)
		case "bad":
			c.Errors = append(c.Errors, Error{Cell: index, Msg: "bad line"})
		}
	}
	if len(c.Errors) > 0 {
		return c, state // an error extends nothing
	}
	return c, next
}

// The graph threads a Sequential language's state through its cells, in
// notebook order, one state per language; the other languages are untouched.
func TestSequentialLanguage(t *testing.T) {
	var begun, otherBegun int
	register(t, seqLang{fakeLang: fakeLang{name: "seq"}, begun: &begun})
	register(t, seqLang{fakeLang: fakeLang{name: "seq2"}, begun: &otherBegun})
	g := AnalyzeSources([]Source{
		{Lang: "seq", Source: "use a"}, // nothing defined yet
		{Lang: "seq", Source: "def a"},
		{Lang: Go, Source: "x := 1"},
		{Lang: "seq", Source: "use a\ndef b"}, // sees a
		{Lang: "seq2", Source: "use a"},       // its own state: not a
		{Lang: "seq", Source: "def c\nbad"},   // an error: c is not kept
		{Lang: "seq", Source: "use c"},
		{Lang: "seq", Source: "use b"},
	})
	var failed []int
	for _, e := range g.Errors {
		failed = append(failed, e.Cell)
	}
	if !slices.Equal(failed, []int{0, 4, 5, 6}) {
		t.Errorf("errors %+v", g.Errors)
	}
	if begun != 1 || otherBegun != 1 {
		t.Errorf("Begin called %d and %d times, want once per language", begun, otherBegun)
	}
	if !slices.Equal(g.Deps[3], []int{1}) || !slices.Equal(g.Deps[7], []int{3}) {
		t.Errorf("deps %v", g.Deps)
	}
	if g.Cells[2].Lang != Go || len(g.Cells[2].Errors) != 0 {
		t.Errorf("Go cell %+v", g.Cells[2])
	}
	// A cell too big is refused without a call, and extends nothing.
	huge := strings.Repeat(" ", DefaultLimits.MaxBytes+1)
	g = AnalyzeSources([]Source{{Lang: "seq", Source: "def a"}, {Lang: "seq", Source: huge}, {Lang: "seq", Source: "use a"}})
	if len(g.Errors) != 1 || g.Errors[0].Cell != 1 || !strings.Contains(g.Errors[0].Msg, "limit") {
		t.Errorf("huge: %+v", g.Errors)
	}
	// One cell at a time (analyzeIn) starts from Begin.
	if c := analyzeIn("seq", 0, "use a"); len(c.Errors) != 1 {
		t.Errorf("alone: %+v", c)
	}
}

func TestUnknownLanguageIsAnalysedAsGo(t *testing.T) {
	g := AnalyzeSources([]Source{{Lang: "nonesuch", Source: "x := 1"}, {Source: "y := x"}})
	if g.Cells[0].Lang != Go || !reflect.DeepEqual(g.Deps[1], []int{0}) {
		t.Errorf("%+v %v", g.Cells[0], g.Deps)
	}
}

func TestSizeLimit(t *testing.T) {
	huge := strings.Repeat(" ", DefaultLimits.MaxBytes+1)
	for _, lang := range []string{Go, Ruby, Julia, C, "nonesuch"} {
		g := AnalyzeSources([]Source{{Lang: lang, Source: huge}})
		if len(g.Errors) != 1 || !strings.Contains(g.Errors[0].Msg, "limit") {
			t.Errorf("%s: %v", lang, g.Errors)
		}
	}
}

// dyn builds notebook: cell 0 reads, 1 defines p, 2 is dynamic, 3 reads,
// 4 reads nothing, 5 is dynamic.
func dynGraph() *Graph {
	return AnalyzeSources([]Source{
		{Lang: "fake", Source: "use q"},
		{Lang: "fake", Source: "def p"},
		{Lang: "fake", Source: "dynamic\ndef q"},
		{Lang: "fake", Source: "use p"},
		{Lang: "fake", Source: "def w"},
		{Lang: "fake", Source: "dynamic"},
	})
}

func TestDynamicCells(t *testing.T) {
	register(t, fakeLang{name: "fake"})
	g := dynGraph()
	// Cell 2 depends on every earlier cell; 0 reads q, which cell 2 defines,
	// so 0 and 2 are on a cycle (an honest error, not a silent order).
	if !reflect.DeepEqual(g.Deps[2], []int{0, 1}) {
		t.Errorf("Deps[2] = %v", g.Deps[2])
	}
	// A later cell that reads any name depends on the dynamic cell.
	if !reflect.DeepEqual(g.Deps[3], []int{1, 2}) {
		t.Errorf("Deps[3] = %v", g.Deps[3])
	}
	// A later cell that reads nothing does not.
	if len(g.Deps[4]) != 0 {
		t.Errorf("Deps[4] = %v", g.Deps[4])
	}
	if !reflect.DeepEqual(g.Deps[5], []int{0, 1, 2, 3, 4}) {
		t.Errorf("Deps[5] = %v", g.Deps[5])
	}
	if !reflect.DeepEqual(g.Dependents[2], []int{0, 3, 5}) {
		t.Errorf("Dependents[2] = %v", g.Dependents[2])
	}
	if len(g.Errors) == 0 {
		t.Error("the 0 <-> 2 cycle is reported")
	}
}

func TestDynamicInvalidatesLaterReaders(t *testing.T) {
	register(t, fakeLang{name: "fake"})
	g := AnalyzeSources([]Source{
		{Lang: "fake", Source: "def a"},
		{Lang: "fake", Source: "dynamic"},
		{Lang: "fake", Source: "use anything"},
		{Lang: "fake", Source: "def lone"},
	})
	if len(g.Errors) != 0 {
		t.Fatal(g.Errors)
	}
	if !reflect.DeepEqual(g.Order, []int{0, 1, 2, 3}) {
		t.Errorf("Order = %v", g.Order)
	}
	// Changing the first cell re-runs the dynamic cell and its readers, not
	// the cell that reads nothing.
	if got := g.Downstream(0); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("Downstream(0) = %v", got)
	}
	// Changing the dynamic cell re-runs the later readers only.
	if got := g.Downstream(1); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Errorf("Downstream(1) = %v", got)
	}
	// Running the last reader needs everything before it through the
	// dynamic cell.
	if got := g.Upstream(2); !reflect.DeepEqual(got, []int{0, 1, 2}) {
		t.Errorf("Upstream(2) = %v", got)
	}
}

func TestDynamicAlongsideOrdinaryEdges(t *testing.T) {
	register(t, fakeLang{name: "fake"})
	g := AnalyzeSources([]Source{
		{Lang: "fake", Source: "def a"},
		{Lang: "fake", Source: "use a\ndynamic"}, // already depends on 0: no duplicate edge
	})
	if !reflect.DeepEqual(g.Deps[1], []int{0}) || !reflect.DeepEqual(g.Dependents[0], []int{1}) {
		t.Errorf("%v %v", g.Deps, g.Dependents)
	}
}

// FuzzDispatch drives the dispatch with arbitrary fence words and sources:
// an unknown or hostile language is analysed as Go and never panics, and a
// known one never leaves a nil cell.
func FuzzDispatch(f *testing.F) {
	for _, w := range []string{"go", "ruby", "julia", "", "R", "c", "\x00", "ruby\n", "__proto__", "../../x", "go ruby"} {
		f.Add(w, "x := 1\nx")
	}
	f.Fuzz(func(t *testing.T, lang, src string) {
		_ = Lookup(lang)
		_ = RunOf(lang)
		_ = LearnsTypes(lang)
		g := AnalyzeSources([]Source{{Lang: lang, Source: src}, {Lang: lang, Source: src}})
		if len(g.Cells) != 2 || g.Cells[0] == nil || len(g.Deps) != 2 {
			t.Fatalf("lang %q: %+v", lang, g)
		}
		if Lookup(lang) == nil && g.Cells[0].Lang != Go {
			t.Fatalf("unknown language %q not analysed as Go: %q", lang, g.Cells[0].Lang)
		}
	})
}
