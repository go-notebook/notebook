// Package rcells runs the R cells of a notebook. The generated program of a
// notebook with R cells imports it and gives each cell to one Session, a
// go-embedded-r interpreter whose global environment persists from cell to
// cell, as the Ruby cells share one interpreter.
//
// It is linked only into a worker that has R cells: the packages that
// analyse, generate and drive notebooks (analysis, gen, kernel) do not import
// go-embedded-r's evaluator, and TestNoEvaluatorImport keeps it so.
package rcells

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/go-embedded-r/r/eval"
	"github.com/go-embedded-r/r/syntax"

	"github.com/go-notebook/notebook/runtime"
)

// Cell is one R cell as the generated program hands it over.
type Cell struct {
	Source string
}

// Session is the R interpreter of one notebook. Its calls are serialised: an
// eval.Session is not safe for concurrent use.
type Session struct {
	mu sync.Mutex
	s  *eval.Session
	l  runtime.RLimits
}

// maxVector is the longest vector a cell may make, in elements: 64 million,
// 512 MiB of doubles, as much as the default of MaxAlloc.
const maxVector = 1 << 26

// maxFileBytes bounds the session's files (readLines, saveRDS, sink...): 64
// MiB. They live in go-embedded-r's private in-memory file system, which is
// heap the cell's MaxAlloc does not count, so the bound is set here rather
// than left at the library's 256 MiB. No FileRoot is set, so no host file is
// reachable, and Env is nil, so Sys.getenv sees no host variable: a notebook
// may come from anyone.
const maxFileBytes = 64 << 20

// parseLimits bound the source of a cell, as analysis does before it is run.
var parseLimits = syntax.Limits{MaxBytes: 4 << 20, MaxDepth: 1000, MaxNodes: 1 << 20}

// New makes a session with the limits the kernel set in the worker's
// environment (runtime.RLimitsEnv), or runtime.DefaultRLimits.
func New() *Session { return NewWith(runtime.ParseRLimits(os.Getenv(runtime.RLimitsEnv))) }

// NewWith makes a session with the given limits, where a zero field is
// runtime.DefaultRLimits' value and runtime.Unbounded (-1), which is
// eval.Unbounded, is no bound: NewWith(runtime.RLimits{}) is bounded.
func NewWith(l runtime.RLimits) *Session { return NewWithDepth(l, 0) }

// NewWithDepth is NewWith with a bound on the nesting of evaluations, R's
// "expressions" option, for an embedder whose stack is smaller than a process's
// (WebAssembly in a browser: the engine's own recursion overflows the host's
// call stack before go-embedded-r's default of 5000 is reached). Zero is that
// default.
func NewWithDepth(l runtime.RLimits, maxDepth int) *Session {
	l = l.Resolve()
	o := newOptions(l)
	o.MaxDepth = maxDepth
	return &Session{l: l, s: eval.NewSession(o)}
}

// newOptions are the options of a session with limits that are resolved.
func newOptions(l runtime.RLimits) eval.Options {
	return eval.Options{
		MaxVector:    maxVector,
		MaxAlloc:     l.MaxAlloc,
		MaxSteps:     l.MaxSteps,
		Timeout:      l.Timeout,
		MaxFileBytes: maxFileBytes,
		Parse:        parseLimits,
	}
}

// Run evaluates a cell. What it prints goes to the worker's standard output,
// what it writes to standard error and its warnings, in R's "Warning message:"
// form, to the standard error; the value of the last expression, when R would
// print it, is already in the output. An error fails the cell, with R's
// "Error in <call> : <message>".
func (s *Session) Run(c Cell) { s.RunContext(context.Background(), c) }

// RunContext is Run that ends, as a failed cell, when ctx is done.
func (s *Session) RunContext(ctx context.Context, c Cell) {
	out, err := s.Eval(ctx, c)
	write(os.Stdout, out.Stdout)
	write(os.Stderr, out.Stderr)
	if err != nil {
		panic(err)
	}
}

// Output is what a cell wrote: Stdout is what it printed, with the value of its
// last expression when R would print it, Stderr what it wrote to standard error
// followed by its warnings, in R's "Warning message:" form.
type Output struct{ Stdout, Stderr string }

// Eval evaluates a cell like RunContext, but returns what it wrote instead of
// writing it to the process's standard streams: an embedder with no such
// streams of its own (the playground's WebAssembly core) shows the text.
// The Output is complete also when the cell fails; the error, a runtime.CellError,
// says why, as RunContext's panic does. Nothing in Eval touches the host.
func (s *Session) Eval(ctx context.Context, c Cell) (Output, error) {
	s.mu.Lock()
	res, err := s.s.EvalContext(ctx, c.Source)
	s.mu.Unlock()
	if err != nil {
		out, f := s.failure(err)
		return out, f
	}
	return Output{res.Output, res.Stderr + WarningText(res.Warnings, false)}, nil
}

// failure is the cell's failure for an error of the evaluation, with what the
// cell wrote before it failed.
func (s *Session) failure(err error) (Output, cellError) {
	var ee *eval.Error
	if errors.As(err, &ee) {
		out := Output{ee.Output, ee.Stderr}
		msg := ee.Error()
		switch {
		case errors.Is(err, context.DeadlineExceeded) && s.l.Timeout > 0:
			msg = fmt.Sprintf("stopped: the R cell ran longer than %s, the limit", s.l.Timeout)
		case errors.Is(err, context.DeadlineExceeded):
			msg = "stopped: the R cell's deadline passed"
		case errors.Is(err, context.Canceled):
			msg = "stopped: the R cell was cancelled"
		}
		if w := WarningText(ee.Warnings, true); w != "" {
			msg += "\n" + w
		}
		return out, cellError(msg)
	}
	var se *syntax.Error
	if errors.As(err, &se) {
		return Output{}, cellError(fmt.Sprintf("syntax error: %s (line %d)", se.Message, se.Line))
	}
	return Output{}, cellError("the R cell is too large or too deeply nested to run: " + err.Error())
}

// WarningText is how R prints its warnings at the end of a cell. One warning
// is "Warning message:" and a line, several are "Warning messages:" and a
// numbered line each; a warning with a call reads "In call : message", one
// without it just the message. After an error R says "In addition: " first.
// The empty string is no warning.
func WarningText(ws []eval.Warning, addition bool) string {
	if len(ws) == 0 {
		return ""
	}
	var b strings.Builder
	if addition {
		b.WriteString("In addition: ")
	}
	line := func(w eval.Warning) string {
		if w.Call == "" {
			return w.Message
		}
		return "In " + w.Call + " : " + w.Message
	}
	if len(ws) == 1 {
		b.WriteString("Warning message:\n" + line(ws[0]) + "\n")
		return b.String()
	}
	b.WriteString("Warning messages:\n")
	for i, w := range ws {
		fmt.Fprintf(&b, "%d: %s\n", i+1, line(w))
	}
	return b.String()
}

// write prints text as it is: cat("a") leaves no newline in R either.
func write(f *os.File, text string) { _, _ = f.WriteString(text) }

// cellError is a cell's own failure, reported as it is rather than with the
// stack of the worker (runtime.CellError).
type cellError string

// CellError is the failure.
func (e cellError) CellError() string { return string(e) }

// Error makes the failure an error, for Eval.
func (e cellError) Error() string { return string(e) }
