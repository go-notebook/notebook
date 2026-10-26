package kernel

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-notebook/notebook/runtime"
)

// sessionDir is the work directory the session tests share, so that the
// worker is compiled once (go build leaves an up-to-date binary alone).
var (
	sessionDirOnce sync.Once
	sessionDir     string
)

// sessionOptions are the options of a session on this source tree, with
// NOTEBOOK_REPLACE's module=dir pairs (a bindings or rbgo change under
// development), in the shared work directory.
func sessionOptions(t *testing.T) Options {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	sessionDirOnce.Do(func() {
		d, err := os.MkdirTemp("", "nb-session-")
		if err != nil {
			t.Fatal(err)
		}
		sessionDir = d
	})
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	opt := Options{Dir: sessionDir, Replace: map[string]string{modulePath: root}}
	for _, pair := range strings.Split(os.Getenv("NOTEBOOK_REPLACE"), ",") {
		if mod, dir, ok := strings.Cut(pair, "="); ok {
			abs, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			opt.Replace[mod] = abs
		}
	}
	return opt
}

func newSession(t *testing.T, opt Options) *Session {
	t.Helper()
	var progress strings.Builder
	s, err := NewSession(opt, &progress, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !strings.Contains(progress.String(), "building the Ruby worker") {
		t.Errorf("progress %q", progress.String())
	}
	return s
}

// eval evaluates src and returns the evaluation and what it printed.
func eval(s *Session, src string, line int) (Evaluation, string) {
	var out strings.Builder
	ev := s.Eval(src, line, func(o string) { out.WriteString(o) })
	return ev, out.String()
}

func TestSession(t *testing.T) {
	s := newSession(t, sessionOptions(t))
	steps := []struct {
		src, inspect, output, err, class string
	}{
		{src: "x = 1 + 1", inspect: "2"},
		{src: "_ * 3", inspect: "6"},
		{src: "x = 'again'", inspect: `"again"`}, // reassignment: no reactive graph here
		{src: "puts x; print 'no newline'", inspect: "nil", output: "again\nno newline"},
		{src: "$stderr.puts 'to stderr'", inspect: "nil", output: "to stderr\n"},
		{src: "def twice(v)\n  v * 2\nend", inspect: ":twice"},
		{src: "twice(_ == :twice ? 21 : 0)", inspect: "42"},
		{src: "raise ArgumentError, 'bad'", err: "bad (ArgumentError)\n\tfrom (irb):10:in '<main>'", class: "ArgumentError"},
		{src: "_", inspect: "42"}, // the failed input left `_` alone
		{src: "$stdin.gets", inspect: "nil"},
		{src: "Stats::Normal.new(loc: 0, scale: 1).cdf(0)", inspect: "0.5"},
		{src: "exit", err: "exit (SystemExit)", class: "SystemExit"},
	}
	line := 1
	for _, st := range steps {
		ev, out := eval(s, st.src, line)
		if ev.Inspect != st.inspect || out != st.output || !strings.HasPrefix(ev.Error, st.err) || ev.Class != st.class || ev.Lost {
			t.Errorf("%q: %+v, printed %q", st.src, ev, out)
		}
		line += strings.Count(st.src, "\n") + 1
	}
	ev, _ := eval(s, "fig, ax = Plot.subplots\nax.plot([1, 2], [3, 1])\nfig", line)
	if !strings.Contains(string(ev.Display["image/svg+xml"]), "<svg") {
		t.Errorf("figure: %+v", ev)
	}
	if got := s.Complete("x.up"); !slices.Contains(got, "x.upcase") {
		t.Errorf("complete x.up: %q", got)
	}
	if got := s.Complete("Sta"); !slices.Contains(got, "Stats") {
		t.Errorf("complete Sta: %q", got)
	}
	s.Interrupt() // between inputs: nothing
	if ev, _ := eval(s, "x", 30); ev.Inspect != `"again"` {
		t.Errorf("after Interrupt between inputs: %+v", ev)
	}
}

func TestSessionStops(t *testing.T) {
	opt := sessionOptions(t)
	opt.CellTimeout = 3 * time.Second
	opt.CellMemory = 256 << 20
	s := newSession(t, opt)
	if ev, _ := eval(s, "kept = 1", 1); ev.Inspect != "1" {
		t.Fatal(ev)
	}
	ev, _ := eval(s, "loop { }", 2)
	if !ev.Lost || !strings.Contains(ev.Error, "stopped: the input ran longer than 3s") {
		t.Errorf("timeout: %+v", ev)
	}
	// A new worker: the input runs, the state is gone.
	if ev, _ := eval(s, "kept", 3); ev.Class != "NameError" || ev.Lost {
		t.Errorf("after the timeout: %+v", ev)
	}
	ev, _ = eval(s, "big = Array.new(60_000_000) { |i| i * 0.5 }", 4)
	if !ev.Lost || !strings.Contains(ev.Error, "over the memory limit of 256 MiB") {
		t.Errorf("memory: %+v", ev)
	}
	// Ctrl-C: the session stops the worker.
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.Interrupt()
	}()
	ev, out := eval(s, "puts 'started'; loop { }", 5)
	if !ev.Lost || !strings.HasPrefix(ev.Error, "interrupted:") || out != "started\n" {
		t.Errorf("interrupt: %+v, printed %q", ev, out)
	}
	if goruntime.GOOS != "windows" { // rbgo has no Process.kill on Windows
		ev, _ = eval(s, "Process.kill(:KILL, Process.pid); sleep 5", 6)
		if !ev.Lost || !strings.HasPrefix(ev.Error, "the worker stopped") {
			t.Errorf("killed: %+v", ev)
		}
	}
	if ev, _ := eval(s, "1 + 1", 7); ev.Inspect != "2" {
		t.Errorf("at the end: %+v", ev)
	}
}

