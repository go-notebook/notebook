package fcells

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-embedded-fortran/fortran/eval"

	"github.com/go-notebook/notebook/runtime"
)

func eval1(t *testing.T, s *Session, src string) Output {
	t.Helper()
	out, err := s.Eval(context.Background(), Cell{Source: src})
	if err != nil {
		t.Fatalf("cell %q: %v", src, err)
	}
	return out
}

// The cells share one session: a module, a procedure and a variable a cell
// defines are there in the next cell, and what a cell prints is its own output.
func TestSessionSharedBetweenCells(t *testing.T) {
	s := New()
	eval1(t, s, "module stats\n  implicit none\ncontains\n  function mean(x, n) result(m)\n    integer, intent(in) :: n\n    real(8), intent(in) :: x\n    real(8) :: m\n    m = x / n\n  end function\nend module\n")
	eval1(t, s, "integer :: n = 4\nreal(8) :: total\ntotal = 10.0d0\n")
	out := eval1(t, s, "use stats\nprint '(f6.2)', mean(total, n)\n")
	if out.Stdout != "  2.50\n" || out.Stderr != "" {
		t.Fatalf("got %+v", out)
	}
}

func TestStopAndStandardError(t *testing.T) {
	s := New()
	out := eval1(t, s, "integer :: k = 7\nwrite (0, '(a)') 'warn'\nstop 2\n")
	if out.Stderr != "warn\nSTOP 2\n" {
		t.Fatalf("got %+v", out)
	}
	if out := eval1(t, s, "print *, k\n"); strings.TrimSpace(out.Stdout) != "7" {
		t.Fatalf("the session after STOP: %+v", out)
	}
}

// An error is a runtime.CellError at a line and column of the cell, not of the
// evaluator's own numbering, with what the cell wrote before it failed.
func TestErrors(t *testing.T) {
	s := New()
	out, err := s.Eval(context.Background(), Cell{Source: "integer :: i\ni = 1\nprint *, 'before'\ni = undefined + 1\n"})
	var ce runtime.CellError
	if !errors.As(err, &ce) || !strings.HasPrefix(ce.CellError(), "line 4, column ") || !strings.Contains(ce.CellError(), "undefined") {
		t.Fatalf("error %v", err)
	}
	// the cell was refused before it ran: nothing printed, nothing defined
	if out.Stdout != "" {
		t.Fatalf("output %q", out.Stdout)
	}
	// a runtime failure keeps what the cell wrote, and the session
	eval1(t, s, "real(8) :: v(2)\n")
	out, err = s.Eval(context.Background(), Cell{Source: "print *, 'before'\nv(3) = 1d0\n"})
	if err == nil || !strings.Contains(err.Error(), "outside 1:2") || !strings.Contains(out.Stdout, "before") {
		t.Fatalf("%v %+v", err, out)
	}
	if o := eval1(t, s, "print *, size(v)\n"); strings.TrimSpace(o.Stdout) != "2" {
		t.Fatalf("session after a failure: %+v", o)
	}
}

