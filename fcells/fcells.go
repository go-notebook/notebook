// Package fcells runs the Fortran cells of a notebook. The generated program of
// a notebook with Fortran cells imports it and gives each cell to one Session,
// a go-embedded-fortran eval.Session whose modules, procedures, derived types
// and variables persist from cell to cell (bib/fortran.md section 7 of
// go-scikit/roadmap).
//
// A Fortran cell shares nothing with the cells of another language
// (analysis.Isolated): its variables live in the session's modules, which the
// other languages cannot reach.
//
// It is linked only into a worker that has Fortran cells: the packages that
// analyse, generate and drive notebooks (analysis, gen, kernel) do not import
// go-embedded-fortran's evaluator, and analysis.TestNoEvaluatorImport keeps it
// so.
package fcells

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-embedded-fortran/fortran/eval"

	"github.com/go-notebook/notebook/runtime"
)

// Cell is one Fortran cell as the generated program hands it over.
type Cell struct {
	Source string
}

// Options bound what one cell may use. The evaluator's own bounds (array
// elements, character length, call depth) apply as well.
type Options struct {
	// MaxSteps is the number of DO iterations and procedure calls a cell may
	// make; zero is DefaultMaxSteps, negative is no bound.
	MaxSteps int64
	// Timeout ends a cell that runs longer; zero is DefaultTimeout, negative is
	// no bound.
	Timeout time.Duration
	// MaxOutput is the number of bytes a cell may write to standard output and
	// standard error together; the rest is dropped, and the cell says so. Zero
	// is DefaultMaxOutput.
	MaxOutput int
	// MaxAlloc bounds the storage the session holds, in bytes (the evaluator's
	// SessionOptions.MaxAlloc, a counted budget): every variable, array,
	// derived-type object and character component is charged before it is
	// allocated and credited when it is let go, and a cell that would pass the
	// bound fails and allocates nothing for it, whatever the size of the
	// allocations. It is the session's own count, not the process's heap. Zero
	// is DefaultMaxAlloc, negative is no bound.
	MaxAlloc int64
}

// The defaults of Options. The step bound is the evaluator's (about a minute of
// its fastest loops); the timeout is R's, runtime.DefaultRLimits.Timeout.
const (
	DefaultMaxSteps  = eval.DefaultMaxSteps
	DefaultMaxOutput = 16 << 20
)

// DefaultMaxAlloc is the heap bound, R's: runtime.DefaultRLimits.MaxAlloc.
var DefaultMaxAlloc = int64(runtime.DefaultRLimits.MaxAlloc)

// DefaultTimeout is the time a cell may run.
var DefaultTimeout = runtime.DefaultRLimits.Timeout

// Session is the Fortran session of one notebook. Its calls are serialised: an
// eval.Session is not safe for concurrent use.
type Session struct {
	mu  sync.Mutex
	opt Options
	// exec runs a cell in the evaluator: eval.Session.Run, a seam for the tests
	// that reach the failures it cannot be made to cause.
	exec func(ctx context.Context, src string, out, errOut io.Writer) (eval.Stats, error)
}

// New makes a session with the default options.
func New() *Session { return NewWith(Options{}) }

// NewWith makes a session; a zero field of o is its default.
func NewWith(o Options) *Session {
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxOutput == 0 {
		o.MaxOutput = DefaultMaxOutput
	}
	if o.MaxAlloc == 0 {
		o.MaxAlloc = DefaultMaxAlloc
	}
	if o.MaxAlloc < 0 {
		o.MaxAlloc = 0 // the evaluator's zero is no bound
	}
	// the analyser's limits on a cell's source are its own defaults: a notebook
	// may come from anyone
	return &Session{opt: o, exec: eval.NewSession(eval.SessionOptions{MaxSteps: o.MaxSteps, MaxAlloc: o.MaxAlloc}).Run}
}

// Run runs a cell. What it prints goes to the worker's standard output, what it
// writes to standard error (and the text of STOP) to the standard error. A
// failure of the cell panics with a runtime.CellError, as R's does.
func (s *Session) Run(c Cell) { s.RunContext(context.Background(), c) }