func TestSessionCompletionStops(t *testing.T) {
	seam(t, &CompleteTimeout, time.Second)
	s := newSession(t, sessionOptions(t))
	if ev, _ := eval(s, "class Slow; def methods = loop { }; end\nslow = Slow.new", 1); ev.Error != "" {
		t.Fatal(ev)
	}
	if got := s.Complete("slow.x"); got != nil {
		t.Errorf("candidates %q", got)
	}
	if got := s.Complete("slow.x"); got != nil {
		t.Errorf("without a worker: %q", got)
	}
	ev, _ := eval(s, "1", 4)
	if !strings.Contains(ev.Notice, `completing "slow.x" took longer than 1s`) || ev.Inspect != "1" {
		t.Errorf("after: %+v", ev)
	}
}

// What is printed during a completion (a Ruby thread's output) is shown with
// the next input, before what it prints.
func TestSessionPendingOutput(t *testing.T) {
	s := &Session{k: &Kernel{}, worker: &exec.Cmd{}}
	s.in = nopCloser{io.Discard}
	s.out = bufio.NewScanner(strings.NewReader(`{"more":true,"output":"late\n"}` + "\n" +
		`{"candidates":["a"]}` + "\n" + `{"more":true,"output":"now\n"}` + "\n" + `{"inspect":"1"}` + "\n"))
	if got := s.Complete("x"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("candidates %q", got)
	}
	ev, out := eval(s, "1", 1)
	if ev.Inspect != "1" || out != "late\nnow\n" {
		t.Errorf("%+v, printed %q", ev, out)
	}
}

// failing is a worker's input that cannot be written to.
type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (failing) Close() error              { return nil }

