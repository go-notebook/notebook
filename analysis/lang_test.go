package analysis

import (
	"errors"
	"reflect"
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

func register(t *testing.T, l Language) {
	t.Helper()
	Register(l)
	t.Cleanup(func() { Unregister(l.Name()) })
}

func TestRegistry(t *testing.T) {
	if got := Languages(); !reflect.DeepEqual(got, []string{"go", "julia", "ruby"}) {
		t.Fatalf("Languages = %v", got)
	}
	for _, n := range []string{"go", "ruby", "julia", ""} {
		if Lookup(n) == nil {
			t.Errorf("Lookup(%q) = nil", n)
		}
	}
	if Lookup("").Name() != "go" {
		t.Error(`"" is not Go`)
	}
	for _, n := range []string{"r", "c", "Go", "go ", "rb\x00", strings.Repeat("x", 1<<16), "\xff\xfe", "../etc", "ruby,julia"} {
		if Lookup(n) != nil {
			t.Errorf("Lookup(%q) found a language", n)
		}
	}
	register(t, fakeLang{name: "fake"})
	if Lookup("fake") == nil || len(Languages()) != 4 {
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
	for lang, lexer := range map[string]string{"go": "go", "ruby": "ruby"} {
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

func TestUnknownLanguageIsAnalysedAsGo(t *testing.T) {
	g := AnalyzeSources([]Source{{Lang: "nonesuch", Source: "x := 1"}, {Source: "y := x"}})
	if g.Cells[0].Lang != Go || !reflect.DeepEqual(g.Deps[1], []int{0}) {
		t.Errorf("%+v %v", g.Cells[0], g.Deps)
	}
}

func TestSizeLimit(t *testing.T) {
	huge := strings.Repeat(" ", DefaultLimits.MaxBytes+1)
	for _, lang := range []string{Go, Ruby, Julia, "nonesuch"} {
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
