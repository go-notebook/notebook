package gen

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// testImporter serves the standard library and a package "example.com/fake"
// whose function returns an unexported type.
type testImporter struct {
	std  types.Importer
	fake *types.Package
}

func newImporter(t *testing.T) *testImporter {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", "package fake\ntype hidden int\nfunc Make() hidden { return 0 }\ntype Box[T any] struct{ V T }\nfunc Boxed() Box[hidden] { return Box[hidden]{} }\nfunc Many() (int, int) { return 1, 2 }\nfunc KeyMap() map[hidden]int { return nil }\nfunc Wrapped() struct{ H hidden } { return struct{ H hidden }{} }\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/fake", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &testImporter{std: importer.ForCompiler(token.NewFileSet(), "source", nil), fake: pkg}
}

func (im *testImporter) Import(path string) (*types.Package, error) {
	if path == "example.com/fake" {
		return im.fake, nil
	}
	return im.std.Import(path)
}

func codes(srcs ...string) []Code {
	out := make([]Code, len(srcs))
	line := 1
	for i, s := range srcs {
		out[i] = Code{Source: s, Line: line + 1}
		line += strings.Count(s, "\n") + 3
	}
	return out
}

func TestGenerate(t *testing.T) {
	imp := newImporter(t)
	p := Generate("nb.md", codes(
		"import (\n\t\"fmt\"\n\t\"strings\"\n\tsc \"strconv\"\n\t\"os\"\n)\n\nfunc twice(x int) int { return 2 * x * k }\n",
		"const k = 3\nconst _private = 1\nvar total, count = 0, 0\nvar buf []byte\nvar _local = 5\nvar (\n\tp = 1\n\tq string\n)\n_ = _local\n",
		"n, err := sc.Atoi(\"21\")\n_ = err\nm, err := twice(n), error(nil)\nw, _ := sc.Atoi(\"1\")\nr := strings.NewReader(\"abc\")\nfmt.Println(m)\n",
		"type pair struct{ A, B int }\nfunc (p pair) Sum() int { return p.A + p.B }\npp := pair{1, 2}\npp.Sum()\n",
		"a := 1\na, b := 2, 3\n_x := 1\nlen(buf) + total + count + p + len(q) + _x\n",
		"r.Len()\n",
		"println(\"void\")\n",
		"fmt.Println\n",
		"sc.ParseFloat(\"2.5\", 64)\n",
		"fmt.Println(\"bytes, err\")\n",
		"sc.ParseBool(\"true\")\n",
	), imp)
	for _, d := range p.Diags {
		t.Errorf("diag: %s", d)
	}
	src := string(p.Source)
	for _, want := range []string{
		"_nb \"github.com/go-notebook/notebook/runtime\"",
		"\tsc \"strconv\"",
		"\ttotal int\n", "\tbuf []byte\n", "\tm int\n", "\tr *strings.Reader\n", "\tpp pair\n", "\tq string\n",
		"/*line nb.md:9:1*/func twice",
		"/*line nb.md:13:1*/const k = 3",
		"total, count = 0, 0",
		"buf = *new([]byte)",
		"p = 1; q = *new(string)",
		"var _local = 5",
		"var err error; n, err =  sc.Atoi",
		"_k.Show(pp.Sum())",
		"_k.Show(len(buf)",
		"_k.Show(r.Len())",
		"*/println(\"void\")\n",
		"_k.Show(fmt.Println)",
		"_k.Result(sc.ParseFloat(\"2.5\", 64))",
		"*/fmt.Println(\"bytes, err\")\n",
		"_k.Result(sc.ParseBool(\"true\"))",
		"\"m\": &m",
		"m, err =  twice(n), error(nil)",
		"w, _ =  sc.Atoi",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q", want)
		}
	}
	if strings.Contains(src, "\"os\"") {
		t.Error("an unused import was kept")
	}
	if p.Imports["strconv"] != "sc" {
		t.Errorf("Imports = %v", p.Imports)
	}
	if len(p.Vars) != 12 {
		t.Errorf("Vars = %+v", p.Vars)
	}
	short, long := Diag{Cell: 1, Msg: "m"}, Diag{Cell: 1, Line: 3, Col: 2, Msg: "m"}
	if short.String() != "cell 1: m" || long.String() != "3:2: cell 1: m" {
		t.Error("Diag.String")
	}
}

