package kernel

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/gen"
)

// newRubyKernel is newKernel for notebooks with Ruby cells. The work module
// requires the released go-scikit/ruby; NOTEBOOK_RUBY_REPLACE, a list of
// module=dir pairs separated by commas, points it at local checkouts
// instead (a bindings or rbgo change under development).
func newRubyKernel(t *testing.T) *Kernel {
	t.Helper()
	k := newKernel(t)
	for _, pair := range strings.Split(os.Getenv("NOTEBOOK_RUBY_REPLACE"), ",") {
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
