package kernel

import (
	"strings"
	"testing"

	"github.com/go-notebook/notebook/gen"
)

func julia(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "julia"} }

// Julia cells are checked, not run: each fails with gen.JuliaNotRun, the
// cells that read them are blocked, and the others run.
func TestJuliaCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		julia("xs = [i^2 for i in 1:n]\ntotal = sum(xs)\n", 6),
		golang("total + 1\n", 11),
		golang("m := n * 2\nm\n", 15),
	}
	for run := range 2 { // the second run: kept, and still not run
		rep, err := k.Run(cells)
		if err != nil {
			t.Fatal(err)
		}
		r := rep.Results
		if r[1].Error != gen.JuliaNotRun || r[1].Ran || r[1].Kept {
			t.Errorf("run %d: Julia cell %+v", run, r[1])
		}
		if r[2].Error != "blocked: it depends on cell 1, a Julia cell, and Julia cells are not run yet" || r[2].Ran {
			t.Errorf("run %d: dependent %+v", run, r[2])
		}
		if r[3].Error != "" || len(r[3].Display) != 1 || string(r[3].Display[0]["text/plain"]) != "20" {
			t.Errorf("run %d: independent %+v", run, r[3])
		}
	}
	// A syntax error stops the notebook, at its line.
	rep, err := k.Run([]gen.Code{julia("x = (1,\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "4:8: cell 0: syntax error: Expected `)` or `,`") {
		t.Errorf("%v %+v", err, rep)
	}
}
