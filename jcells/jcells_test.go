package jcells

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-embedded-julia/julia/interp"

	"github.com/go-notebook/notebook/runtime"
)

// The tests that run a cell need go-embedded-julia's Base image, fetched once
// from its release and kept in the user's cache directory (about 100 MB, a
// second to boot). Offline they skip; in CI (CI is set) a kernel that cannot
// start fails the test, so that a lane that ran nothing is not green.
var (
	sharedOnce sync.Once
	shared     *Session
	sharedErr  error
)

// sharedSession is the session of the tests that only need a working kernel.
// Julia cells keep their globals from one to the next, so each test names its
// own.
func sharedSession(t *testing.T) *Session {
	t.Helper()
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	sharedOnce.Do(func() {
		// not the default 30 s: the first use of a Julia construct (a tuple, a
		// comprehension, a MethodError) takes seconds natively and over 30 s under
		// -race on a CI runner, and these tests are about values, not time
		shared = NewWith(runtime.RLimits{Timeout: 5 * time.Minute})
		_, sharedErr = shared.Eval(context.Background(), Cell{Source: "1"})
	})
	needKernel(t, sharedErr)
	return shared
}

// needKernel skips the test when the image cannot be had and the machine is
// offline, and fails it in CI.
func needKernel(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatalf("the Julia kernel did not start in CI: %v", err)
	}
	t.Skipf("no Julia kernel (offline?): %v", err)
}

// bounded is a session with its own limits, started like the shared one.
func bounded(t *testing.T, l runtime.RLimits) *Session {
	t.Helper()
	if testing.Short() {
		t.Skip("boots the Julia image")
	}
	s := NewWith(l)
	_, err := s.Eval(context.Background(), Cell{Source: "1"})
	needKernel(t, err)
	t.Cleanup(s.Close)
	return s
}

// heap is the live heap, in bytes.
func heap() uint64 {
	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func eval(t *testing.T, s *Session, src string) Output {
	t.Helper()
	out, err := s.Eval(context.Background(), Cell{Source: src})
	if err != nil {
		t.Fatalf("cell %q: %v", src, err)
	}
	return out
}

func cellErr(t *testing.T, err error) string {
	t.Helper()
	var ce runtime.CellError
	if !errors.As(err, &ce) {
		t.Fatalf("not a runtime.CellError: %v", err)
	}
	return ce.CellError()
}

// What the image is: the release and digest this package pins are a digest
// the kernel can parse, equal to the line of go-embedded-julia's images.sum
// for that release, and they are what a session asks the fetcher for. No
// network: the fetcher is replaced.
func TestImageIsPinned(t *testing.T) {
	d, err := interp.ParseDigest(Digest)
	if err != nil || d != pinned {
		t.Fatalf("Digest %q does not parse: %v", Digest, err)
	}
	if Version != "v0.24.0" || Digest != "cd5ddf2107c2a553247b87c59b04f803b7104d020a5ce5bbb2c044b82ff4cb35" {
		t.Fatalf("the pinned image changed: %s %s (moving it is a decision: go.mod, images.sum, this test)", Version, Digest)
	}
	if want := "julia-image-v0.24.0.bin"; interp.ImageAssetName(Version) != want {
		t.Fatalf("asset %s", interp.ImageAssetName(Version))
	}
	old := fetchKernel
	t.Cleanup(func() { fetchKernel = old })
	var gotV string
	var gotD [32]byte
	boom := errors.New("offline")
	calls := 0
	var deadline time.Time
	fetchKernel = func(ctx context.Context, v string, d [32]byte) (*interp.Kernel, error) {
		calls++
		gotV, gotD = v, d
		deadline, _ = ctx.Deadline()
		return nil, boom
	}
	s := New()
	_, err = s.Eval(context.Background(), Cell{Source: "1"})
	if gotV != Version || gotD != pinned {
		t.Fatalf("asked for %s %x", gotV, gotD)
	}
	// the generated program's context has no deadline: the fetch has its own
	if until := time.Until(deadline); until <= 0 || until > fetchTimeout {
		t.Fatalf("the fetch's deadline is %v away", until)
	}
	first := cellErr(t, err)
	if !strings.HasPrefix(first, "the Julia kernel could not start: offline") {
		t.Fatalf("%q", first)
	}
	if s.k != nil {
		t.Fatal("a kernel after a failed start")
	}
	// the failure is the session's: the next cell fails at once, with the same
	// text, and nothing is fetched again
	_, err = s.Eval(context.Background(), Cell{Source: "2"})
	if calls != 1 || cellErr(t, err) != first {
		t.Fatalf("%d fetches, then %v", calls, err)
	}
	s.Close() // nothing to close

	// The caller's own cancellation is not a failure to start: the next cell
	// tries again. The fetch's own deadline is one, and is kept.
	calls = 0
	fetchKernel = func(ctx context.Context, _ string, _ [32]byte) (*interp.Kernel, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	c := New()
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Eval(cctx, Cell{Source: "1"}); err == nil || c.startErr != nil || calls != 1 {
		t.Fatalf("a cancelled caller: %v, kept %v", err, c.startErr)
	}
	oldTimeout := fetchTimeout
	fetchTimeout = 10 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = oldTimeout })
	// the next cell tries again, and now the fetch's own deadline stops it
	if _, err = c.Eval(context.Background(), Cell{Source: "1"}); calls != 2 || err == nil || c.startErr == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("the fetch's deadline: %v", err)
	}
}

