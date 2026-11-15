package kernel

import (
	"os"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/gen"
)

func julia(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "julia"} }

// needImage skips the test when the worker could not boot the Julia image (the
// machine is offline and the image is not cached), and fails it in CI.
func needImage(t *testing.T, errText string) {
	t.Helper()
	if strings.Contains(errText, "the Julia kernel could not start") {
		if os.Getenv("CI") != "" {
			t.Fatalf("no Julia kernel in CI: %s", errText)
		}
		t.Skipf("no Julia image (offline?): %s", errText)
	}
}

// Julia cells run, through the registry alone, in one Julia kernel of the
// worker: their globals persist from cell to cell, and the other languages
// neither see nor are seen by them.
func TestJuliaCells(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 10\n", 2),
		julia("xs = [i^2 for i in 1:4]\ntotal = sum(xs)\n", 6),
		julia("println(\"total = \", total)\ntotal + 1\n", 11),
		golang("m := n * 2\nm\n", 15),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		needImage(t, r.Error)
		if r.Error != "" {
			t.Fatalf("cell %d: %s", r.Cell, r.Error)
		}
	}
	r := rep.Results
	if got := ran(rep); got != "RRRR" {
		t.Errorf("first run: %s", got)
	}
	if r[1].Output != "30\n" || r[2].Output != "total = 30\n31\n" || shown(r[3]) != "20" {
		t.Errorf("outputs %q %q %q", r[1].Output, r[2].Output, shown(r[3]))
	}
	// Nothing changed: nothing runs.
	rep, err = k.Run(cells)
	if err != nil || ran(rep) != "...." {
		t.Errorf("second run: %v %s", err, ran(rep))
	}
}

// A Julia failure is the cell's own, with julia's text; the session goes on,
// and the globals are not Go's: a Go cell that reads one does not compile.
func TestJuliaErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	k := newKernel(t)
	rep, err := k.Run([]gen.Code{
		julia("total = 5\n", 2),
		julia("println(\"before\")\nerror(\"boom\")\n", 7),
		julia("total + 1\n", 12),
	})
	if err != nil {
		t.Fatal(err)
	}
	needImage(t, rep.Results[0].Error)
	r := rep.Results
	if r[1].Error != "ERROR: boom" || r[1].Output != "before\n" || !r[1].Ran {
		t.Errorf("failing cell %+v", r[1])
	}
	if r[2].Error != "" || r[2].Output != "6\n" {
		t.Errorf("the session after a failure %+v", r[2])
	}
	rep, err = k.Run([]gen.Code{julia("total = 5\n", 2), golang("t := total + 1\n", 6)})
	if err != nil || !strings.Contains(rep.Results[1].Error, "undefined: total") {
		t.Errorf("%v %+v", err, rep)
	}
	// A syntax error stops the notebook, at its line.
	rep, err = k.Run([]gen.Code{julia("x = (1,\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "4:8: cell 0: syntax error: Expected `)` or `,`") {
		t.Errorf("%v %+v", err, rep)
	}
}