func TestGenerateTypes(t *testing.T) {
	imp := newImporter(t)
	p := Generate("nb.md", codes(
		"import (\n\t\"example.com/fake\"\n\t\"bufio\"\n\t\"os\"\n)\nsc := bufio.NewScanner(os.Stdin)\n",
		"b := sc.Buffer\nsplit := bufio.ScanLines\n",
		"x := 1.5\nc := 2i\ns := []map[string]*[3]chan int{}\nf := func(a int) (string, error) { return \"\", nil }\nst := struct{ A []int }{}\n",
	), imp)
	for _, d := range p.Diags {
		t.Errorf("diag: %s", d)
	}
	src := string(p.Source)
	for _, want := range []string{"\tx float64\n", "\tc complex128\n", "\ts []map[string]*[3]chan int\n",
		"\tf func(a int) (string, error)\n", "\tst struct{A []int}\n", "\tb func(buf []byte, max int)\n"} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q", want)
		}
	}
	// A type from a package no cell imports by name gets a generated import.
	p = Generate("nb.md", codes("import \"os\"\nfi, _ := os.Stdin.Stat()\nt := fi.ModTime()\n"), imp)
	if len(p.Diags) != 0 || !strings.Contains(string(p.Source), "_nbimp1 \"time\"") || !strings.Contains(string(p.Source), "t _nbimp1.Time") {
		t.Errorf("synthetic import: %v\n%s", p.Diags, p.Source)
	}
}

func TestGenerateDiags(t *testing.T) {
	imp := newImporter(t)
	cases := []struct {
		name  string
		cells []Code
		want  string
	}{
		{"syntax", codes("x := (\n"), "expected"},
		{"duplicate", codes("x := 1\n", "x := 2\n"), "already defined"},
		{"import names", codes("import r \"math/rand\"\n_ = r.Int\n", "import r \"regexp\"\n_ = r.Compile\n"), "names both"},
		{"unexported type", codes("import \"example.com/fake\"\nh := fake.Make()\n"), "fake.hidden, which is not exported"},
		{"unexported type argument", codes("import \"example.com/fake\"\nh := fake.Boxed()\n"), "not exported"},
		{"pointer", codes("import \"example.com/fake\"\nv := fake.Make()\nh := &v\n"), "not exported"},
		{"composite", codes("import \"example.com/fake\"\ns := []map[int]chan [1]func(int) struct{ X any }{}\nt := [2]map[int]any{}\nh := map[string]func() []int{}\nv := fake.Make\n"), "not exported"},
		{"map key", codes("import \"example.com/fake\"\nh := fake.KeyMap()\n"), "not exported"},
		{"struct field", codes("import \"example.com/fake\"\nh := fake.Wrapped()\n"), "not exported"},
		{"undefined display", codes("nope\n"), "undefined: nope"},
		{"type error", codes("x := 1\n", "y := x + \"a\"\n"), "mismatched types"},
		{"undefined", codes("y := nope\n"), "undefined: nope"},
	}
	for _, c := range cases {
		p := Generate("nb.md", c.cells, imp)
		var all []string
		for _, d := range p.Diags {
			all = append(all, d.String())
		}
		if !strings.Contains(strings.Join(all, "\n"), c.want) {
			t.Errorf("%s: diags %v, want %q", c.name, all, c.want)
		}
	}
	// Positions are the notebook's.
	p := Generate("nb.md", []Code{{Source: "x := 1\n", Line: 10}, {Source: "\n\ny := x + \"a\"\n", Line: 20}}, imp)
	if len(p.Diags) != 1 || p.Diags[0].Line != 22 || p.Diags[0].Cell != 1 {
		t.Errorf("position: %+v", p.Diags)
	}
	p = Generate("nb.md", []Code{{Source: "x := (\n", Line: 10}}, imp)
	if p.Diags[0].Line < 10 {
		t.Errorf("syntax error position: %+v", p.Diags)
	}
}

func TestInternalErrors(t *testing.T) {
	g := &generator{path: "nb.md", cells: codes("x := 1\n"), prog: &Program{}}
	g.prog.Source = []byte("package main\nfunc {")
	g.check()
	if len(g.prog.Diags) != 1 || g.prog.Diags[0].Cell != -1 || !strings.HasPrefix(g.prog.Diags[0].Msg, "internal") {
		t.Errorf("unparsable program: %+v", g.prog.Diags)
	}
	if g.cellAtLine(1, "other.go") != -1 || g.cellAtLine(99, "nb.md") != -1 {
		t.Error("cellAtLine outside the notebook")
	}
}
