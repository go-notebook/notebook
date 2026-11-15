// Package jcells runs the Julia cells of a notebook. The generated program of a
// notebook with Julia cells imports it and gives each cell to one Session, a
// go-embedded-julia interp.Kernel whose Main module persists from cell to cell,
// as the global environment of R's does.
//
// The kernel boots from go-embedded-julia's prebuilt Base image, which is not
// part of the module: the Session fetches it on its first cell, from the
// release Version, and only bytes whose SHA-256 is Digest are loaded (the
// digest is pinned here, as go.sum pins a module). The verified image is kept
// in os.UserCacheDir and hashed again each time it is used. The cold start is
// about a second; nothing is fetched until a notebook runs a Julia cell.
//
// A Julia cell shares nothing with the cells of another language
// (analysis.Isolated): its variables live in the kernel's Main, which the other
// languages cannot reach.
//
// It is linked only into a worker that has Julia cells: the packages that
// analyse, generate and drive notebooks (analysis, gen, kernel) do not import
// go-embedded-julia's interpreter, and analysis.TestNoEvaluatorImport keeps it
// so.
package jcells

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/go-embedded-julia/julia/interp"

	"github.com/go-notebook/notebook/runtime"
)

// The Base image the kernel boots from: the release of go-embedded-julia that
// ships it, and the SHA-256 of its julia-image-<Version>.bin asset (from that
// release's notes and its images.sum). Moving to another release is changing
// both here, with the module's version in go.mod.
const (
	Version = "v0.15.0"
	Digest  = "a4568120a0db7acb054b1ab50727d41fe5091177b6c494541e5c88ff282c1bcf"
)

// pinned is Digest as the kernel takes it; TestImageIsPinned checks that the
// constant parses.
var pinned, _ = interp.ParseDigest(Digest)

// elemBytes is the size of what the kernel counts a cell's allocations in: its
// MaxAlloc is in elements (array and dictionary slots), so a limit in bytes
// is divided by eight, a Float64 or an Int64.
const elemBytes = 8

// maxOutput is the number of bytes a cell may write to each of standard output
// and standard error; the rest is dropped, and the cell says so.
// (A variable, for the test that fills it in less than a minute.)
var maxOutput = 16 << 20

// Cell is one Julia cell as the generated program hands it over.
type Cell struct {
	Source string
}

// Session is the Julia kernel of one notebook. Its calls are serialised: a
// kernel runs one cell at a time.
type Session struct {
	mu sync.Mutex
	l  runtime.RLimits
	k  *interp.Kernel
}

// fetchKernel boots a kernel from the pinned image: interp.FetchKernel, a seam
// for the tests that cannot reach the network and for the one that checks what
// is pinned.
var fetchKernel = func(ctx context.Context, version string, digest [32]byte) (*interp.Kernel, error) {
	return interp.FetchKernel(ctx, version, digest, interp.FetchOptions{}, interp.LoadOptions{}, interp.KernelOptions{})
}

// New makes a session with the limits the kernel set in the worker's
// environment (runtime.RLimitsEnv), or runtime.DefaultRLimits.
func New() *Session { return NewWith(runtime.ParseRLimits(os.Getenv(runtime.RLimitsEnv))) }

// NewWith makes a session with the given limits, where a zero field is
// runtime.DefaultRLimits' value and runtime.Unbounded (-1) is no bound:
// NewWith(runtime.RLimits{}) is bounded. MaxAlloc is in bytes and counts the
// elements of the arrays and dictionaries a cell makes, eight bytes each;
// MaxSteps counts the statements a cell executes.
func NewWith(l runtime.RLimits) *Session { return &Session{l: l.Resolve()} }

// options are the budgets of a cell for the kernel: a negative field is no
// bound there, as here.
func (s *Session) options() interp.CellOptions {
	return interp.CellOptions{
		Timeout:   s.l.Timeout,
		MaxSteps:  int64(s.l.MaxSteps),
		MaxAlloc:  elements(s.l.MaxAlloc),
		MaxOutput: maxOutput,
	}
}

// elements is a limit in bytes as one in elements; a negative limit stays
// negative, which is no bound.
func elements(bytes int) int64 {
	if bytes < 0 {
		return -1
	}
	return int64(bytes / elemBytes)
}

// Run evaluates a cell. What it prints goes to the worker's standard output,
// with the value of the last expression as the Julia REPL shows it (nothing
// when the value is nothing or the cell ends in a semicolon); what it writes
// to standard error goes to the standard error. An exception fails the cell,
// with julia's text of it after "ERROR: ".
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

// Output is what a cell wrote: Stdout is what it printed followed by the value
// of its last expression when the REPL would show it, Stderr what it wrote to
// standard error.
type Output struct{ Stdout, Stderr string }

// Eval evaluates a cell like RunContext, but returns what it wrote instead of
// writing it to the process's standard streams. The Output is complete also
// when the cell fails; the error, a runtime.CellError, says why. A kernel that
// cannot be made (the image cannot be fetched, or is not the pinned one) is
// the cell's failure, and the next cell tries again.
func (s *Session) Eval(ctx context.Context, c Cell) (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.k == nil {
		k, err := fetchKernel(ctx, Version, pinned)
		if err != nil {
			return Output{}, cellError("the Julia kernel could not start: " + err.Error())
		}
		s.k = k
	}
	res := s.k.EvalWith(ctx, c.Source, s.options())
	out := Output{Stdout: res.Stdout, Stderr: res.Stderr}
	if res.Truncated {
		out.Stdout += fmt.Sprintf("[output truncated: more than %d bytes]\n", maxOutput)
	}
	if res.Err != nil {
		return out, s.failure(res.Err)
	}
	if show(res.Value, c.Source) {
		out.Stdout += res.Value + "\n"
	}
	return out, nil
}

// Close ends the session's kernel, if one was started. A cell that runs is
// not stopped: cancel its context first.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.k != nil {
		s.k.Close()
		s.k = nil
	}
}

// show reports whether the REPL shows the value of a cell: not nothing, and not
// when the cell's last statement ends in a semicolon.
func show(value, src string) bool {
	return value != "" && value != "nothing" && !strings.HasSuffix(strings.TrimSpace(src), ";")
}

// failure is the cell's failure for the error of an evaluation.
func (s *Session) failure(err error) cellError {
	var je *interp.JuliaError
	switch {
	case errors.As(err, &je):
		return cellError("ERROR: " + je.Message)
	case errors.Is(err, interp.ErrCellTimeout) && s.l.Timeout > 0:
		return cellError(fmt.Sprintf("stopped: the Julia cell ran longer than %s, the limit", s.l.Timeout))
	case errors.Is(err, interp.ErrCellTimeout):
		return cellError("stopped: the Julia cell's deadline passed")
	case errors.Is(err, interp.ErrCellCanceled):
		return cellError("stopped: the Julia cell was cancelled")
	case errors.Is(err, interp.ErrStepLimit):
		return cellError(fmt.Sprintf("stopped: the Julia cell executed more than %d statements, the limit", s.l.MaxSteps))
	case errors.Is(err, interp.ErrMemoryLimit):
		return cellError(fmt.Sprintf("stopped: the Julia cell allocated more than %d bytes of array elements, the limit", s.l.MaxAlloc))
	}
	return cellError("the Julia cell failed: " + err.Error())
}

// cellError is a cell's own failure, reported as it is rather than with the
// stack of the worker (runtime.CellError).
type cellError string

// CellError is the failure.
func (e cellError) CellError() string { return string(e) }

// Error makes the failure an error, for Eval.
func (e cellError) Error() string { return string(e) }
