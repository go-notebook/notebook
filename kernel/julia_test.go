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
// worker, and their variables cross to the Go cells as Ruby's do: the Go type of
// a value is learned when the cell has run, and the Go cells that read it are
// compiled then. Go values go in the other way.
func TestJuliaCells(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	k := newKernel(t)
	cells := []gen.Code{
		golang("n := 4\n", 2),
		julia("xs = [Float64(i) for i in 1:n]\ntotal = sum(xs)\nA = [1.0 2.0; 3.0 4.0]\nsquare(x) = x^2\n", 6),
		golang("t := total + 1\nt\n", 11),
		julia("println(\"total = \", total)\nsquare(total)\n", 15),
		golang("m := len(xs) * 2\nm\n", 19),
		golang("a := A.At(1, 0)\na\n", 23),
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
	if got := ran(rep); got != "RRRRRR" {
		t.Errorf("first run: %s", got)
	}
	if r[1].Output != "square (generic function with 1 method)\n" || r[3].Output != "total = 10.0\n100.0\n" {
		t.Errorf("Julia outputs %q %q", r[1].Output, r[3].Output)
	}
	if shown(r[2]) != "11" || shown(r[4]) != "8" || shown(r[5]) != "3" {
		t.Errorf("Go cells that read Julia variables: %q %q %q", shown(r[2]), shown(r[4]), shown(r[5]))
	}
	// Nothing changed: nothing runs.
	rep, err = k.Run(cells)
	if err != nil || ran(rep) != "......" {
		t.Errorf("second run: %v %s", err, ran(rep))
	}
	// A Go cell changes: the Julia cell that reads it runs again, and the Go cells
	// that read what it defines run again too.
	cells[0] = golang("n := 5\n", 2)
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if shown(rep.Results[4]) != "10" || shown(rep.Results[2]) != "16" {
		t.Errorf("after the change: %q %q (ran %s)", shown(rep.Results[4]), shown(rep.Results[2]), ran(rep))
	}
}

// A Julia failure is the cell's own, with julia's text; the session goes on. A
// variable of a type that does not cross stays in Julia: the Go cell that reads
// it fails with a message that names its Julia type, and Julia cells read it as
// before. A Go value Julia cannot take fails the Julia cell that reads it,
// clearly.
func TestJuliaErrorsAndTypesThatDoNotCross(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	k := newKernel(t)
	rep, err := k.Run([]gen.Code{
		julia("total = 5\nd = Dict(1 => 2)\n", 2),
		julia("println(\"before\")\nerror(\"boom\")\n", 7),
		julia("total + d[1]\n", 12),
	})
	if err != nil {
		t.Fatal(err)
	}
	needImage(t, rep.Results[0].Error)
	r := rep.Results
	if r[1].Error != "ERROR: boom" || r[1].Output != "before\n" || !r[1].Ran {
		t.Errorf("failing cell %+v", r[1])
	}
	if r[2].Error != "" || r[2].Output != "7\n" {
		t.Errorf("the session after a failure, and a variable that does not cross %+v", r[2])
	}
	rep, err = k.Run([]gen.Code{julia("d = Dict(1 => 2)\n", 2), golang("x := d\n", 6)})
	if err != nil || !strings.Contains(rep.Results[1].Error, "it reads d, a Julia variable of type Dict{Int64, Int64}, which does not cross to Go") {
		t.Errorf("%v %+v", err, rep)
	}
	rep, err = k.Run([]gen.Code{golang("words := []string{\"a\"}\n", 2), julia("length(words)\n", 6)})
	if err != nil || !strings.Contains(rep.Results[1].Error, "the Julia cell reads words, a Go []string that Julia cannot take") {
		t.Errorf("%v %+v", err, rep)
	}
	// A syntax error stops the notebook, at its line.
	rep, err = k.Run([]gen.Code{julia("x = (1,\n", 4)})
	if err != nil || !strings.Contains(rep.Results[0].Error, "4:8: cell 0: syntax error: Expected `)` or `,`") {
		t.Errorf("%v %+v", err, rep)
	}
}
