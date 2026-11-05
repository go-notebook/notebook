package rcells

import (
	"context"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-embedded-r/r/eval"

	"github.com/go-notebook/notebook/runtime"
)

// cellRun is what one cell did: what it wrote on standard output and error,
// and the failure it ended with ("" when it did not fail).
type cellRun struct{ stdout, stderr, failure string }

// run runs a cell as the worker does, with os.Stdout and os.Stderr pointed at
// files, and recovers its failure as runtime.run does.
func run(t *testing.T, s *Session, src string) cellRun {
	return runCtx(t, context.Background(), s, src)
}

func runCtx(t *testing.T, ctx context.Context, s *Session, src string) (r cellRun) {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(dir + "/out")
	if err != nil {
		t.Fatal(err)
	}
	errf, err := os.Create(dir + "/err")
	if err != nil {
		t.Fatal(err)
	}
	so, se := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errf
	func() {
		defer func() {
			os.Stdout, os.Stderr = so, se
			if v := recover(); v != nil {
				ce, ok := v.(runtime.CellError)
				if !ok {
					t.Fatalf("panic that is no CellError: %v", v)
				}
				r.failure = ce.CellError()
			}
		}()
		s.RunContext(ctx, Cell{Source: src})
	}()
	_ = out.Close()
	_ = errf.Close()
	o, _ := os.ReadFile(dir + "/out")
	e, _ := os.ReadFile(dir + "/err")
	r.stdout, r.stderr = string(o), string(e)
	return r
}

// sloppy are limits no test cell reaches.
var sloppy = runtime.RLimits{MaxAlloc: 1 << 30, MaxSteps: 1 << 30, Timeout: time.Minute}

func TestOutput(t *testing.T) {
	s := NewWith(sloppy)
	// A visible value is printed, an invisible one is not, and the session keeps
	// the variables from one cell to the next.
	if r := run(t, s, "x <- c(1.5, 2.5)\n"); r != (cellRun{}) {
		t.Errorf("an assignment is invisible: %+v", r)
	}
	if r := run(t, s, "sum(x)\ninvisible(7)\ncat('a')\n"); r != (cellRun{stdout: "[1] 4\na"}) {
		t.Errorf("%+v", r)
	}
	if r := run(t, s, "print('p')\nmessage('m')\n"); r != (cellRun{stdout: "[1] \"p\"\n", stderr: "m\n"}) {
		t.Errorf("%+v", r)
	}
}

func TestWarnings(t *testing.T) {
	s := NewWith(sloppy)
	r := run(t, s, "f <- function(x) { warning('careful'); x }\nf(1)\n")
	if r.failure != "" || r.stdout != "[1] 1\n" || r.stderr != "Warning message:\nIn f(1) : careful\n" {
		t.Errorf("one warning: %+v", r)
	}
	r = run(t, s, "warning('a')\nf(2)\n")
	if r.stderr != "Warning messages:\n1: a\n2: In f(2) : careful\n" {
		t.Errorf("two warnings: %+v", r)
	}
	// An error comes with the warnings raised before it, as R prints them.
	r = run(t, s, "f(1)\nstop('late')\n")
	if r.failure != "Error: late\nIn addition: Warning message:\nIn f(1) : careful\n" || r.stdout != "[1] 1\n" {
		t.Errorf("error after a warning: %+v", r)
	}
}

func TestErrors(t *testing.T) {
	s := NewWith(sloppy)
	// R's own form, the output before the error kept, the session kept too.
	r := run(t, s, "y <- 3\ncat('before\\n')\nmessage('said')\ng <- function(a) stop('boom ', a)\ng(y)\n")
	if r.failure != "Error in g(y) : boom 3" || r.stdout != "before\n" || r.stderr != "said\n" {
		t.Errorf("%+v", r)
	}
	if r := run(t, s, "y\n"); r.stdout != "[1] 3\n" {
		t.Errorf("the session lost y: %+v", r)
	}
	if r := run(t, s, "stop('no call')"); r.failure != "Error: no call" {
		t.Errorf("%+v", r)
	}
	if r := run(t, s, "tryCatch(stop('x'), error = function(e) cat('caught', conditionMessage(e), '\\n'))"); r.failure != "" || r.stdout != "caught x \n" {
		t.Errorf("a handled error: %+v", r)
	}
	// A cell that cannot be parsed or is over the parse limits is a failure too.
	if r := run(t, s, "x <- <- 1"); !strings.HasPrefix(r.failure, "syntax error: ") || !strings.HasSuffix(r.failure, "(line 1)") {
		t.Errorf("%+v", r)
	}
	if r := run(t, s, strings.Repeat(" ", parseLimits.MaxBytes+1)); !strings.HasPrefix(r.failure, "the R cell is too large or too deeply nested to run: ") {
		t.Errorf("%+v", r)
	}
}

