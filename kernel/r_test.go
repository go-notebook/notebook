package kernel

import (
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
)

func rCell(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "r"} }

// R cells are checked, not run, through the registry alone: each fails with
// analysis.RNotRun, the cells that read them are blocked, a dynamic R cell
// blocks the later cells that read any name, and the others run.
func TestRCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		rCell("xs <- seq_len(n)^2\ntotal <- sum(xs)\n", 6),
		golang("total + 1\n", 11),
		golang("m := n * 2\nm\n", 15),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	if r[1].Error != analysis.RNotRun || r[1].Ran {
		t.Errorf("R cell %+v", r[1])
	}
	if r[2].Error != "blocked: it depends on cell 1, an R cell, and R cells are not run yet" || r[2].Ran {
		t.Errorf("dependent %+v", r[2])
	}
	if r[3].Error != "" || len(r[3].Display) != 1 || string(r[3].Display[0]["text/plain"]) != "20" {
		t.Errorf("independent %+v", r[3])
	}
	// A syntax error stops the notebook, at its line and column, in R's words.
	rep, err = k.Run([]gen.Code{rCell("y <- 2\nx <- y y\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "5:8: cell 0: syntax error: unexpected symbol") {
		t.Errorf("%v %+v", err, rep)
	}
}
