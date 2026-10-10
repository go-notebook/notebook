package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/gen"
)

// newKernel returns a kernel whose work module uses this repository's
// runtime.
func newKernel(t *testing.T) *Kernel {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	k, err := New(Options{Dir: t.TempDir(), Name: "nb.md", Replace: map[string]string{modulePath: root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func codes(srcs ...string) []gen.Code {
	out := make([]gen.Code, len(srcs))
	line := 1
	for i, s := range srcs {
		out[i] = gen.Code{Source: s, Line: line}
		line += strings.Count(s, "\n") + 3
	}
	return out
}

// ran lists which cells ran.
func ran(rep *Report) string {
	var b strings.Builder
	for _, r := range rep.Results {
		if r.Ran {
			b.WriteByte('R')
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

func shown(r Result) string {
	if len(r.Display) == 0 {
		return ""
	}
	return string(r.Display[0]["text/plain"])
}

func TestRunOnlyWhatChanged(t *testing.T) {
	k := newKernel(t)
	cells := codes(
		"import \"strings\"\nwords := strings.Fields(\"a b c\")\n",
		"n := len(words)\nn\n",
		"import \"fmt\"\nm := n * 10\nfmt.Println(\"m\")\nm\n",
		"other := 7\nother\n",
	)
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if got := ran(rep); got != "RRRR" || shown(rep.Results[2]) != "30" || !rep.Built {
		t.Fatalf("first run: %s %+v", got, rep.Results)
	}
	if !strings.Contains(rep.Results[2].Output, "m") {
		t.Errorf("output not captured: %q", rep.Results[2].Output)
	}

	// Nothing changed: nothing runs, nothing is built.
	rep, _ = k.Run(cells)
	if got := ran(rep); got != "...." || rep.Built || shown(rep.Results[2]) != "30" || !rep.Results[2].Kept {
		t.Errorf("no change: %s built=%v kept=%v", got, rep.Built, rep.Results[2].Kept)
	}

	// A leaf changes: only it runs, after a rebuild; its inputs are handed
	// over to the new worker, not recomputed.
	cells[2].Source = "m := n * 100\nm\n"
	rep, _ = k.Run(cells)
	if got := ran(rep); got != "..R." || !rep.Built || shown(rep.Results[2]) != "300" {
		t.Errorf("leaf change: %s %q", got, shown(rep.Results[2]))
	}

	// A root changes: it and its readers run, the unrelated cell does not.
	cells[0].Source = "import \"strings\"\nwords := strings.Fields(\"a b c d\")\n"
	rep, _ = k.Run(cells)
	if got := ran(rep); got != "RRR." || shown(rep.Results[2]) != "400" {
		t.Errorf("root change: %s %q", got, shown(rep.Results[2]))
	}

	// Moving a cell runs nothing: its key does not hold its position.
	cells[3], cells[2] = cells[2], cells[3]
	rep, _ = k.Run(cells)
	if got := ran(rep); got != "...." || shown(rep.Results[3]) != "400" {
		t.Errorf("moved cells: %s %q", got, shown(rep.Results[3]))
	}
	cells[3], cells[2] = cells[2], cells[3]

	// RunCell forces a cell and its readers.
	rep, err = k.RunCell(cells, 1)
	if err != nil || ran(rep) != ".RR." {
		t.Errorf("RunCell: %s %v", ran(rep), err)
	}
	// Again, on the same worker: words is already loaded there.
	rep, err = k.RunCell(cells, 1)
	if err != nil || ran(rep) != ".RR." {
		t.Errorf("RunCell again: %s %v", ran(rep), err)
	}
}

func TestVolatileAndErrors(t *testing.T) {
	k := newKernel(t)
	cells := codes(
		"import \"strings\"\nr := strings.NewReader(\"abc\")\n", // volatile: unexported fields
		"size := r.Len()\nsize\n",
		"var fail []int\n_ = fail[size]\n", // panics: index out of range
		"after := size + 1\nafter\n",
		"blocked := fail\n_ = blocked\n",
	)
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Results[2].Error, "index out of range") {
		t.Errorf("panic not reported: %q", rep.Results[2].Error)
	}
	if !strings.Contains(rep.Results[4].Error, "not run: cell 2 failed") {
		t.Errorf("reader of a failed cell: %q", rep.Results[4].Error)
	}
	if shown(rep.Results[3]) != "4" {
		t.Errorf("independent cell: %+v", rep.Results[3])
	}
	// Change the reader of the volatile value: after the rebuild the
	// volatile cell has to run again to give it its input.
	cells[1].Source = "size := r.Len() * 2\nsize\n"
	rep, _ = k.Run(cells)
	if got := ran(rep); got != "RRRR." {
		t.Errorf("volatile input: %s", got)
	}
	if shown(rep.Results[3]) != "7" {
		t.Errorf("after = %q", shown(rep.Results[3]))
	}
}

func TestDiagnostics(t *testing.T) {
	k := newKernel(t)
	rep, err := k.Run(codes("x := 1\n", "x := 2\n", "y := undefinedName\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Diags) == 0 || !strings.Contains(rep.Results[1].Error, "already defined") {
		t.Errorf("duplicate: %+v", rep.Diags)
	}
	if rep.Results[0].Error != "not run: the notebook has errors" || rep.Results[0].Kept || rep.Results[0].Ran {
		t.Errorf("a cell stopped by another's error: %+v", rep.Results[0])
	}
	rep, _ = k.Run(codes("x := 1\n", "y := undefinedName + x\n"))
	if !strings.Contains(rep.Results[1].Error, "undefined: undefinedName") || !strings.Contains(rep.Results[1].Error, "5:6") {
		t.Errorf("type error at notebook position: %+v", rep.Results[1])
	}
}

func TestWorkerDeath(t *testing.T) {
	k := newKernel(t)
	cells := codes("import \"os\"\nx := 1\n", "os.Exit(3)\n", "y := x + 1\ny\n")
	rep, err := k.Run(cells)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Results[1].Error, "worker stopped") {
		t.Errorf("exit: %+v", rep.Results[1])
	}
	cells[1].Source = "z := 2\n"
	rep, err = k.Run(cells)
	if err != nil || shown(rep.Results[2]) != "2" {
		t.Errorf("after restart: %+v %v", rep.Results, err)
	}
	_ = os.Getenv
}