// The limits each end a cell that would otherwise run without end, with R's own
// error, and leave the session usable.
func TestLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    runtime.RLimits
		src  string
		want string
	}{
		{"steps", runtime.RLimits{MaxSteps: 10_000}, "while(TRUE) 0", "Error: evaluation step limit reached"},
		{"steps in a loop that catches errors", runtime.RLimits{MaxSteps: 10_000, Timeout: time.Minute},
			"repeat tryCatch(NULL, error = function(e) NULL)", "Error: evaluation step limit reached"},
		{"time", runtime.RLimits{Timeout: 50 * time.Millisecond},
			"repeat tryCatch(NULL, error = function(e) NULL)", "stopped: the R cell ran longer than 50ms, the limit"},
		{"allocation, over time", runtime.RLimits{MaxAlloc: 64 << 20}, "for (i in 1:1000) numeric(1e6)",
			"Error: memory limit exceeded: more than 67108864 bytes of vectors allocated"},
		{"allocation, in a loop that grows a vector", runtime.RLimits{MaxAlloc: 8 << 20}, "x <- NULL\nfor (i in 1:100000) x <- c(x, i)",
			"Error: memory limit exceeded: more than 8388608 bytes of vectors allocated"},
		{"vector length", sloppy, "x <- numeric(2^27)", "Error in numeric(2^27) : cannot allocate vector of length 67108865"},
		{"depth", sloppy, "f <- function(n) f(n + 1)\nf(1)",
			"Error: evaluation nested too deeply: infinite recursion / options(expressions=)?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewWith(tc.l)
			start := time.Now()
			r := run(t, s, tc.src)
			if r.failure != tc.want {
				t.Errorf("failure %q, want %q", r.failure, tc.want)
			}
			if d := time.Since(start); d > 30*time.Second {
				t.Errorf("took %v", d)
			}
			if r := run(t, s, "1 + 1"); r.failure != "" || r.stdout != "[1] 2\n" {
				t.Errorf("the session after the limit: %+v", r)
			}
		})
	}
}

// A zero RLimits is bounded, as DefaultRLimits: the cell ends on a limit.
func TestZeroIsBounded(t *testing.T) {
	s := NewWith(runtime.RLimits{})
	if s.l != runtime.DefaultRLimits {
		t.Errorf("limits %+v, want %+v", s.l, runtime.DefaultRLimits)
	}
	if r := run(t, s, "while(TRUE) 0"); r.failure != "Error: evaluation step limit reached" {
		t.Errorf("%+v", r)
	}
}

// -1 is eval.Unbounded, field by field: the options carry it, and a cell runs
// past what the zero field (the default) allows.
func TestUnbounded(t *testing.T) {
	if runtime.Unbounded != eval.Unbounded {
		t.Fatalf("runtime.Unbounded %d, eval.Unbounded %d", runtime.Unbounded, eval.Unbounded)
	}
	u := newOptions(runtime.RLimits{MaxAlloc: -1, MaxSteps: -1, Timeout: -1}.Resolve())
	if u.MaxAlloc != eval.Unbounded || u.MaxSteps != eval.Unbounded || u.Timeout != eval.Unbounded {
		t.Errorf("options %+v", u)
	}
	d := newOptions(runtime.RLimits{}.Resolve())
	if d.MaxAlloc != runtime.DefaultRLimits.MaxAlloc || d.MaxSteps != runtime.DefaultRLimits.MaxSteps || d.Timeout != runtime.DefaultRLimits.Timeout {
		t.Errorf("default options %+v", d)
	}
	for _, tc := range []struct {
		name string
		l    runtime.RLimits // the field under test is zero in bounded, -1 in unbounded
		src  string
		want string // the failure of the bounded session
	}{
		// 3 steps an iteration: 60 million, over the default 50 million.
		{"steps", runtime.RLimits{MaxSteps: -1}, "s <- 0\nfor (i in 1:2e7) s <- s + i",
			"Error: evaluation step limit reached"},
		// 20 vectors of 32 MB: 640 MB, over the default 512 MiB.
		{"alloc", runtime.RLimits{MaxAlloc: -1}, "for (i in 1:20) numeric(4e6)",
			"Error: memory limit exceeded: more than 536870912 bytes of vectors allocated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if r := run(t, NewWith(tc.l), tc.src); r.failure != "" {
				t.Errorf("unbounded: %+v", r)
			}
			zero := tc.l
			zero.MaxSteps, zero.MaxAlloc = 0, 0
			if r := run(t, NewWith(zero), tc.src); r.failure != tc.want {
				t.Errorf("default: %q, want %q", r.failure, tc.want)
			}
		})
	}
}