// RunContext is Run that ends, as a failed cell, when ctx is done.
func (s *Session) RunContext(ctx context.Context, c Cell) {
	out, err := s.Eval(ctx, c)
	_, _ = os.Stdout.WriteString(out.Stdout)
	_, _ = os.Stderr.WriteString(out.Stderr)
	if err != nil {
		panic(err)
	}
}

// Output is what a cell wrote.
type Output struct{ Stdout, Stderr string }

// Eval runs a cell like RunContext, but returns what it wrote instead of
// writing it to the process's standard streams: an embedder with no such
// streams of its own (the playground's WebAssembly core) shows the text. The
// Output is complete also when the cell fails; the error, a
// runtime.CellError, says why. Nothing in Eval touches the host: the
// evaluator has no file or environment access.
func (s *Session) Eval(ctx context.Context, c Cell) (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opt.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.opt.Timeout)
		defer cancel()
	}
	room := &outRoom{left: s.opt.MaxOutput, max: s.opt.MaxOutput}
	out, errOut := &capped{room: room}, &capped{room: room}
	_, err := s.run(ctx, c.Source, out, errOut)
	o := Output{Stdout: out.text(), Stderr: errOut.text()}
	if err != nil {
		return o, s.failure(err)
	}
	return o, nil
}

// run is the evaluator's Run, with a panic of the evaluator turned into the
// cell's failure: a defect in it must not take the notebook's worker down.
func (s *Session) run(ctx context.Context, src string, out, errOut *capped) (st eval.Stats, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the Fortran evaluator failed on this cell (%v); please report it", r)
		}
	}()
	return s.exec(ctx, src, out, errOut)
}

// failure is the cell's failure for an error of the evaluation.
func (s *Session) failure(err error) cellError {
	var ee *eval.Error
	if !errors.As(err, &ee) {
		return cellError(err.Error())
	}
	msg := ee.Msg
	switch {
	case errors.Is(err, context.DeadlineExceeded) && s.opt.Timeout > 0:
		msg = fmt.Sprintf("stopped: the Fortran cell ran longer than %s, the limit", s.opt.Timeout)
	case errors.Is(err, context.DeadlineExceeded):
		msg = "stopped: the Fortran cell's deadline passed"
	case errors.Is(err, context.Canceled):
		msg = "stopped: the Fortran cell was cancelled"
	}
	if where := position(ee.Pos); where != "" {
		return cellError(where + ": " + msg)
	}
	return cellError(msg)
}

// position turns the evaluator's "cell7.f90:3:5" into "line 3, column 5": the
// evaluator numbers its own cells, which the notebook's reader does not see.
func position(pos string) string {
	_, rest, ok := strings.Cut(pos, ".f90:")
	if !ok {
		return pos
	}
	line, col, ok := strings.Cut(rest, ":")
	if !ok {
		return "line " + rest
	}
	return "line " + line + ", column " + col
}

// capped keeps the first max bytes written to it and counts the rest.
type capped struct {
	room    *outRoom
	b       []byte
	dropped int
}

// outRoom is the output a cell may still write, shared by its standard output
// and its standard error: one cap on both, so that a cell cannot write twice
// the bound by alternating them.
type outRoom struct{ left, max int }

func (c *capped) Write(p []byte) (int, error) {
	room := min(c.room.left, len(p))
	if room > 0 {
		c.b = append(c.b, p[:room]...)
		c.room.left -= room
	}
	c.dropped += len(p) - max(room, 0)
	return len(p), nil
}

func (c *capped) text() string {
	if c.dropped == 0 {
		return string(c.b)
	}
	return fmt.Sprintf("%s\n[output truncated: %d bytes dropped after the first %d in all]\n", c.b, c.dropped, c.room.max)
}

// cellError is a cell's own failure, reported as it is rather than with the
// stack of the worker (runtime.CellError).
type cellError string

// CellError is the failure.
func (e cellError) CellError() string { return string(e) }

// Error makes the failure an error, for Eval.
func (e cellError) Error() string { return string(e) }
