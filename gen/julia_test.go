package gen

import (
	"go/types"
	"strings"
	"testing"
)

// juliaImporter adds jcells' surface to the Ruby test importer.
type juliaImporter struct {
	*rubyImporter
	jcells *types.Package
}

func newJuliaImporter(t *testing.T) *juliaImporter {
	return &juliaImporter{
		rubyImporter: newRubyImporter(t),
		jcells: check(t, JuliaPath, `package jcells
type Cell struct { Source string }
type Session struct{}
func New() *Session { return nil }
func (s *Session) Run(c Cell) {}
`, nil),
	}
}

func (im *juliaImporter) Import(path string) (*types.Package, error) {
	if path == JuliaPath {
		return im.jcells, nil
	}
	return im.rubyImporter.Import(path)
}

// Julia cells are calls of the notebook's Julia session, which holds their
// globals: they share none with the other languages, so a Go cell that reads a
// Julia name is not compiled against it, and what a cell declares is a
// volatile marker.
func TestGenerateJulia(t *testing.T) {
	im := newJuliaImporter(t)
	cells := rubyCodes(
		Code{Source: "n := 10\n"},
		Code{Source: "xs = [i^2 for i in 1:3]\ntotal = sum(xs)\n", Lang: "julia"},
		Code{Source: "println(total)\n", Lang: "julia"},
		Code{Source: "m := n * 2\nm\n"},
	)
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	if len(p.Deferred) != 0 {
		t.Errorf("deferred %v", p.Deferred)
	}
	var vars []string
	for _, v := range p.Vars {
		if v.Cell == 1 || v.Cell == 2 {
			vars = append(vars, v.Name+" "+v.Type)
		}
	}
	if strings.Join(vars, ",") != "_nbj1 func(),_nbj2 func()" {
		t.Errorf("vars %v", vars)
	}
	src := string(p.Source)
	for _, want := range []string{
		`_jc "` + JuliaPath + `"`,
		"var _jcs = _jc.New()\n",
		"func _nbcell1(_k *_nb.Cell) {\n\t_jcs.Run(_jc.Cell{Source: \"xs = [i^2 for i in 1:3]\\ntotal = sum(xs)\\n\"})\n}",
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
