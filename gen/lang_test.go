package gen

import (
	"fmt"
	"go/types"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
)

// checkOnlyLang is a language that never runs and defines the variable named
// by its cell's source; it is not Julia, so nothing here is Julia's.
type checkOnlyLang struct{}

func (checkOnlyLang) Name() string { return "chk" }
func (checkOnlyLang) Analyze(src string, _ analysis.Limits) (analysis.Names, []analysis.Diagnostic, error) {
	return analysis.Names{Defines: []string{strings.TrimSpace(src)}}, nil, nil
}
func (checkOnlyLang) Crosses(_, _ *analysis.Cell, _ string) bool { return true }
func (checkOnlyLang) Run() analysis.Run {
	return analysis.Run{
		NotRun:       "chk cells are only checked",
		Blocked:      func(c int) string { return fmt.Sprintf("blocked by chk %d", c) },
		Reads:        func(c int) string { return fmt.Sprintf("reads chk %d", c) },
		ReadsBlocked: func(c int) string { return fmt.Sprintf("reads a cell blocked by chk %d", c) },
	}
}

// fkLang is a language that runs: its cells define the variables named, one
// per line, as ints. It implements analysis.Runtime; its Backend is
// registered without editing gen.
type fkLang struct{}

func (fkLang) Name() string { return "fk" }
func (fkLang) Analyze(src string, _ analysis.Limits) (analysis.Names, []analysis.Diagnostic, error) {
	return analysis.Names{Defines: strings.Fields(src)}, nil, nil
}
func (fkLang) Crosses(_, _ *analysis.Cell, _ string) bool { return true }
func (fkLang) Run() analysis.Run                          { return analysis.Run{Runs: true} }
func (fkLang) Runtime() analysis.Module {
	return analysis.Module{Path: "example.com/fk/cells", Module: "example.com/fk", Version: "v1.0.0"}
}

type fkBackend struct{}

func (fkBackend) Infer(c *Context, i int) {
	for _, n := range c.Cell(i).Defines {
		c.Declare(Var{Name: n, Cell: i, Type: "int"})
	}
	c.Done(i)
}

func (fkBackend) Write(c *Context, b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_fkrun(%q, %q, %d)\n", c.FuncName(k), c.Path(), c.Code(k).Source, c.Code(k).Line)
	for _, v := range c.Vars() {
		if v.Cell == k {
			fmt.Fprintf(b, "\t%s = 1\n", v.Name)
		}
	}
	b.WriteString("}\n\n")
}

func (fkBackend) Runtime() (string, string) {
	return "_fk", "func _fkrun(string, string, int) { _fk.Mark() }\n\n"
}

type fkImporter struct {
	*testImporter
	pkg *types.Package
}

func (im fkImporter) Import(path string) (*types.Package, error) {
	if path == "example.com/fk/cells" {
		return im.pkg, nil
	}
	return im.testImporter.Import(path)
}

// A language that runs plugs in with a registered Backend alone.
func TestRegisteredBackend(t *testing.T) {
	analysis.Register(fkLang{})
	RegisterBackend("fk", fkBackend{})
	t.Cleanup(func() { analysis.Unregister("fk"); UnregisterBackend("fk") })
	mustPanic := func(f func()) {
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		f()
	}
	mustPanic(func() { RegisterBackend("fk", fkBackend{}) })
	mustPanic(func() { RegisterBackend("", fkBackend{}) })

	im := fkImporter{newImporter(t), check(t, "example.com/fk/cells", "package cells\nfunc Mark() {}\n", nil)}
	p := GenerateWith("nb.md", rubyCodes(
		Code{Source: "a b\n", Lang: "fk"},
		Code{Source: "c := a + b\n"},
	), im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	src := string(p.Source)
	for _, want := range []string{`_fk "example.com/fk/cells"`, "func _fkrun(", "\ta int\n", "a = 1", "c =  a + b"} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
	if len(p.Deferred) != 0 {
		t.Errorf("deferred %v", p.Deferred)
	}
	// The Context's other views.
	g := &generator{path: "p", cells: []Code{{Source: "x"}}, opt: Options{RubyTypes: map[string]string{"k": "int"}}, unknown: map[string]bool{},
		prog: &Program{Graph: analysis.AnalyzeSources([]analysis.Source{{Source: "x := 1"}}), Deferred: map[int]string{}}}
	c := &Context{g}
	c.Unknown("u")
	c.Defer(0, "why")
	if c.Options().RubyTypes["k"] != "int" || !g.unknown["u"] || g.prog.Deferred[0] != "why" || c.WaitsOnDeferred(0) != "" || c.ResolveType("[]int") != "[]int" || len(c.Inputs(0)) != 0 {
		t.Error("Context views")
	}
}

// A check-only language other than Julia is deferred through the registry
// alone, with its own messages.
func TestGenerateCheckOnlyLanguage(t *testing.T) {
	analysis.Register(checkOnlyLang{})
	t.Cleanup(func() { analysis.Unregister("chk") })
	im := newRubyImporter(t)
	p := GenerateWith("nb.md", rubyCodes(
		Code{Source: "v", Lang: "chk"},
		Code{Source: "a := v\n"},
		Code{Source: "b := a\n"},
		Code{Source: "c := 1\n"},
	), im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	want := map[int]string{0: "chk cells are only checked", 1: "reads chk 0", 2: "reads a cell blocked by chk 1"}
	for i, w := range want {
		if p.Deferred[i] != w {
			t.Errorf("cell %d: %q, want %q", i, p.Deferred[i], w)
		}
	}
	if _, ok := p.Deferred[3]; ok {
		t.Errorf("deferred %v", p.Deferred)
	}
}