// A different image is refused before it is loaded: a cached file that is not
// the pinned one is hashed again, found different, removed and never loaded.
// The file has the name the fetcher gives its cache (version and the first
// eight bytes of the digest), so the check is what is exercised: without it the
// bytes would reach LoadImage and the error would be the loader's, not a
// failed fetch, and the file would stay. The only place to download from
// refuses the connection, so there is no other source.
func TestAWrongImageIsNeverLoaded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("%s-%x.img", Version, pinned[:8]))
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	k, err := interp.FetchKernel(ctx, Version, pinned, interp.FetchOptions{CacheDir: dir, BaseURL: "https://127.0.0.1:1"}, interp.LoadOptions{}, interp.KernelOptions{})
	if err == nil {
		k.Close()
		t.Fatal("a file that is not the pinned image became a kernel")
	}
	if !errors.Is(err, interp.ErrImageFetch) {
		t.Fatalf("the error is not the failed download that follows the refusal: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the wrong file was kept: %v", err)
	}
}

func TestLimitsOfASession(t *testing.T) {
	s := NewWith(runtime.RLimits{})
	if s.l != runtime.DefaultRLimits {
		t.Fatalf("%+v", s.l)
	}
	o := s.options()
	if o.Timeout != runtime.DefaultRLimits.Timeout || o.MaxSteps != int64(runtime.DefaultRLimits.MaxSteps) || o.MaxAlloc != int64(runtime.DefaultRLimits.MaxAlloc/16) || o.MaxOutput != maxOutput {
		t.Fatalf("%+v", o)
	}
	u := NewWith(runtime.RLimits{MaxAlloc: runtime.Unbounded, MaxSteps: runtime.Unbounded, Timeout: runtime.Unbounded}).options()
	if u.Timeout >= 0 || u.MaxSteps >= 0 || u.MaxAlloc >= 0 {
		t.Fatalf("unbounded is a negative bound: %+v", u)
	}
	// a limit of 1 to 15 bytes is one element, not zero, which would be the
	// kernel's own default
	for _, b := range []int{1, 7, 15, 16} {
		if got := NewWith(runtime.RLimits{MaxAlloc: b}).options().MaxAlloc; got != 1 {
			t.Errorf("MaxAlloc %d bytes is %d elements", b, got)
		}
	}
	if got := NewWith(runtime.RLimits{MaxAlloc: 32}).options().MaxAlloc; got != 2 {
		t.Errorf("32 bytes is %d elements", got)
	}
	t.Setenv(runtime.RLimitsEnv, "800,5,1000000000")
	if o := New().options(); o.MaxAlloc != 50 || o.MaxSteps != 5 || o.Timeout != time.Second {
		t.Fatalf("from the environment: %+v", o)
	}
}