func TestSessionProtocolFailures(t *testing.T) {
	req := runtime.Request{Op: "eval", Source: "1"}
	s := &Session{k: &Kernel{}}
	s.in = failing{}
	if _, err := s.call(req, nil); err == nil || err.Error() != "broken pipe" {
		t.Errorf("write: %v", err)
	}
	s.in = nopCloser{io.Discard}
	s.out = bufio.NewScanner(strings.NewReader("not json\n"))
	if _, err := s.call(req, nil); err == nil || !strings.HasPrefix(err.Error(), "bad reply") {
		t.Errorf("reply: %v", err)
	}
	s.out = bufio.NewScanner(strings.NewReader(""))
	if _, err := s.call(req, nil); err != io.EOF {
		t.Errorf("end: %v", err)
	}
	if why := s.died(io.EOF, false, "input"); why != "the worker stopped: EOF" {
		t.Errorf("died: %q", why)
	}
	// A completion that fails for another reason than its time says why.
	s.in, s.worker = failing{}, &exec.Cmd{}
	if got := s.Complete("x"); got != nil || s.notice != "the worker stopped: broken pipe" {
		t.Errorf("complete: %q, notice %q", got, s.notice)
	}
}

func TestSessionStartFails(t *testing.T) {
	k, err := New(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{k: k}
	if err := s.start(); err == nil || !strings.Contains(err.Error(), "the Ruby worker did not start") {
		t.Errorf("start: %v", err)
	}
	// Eval tries to start the missing worker, and says why it cannot.
	if ev, _ := eval(s, "1", 1); !ev.Lost || !strings.Contains(ev.Error, "did not start") {
		t.Errorf("eval: %+v", ev)
	}
	if got := s.Complete("x"); got != nil {
		t.Errorf("complete: %q", got)
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	// NewSession fails as its steps do: a work directory that cannot be made.
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSession(Options{Dir: filepath.Join(file, "sub")}, io.Discard, io.Discard); err == nil {
		t.Error("a work directory under a file")
	}
}

func TestSessionBuildCached(t *testing.T) {
	dir := t.TempDir()
	k, err := New(Options{Dir: dir, RuntimeVersion: "v0.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	k.ruby = true
	s := &Session{k: k}
	if err := os.WriteFile(filepath.Join(dir, "worker.stamp"), []byte(sessionStamp(k.goMod())), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(k.binary(), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	if err := s.build(&progress); err != nil || progress.Len() != 0 {
		t.Errorf("an up-to-date worker was built again: %v, %q", err, progress.String())
	}
	// Each write failing in turn: go.mod, then main.go.
	fail := errors.New("disk full")
	for at := 1; at <= 2; at++ {
		_ = os.Remove(k.binary())
		writes := 0
		seam(t, &writeFile, func(name string, data []byte, perm os.FileMode) error {
			if writes++; writes == at {
				return fail
			}
			return os.WriteFile(name, data, perm)
		})
		if err := s.build(io.Discard); !errors.Is(err, fail) {
			t.Errorf("write %d: %v", at, err)
		}
	}
}

func TestSessionBuildFails(t *testing.T) {
	opt := sessionOptions(t)
	opt.Dir = t.TempDir()
	k, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	k.ruby = true
	s := &Session{k: k}
	fail := errors.New("injected")
	seam(t, &goBuild, func(string, string) ([]byte, error) { return []byte("compiler says no"), fail })
	if _, err := NewSession(opt, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "compiler says no") {
		t.Errorf("build: %v", err)
	}
	// A build that makes no binary: the worker does not start.
	goBuild = func(string, string) ([]byte, error) { return nil, nil }
	if _, err := NewSession(opt, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Errorf("start: %v", err)
	}
	// The stamp, written last, cannot be.
	writes := 0
	seam(t, &writeFile, func(name string, data []byte, perm os.FileMode) error {
		if writes++; writes == 3 {
			return fail
		}
		return os.WriteFile(name, data, perm)
	})
	if err := s.build(io.Discard); !errors.Is(err, fail) {
		t.Errorf("stamp: %v", err)
	}
	// go mod tidy fails on a module replaced by a directory that is not there.
	k.opt.Replace["github.com/go-scikit/ruby"] = filepath.Join(opt.Dir, "missing")
	if err := s.build(io.Discard); err == nil || !strings.HasPrefix(err.Error(), "go mod tidy:") {
		t.Errorf("tidy: %v", err)
	}
}
