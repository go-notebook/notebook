package kernel

import (
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
)

func fortranCell(src string, line int) gen.Code {
	return gen.Code{Source: src, Line: line, Lang: "fortran"}
}

// Fortran cells are checked, not run, through the registry alone: each fails
// with analysis.FortranNotRun, the cells that read what they define are
// blocked, and the others run.
func TestFortranCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		fortranCell("integer :: total = 5\n", 6),
		fortranCell("integer :: twice\ntwice = total * 2\n", 11),
		golang("total + 1\n", 15),
		golang("m := n * 2\nm\n", 19),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	if r[1].Error != analysis.FortranNotRun || r[1].Ran {
		t.Errorf("Fortran cell %+v", r[1])
	}
	if r[2].Error != analysis.FortranBlocked(1) || r[2].Ran {
		t.Errorf("second Fortran cell, which reads the first %+v", r[2])
	}
	if r[3].Error != "blocked: it depends on cell 1, a Fortran cell, and Fortran cells are not run yet" || r[3].Ran {
		t.Errorf("dependent %+v", r[3])
	}
	if r[4].Error != "" || len(r[4].Display) != 1 || string(r[4].Display[0]["text/plain"]) != "20" {
		t.Errorf("independent %+v", r[4])
	}
	// A Fortran error stops the notebook, at its line.
	rep, err = k.Run([]gen.Code{fortranCell("integer :: a\ninteger :: y = ;\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "cell 0:") {
		t.Errorf("%v %+v", err, rep)
	}
}