func TestFailureText(t *testing.T) {
	s := NewWith(runtime.RLimits{Timeout: time.Second, MaxSteps: 7, MaxAlloc: 800})
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"julia":    {&interp.JuliaError{Type: "DomainError", Message: "DomainError with -1.0"}, "ERROR: DomainError with -1.0"},
		"timeout":  {interp.ErrCellTimeout, "stopped: the Julia cell ran longer than 1s, the limit"},
		"cancel":   {interp.ErrCellCanceled, "stopped: the Julia cell was cancelled"},
		"steps":    {interp.ErrStepLimit, "stopped: the Julia cell executed more than 7 statements, the limit"},
		"memory":   {fmt.Errorf("%w: the cell allocated more than 50 elements", interp.ErrMemoryLimit), "stopped: the Julia cell went over its memory limit: the cell allocated more than 50 elements"},
		"internal": {interp.ErrInternal, "the Julia cell failed: interp: internal error"},
		"busy":     {interp.ErrKernelBusy, "the Julia cell failed: interp: the kernel is running a cell"},
	} {
		if got := string(s.failure(tc.err)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// no timeout set: the deadline is the caller's
	u := NewWith(runtime.RLimits{Timeout: runtime.Unbounded})
	if got := string(u.failure(interp.ErrCellTimeout)); got != "stopped: the Julia cell's deadline passed" {
		t.Errorf("%q", got)
	}
}

func TestShow(t *testing.T) {
	for _, tc := range []struct {
		value, src string
		want       bool
	}{
		{"3", "1 + 2", true},
		{"3", "1 + 2;", false},
		{"3", "1 + 2;  \n", false},
		{"nothing", "println(1)", false},
		{"", "", false},
	} {
		if got := show(tc.value, tc.src); got != tc.want {
			t.Errorf("show(%q, %q) = %v", tc.value, tc.src, got)
		}
	}
	if e := cellError("x"); e.Error() != "x" || e.CellError() != "x" {
		t.Fatal(e)
	}
}

// The cells share one Main: a global, a function and a struct a cell defines
// are there in the next cell, and what a cell prints is its own output.
func TestSessionSharedBetweenCells(t *testing.T) {
	s := sharedSession(t)
	eval(t, s, "shared_n = 4")
	eval(t, s, "shared_double(x) = 2x\nstruct SharedPoint\n    x::Int\nend\n")
	out := eval(t, s, "println(\"n = \", shared_n)\nshared_double(shared_n) + SharedPoint(1).x")
	if out.Stdout != "n = 4\n9\n" || out.Stderr != "" {
		t.Fatalf("got %+v", out)
	}
	// the REPL shows no value after a semicolon or for nothing
	if out := eval(t, s, "shared_n + 1;"); out.Stdout != "" {
		t.Fatalf("a semicolon: %+v", out)
	}
	if out := eval(t, s, "println(shared_n)"); out.Stdout != "4\n" {
		t.Fatalf("println returns nothing: %+v", out)
	}
	// standard error is a stream of its own
	if out := eval(t, s, "println(stderr, \"warn\")"); out.Stderr != "warn\n" || out.Stdout != "" {
		t.Fatalf("stderr: %+v", out)
	}
}

// The failure of a cell is julia's own text, with what the cell printed before
// it failed, and the session goes on with what it had.
func TestJuliaError(t *testing.T) {
	s := sharedSession(t)
	eval(t, s, "err_kept = 3")
	out, err := s.Eval(context.Background(), Cell{Source: "println(\"before\")\nerr_kept = sqrt(-1.0)\n"})
	if msg := cellErr(t, err); msg != "ERROR: DomainError with -1.0:\nsqrt was called with a negative real argument but will only return a complex result if called with a complex argument. Try sqrt(Complex(x))." {
		t.Fatalf("message %q", msg)
	}
	if out.Stdout != "before\n" {
		t.Fatalf("output %q", out.Stdout)
	}
	if out := eval(t, s, "err_kept"); out.Stdout != "3\n" {
		t.Fatalf("the session after a failure: %+v", out)
	}
	_, err = s.Eval(context.Background(), Cell{Source: "undefined_name + 1"})
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "ERROR: UndefVarError:") || !strings.Contains(msg, "undefined_name") {
		t.Fatalf("message %q", msg)
	}
	_, err = s.Eval(context.Background(), Cell{Source: "x = (1,"})
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "ERROR: ") {
		t.Fatalf("a syntax error: %q", msg)
	}
	// Run panics with the CellError, as the generated program expects
	defer func() {
		var ce runtime.CellError
		if r, _ := recover().(error); !errors.As(r, &ce) || !strings.Contains(ce.CellError(), "boom") {
			t.Fatalf("recovered %v", r)
		}
	}()
	s.Run(Cell{Source: "error(\"boom\")"})
}

