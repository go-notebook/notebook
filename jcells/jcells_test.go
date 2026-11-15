package jcells

import (
	"context"
	"errors"
	"os"
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
		shared = New()
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
	if Version != "v0.15.0" || Digest != "a4568120a0db7acb054b1ab50727d41fe5091177b6c494541e5c88ff282c1bcf" {
		t.Fatalf("the pinned image changed: %s %s (moving it is a decision: go.mod, images.sum, this test)", Version, Digest)
	}
	if want := "julia-image-v0.15.0.bin"; interp.ImageAssetName(Version) != want {
		t.Fatalf("asset %s", interp.ImageAssetName(Version))
	}
	old := fetchKernel
	t.Cleanup(func() { fetchKernel = old })
	var gotV string
	var gotD [32]byte
	boom := errors.New("offline")
	fetchKernel = func(_ context.Context, v string, d [32]byte) (*interp.Kernel, error) {
		gotV, gotD = v, d
		return nil, boom
	}
	s := New()
	_, err = s.Eval(context.Background(), Cell{Source: "1"})
	if gotV != Version || gotD != pinned {
		t.Fatalf("asked for %s %x", gotV, gotD)
	}
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "the Julia kernel could not start: offline") {
		t.Fatalf("%q", msg)
	}
	if s.k != nil {
		t.Fatal("a kernel after a failed start")
	}
	s.Close() // nothing to close
}

// A different image is refused before it is loaded: a cached file that is not
// the pinned one is hashed, found different and never run. Offline the refusal
// is the failed download that follows, which is a failure too.
func TestAWrongImageIsNeverLoaded(t *testing.T) {
	if testing.Short() {
		t.Skip("downloads")
	}
	dir := t.TempDir()
	path := dir + "/" + interp.ImageAssetName(Version)
	if err := os.WriteFile(path, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// the only place to download from refuses the connection, so the cache is
	// all there is, and it is not the pinned image
	k, err := interp.FetchKernel(ctx, Version, pinned, interp.FetchOptions{CacheDir: dir, BaseURL: "https://127.0.0.1:1"}, interp.LoadOptions{}, interp.KernelOptions{})
	if err == nil {
		k.Close()
		t.Fatal("a file that is not the pinned image became a kernel")
	}
}

func TestLimitsOfASession(t *testing.T) {
	s := NewWith(runtime.RLimits{})
	if s.l != runtime.DefaultRLimits {
		t.Fatalf("%+v", s.l)
	}
	o := s.options()
	if o.Timeout != runtime.DefaultRLimits.Timeout || o.MaxSteps != int64(runtime.DefaultRLimits.MaxSteps) || o.MaxAlloc != int64(runtime.DefaultRLimits.MaxAlloc/8) || o.MaxOutput != maxOutput {
		t.Fatalf("%+v", o)
	}
	u := NewWith(runtime.RLimits{MaxAlloc: runtime.Unbounded, MaxSteps: runtime.Unbounded, Timeout: runtime.Unbounded}).options()
	if u.Timeout >= 0 || u.MaxSteps >= 0 || u.MaxAlloc >= 0 {
		t.Fatalf("unbounded is a negative bound: %+v", u)
	}
	t.Setenv(runtime.RLimitsEnv, "800,5,1000000000")
	if o := New().options(); o.MaxAlloc != 100 || o.MaxSteps != 5 || o.Timeout != time.Second {
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
		"memory":   {interp.ErrMemoryLimit, "stopped: the Julia cell allocated more than 800 bytes of array elements, the limit"},
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
	s := bounded(t, runtime.RLimits{MaxAlloc: 8 * 1000})
	_, err := s.Eval(context.Background(), Cell{Source: "zeros(Float64, 10^7)"})
	if msg := cellErr(t, err); msg != "stopped: the Julia cell allocated more than 8000 bytes of array elements, the limit" {
		t.Fatalf("message %q", msg)
	}
	if out := eval(t, s, "length(zeros(10))"); out.Stdout != "10\n" {
		t.Fatalf("the session after the limit: %+v", out)
	}
}

// A bomb of arrays and a runaway loop, run under GOMEMLIMIT and ulimit -v in
// CI (the security job): the refusal must come from the limits.
func TestAllocBomb(t *testing.T) {
	s := bounded(t, runtime.RLimits{})
	for _, src := range []string{
		"Array{Float64}(undef, 10^10)",
		"xs = []\nfor i in 1:100\n    push!(xs, Array{Float64}(undef, 10^7))\nend",
	} {
		_, err := s.Eval(context.Background(), Cell{Source: src})
		msg := cellErr(t, err)
		t.Logf("%q: %v", src, msg)
		if !strings.HasPrefix(msg, "stopped: the Julia cell allocated") && !strings.HasPrefix(msg, "ERROR: ") {
			t.Fatalf("%q: message %q", src, msg)
		}
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

// Soft scope (go-embedded-julia#54): at the top level of a cell a loop that
// assigns a global that the cell defined raises UndefVarError here, where
// julia 1.12 in the REPL (and IJulia) updates the global. This pins today's
// behaviour; when the interpreter is fixed the test fails and says so, and the
// README's known limitation goes with it.
func TestKnownLimitationHardScope(t *testing.T) {
	s := sharedSession(t)
	_, err := s.Eval(context.Background(), Cell{Source: "scope_s = 0\nfor i in 1:10\n    scope_s += i\nend\nscope_s"})
	if err == nil {
		t.Fatal("go-embedded-julia#54 is fixed: the loop updated the global. Remove this test and the README's limitation, and expect 55")
	}
	// (the text is a Go repr here, not julia's "UndefVarError: `scope_s` not
	// defined": go-embedded-julia#55)
	if msg := cellErr(t, err); !strings.HasPrefix(msg, "ERROR: UndefVarError") || !strings.Contains(msg, "scope_s") {
		t.Fatalf("message %q", msg)
	}
	// the same loop inside a function is a local accumulator, which works
	if out := eval(t, s, "function scope_sum(n)\n    s = 0\n    for i in 1:n\n        s += i\n    end\n    s\nend\nscope_sum(10)"); out.Stdout != "55\n" {
		t.Fatalf("%+v", out)
	}
}