// An infinite loop in a cell is refused, by the step bound and by the time
// limit, and the session goes on.
func TestLimits(t *testing.T) {
	s := NewWith(Options{MaxSteps: 1 << 20})
	eval1(t, s, "integer :: kept = 3\n")
	_, err := s.Eval(context.Background(), Cell{Source: "do\nend do\n"})
	if err == nil || !strings.Contains(err.Error(), "step limit reached") {
		t.Fatalf("the step bound: %v", err)
	}

	s = NewWith(Options{MaxSteps: -1, Timeout: 200 * time.Millisecond})
	eval1(t, s, "integer :: kept = 3\n")
	start := time.Now()
	_, err = s.Eval(context.Background(), Cell{Source: "do\nend do\n"})
	if err == nil || err.Error() != "stopped: the Fortran cell ran longer than 200ms, the limit" {
		t.Fatalf("the time limit: %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	if o := eval1(t, s, "print *, kept\n"); strings.TrimSpace(o.Stdout) != "3" {
		t.Fatalf("session after a limit: %+v", o)
	}

	// a cancelled context ends the cell too
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewWith(Options{MaxSteps: -1}).Eval(ctx, Cell{Source: "do\nend do\n"})
	if err == nil || err.Error() != "stopped: the Fortran cell was cancelled" {
		t.Fatalf("cancel: %v", err)
	}
}

// A cell that prints without end is cut at MaxOutput, and says so.
func TestOutputIsBounded(t *testing.T) {
	s := NewWith(Options{MaxOutput: 1000})
	out := eval1(t, s, "integer :: i\ndo i = 1, 5000\n  print *, 'xxxxxxxxxx', i\nend do\n")
	if len(out.Stdout) > 1200 || !strings.Contains(out.Stdout, "[output truncated:") {
		t.Fatalf("%d bytes: %q...", len(out.Stdout), out.Stdout[:min(80, len(out.Stdout))])
	}
}

// The evaluator's bounds apply to a cell: an array that would not fit is
// refused, with the session intact.
func TestAllocBomb(t *testing.T) {
	s := New()
	_, err := s.Eval(context.Background(), Cell{Source: "real(8), allocatable :: a(:)\nallocate(a(2000000000))\n"})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("%v", err)
	}
	if o := eval1(t, s, "print *, 1\n"); strings.TrimSpace(o.Stdout) != "1" {
		t.Fatal(o)
	}
}

func TestPosition(t *testing.T) {
	for in, want := range map[string]string{
		"cell3.f90:4:9": "line 4, column 9",
		"cell3.f90:4":   "line 4",
		"":              "",
	} {
		if got := position(in); got != want {
			t.Errorf("position(%q) = %q, want %q", in, got, want)
		}
	}
}

// Run writes to the process's streams and panics with the cell's failure, as the
// generated program expects.
func TestRunPanicsWithTheCellsFailure(t *testing.T) {
	s := New()
	s.Run(Cell{Source: "integer :: k = 1\n"})
	defer func() {
		r := recover()
		ce, ok := r.(runtime.CellError)
		if !ok || !strings.Contains(ce.CellError(), "outside 1:1") || ce.(error).Error() != ce.CellError() {
			t.Fatalf("panic %v", r)
		}
	}()
	s.Run(Cell{Source: "real(8) :: v(1)\nv(2) = 1d0\n"})
	t.Fatal("no panic")
}

