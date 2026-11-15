package kernel

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/gen"
)

func fortranCell(src string, line int) gen.Code {
	return gen.Code{Source: src, Line: line, Lang: "fortran"}
}

// Fortran cells run, through the registry alone, in one Fortran session of the
// worker: their modules, procedures and variables persist from cell to cell,
// and the other languages neither see nor are seen by them.
func TestFortranCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		fortranCell("module geometry\n  implicit none\ncontains\n  function area(r) result(a)\n    real(8), intent(in) :: r\n    real(8) :: a\n    a = 3.0d0 * r * r\n  end function\nend module\n", 6),
		fortranCell("use geometry\nreal(8) :: v(4)\ninteger :: i\ndo i = 1, 4\n  v(i) = area(real(i, 8))\nend do\nprint *, sum(v)\n", 18),
		fortranCell("print '(f8.1)', v(4)\n", 28),
		golang("m := n * 2\nm\n", 32),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	for _, c := range r {
		if c.Error != "" {
			t.Fatalf("cell %d: %s", c.Cell, c.Error)
		}
	}
	if got := ran(rep); got != "RRRRR" {
		t.Errorf("first run: %s", got)
	}
	if strings.TrimSpace(r[2].Output) != "90.000000000000000" || r[3].Output != "    48.0\n" || shown(r[4]) != "20" {
		t.Errorf("outputs %q %q %q", r[2].Output, r[3].Output, shown(r[4]))
	}
	// Nothing changed: nothing runs.
	rep, err = k.Run(cells)
	if err != nil || ran(rep) != "....." {
		t.Errorf("second run: %v %s", err, ran(rep))
	}
	// A Go cell changes: the worker is rebuilt, and with it the Fortran session,
	// which is empty. A new Fortran cell reads v, so the cells that define what it
	// reads run again, in the new session, before it.
	cells[0] = golang("n := 11\n", 2)
	cells = append(cells, fortranCell("print '(f8.1)', v(2) + v(3)\n", 37))
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Results[5].Error != "" || rep.Results[5].Output != "    39.0\n" {
		t.Errorf("cell 5 %+v", rep.Results[5])
	}
}

// A Fortran failure is the cell's own, at its line and column of the cell; the
// session goes on, and the variables are not Go's.
func TestFortranErrors(t *testing.T) {
	k := newKernel(t)
	rep, err := k.Run([]gen.Code{
		fortranCell("integer :: total = 5\nreal(8) :: v(2)\n", 2),
		fortranCell("print *, 'before'\nv(3) = 1d0\n", 7),
		fortranCell("print *, total\n", 12),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	if !strings.Contains(r[1].Error, "outside 1:2") || r[1].Output != " before\n" || !r[1].Ran {
		t.Errorf("failing cell %+v", r[1])
	}
	if r[2].Error != "" || strings.TrimSpace(r[2].Output) != "5" {
		t.Errorf("the session after a failure %+v", r[2])
	}
	// The Fortran variables are not Go's: a Go cell that reads one does not compile.
	rep, err = k.Run([]gen.Code{fortranCell("integer :: total = 5\n", 2), golang("t := total + 1\n", 6)})
	if err != nil || !strings.Contains(rep.Results[1].Error, "undefined: total") {
		t.Errorf("%v %+v", err, rep)
	}
	// A syntax error stops the notebook, at its line.
	rep, err = k.Run([]gen.Code{fortranCell("integer :: a\ninteger :: y = ;\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "cell 0:") {
		t.Errorf("%v %+v", err, rep)
	}
}

// An infinite loop in a Fortran cell is stopped: by the step bound, which
// fcells sets, here at the kernel's own cell timeout first.
func TestFortranTimeout(t *testing.T) {
	k := newKernel(t)
	k.opt.CellTimeout = 500 * time.Millisecond
	rep, err := k.Run([]gen.Code{fortranCell("do\nend do\n", 2)})
	if err != nil || !strings.HasPrefix(rep.Results[0].Error, "stopped: the cell ran longer than 500ms") {
		t.Fatalf("%v %+v", err, rep)
	}
}

// examples/fortran.md runs end to end, as `notebook run` runs it: a module, a
// derived type, arrays and procedures that persist from cell to cell.
func TestFortranExample(t *testing.T) {
	b, err := os.ReadFile("../examples/fortran.md")
	if err != nil {
		t.Fatal(err)
	}
	var cells []gen.Code
	for _, c := range format.Parse(string(b)).Cells {
		if c.Kind == format.Code {
			cells = append(cells, gen.Code{Source: c.Source, Line: c.Line, Lang: c.Lang})
		}
	}
	k := newKernel(t)
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"",
		"integral of sin over [0, pi] =  2.000000011\n",
		"",
		"p = ( 12.50,  100.0\n   27.500000000000000     \n",
		"  0.50000000000000000     \n",
	}
	if len(rep.Results) != len(want) {
		t.Fatalf("%d cells, want %d", len(rep.Results), len(want))
	}
	for i, r := range rep.Results {
		if r.Error != "" || r.Output != want[i] {
			t.Errorf("cell %d: error %q output %q, want %q", i, r.Error, r.Output, want[i])
		}
	}
}
