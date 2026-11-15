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
	"time"

	"github.com/go-embedded-julia/julia/interp"

	"github.com/go-notebook/notebook/runtime"
)

// The Base image the kernel boots from: the release of go-embedded-julia that
// ships it, and the SHA-256 of its julia-image-<Version>.bin asset (from that
// release's notes and its .sha256 asset; the images.sum ledger of go-embedded-julia's
// main has the line, the v0.15.0 tag's own copy does not). Moving to another release is changing
// both here, with the module's version in go.mod.
const (
	Version = "v0.17.0"
	Digest  = "d243d2b748afb7d331227195d835357b6502184bedcdc4bcc296854276c2f6ec"
)

// pinned is Digest as the kernel takes it; TestImageIsPinned checks that the
// constant parses.
var pinned, _ = interp.ParseDigest(Digest)

// elemBytes is what one element costs, for turning a limit in bytes into the
// kernel's: its MaxAlloc counts the elements of the memories (arrays and
// dictionary tables) a cell allocates, and each element is a Go `any`, 16 bytes
// on a 64-bit machine. That is a floor, not the heap: a boxed value (a Float64
// or a struct in an Array{Any}) costs more, and strings and BigInts are not
// counted at all. Each memory is also capped at 2^24 elements by the VM
// (interp.DefaultMaxMemoryElems), whatever the limit.
const elemBytes = 16

// fetchTimeout bounds the first cell's fetch and load of the image, about 100
// MB, whatever context the generated program (context.Background) gives. A
// variable for the test.
var fetchTimeout = 5 * time.Minute

// maxOutput is the number of bytes a cell may write to each of standard output
// and standard error; the rest is dropped, and the cell says so.
// (A variable, for the test that fills it in less than a minute.)
var maxOutput = 16 << 20

// Var is a notebook variable at the boundary: its name and a pointer to the Go
// variable that holds it.
type Var struct {
	Name string
	Ptr  any
	// IfAbsent gives the kernel the variable only when Main does not hold it
	// already: a Julia cell's variable, which the kernel keeps from cell to cell
	// but which a new worker gets back from its Go holder.
	IfAbsent bool
}

// Cell is one Julia cell as the generated program hands it over.
type Cell struct {
	Source string
	// In are the variables the cell reads that other cells define, Out those it
	// assigns. Only values of the types in the package comment cross.
	In, Out []Var
	// Note records the Go type each Out variable has now (see GoType), which
	// the next program declares it with; nil notes nothing.
	Note func(name, typ string)
}

// Session is the Julia kernel of one notebook. Its calls are serialised: a
// kernel runs one cell at a time.
type Session struct {
	mu sync.Mutex
	l  runtime.RLimits
	k  *interp.Kernel
	// startErr is why the kernel could not start, kept for the session.
	startErr error
	// held are the variables the kernel's Main holds that this session put there
	// or read out (the kernel has no cheap test of presence: Get copies the
	// array): a Julia cell's own variables, so that a new worker, whose session
	// is new and whose kernel is empty, gets them back from their Go holders.
	held map[string]bool
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
// NewWith(runtime.RLimits{}) is bounded. MaxSteps counts the statements a cell
// executes. MaxAlloc is a budget for the elements of the arrays and dictionary
// tables a cell allocates, counted at 16 bytes each (elemBytes): a floor of the
// memory, since boxed values cost more and strings are not counted, so it is
// not a bound on the heap; no array may have more than 2^24 elements either.
func NewWith(l runtime.RLimits) *Session { return &Session{l: l.Resolve(), held: map[string]bool{}} }

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

// elements is a limit in bytes as one in elements, at least one (zero is the
// kernel's default, not a bound); a negative limit stays negative, which is no
// bound.
func elements(bytes int) int64 {
	if bytes < 0 {
		return -1
	}
	return max(int64(bytes/elemBytes), 1)
}

// Run evaluates a cell. What it prints goes to the worker's standard output,
// with the value of the last expression as the Julia REPL shows it (nothing
// when the value is nothing or the cell ends in a semicolon); what it writes
// to standard error goes to the standard error. An exception fails the cell,
// with julia's text of it after "ERROR: ".
func (s *Session) Run(c Cell) { s.RunContext(context.Background(), c) }

// RunContext is Run that ends, as a failed cell, when ctx is done. The generated
// program calls Run, so for it only the limits and the worker's own timeout end
// a cell; the context is for an embedder that calls Eval or RunContext.
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
// cannot be made (the image cannot be fetched in fetchTimeout, or is not the
// pinned one) fails every Julia cell of the session with the same error, at
// once: the fetch is tried a single time, not at each cell.
func (s *Session) Eval(ctx context.Context, c Cell) (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.k == nil && s.startErr == nil {
		fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		k, err := fetchKernel(fctx, Version, pinned)
		cancel()
		if err != nil {
			failure := cellError("the Julia kernel could not start: " + err.Error())
			if ctx.Err() != nil {
				// the caller gave up (its context is done): that is not why
				// the kernel cannot start, so the next cell tries again
				return Output{}, failure
			}
			// a fetch or load failure, or the fetch's own deadline: the session's
			s.startErr = failure
		}
		s.k = k
	}
	if s.startErr != nil {
		return Output{}, s.startErr
	}
	if err := s.give(c.In); err != nil {
		return Output{}, err
	}
	res := s.k.EvalWith(ctx, c.Source, s.options())
	out := Output{Stdout: res.Stdout, Stderr: res.Stderr}
	if res.Truncated {
		out.Stdout += fmt.Sprintf("[output truncated: more than %d bytes]\n", maxOutput)
	}
	if res.Err != nil {
		return out, s.failure(res.Err)
	}
	s.take(c)
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
		s.held = map[string]bool{}
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
		// the kernel's own words say which bound: the cell's total of elements,
		// or the 2^24 elements one array may have (VM.MaxMemoryElems)
		return cellError("stopped: the Julia cell went over its memory limit: " + strings.TrimPrefix(err.Error(), "interp: memory limit exceeded: "))
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