func TestContext(t *testing.T) {
	s := NewWith(runtime.RLimits{MaxSteps: 1 << 30, Timeout: runtime.Unbounded})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := runCtx(t, ctx, s, "while(TRUE) 0"); r.failure != "stopped: the R cell was cancelled" {
		t.Errorf("cancelled: %+v", r)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if r := runCtx(t, ctx, s, "repeat tryCatch(NULL, error = function(e) NULL)"); r.failure != "stopped: the R cell's deadline passed" {
		t.Errorf("deadline of the context, no timeout of the session: %+v", r)
	}
}

// Calls are serialised: eval.Session is not safe for concurrent use, and the
// race detector says so if they are not (CI runs this with -race).
func TestSerialised(t *testing.T) {
	s := NewWith(sloppy)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { s.Run(Cell{Source: "v <- 1; w <- v + 1"}) })
	}
	wg.Wait()
	if v, ok := s.s.Get("w"); !ok || eval.Length(v) != 1 {
		t.Errorf("w = %v, %v", v, ok)
	}
}

func TestWarningText(t *testing.T) {
	if WarningText(nil, false) != "" || WarningText(nil, true) != "" {
		t.Error("no warning")
	}
	one := []eval.Warning{{Message: "m", Call: "f(x)"}}
	if got := WarningText(one, false); got != "Warning message:\nIn f(x) : m\n" {
		t.Errorf("%q", got)
	}
	if got := WarningText(one, true); got != "In addition: Warning message:\nIn f(x) : m\n" {
		t.Errorf("%q", got)
	}
	two := []eval.Warning{{Message: "a"}, {Message: "b", Call: "g()"}}
	if got := WarningText(two, false); got != "Warning messages:\n1: a\n2: In g() : b\n" {
		t.Errorf("%q", got)
	}
}

func TestNew(t *testing.T) {
	t.Setenv(runtime.RLimitsEnv, "0,5000,0")
	s := New()
	if s.l != (runtime.RLimits{MaxSteps: 5000}).Resolve() {
		t.Errorf("limits %+v", s.l)
	}
	if r := run(t, s, "while(TRUE) 0"); r.failure != "Error: evaluation step limit reached" {
		t.Errorf("%+v", r)
	}
	t.Setenv(runtime.RLimitsEnv, "")
	if New().l != runtime.DefaultRLimits {
		t.Error("no environment is the defaults")
	}
}

// An allocation bomb is refused, and the heap stays bounded while it is: the
// test CI runs by itself under GOMEMLIMIT and ulimit -v.
func TestAllocBomb(t *testing.T) {
	s := NewWith(runtime.DefaultRLimits)
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m goruntime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				goruntime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	// Each pass makes a vector of 80 MB that nothing keeps, and the vector that
	// holds the rest is grown 100000 times: either would take every byte of the
	// machine without the bound.
	r1 := run(t, s, "for (i in 1:100000) numeric(1e7)")
	r2 := run(t, s, "x <- NULL\nfor (i in 1:10000000) x <- c(x, numeric(1e5))")
	r3 := run(t, s, "m <- matrix(0, 1e5, 1e5)")
	close(stop)
	<-done
	for i, r := range []cellRun{r1, r2, r3} {
		if !strings.Contains(r.failure, "memory limit exceeded") && !strings.Contains(r.failure, "vector size too large") {
			t.Errorf("bomb %d: %+v", i, r)
		}
	}
	if limit := uint64(runtime.DefaultRLimits.MaxAlloc) + 256<<20; peak > limit {
		t.Errorf("the heap reached %d bytes, over %d", peak, limit)
	}
	if r := run(t, s, "1"); r.stdout != "[1] 1\n" {
		t.Errorf("the session after the bombs: %+v", r)
	}
}
