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
	c := &capped{max: 5}
	c.Write([]byte("ab"))
	c.Write([]byte("cdefg"))
	c.Write([]byte("h"))
	if string(c.b) != "abcde" || c.dropped != 3 || !strings.Contains(c.text(), "[output truncated: 3 bytes dropped after the first 5]") {
		t.Fatalf("%q %d %q", c.b, c.dropped, c.text())
	}
	if c := (&capped{max: 5}); func() string { c.Write([]byte("x")); return c.text() }() != "x" {
		t.Fatal("a short output is as it is")
	}
}
