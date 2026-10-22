package gen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// rubyImporter adds to the test importer a stand-in for the cells package,
// with the API the generated program calls, and a package with a named type
// a Ruby value can have.
type rubyImporter struct {
	*testImporter
	cells, plot *types.Package
}

func check(t *testing.T, path, src string, im types.Importer) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path+".go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{Importer: im}).Check(path, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func newRubyImporter(t *testing.T) *rubyImporter {
	return &rubyImporter{
		testImporter: newImporter(t),
		cells: check(t, RubyPath, `package cells
type Var struct { Name string; Ptr any; IfAbsent bool }
type Cell struct { Source, File string; Line int; Display bool; In, Out []Var }
type Session struct{}
func New() *Session { return nil }
func (s *Session) Run(c Cell, show func(...any), note func(name, typ string)) {}
`, nil),
		plot: check(t, "example.com/plot", "package plot\ntype Figure struct{ W int }\n", nil),
	}
}

func (im *rubyImporter) Import(path string) (*types.Package, error) {
	switch path {
	case RubyPath:
		return im.cells, nil
	case "example.com/plot":
		return im.plot, nil
	}
	return im.testImporter.Import(path)
}

func rubyCodes(cells ...Code) []Code {
	line := 1
	for i := range cells {
		cells[i].Line = line + 1
		line += strings.Count(cells[i].Source, "\n") + 3
	}
	return cells
}

func TestGenerateRuby(t *testing.T) {
	im := newRubyImporter(t)
	cells := rubyCodes(
		Code{Source: "xs := []float64{1, 2, 3}\nN := 2\n"},
		Code{Source: "m = xs.sum / N\nlen = xs.size\nfig = 1\ndef helper = 1\nm\n", Lang: "ruby"},
		Code{Source: "y := m * 2\n"},
		Code{Source: "z = y + m + len + helper\n", Lang: "ruby"},
		Code{Source: "w := fig.W\n"},
		Code{Source: "v := y + 1\n"},
	)
	// Nothing known yet: the Go cell reading m waits, and so does the Ruby
	// cell reading what that Go cell defines; the one reading fig waits too.
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	for _, i := range []int{2, 3, 4, 5} {
		if p.Deferred[i] == "" {
			t.Errorf("cell %d is not deferred: %v", i, p.Deferred)
		}
	}
	src := string(p.Source)
	for _, want := range []string{
		`_rb "github.com/go-scikit/ruby/cells"`,
		"var _rbs = _rb.New()",
		"\tm any\n", "\t_nbrb_len any\n", "\t_nbrbdefs1 func()\n",
		`In: []_rb.Var{{Name: "N", Ptr: &N, IfAbsent: false}, {Name: "xs", Ptr: &xs, IfAbsent: false}}`,
		`Out: []_rb.Var{{Name: "m", Ptr: &m}, {Name: "len", Ptr: &_nbrb_len}, {Name: "fig", Ptr: &fig}}`,
		`panic(_nb.Deferred("not run yet: it reads m, whose Go type is known once cell 1 has run"))`,
		`panic(_nb.Deferred("not run yet: it reads cell 2, which waits for a Ruby cell"))`,
		`"len": &_nbrb_len`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
	// With the types known, everything compiles.
	p = GenerateWith("nb.md", cells, im, Options{RubyTypes: map[string]string{
		"m": "float64", "len": "int64", "fig": "*{example.com/plot}.Figure", "y": "float64", "z": "float64",
	}})
	if len(p.Diags) > 0 || len(p.Deferred) > 0 {
		t.Fatalf("diags %v deferred %v\n%s", p.Diags, p.Deferred, p.Source)
	}
	src = string(p.Source)
	for _, want := range []string{
		"\tm float64\n", "\tfig *_nbimp1.Figure\n", `_nbimp1 "example.com/plot"`,
		`In: []_rb.Var{{Name: "len", Ptr: &_nbrb_len, IfAbsent: true}, {Name: "m", Ptr: &m, IfAbsent: true}, {Name: "y", Ptr: &y, IfAbsent: false}}`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
	// A type of a package the program cannot see is any.
	p = GenerateWith("nb.md", cells[:2], im, Options{RubyTypes: map[string]string{"fig": "*{example.com/missing}.Figure", "m": "float64"}})
	if !strings.Contains(string(p.Source), "\tfig any\n") {
		t.Fatalf("unresolvable type:\n%s", p.Source)
	}
	// A Ruby cell reading a Go func or type sees nothing of it.
	p = GenerateWith("nb.md", rubyCodes(Code{Source: "func sq(x int) int { return x * x }\ntype T int\n"}, Code{Source: "sq(2)\nT\n", Lang: "ruby"}), im, Options{})
	if len(p.Diags) > 0 || !strings.Contains(string(p.Source), "In: []_rb.Var{}") {
		t.Fatalf("%v\n%s", p.Diags, p.Source)
	}
}

// A package only deferred cells use is imported blank, so the program
// compiles before they do.
func TestDeferredImports(t *testing.T) {
	im := newRubyImporter(t)
	cells := rubyCodes(Code{Source: "n = 1\n", Lang: "ruby"}, Code{Source: "import \"strings\"\n\ns := strings.Repeat(\"a\", int(n))\n"})
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 || !strings.Contains(string(p.Source), "\t_ \"strings\"\n") {
		t.Fatalf("%v\n%s", p.Diags, p.Source)
	}
	p = GenerateWith("nb.md", cells, im, Options{RubyTypes: map[string]string{"n": "int64"}})
	if len(p.Diags) > 0 || !strings.Contains(string(p.Source), "\tstrings \"strings\"\n") {
		t.Fatalf("%v\n%s", p.Diags, p.Source)
	}
	// A package a cell that runs uses stays imported by name.
	cells = append(cells, Code{Source: "u := strings.ToUpper(\"a\")\n", Line: 20})
	p = GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 || !strings.Contains(string(p.Source), "\tstrings \"strings\"\n") {
		t.Fatalf("%v\n%s", p.Diags, p.Source)
	}
	// A package a declared type names stays imported by name.
	cells = rubyCodes(Code{Source: "f = 1\nn = 1\n", Lang: "ruby"}, Code{Source: "import \"example.com/plot\"\n\nw := plot.Figure{W: int(n)}\n"})
	p = GenerateWith("nb.md", cells, im, Options{RubyTypes: map[string]string{"f": "*{example.com/plot}.Figure"}})
	if len(p.Diags) > 0 || !strings.Contains(string(p.Source), "\tplot \"example.com/plot\"\n") {
		t.Fatalf("%v\n%s", p.Diags, p.Source)
	}
}
