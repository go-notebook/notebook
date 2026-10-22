package kernel

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-notebook/notebook/gen"
)

// newRubyKernel is newKernel for notebooks with Ruby cells. The work module
// requires the released go-scikit/ruby; NOTEBOOK_REPLACE, a list of
// module=dir pairs separated by commas, points it at local checkouts
// instead (a bindings or rbgo change under development).
func newRubyKernel(t *testing.T) *Kernel {
	t.Helper()
	k := newKernel(t)
	for _, pair := range strings.Split(os.Getenv("NOTEBOOK_REPLACE"), ",") {
		if mod, dir, ok := strings.Cut(pair, "="); ok {
			abs, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			k.opt.Replace[mod] = abs
		}
	}
	return k
}

func ruby(src string, line int) gen.Code { return gen.Code{Source: src, Line: line, Lang: "ruby"} }
func golang(src string, line int) gen.Code {
	return gen.Code{Source: src, Line: line, Lang: "go"}
}

// A mixed notebook: data drawn in Go, fitted in Ruby, the fit read back in Go.
func TestMixedNotebook(t *testing.T) {
	k := newRubyKernel(t)
	cells := []gen.Code{
		golang("xs := []float64{2.1, 3.4, 1.9, 5.6, 4.4, 3.3, 2.8, 4.9}\n", 2),
		ruby("fit = Stats::Normal.fit(xs)\nloc = fit.loc\nscale = fit.scale\nfit\n", 6),
		golang("z := (xs[0] - loc) / scale\nz\n", 13),
		ruby("puts \"z = #{z.round(4)}\"\nStats::Normal.new.cdf(z)\n", 18),
	}
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Error != "" {
			t.Fatalf("cell %d: %s", r.Cell, r.Error)
		}
	}
	if got := ran(rep); got != "RRRR" {
		t.Fatalf("first run: %s", got)
	}
	if !strings.HasPrefix(shown(rep.Results[1]), "#<Stats::Normal loc=3.55") {
		t.Fatalf("fit shown as %q", shown(rep.Results[1]))
	}
	// The z-score, computed here from the maximum-likelihood fit (SciPy's
	// norm.fit: the mean and the uncorrected standard deviation).
	xs := []float64{2.1, 3.4, 1.9, 5.6, 4.4, 3.3, 2.8, 4.9}
	var sum, ss float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	for _, x := range xs {
		ss += (x - mean) * (x - mean)
	}
	want := fmt.Sprintf("z = %.4f\n", (xs[0]-mean)/math.Sqrt(ss/float64(len(xs))))
	if rep.Results[3].Output != want {
		t.Fatalf("output %q", rep.Results[3].Output)
	}
	// Again: nothing runs.
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(rep); got != "...." {
		t.Fatalf("second run: %s", got)
	}
	// Edit the Ruby fit: it and its dependants run, the data does not.
	cells[1] = ruby("fit = Stats::Normal.fit(xs, fixed: {loc: 3})\nloc = fit.loc\nscale = fit.scale\nfit\n", 6)
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(rep); got != ".RRR" {
		t.Fatalf("after an edit: %s", got)
	}
	// A new kernel on the same directory (a new process) keeps everything.
	k2, err := New(k.opt)
	if err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	rep, err = k2.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(rep); got != "...." {
		t.Fatalf("new process: %s", got)
	}
}

func TestRubyErrorAtItsLine(t *testing.T) {
	k := newRubyKernel(t)
	rep, err := k.Run([]gen.Code{ruby("a = 1\nb = a / 0\n", 4)})
	if err != nil {
		t.Fatal(err)
	}
	if e := rep.Results[0].Error; !strings.HasPrefix(e, "nb.md:5: divided by 0 (ZeroDivisionError)") {
		t.Fatalf("error %q", e)
	}
}

// A Ruby variable that changes type: the Go cell reading it is compiled again
// with the new type, in the same Run.
func TestRubyTypeChange(t *testing.T) {
	k := newRubyKernel(t)
	cells := []gen.Code{ruby("n = 1.5\n", 2), golang("import \"fmt\"\n\ns := fmt.Sprintf(\"%T\", n)\ns\n", 5)}
	rep, err := k.Run(cells)
	if err != nil || shown(rep.Results[1]) != "float64" {
		t.Fatalf("%v %+v", err, rep)
	}
	cells[0] = ruby("n = [1, 2]\n", 2)
	rep, err = k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := shown(rep.Results[1]); got != "[]int64" || ran(rep) != "RR" {
		t.Fatalf("after the change: %q %s %+v", got, ran(rep), rep.Results)
	}
}

// Chaos: what a cell can do to its worker, and that the kernel recovers.
func TestRubyChaos(t *testing.T) {
	k := newRubyKernel(t)
	k.opt.CellTimeout = 3 * time.Second
	k.opt.CellMemory = 256 << 20
	ok := []gen.Code{ruby("x = 41\n", 2), golang("y := x + 1\ny\n", 5)}
	type chaos struct {
		name string
		cell gen.Code
		want string
	}
	// rbgo has no Process.kill on Windows; a Go cell kills its worker
	// everywhere.
	var killRuby []chaos
	if goruntime.GOOS != "windows" {
		killRuby = append(killRuby, chaos{"a worker killed mid-cell, from Ruby", ruby("Process.kill(:KILL, Process.pid)\nsleep 5\n", 9), "the worker stopped"})
	}
	for _, c := range append(killRuby, []chaos{
		{"a worker killed mid-cell, from Go", golang("import (\n\t\"os\"\n\t\"time\"\n)\n\n_p, _ := os.FindProcess(os.Getpid())\n_ = _p.Kill()\ntime.Sleep(5 * time.Second)\n", 9), "the worker stopped"},
		{"an endless Ruby loop", ruby("loop { }\n", 9), "stopped: the cell ran longer than 3s"},
		{"an endless Go loop", golang("for {\n}\n", 9), "stopped: the cell ran longer than 3s"},
		{"exit! in Ruby", ruby("exit!(3)\n", 9), "SystemExit"},
		{"a Go panic", golang("var m map[string]int\nm[\"a\"] = 1\n", 9), "assignment to entry in nil map"},
		{"a huge Ruby allocation", ruby("'x' * (2**62)\n", 9), "failed to allocate memory (NoMemoryError)"},
		{"a Ruby cell over its memory limit", ruby("big = Array.new(60_000_000) { |i| i * 0.5 }\n", 9), "stopped: the cell went over its memory limit of 268435456 bytes"},
		{"a Go cell over its memory limit", golang("big := make([][]float64, 0)\nfor i := 0; i < 1000; i++ {\n\tbig = append(big, make([]float64, 1<<20))\n}\n", 9), "stopped: the cell went over its memory limit"},
		{"runaway recursion", ruby("def f(n) = f(n + 1)\nf(0)\n", 9), "stack level too deep (SystemStackError)"},
	}...) {
		t.Run(c.name, func(t *testing.T) {
			rep, err := k.Run(append(append([]gen.Code{}, ok...), c.cell))
			if err != nil {
				t.Fatal(err)
			}
			if e := rep.Results[2].Error; !strings.Contains(e, c.want) {
				t.Fatalf("error %q, want %q", e, c.want)
			}
			// The notebook runs again, from a sound worker.
			rep, err = k.Run(append(append([]gen.Code{}, ok...), ruby("y * 2\n", 9)))
			if err != nil || rep.Results[2].Error != "" || shown(rep.Results[2]) != "84" {
				t.Fatalf("after: %v %+v", err, rep.Results)
			}
		})
	}
}
