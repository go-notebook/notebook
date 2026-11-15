package gen

import (
	"go/types"
	"strings"
	"testing"
)

// fortranImporter adds fcells' surface to the Ruby test importer.
type fortranImporter struct {
	*rubyImporter
	fcells *types.Package
}

func newFortranImporter(t *testing.T) *fortranImporter {
	return &fortranImporter{
		rubyImporter: newRubyImporter(t),
		fcells: check(t, FortranPath, `package fcells
type Cell struct { Source string }
type Session struct{}
func New() *Session { return nil }
func (s *Session) Run(c Cell) {}
`, nil),
	}
}

func (im *fortranImporter) Import(path string) (*types.Package, error) {
	if path == FortranPath {
		return im.fcells, nil
	}
	return im.rubyImporter.Import(path)
}

// Fortran cells are calls of the notebook's Fortran session, which holds their
// modules and variables: they share none with the other languages, so a Go
// cell that reads a Fortran name is not compiled against it, and what a cell
// declares is a volatile marker.
func TestGenerateFortran(t *testing.T) {
	im := newFortranImporter(t)
	cells := rubyCodes(
		Code{Source: "n := 10\n"},
		Code{Source: "integer :: x = 3\n", Lang: "fortran"},
		Code{Source: "print *, x\n", Lang: "fortran"},
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
	if strings.Join(vars, ",") != "_nbf1 func(),_nbf2 func()" {
		t.Errorf("vars %v", vars)
	}
	src := string(p.Source)
	for _, want := range []string{
		`_fc "` + FortranPath + `"`,
		"var _fcs = _fc.New()\n",
		"func _nbcell1(_k *_nb.Cell) {\n\t_fcs.Run(_fc.Cell{Source: \"integer :: x = 3\\n\"})\n}",
		"m =  n * 2",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
}
