package analysis

import (
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeCell(t *testing.T) {
	src := `import (
	"math"
	m2 "math/rand"
	_ "embed"
)

type Point struct{ X, Y float64 }

func (p Point) Norm() float64 { return math.Hypot(p.X, p.Y) }

func sq(x float64) float64 { return x * x }

p := Point{3, 4}
var scale, _ = 2.0, 0
const k = 3
y := sq(p.Norm()) * scale * k + base
func() { local := 1; _ = local }()
if y > 0 { tmp := y; _ = tmp }
type inner int
y + float64(m2.Intn(2))
`
	c := AnalyzeCell(0, src)
	if len(c.Errors) != 0 {
		t.Fatalf("errors: %v", c.Errors)
	}
	if want := []string{"Point", "sq", "inner", "p", "scale", "k", "y"}; !reflect.DeepEqual(c.Defines, want) {
		t.Errorf("Defines = %v, want %v", c.Defines, want)
	}
	if want := []string{"sq"}; !reflect.DeepEqual(c.Funcs, want) {
		t.Errorf("Funcs = %v", c.Funcs)
	}
	if want := []string{"Point", "inner"}; !reflect.DeepEqual(c.Types, want) {
		t.Errorf("Types = %v", c.Types)
	}
	if want := map[string]string{"math": "math", "m2": "math/rand"}; !reflect.DeepEqual(c.Imports, want) {
		t.Errorf("Imports = %v", c.Imports)
	}
	// math and m2 are imports; tmp and local are local; base and the builtin
	// types are free.
	if want := []string{"base", "float64", "int"}; !reflect.DeepEqual(c.Uses, want) {
		t.Errorf("Uses = %v, want %v", c.Uses, want)
	}
	if c.Display == nil || src[c.DisplayStart:c.DisplayEnd] != "y + float64(m2.Intn(2))" {
		t.Errorf("Display = %q", src[c.DisplayStart:c.DisplayEnd])
	}
	kinds := ""
	for _, ch := range c.Chunks {
		if ch.Decl {
			kinds += "D"
		} else {
			kinds += "s"
		}
	}
	if kinds != "DDDDsDs" {
		t.Errorf("chunks = %s %+v", kinds, c.Chunks)
	}
}

func TestChunkEdges(t *testing.T) {
	for src, want := range map[string]string{
		"":                                 "",
		"x := 1; y := 2":                   "s",
		"func(){}()":                       "s",
		"func (a int) {}(1)":               "s",
		"func (r *T) M() {}":               "D",
		"func f() {}\nx := 1\nfunc g() {}": "DsD",
		"x := []int{\n1,\n2,\n}\ny := x":   "s",
		"type (\nA int\nB string\n)":       "D",
		"func\n":                           "s",
		"func * 2\n":                       "s",
	} {
		got := ""
		for _, ch := range chunk(src) {
			if ch.Decl {
				got += "D"
			} else {
				got += "s"
			}
		}
		if got != want {
			t.Errorf("chunk(%q) = %s, want %s", src, got, want)
		}
	}
	// A written semicolon ends with the chunk; an inserted one does not.
	if ch := chunk("x := 1;"); ch[0].End != 7 {
		t.Errorf("written semicolon: %+v", ch)
	}
	if ch := chunk("func f() {}\n"); ch[0].End != 11 {
		t.Errorf("inserted semicolon: %+v", ch)
	}
}

func TestAnalyzeErrorsAndDisplay(t *testing.T) {
	c := AnalyzeCell(2, "x := (\n")
	if len(c.Errors) == 0 || c.Errors[0].Cell != 2 {
		t.Errorf("syntax error not reported: %+v", c.Errors)
	}
	if !strings.Contains(c.Errors[0].Error(), "cell 2") {
		t.Errorf("Error() = %s", c.Errors[0].Error())
	}
	c = AnalyzeCell(0, "func f( {\n")
	if len(c.Errors) == 0 {
		t.Error("declaration syntax error not reported")
	}
	c = AnalyzeCell(0, "x := 1\n")
	if c.Display != nil {
		t.Error("an assignment is not a display value")
	}
	c = AnalyzeCell(0, "import \"fmt\"\n")
	if len(c.Defines) != 0 || c.Imports["fmt"] != "fmt" {
		t.Errorf("import-only cell: %+v", c)
	}
	c = AnalyzeCell(0, "_tmp, err := 1, 2\nx := 1\nx := 2\nfunc _() {}\nfunc init() {}\ntype _ int\nimport . \"math\"\n")
	if !reflect.DeepEqual(c.Defines, []string{"x"}) {
		t.Errorf("Defines = %v", c.Defines)
	}
}

func TestGraph(t *testing.T) {
	g := Analyze([]string{
		"z := y * 2\n",                        // 0 reads 1
		"y := x + 1\n",                        // 1 reads 2
		"x := 1\nfunc f() int { return x }\n", // 2
		"f() + z\n",                           // 3 reads 2 (f) and 0 (z)
		"w := 5\n",                            // 4
	})
	if len(g.Errors) != 0 {
		t.Fatalf("errors: %v", g.Errors)
	}
	if want := []int{2, 1, 0, 3, 4}; !reflect.DeepEqual(g.Order, want) {
		t.Errorf("Order = %v, want %v", g.Order, want)
	}
	if want := []int{0, 2}; !reflect.DeepEqual(g.Deps[3], want) {
		t.Errorf("Deps[3] = %v", g.Deps[3])
	}
	if want := []int{2, 1, 0, 3}; !reflect.DeepEqual(g.Downstream(2), want) {
		t.Errorf("Downstream(2) = %v", g.Downstream(2))
	}
	if want := []int{1, 0, 3}; !reflect.DeepEqual(g.Downstream(1, 1), want) {
		t.Errorf("Downstream(1) = %v", g.Downstream(1, 1))
	}
	if want := []int{2, 1, 0}; !reflect.DeepEqual(g.Upstream(0), want) {
		t.Errorf("Upstream(0) = %v", g.Upstream(0))
	}
	if want := []int{2, 1, 0, 3}; !reflect.DeepEqual(g.Upstream(3, 0), want) {
		t.Errorf("Upstream(3) = %v", g.Upstream(3, 0))
	}
}

func TestGraphErrors(t *testing.T) {
	g := Analyze([]string{"x := 1\n", "x := 2\n", "x = 3\nx++\n"})
	var msgs []string
	for _, e := range g.Errors {
		msgs = append(msgs, e.Msg)
	}
	all := strings.Join(msgs, "\n")
	if !strings.Contains(all, "x is already defined by cell 0") || strings.Count(all, "cannot assign to x") != 2 {
		t.Errorf("errors:\n%s", all)
	}
	g = Analyze([]string{"a := b\n", "b := a\n", "c := 1\n"})
	if len(g.Errors) != 2 || !strings.Contains(g.Errors[0].Msg, "cells 0, 1") || !reflect.DeepEqual(g.Order, []int{2}) {
		t.Errorf("cycle: %+v order %v", g.Errors, g.Order)
	}
	g = Analyze([]string{"x := (\n"})
	if len(g.Errors) == 0 {
		t.Error("syntax error not carried to the graph")
	}
}
