package kernel

import (
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
)

func cCell(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "c"} }

// C cells are checked, not run, through the registry alone: each fails with
// analysis.CNotRun, the cells that read what they define are blocked, and the
// others run. A later C cell sees the typedefs and macros of the earlier ones,
// so it reads the first and is blocked by it.
func TestCCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		cCell("typedef int myint;\n#define K 3\nint total = 5;\n", 6),
		cCell("myint twice = K * 2;\n", 11),
		golang("total + 1\n", 15),
		golang("m := n * 2\nm\n", 19),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	if r[1].Error != analysis.CNotRun || r[1].Ran {
		t.Errorf("C cell %+v", r[1])
	}
	if r[2].Error != analysis.CBlocked(1) || r[2].Ran {
		t.Errorf("second C cell, which reads the first %+v", r[2])
	}
	if r[3].Error != "blocked: it depends on cell 1, a C cell, and C cells are not run yet" || r[3].Ran {
		t.Errorf("dependent %+v", r[3])
	}
	if r[4].Error != "" || len(r[4].Display) != 1 || string(r[4].Display[0]["text/plain"]) != "20" {
		t.Errorf("independent %+v", r[4])
	}
	// A C error stops the notebook, at its line, in cc/v4's words.
	rep, err = k.Run([]gen.Code{cCell("int a;\nint y = ;\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "cell 0: unexpected ';', expected unary expression") {
		t.Errorf("%v %+v", err, rep)
	}
}
