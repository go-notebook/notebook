package gen

import (
	"go/types"
	"strings"
	"testing"
)

// rImporter adds rcells' surface to the Ruby test importer.
type rImporter struct {
	*rubyImporter
	rcells *types.Package
}

func newRImporter(t *testing.T) *rImporter {
	return &rImporter{
		rubyImporter: newRubyImporter(t),
		rcells: check(t, RPath, `package rcells
type Cell struct { Source string }
type Session struct{}
func New() *Session { return nil }
func (s *Session) Run(c Cell) {}
`, nil),
	}
}

func (im *rImporter) Import(path string) (*types.Package, error) {
	if path == RPath {
		return im.rcells, nil
	}
	return im.rubyImporter.Import(path)
}

// R cells are calls of the notebook's R session, which holds their variables:
// they share none with the other languages, so a Go cell that reads an R name
// is not compiled against it, an R cell that reads a Go name does not wait for
// it, and what a cell declares is a volatile marker.
func TestGenerateR(t *testing.T) {
	im := newRImporter(t)
	cells := rubyCodes(
		Code{Source: "n := 10\n"},
		Code{Source: "x <- n\ny <- x + 1\n", Lang: "r"},
		Code{Source: "z <- y * 2\nz\n", Lang: "r"},
		Code{Source: "m := n * 2\nm\n"},
		Code{Source: "j = 1\n", Lang: "julia"},
		Code{Source: "assign(nm, 1)\n", Lang: "r"},
	)
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	// The Julia cell is check-only; the dynamic R cell that follows it reads
	// every name, so it waits for it.
	if len(p.Deferred) != 2 || p.Deferred[4] != JuliaNotRun || p.Deferred[5] != "not run: it reads cell 4, a Julia cell, and Julia cells are not run yet" {
		t.Errorf("deferred %v", p.Deferred)
	}
	var vars []string
	for _, v := range p.Vars {
		if v.Cell == 1 || v.Cell == 2 {
			vars = append(vars, v.Name+" "+v.Type)
		}
	}
	if strings.Join(vars, ",") != "_nbr1 func(),_nbr2 func()" {
		t.Errorf("vars %v", vars)
	}
	src := string(p.Source)
	for _, want := range []string{
		`_rc "` + RPath + `"`,
		"var _rcs = _rc.New()\n",
		"func _nbcell1(_k *_nb.Cell) {\n\t_rcs.Run(_rc.Cell{Source: \"x <- n\\ny <- x + 1\\n\"})\n}",
		"m =  n * 2",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("program lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "assign(nm") {
		t.Errorf("a deferred R cell is emitted:\n%s", src)
	}
}
