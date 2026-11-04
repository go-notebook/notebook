package kernel

import (
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/runtime"
)

func rCell(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "r"} }

// R cells run, through the registry alone, in one R session of the worker:
// their variables persist from cell to cell, and the other languages neither
// see nor are seen by them.
func TestRCells(t *testing.T) {
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		rCell("xs <- 1:5\ntotal <- sum(xs)\nprint(total)\n", 6),
		rCell("total * 2\ninvisible(0)\nwarning('careful')\n", 12),
		golang("m := n * 2\nm\n", 17),
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
	if got := ran(rep); got != "RRRR" {
		t.Errorf("first run: %s", got)
	}
	if r[1].Output != "[1] 15\n" || r[2].Output != "[1] 30\nWarning message:\ncareful\n" || shown(r[3]) != "20" {
		t.Errorf("outputs %q %q %q", r[1].Output, r[2].Output, shown(r[3]))
	}
	// Nothing changed: nothing runs.
	rep, err = k.Run(cells)
	if err != nil || ran(rep) != "...." || rep.Results[2].Output != r[2].Output {
		t.Errorf("second run: %v %s %+v", err, ran(rep), rep.Results)
	}
	// A Go cell changes: the worker is rebuilt, and with it the R session,
	// which is empty. A new R cell reads total, so the cell that defines it runs
	// again, in the new session, before it; the cell between them is kept.
	cells[0] = golang("n := 11\n", 2)
	cells = append(cells, rCell("total + length(xs)\n", 21))
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(rep); got != "RR.RR" {
		t.Errorf("third run: %s", got)
	}
	if rep.Results[4].Error != "" || rep.Results[4].Output != "[1] 20\n" {
		t.Errorf("cell 4 %+v", rep.Results[4])
	}
	if rep.Results[1].Output != "[1] 15\n" || rep.Results[2].Output != r[2].Output {
		t.Errorf("cells 1 and 2: %+v %+v", rep.Results[1], rep.Results[2])
	}
}

// An R error is R's own, with its call; the cells that read it do not run, and
// the session goes on.
func TestRError(t *testing.T) {
	k := newKernel(t)
	rep, err := k.Run([]gen.Code{
		rCell("f <- function(x) stop('boom ', x)\nv <- 1\n", 2),
		rCell("cat('before\\n')\nf(v)\n", 6),
		rCell("w <- v + 1\n", 11),
		rCell("w\n", 15),
		rCell("z <- 1\nz\n", 19),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	if r[1].Error != "Error in f(v) : boom 1" || r[1].Output != "before\n" || !r[1].Ran {
		t.Errorf("failing cell %+v", r[1])
	}
	if r[2].Error != "" || r[3].Error != "" || r[3].Output != "[1] 2\n" || r[4].Output != "[1] 1\n" {
		t.Errorf("cells that do not read it %+v %+v %+v", r[2], r[3], r[4])
	}
	// The R variables are not Go's: a Go cell that reads one does not compile.
	rep, err = k.Run([]gen.Code{rCell("total <- 1\n", 2), golang("t := total + 1\n", 6)})
	if err != nil || !strings.Contains(rep.Results[1].Error, "undefined: total") {
		t.Errorf("%v %+v", err, rep)
	}
	// And a syntax error stops the notebook, at its line and column, in R's words.
	rep, err = k.Run([]gen.Code{rCell("y <- 2\nx <- y y\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "5:8: cell 0: syntax error: unexpected symbol") {
		t.Errorf("%v %+v", err, rep)
	}
}

// Each of the kernel's R limits ends a cell that would otherwise not end, the
// worker and its session stay up, and the limits are the kernel's to set.
func TestRLimits(t *testing.T) {
	k := newKernel(t)
	k.opt.R = runtime.RLimits{MaxAlloc: 16 << 20, MaxSteps: 100_000, Timeout: time.Minute}
	cells := []gen.Code{
		rCell("kept <- 1\n", 2),
		rCell("while(TRUE) 0\n", 6),
		rCell("for (i in 1:100000) numeric(1e6)\n", 10),
		rCell("f <- function(n) f(n + 1)\nf(1)\n", 14),
		rCell("kept\n", 19),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Results
	for i, want := range map[int]string{
		1: "Error: evaluation step limit reached",
		2: "Error: memory limit exceeded: more than 16777216 bytes of vectors allocated",
		3: "Error: evaluation nested too deeply: infinite recursion / options(expressions=)?",
	} {
		if r[i].Error != want {
			t.Errorf("cell %d: %q, want %q", i, r[i].Error, want)
		}
	}
	if r[4].Error != "" || r[4].Output != "[1] 1\n" {
		t.Errorf("the session after the limits: %+v", r[4])
	}
}

// The time limit is the one a loop that catches the step limit's error meets;
// with the R limits off, the kernel's own cell timeout still stops it.
func TestRTimeout(t *testing.T) {
	k := newKernel(t)
	k.opt.R = runtime.RLimits{MaxSteps: -1, MaxAlloc: -1, Timeout: 300 * time.Millisecond}
	rep, err := k.Run([]gen.Code{rCell("repeat tryCatch(NULL, error = function(e) NULL)\n", 2)})
	if err != nil || rep.Results[0].Error != "stopped: the R cell ran longer than 300ms, the limit" {
		t.Fatalf("%v %+v", err, rep)
	}
	k = newKernel(t)
	k.opt.R = runtime.RLimits{MaxSteps: -1, MaxAlloc: -1, Timeout: -1}
	k.opt.CellTimeout = 500 * time.Millisecond
	rep, err = k.Run([]gen.Code{rCell("while(TRUE) 0\n", 2)})
	if err != nil || !strings.HasPrefix(rep.Results[0].Error, "stopped: the cell ran longer than 500ms") {
		t.Fatalf("%v %+v", err, rep)
	}
}
