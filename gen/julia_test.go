package gen

import (
	"go/types"
	"strings"
	"testing"
)

// juliaImporter adds jcells' surface, and the interp package its Matrix type
// comes from, to the Ruby test importer.
type juliaImporter struct {
	*rubyImporter
	jcells, interp *types.Package
}

func newJuliaImporter(t *testing.T) *juliaImporter {
	return &juliaImporter{
		rubyImporter: newRubyImporter(t),
		jcells: check(t, JuliaPath, `package jcells
type Var struct { Name string; Ptr any; IfAbsent bool }
type Cell struct { Source string; In, Out []Var; Note func(name, typ string) }
type Session struct{}
func New() *Session { return nil }
func (s *Session) Run(c Cell) {}
`, nil),
		interp: check(t, "github.com/go-embedded-julia/julia/interp", "package interp\ntype Matrix[T float64 | int64] struct { Rows, Cols int; Data []T }\n", nil),
	}
}

func (im *juliaImporter) Import(path string) (*types.Package, error) {
	switch path {
	case JuliaPath:
		return im.jcells, nil
	case "github.com/go-embedded-julia/julia/interp":
		return im.interp, nil
	}
	return im.rubyImporter.Import(path)
}

const matrixType = "{github.com/go-embedded-julia/julia/interp}.Matrix[float64]"

// Julia cells are calls of the notebook's Julia session, which takes and gives
// the variables that cross. Their Go types are learned from the run, as Ruby's:
// a Go cell that reads one whose type is not known yet waits for it.
func TestGenerateJulia(t *testing.T) {
	im := newJuliaImporter(t)
	cells := rubyCodes(
		Code{Source: "n := 10\n"},
		Code{Source: "xs = [i^2 for i in 1:n]\ntotal = sum(xs)\nf(x) = x\nA = [1.0 2.0; 3.0 4.0]\n", Lang: "julia"},
		Code{Source: "t := total + 1\n"},
		Code{Source: "u = total * 2\n", Lang: "julia"},
		Code{Source: "m := n * 2\nm\n"},
	)
	// the first run: no types known, so the Go cell that reads one waits
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	if p.Deferred[2] != "not run yet: it reads total, whose Go type is known once cell 1 has run" || len(p.Deferred) != 1 {
		t.Errorf("deferred %v", p.Deferred)
	}
	// the types are known: nothing waits, the variables are typed, the marker is
	// there and the functions are not variables
	p = GenerateWith("nb.md", cells, im, Options{RubyTypes: map[string]string{
		"xs": "[]int64", "total": "int64", "A": matrixType, "u": "int64"}})
	if len(p.Diags) > 0 || len(p.Deferred) != 0 {
		t.Fatalf("%v %v", p.Diags, p.Deferred)
	}
	var vars []string
	for _, v := range p.Vars {
		if v.Cell == 1 || v.Cell == 3 {
			vars = append(vars, v.Name+" "+v.Type)
		}
	}
	if got := strings.Join(vars, ","); got != "xs []int64,total int64,A _nbimp1.Matrix[float64],_nbj1 func(),u int64,_nbj3 func()" {
		t.Errorf("vars %s", got)
	}
	src := string(p.Source)
	for _, want := range []string{
		`_jc "` + JuliaPath + `"`,
		"var _jcs = _jc.New()\n",
		"func _nbcell1(_k *_nb.Cell) {\n\t_jcs.Run(_jc.Cell{\n\t\tSource: \"xs = [i^2 for i in 1:n]\\ntotal = sum(xs)\\nf(x) = x\\nA = [1.0 2.0; 3.0 4.0]\\n\",\n\t\tIn: []_jc.Var{{Name: \"n\", Ptr: &n, IfAbsent: false}},\n\t\tOut: []_jc.Var{{Name: \"xs\", Ptr: &xs}, {Name: \"total\", Ptr: &total}, {Name: \"A\", Ptr: &A}},\n\t\tNote: _k.Note,\n\t})\n}",
		// a Julia cell's own variable is given only if the kernel lacks it
		"In: []_jc.Var{{Name: \"total\", Ptr: &total, IfAbsent: true}}",
		"t =  total + 1",
		"m =  n * 2",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
	// A syntax error in a Julia cell is a diagnostic at its line.
	p = GenerateWith("nb.md", rubyCodes(Code{Source: "y = 2\nx = (1,\n", Lang: "julia"}), im, Options{})
	if len(p.Diags) != 1 || p.Diags[0].Line != 3 || p.Diags[0].Col != 8 || p.Diags[0].Msg != "syntax error: Expected `)` or `,`" {
		t.Errorf("diags %+v", p.Diags)
	}
}

// A Julia variable of a type that does not cross stays in Julia: a Go cell that
// reads it is deferred with its Julia type, and so is every cell after that; the
// Julia cells that read it are not.
func TestGenerateJuliaUnsupported(t *testing.T) {
	im := newJuliaImporter(t)
	cells := rubyCodes(
		Code{Source: "d = Dict(1 => 2)\nk = 3\n", Lang: "julia"},
		Code{Source: "x := d\n"},
		Code{Source: "y := x\n"},
		Code{Source: "v = d[1] + k\n", Lang: "julia"},
		Code{Source: "w := k + 1\n"},
	)
	p := GenerateWith("nb.md", cells, im, Options{RubyTypes: map[string]string{"d": UnsupportedPrefix + "Dict{Int64, Int64}", "k": "int64", "v": "int64"}})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	want := "not run: it reads d, a Julia variable of type Dict{Int64, Int64}, which does not cross to Go (Julia's Int64, Float64, Bool, String, and Vector and Matrix of Float64 or Int64 do)"
	if p.Deferred[1] != want || p.Deferred[2] == "" || len(p.Deferred) != 2 {
		t.Errorf("deferred %v", p.Deferred)
	}
	// a variable with no Go-identifier name has a holder of its own
	p = GenerateWith("nb.md", rubyCodes(Code{Source: "type = 1\n", Lang: "julia"}), im, Options{RubyTypes: map[string]string{"type": "int64"}})
	if !strings.Contains(string(p.Source), "Ptr: &_nbjl_type") {
		t.Errorf("program:\n%s", p.Source)
	}
	// a type that names a package the program cannot see is declared any
	p = GenerateWith("nb.md", rubyCodes(Code{Source: "z = 1\n", Lang: "julia"}), im, Options{RubyTypes: map[string]string{"z": "{example.com/missing}.T"}})
	if len(p.Vars) == 0 || p.Vars[0].Type != "any" {
		t.Errorf("vars %+v", p.Vars)
	}
	// a Julia cell that waits on a deferred cell is deferred
	p = GenerateWith("nb.md", rubyCodes(
		Code{Source: "a = 1\n", Lang: "ruby"},
		Code{Source: "b := a\n"},
		Code{Source: "c = b\n", Lang: "julia"},
	), im, Options{})
	if p.Deferred[2] == "" {
		t.Errorf("deferred %v", p.Deferred)
	}
}