// RunContext writes to the process's streams: a cell that prints leaves it in
// os.Stdout and os.Stderr.
func TestRunWritesToTheStreams(t *testing.T) {
	s := sharedSession(t)
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wErr
	s.Run(Cell{Source: "println(\"to out\")\nprintln(stderr, \"to err\")\n:ok"})
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = wOut.Close()
	_ = wErr.Close()
	buf := make([]byte, 64)
	n, _ := rOut.Read(buf)
	m, _ := rErr.Read(buf[n:])
	if got := string(buf[:n]); got != "to out\n:ok\n" {
		t.Fatalf("stdout %q", got)
	}
	if got := string(buf[n : n+m]); got != "to err\n" {
		t.Fatalf("stderr %q", got)
	}
}

// Output past the cap is dropped, and the cell says so.
func TestOutputCap(t *testing.T) {
	s := bounded(t, runtime.RLimits{})
	old := maxOutput
	maxOutput = 1000
	t.Cleanup(func() { maxOutput = old })
	out := eval(t, s, "for i in 1:1000; println(\"0123456\"); end")
	if !strings.HasSuffix(out.Stdout, "[output truncated: more than 1000 bytes]\n") || len(out.Stdout) > 1100 {
		t.Fatalf("no notice: %d bytes, tail %q", len(out.Stdout), out.Stdout[max(0, len(out.Stdout)-60):])
	}
}