// What the evaluator fails with besides its own error is the cell's failure
// too, and a panic in it never reaches the worker.
func TestFailures(t *testing.T) {
	s := New()
	for name, tc := range map[string]struct {
		exec func(context.Context, string, io.Writer, io.Writer) (eval.Stats, error)
		want string
	}{
		"other error": {func(context.Context, string, io.Writer, io.Writer) (eval.Stats, error) {
			return eval.Stats{}, errors.New("odd")
		}, "odd"},
		"panic": {func(context.Context, string, io.Writer, io.Writer) (eval.Stats, error) {
			panic("boom")
		}, "the Fortran evaluator failed on this cell (boom); please report it"},
		"no position": {func(context.Context, string, io.Writer, io.Writer) (eval.Stats, error) {
			return eval.Stats{}, &eval.Error{Msg: "plain"}
		}, "plain"},
	} {
		s.exec = tc.exec
		_, err := s.Eval(context.Background(), Cell{})
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// with no time limit of ours, the caller's deadline is named as the caller's
	s = NewWith(Options{Timeout: -1})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.Eval(ctx, Cell{Source: "do\nend do\n"})
	if err == nil || err.Error() != "stopped: the Fortran cell's deadline passed" {
		t.Fatalf("%v", err)
	}
}

func TestCapped(t *testing.T) {
	c := &capped{room: &outRoom{left: 5, max: 5}}
	c.Write([]byte("ab"))
	c.Write([]byte("cdefg"))
	c.Write([]byte("h"))
	if string(c.b) != "abcde" || c.dropped != 3 || !strings.Contains(c.text(), "[output truncated: 3 bytes dropped after the first 5 in all]") {
		t.Fatalf("%q %d %q", c.b, c.dropped, c.text())
	}
	if c := (&capped{room: &outRoom{left: 5, max: 5}}); func() string { c.Write([]byte("x")); return c.text() }() != "x" {
		t.Fatal("a short output is as it is")
	}
}

// Standard output and standard error share one cap: a cell that alternates
// them does not get twice the bound.
func TestOneCapForBothStreams(t *testing.T) {
	room := &outRoom{left: 10, max: 10}
	out, errOut := &capped{room: room}, &capped{room: room}
	out.Write([]byte("123456"))
	errOut.Write([]byte("abcdef"))
	out.Write([]byte("xyz"))
	if string(out.b)+string(errOut.b) != "123456abcd" || out.dropped != 3 || errOut.dropped != 2 {
		t.Fatalf("%q %q %d %d", out.b, errOut.b, out.dropped, errOut.dropped)
	}
}

// The budget counts what a session holds, whatever the size of each
// allocation: two arrays of 2^25 int64 (256 MiB each) under 512 MiB, 100000
// objects of a type with a 64000-byte array component (6.4 GB, no allocation
// over 64 KiB) and objects with a 1 MiB character component are refused, each
// as the cell's own failure, before they are allocated, and the session runs the
// next cell. A check reduced to the size of one allocation lets all three through.
func TestAllocBombIsRefused(t *testing.T) {
	for name, src := range map[string]string{
		"accumulation":        "integer, allocatable :: a(:), b(:)\nallocate(a(2**25))\nallocate(b(2**25))\n",
		"array component":     "type :: t\nreal(8) :: x(8000)\nend type\ntype(t) :: a(100000)\n",
		"character component": "type :: u\ncharacter(len=1048576) :: c\nend type\ntype(u) :: a(2000)\n",
	} {
		s := NewWith(Options{MaxAlloc: 512 << 20})
		_, err := s.Eval(context.Background(), Cell{Source: src})
		if err == nil || !strings.Contains(err.Error(), "memory limit exceeded") {
			t.Fatalf("%s: %v", name, err)
		}
		out := eval1(t, s, "print *, 6 * 7\n")
		if strings.TrimSpace(out.Stdout) != "42" {
			t.Fatalf("%s: %q", name, out.Stdout)
		}
	}
	s := NewWith(Options{MaxAlloc: 256 << 20})
	_, err := s.Eval(context.Background(), Cell{Source: "integer, allocatable :: a(:), b(:)\nallocate(a(2**27))\nallocate(b(2**27))\n"})
	if err == nil || !strings.Contains(err.Error(), "memory limit exceeded") {
		t.Fatalf("%v", err)
	}
	out := eval1(t, s, "print *, 6 * 7\n")
	if strings.TrimSpace(out.Stdout) != "42" {
		t.Fatalf("%q", out.Stdout)
	}
	if o := NewWith(Options{}).opt; o.MaxAlloc != DefaultMaxAlloc || NewWith(Options{MaxAlloc: -1}).opt.MaxAlloc != 0 {
		t.Fatalf("defaults: %+v", o)
	}
}

// A cell that prints an array too big for its budget is not refused when the
// output cap would cut it: the evaluator builds only the values that could be
// printed within MaxOutput.
func TestPrintOfABigArrayStopsAtTheCap(t *testing.T) {
	s := NewWith(Options{MaxAlloc: 100 << 20, MaxOutput: 1000})
	out, err := s.Eval(context.Background(), Cell{Source: "integer :: a(2**22)\na = 7\nprint *, a\n"})
	if err != nil || out.Stdout == "" {
		t.Fatalf("%q, %v", out.Stdout, err)
	}
	if len(out.Stdout) > 2000 {
		t.Fatalf("%d bytes kept of a cap of 1000", len(out.Stdout))
	}
}