func TestTimeout(t *testing.T) {
	s := bounded(t, runtime.RLimits{Timeout: 300 * time.Millisecond, MaxSteps: runtime.Unbounded})
	eval(t, s, "kept = 1")
	start := time.Now()
	_, err := s.Eval(context.Background(), Cell{Source: "while true\nend"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell ran longer than 300ms, the limit" {
		t.Fatalf("message %q", msg)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("stopped after %s", d)
	}
	if out := eval(t, s, "kept"); out.Stdout != "1\n" {
		t.Fatalf("the session after the timeout: %+v", out)
	}
}

func TestStepLimit(t *testing.T) {
	s := bounded(t, runtime.RLimits{MaxSteps: 10_000})
	_, err := s.Eval(context.Background(), Cell{Source: "function spin()\n    for i in 1:10^9\n        i + 1\n    end\nend\nspin()"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell executed more than 10000 statements, the limit" {
		t.Fatalf("message %q", msg)
	}
	if out := eval(t, s, "1 + 1"); out.Stdout != "2\n" {
		t.Fatalf("the session after the limit: %+v", out)
	}
}

func TestMemoryLimit(t *testing.T) {
	// 16,000 bytes are 1,000 elements, at 16 bytes each
	s := bounded(t, runtime.RLimits{MaxAlloc: 16 * 1000})
	_, err := s.Eval(context.Background(), Cell{Source: "zeros(Float64, 10^7)"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell went over its memory limit: the cell allocated more than 1000 elements" {
		t.Fatalf("message %q", msg)
	}
	if out := eval(t, s, "length(zeros(10))"); out.Stdout != "10\n" {
		t.Fatalf("the session after the limit: %+v", out)
	}
	// a limit under one element is one element
	one := bounded(t, runtime.RLimits{MaxAlloc: 3})
	_, err = one.Eval(context.Background(), Cell{Source: "zeros(Float64, 10)"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell went over its memory limit: the cell allocated more than 1 elements" {
		t.Fatalf("message %q", msg)
	}
}

// One array has at most 2^24 elements (VM.MaxMemoryElems), whatever the limit,
// also with none: the stated bound is the kernel's, in its own words.
func TestOneArrayIsCapped(t *testing.T) {
	s := bounded(t, runtime.RLimits{MaxAlloc: runtime.Unbounded})
	_, err := s.Eval(context.Background(), Cell{Source: "Array{Float64}(undef, 2^25)"})
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "stopped: the Julia cell went over its memory limit: ") || !strings.Contains(msg, "VM.MaxMemoryElems is 16777216") || strings.Contains(msg, "-1") {
		t.Fatalf("message %q", msg)
	}
}

// A bomb of arrays, run under GOMEMLIMIT and ulimit -v in CI (the security
// job): the refusal must be the memory limit's, and the heap must stay under
// the limit plus the largest single array (2^24 elements, 256 MiB) and the
// garbage of the run, 256 MiB more. Elements are counted at 16 bytes; a boxed
// value costs more, which is why the bomb is made of Float64 arrays.
func TestAllocBomb(t *testing.T) {
	s := bounded(t, runtime.RLimits{})
	base := heap()
	stop, peak := make(chan struct{}), make(chan [2]uint64)
	go func() {
		var top, n uint64
		for {
			select {
			case <-stop:
				peak <- [2]uint64{top, n}
				return
			case <-time.After(250 * time.Millisecond): // ReadMemStats stops the world: not often, under -race
				top, n = max(top, heap()), n+1
			}
		}
	}()
	_, err := s.Eval(context.Background(), Cell{Source: "xs = []\nfor i in 1:100\n    push!(xs, Array{Float64}(undef, 10^7))\nend"})
	// The bomb may be over before the first tick: one sample now, while xs, a
	// global of the kernel, still holds the arrays the cell made.
	final := heap()
	close(stop)
	p := <-peak
	top, samples := max(p[0], final), p[1]+1
	if samples == 0 || top == 0 {
		t.Fatal("no heap sample was taken")
	}
	if limit := uint64(runtime.DefaultRLimits.MaxAlloc) + 512<<20; top > base+limit {
		t.Fatalf("the heap reached %d MiB over its start, the bound is %d MiB", (top-base)>>20, limit>>20)
	}
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "stopped: the Julia cell went over its memory limit: the cell allocated more than 33554432 elements") {
		t.Fatalf("message %q", msg)
	}
}

func TestCancel(t *testing.T) {
	s := bounded(t, runtime.RLimits{Timeout: runtime.Unbounded, MaxSteps: runtime.Unbounded})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err := s.Eval(ctx, Cell{Source: "while true\nend"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell was cancelled" {
		t.Fatalf("message %q", msg)
	}
	// a context that is done before the cell starts runs nothing
	_, err = s.Eval(ctx, Cell{Source: "println(\"never\")"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell was cancelled" {
		t.Fatalf("message %q", msg)
	}
	if out := eval(t, s, "2 + 2"); out.Stdout != "4\n" {
		t.Fatalf("the session after the cancellation: %+v", out)
	}
	// RunContext fails the cell the same way
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RunContext of a cancelled context did not fail the cell")
		}
	}()
	s.RunContext(ctx, Cell{Source: "1"})
}

// Soft scope (go-embedded-julia#54, fixed in v0.17.0): at the top level of a
// cell a loop updates a global the cell defined, as julia 1.12 in the REPL does.
func TestSoftScope(t *testing.T) {
	s := sharedSession(t)
	if out := eval(t, s, "scope_s = 0\nfor i in 1:10\n    scope_s += i\nend\nscope_s"); out.Stdout != "55\n" {
		t.Fatalf("%+v", out)
	}
	// and a global of an earlier cell
	eval(t, s, "scope_t = 0")
	if out := eval(t, s, "for i in 1:4\n    scope_t += i\nend\nscope_t"); out.Stdout != "10\n" {
		t.Fatalf("%+v", out)
	}
}

// A MethodError is julia's text (go-embedded-julia#55, fixed in v0.17.0).
func TestMethodErrorText(t *testing.T) {
	// the text of a MethodError is built by julia code that takes 2 s natively
	// and over 30 s under -race on a CI runner: the default limit is not the
	// test's business
	s := bounded(t, runtime.RLimits{Timeout: 5 * time.Minute})
	_, err := s.Eval(context.Background(), Cell{Source: "sin(\"a\")"})
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "ERROR: MethodError: no method matching sin(::String)") {
		t.Fatalf("message %q", msg)
	}
}

// Level-3 BLAS works on the Base image since v0.22.0 (go-embedded-julia#113,
// forwarded to go-blas): a Float64 matrix product and A'*A. It runs under the
// default limits: since v0.24.0 the image carries the inference of a matrix
// display, which cost about 70 million statements on the first product before.
func TestMatrixProductsWork(t *testing.T) {
	s := bounded(t, runtime.RLimits{})
	for name, tc := range map[string]struct{ src, want string }{
		"a Float64 matrix product A*B": {"[1.0 2.0; 3.0 4.0] * [5.0 6.0; 7.0 8.0]", "2×2 Matrix{Float64}:\n 19.0  22.0\n 43.0  50.0\n"},
		"a triangular product (trmm)":  {"using LinearAlgebra; LowerTriangular([2.0 0.0; 1.0 3.0]) * [1.0 2.0; 3.0 4.0]", "2×2 Matrix{Float64}:\n  2.0   4.0\n 10.0  14.0\n"},
		"A'*A":                         {"A = [1.0 2.0; 3.0 4.0]; A' * A", "2×2 Matrix{Float64}:\n 10.0  14.0\n 14.0  20.0\n"},
	} {
		out, err := s.Eval(context.Background(), Cell{Source: tc.src})
		if err != nil || out.Stdout != tc.want {
			t.Errorf("%s: %q %v, want %q", name, out.Stdout, err, tc.want)
		}
	}
}

// What the Base image lacked before v0.18.0 works now: a Float64 matrix-vector
// product (go-blas, #67), and case conversion and normalization of non-ASCII
// text (utf8proc, #65).
func TestWhatTheImageLackedWorks(t *testing.T) {
	s := sharedSession(t)
	for name, tc := range map[string]struct{ src, want string }{
		"a Float64 matrix-vector product (BLAS, #67)": {"[1.0 2.0; 3.0 4.0] * [1.0, 1.0]", "2-element Vector{Float64}:\n 3.0\n 7.0\n"},
		"uppercase of a non-ASCII string (#65)":       {"uppercase(\"é\")", "\"É\"\n"},
		"lowercase of a non-ASCII string (#65)":       {"lowercase(\"ÉÀ\")", "\"éà\"\n"},
		"normalization (#65)":                         {"Base.Unicode.normalize(\"e\\u0301\", :NFC) == \"\\u00e9\"", "true\n"},
	} {
		out, err := s.Eval(context.Background(), Cell{Source: tc.src})
		if err != nil || out.Stdout != tc.want {
			t.Errorf("%s: %q %v, want %q", name, out.Stdout, err, tc.want)
		}
	}
}
