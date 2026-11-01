package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-ruby-reline/reline"
	"github.com/go-ruby-reline/reline/terminal"
	"github.com/spf13/cobra"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/kernel"
)

// irbWork is the work directory the session tests share, so that the worker
// is compiled once.
var (
	irbWorkOnce sync.Once
	irbWorkDir  string
)

func irbWork(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	irbWorkOnce.Do(func() {
		d, err := os.MkdirTemp("", "nb-irb-")
		if err != nil {
			t.Fatal(err)
		}
		irbWorkDir = d
	})
	return irbWorkDir
}

// irbCmd runs notebook irb on input, which is not a terminal.
func irbCmd(t *testing.T, input string, args ...string) (string, string, error) {
	t.Helper()
	root := RootCmd()
	var out, errs bytes.Buffer
	root.SetIn(strings.NewReader(input))
	root.SetOut(&out)
	root.SetErr(&errs)
	root.SetArgs(append([]string{"irb"}, args...))
	err := root.Execute()
	return out.String(), errs.String(), err
}

// A scripted session: what it prints reads as irb's, and --save writes a
// notebook that notebook run runs.
func TestIrbScripted(t *testing.T) {
	work := irbWork(t)
	dir := t.TempDir()
	saved, figs := filepath.Join(dir, "session.md"), filepath.Join(dir, "figs")
	input := strings.Join([]string{
		"x = [2.1, 3.4, 1.9, 5.6]",
		"fit = Stats::Normal.fit(x)",
		"fit.loc",
		"_ * 2",
		"def sq(v)",
		"  v * v",
		"end",
		"sq(3)",
		"",
		"x = x.sum",
		"raise 'boom'",
		`s = "two`,
		`lines"`,
		"fig, ax = Plot.subplots",
		"ax.plot([1, 2, 3], [3, 1, 2])",
		"fig",
		"puts 'hello'",
		"exit",
		"puts 'not run'",
	}, "\n") + "\n"
	out, errs, err := irbCmd(t, input, "--work", work, "--history", filepath.Join(dir, "hist"), "--save", saved, "--svg-dir", figs)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := []string{
		"irb(main):001> x = [2.1, 3.4, 1.9, 5.6]\n=> [2.1, 3.4, 1.9, 5.6]\n",
		"irb(main):003> fit.loc\n=> 3.25\n",
		"irb(main):004> _ * 2\n=> 6.5\n",
		"irb(main):005> def sq(v)\nirb(main):006*   v * v\nirb(main):007* end\n=> :sq\n",
		"irb(main):008> sq(3)\n=> 9\n",
		"irb(main):009> \nirb(main):010> x = x.sum\n=> 13.0\n",
		"irb(main):011> raise 'boom'\nboom (RuntimeError)\n\tfrom (irb):11:in '<main>'\n",
		"irb(main):012> s = \"two\nirb(main):013\" lines\"\n=> \"two\\nlines\"\n",
		"=> #<Plot::Figure 640x480>  (saved as " + filepath.Join(figs, "figure-1.svg") + ")\n",
		"irb(main):017> puts 'hello'\nhello\n=> nil\n",
		"irb(main):018> exit\n",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	if strings.Contains(out, "not run") {
		t.Error("an input after exit ran")
	}
	if svg, err := os.ReadFile(filepath.Join(figs, "figure-1.svg")); err != nil || !bytes.Contains(svg, []byte("<svg")) {
		t.Errorf("figure: %v", err)
	}
	if strings.Contains(out, "<svg") {
		t.Error("SVG on the terminal")
	}
	if !strings.Contains(errs, "wrote "+saved) {
		t.Errorf("stderr %q", errs)
	}
	// The saved session runs.
	var runOut, runErr bytes.Buffer
	if err := run([]string{"run", "--work", t.TempDir(), saved}, &runOut, &runErr); err != nil {
		b, _ := os.ReadFile(saved)
		t.Fatalf("run: %v\n%s\n%s\n%s", err, runOut.String(), runErr.String(), b)
	}
	for _, w := range []string{"<*plot.Figure>", "hello"} {
		if !strings.Contains(runOut.String(), w) {
			t.Errorf("run output lacks %q:\n%s", w, runOut.String())
		}
	}
	b, _ := os.ReadFile(saved)
	if !strings.Contains(string(b), "The input that raised an exception is left out.") || strings.Contains(string(b), "boom") {
		t.Errorf("saved:\n%s", b)
	}
}

// The forms that end a session, and an input the end of the stream cuts.
func TestIrbEnds(t *testing.T) {
	work := irbWork(t)
	for _, tc := range []struct{ input, want string }{
		{"1\nquit\n2\n", "=> 1\n"},
		{"1\nexit!\n2\n", "=> 1\n"},
		{"1\nexit 3\n2\n", "=> 1\n"},
		{"def f\n  1\n", "SyntaxError"},
		{"1 + 1", "=> 2\n"}, // no final newline
	} {
		out, errs, err := irbCmd(t, tc.input, "--work", work, "--history-size", "0")
		if err != nil || !strings.Contains(out, tc.want) || strings.Contains(out, "=> 2\n") != (tc.want == "=> 2\n") {
			t.Errorf("%q: %v\n%s%s", tc.input, err, out, errs)
		}
	}
}

func TestIrbLimits(t *testing.T) {
	input := "kept = 1\nloop { }\nkept\nbig = Array.new(60_000_000) { |i| i * 0.5 }\n1 + 1\n"
	out, errs, err := irbCmd(t, input, "--work", irbWork(t), "--history-size", "0", "--timeout", "3s", "--memory", "256")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	for _, w := range []string{
		"stopped: the input ran longer than 3s, the limit; its worker was killed\nnotebook irb: the session's state (locals, methods, classes) is lost",
		"undefined local variable or method 'kept' for main (NameError)",
		"stopped: the input went over the memory limit of 256 MiB",
		"irb(main):005> 1 + 1\n=> 2\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
}

func TestIrbInterrupt(t *testing.T) {
	work := irbWork(t)
	seam(t, &notifyInterrupt, func(c chan<- os.Signal) {
		go func() {
			time.Sleep(time.Second)
			c <- os.Interrupt
		}()
	})
	out, errs, err := irbCmd(t, "loop { }\n1\n", "--work", work, "--history-size", "0")
	if err != nil || !strings.Contains(out, "interrupted: the input was stopped by killing its worker") || !strings.Contains(out, "=> 1\n") {
		t.Errorf("%v\n%s%s", err, out, errs)
	}
}

// A worker stopped while completing: the next input says so.
func TestIrbNotice(t *testing.T) {
	work := irbWork(t)
	seam(t, &kernel.CompleteTimeout, 500*time.Millisecond)
	sessions := make(chan *kernel.Session, 1)
	seam(t, &newSession, func(opt kernel.Options, progress, stderr io.Writer) (*kernel.Session, error) {
		s, err := kernel.NewSession(opt, progress, stderr)
		sessions <- s
		return s, err
	})
	root := RootCmd()
	var out syncBuffer
	in, w := io.Pipe()
	root.SetIn(in)
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"irb", "--work", work, "--history-size", "0"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	_, _ = io.WriteString(w, "class Slow; def methods = loop { }; end\nslow = Slow.new\n")
	s := <-sessions
	waitFor(t, &out, "=> #<Slow")
	// Tab, while the session waits for its next line.
	if got := (&ttyReader{s: s}).complete("slow.x", "", ""); got != nil {
		t.Errorf("candidates %q", got)
	}
	_, _ = io.WriteString(w, "1\n")
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `notebook irb: stopped: completing "slow.x" took longer than 500ms; its worker was killed; the session's state was lost with it`) {
		t.Errorf("%s", out.String())
	}
}

// Tab completes a top-level constant typed as a prefix, through the real
// completer: go-scikit/ruby v0.2.1 answers it from Object.constants.
func TestIrbCompletesConstant(t *testing.T) {
	work := irbWork(t)
	sessions := make(chan *kernel.Session, 1)
	seam(t, &newSession, func(opt kernel.Options, progress, stderr io.Writer) (*kernel.Session, error) {
		s, err := kernel.NewSession(opt, progress, stderr)
		sessions <- s
		return s, err
	})
	root := RootCmd()
	var out syncBuffer
	in, w := io.Pipe()
	root.SetIn(in)
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"irb", "--work", work, "--history-size", "0"})
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	_, _ = io.WriteString(w, "1\n")
	s := <-sessions
	waitFor(t, &out, "=> 1")
	got := (&ttyReader{s: s}).complete("Compa", "", "")
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "Comparable") {
		t.Errorf("candidates for Compa: %q", got)
	}
}

func TestIrbFlagErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--history-size", "-1"},
		{"--memory", "-1"},
		{"--timeout", "-1s"},
		{"extra"},
	} {
		if _, _, err := irbCmd(t, "", args...); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	// No cache directory for the default work directory.
	for _, v := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData", "home"} {
		t.Setenv(v, "")
	}
	if _, _, err := irbCmd(t, "", "--history-size", "0"); err == nil {
		t.Error("no cache directory, yet a session")
	}
}

// fakeSession makes no worker: inputs that evaluate nothing only.
func fakeSession(t *testing.T, err error) {
	t.Helper()
	seam(t, &newSession, func(kernel.Options, io.Writer, io.Writer) (*kernel.Session, error) {
		if err != nil {
			return nil, err
		}
		return &kernel.Session{}, nil
	})
}

func TestIrbFailures(t *testing.T) {
	fail := errors.New("no worker")
	fakeSession(t, fail)
	if _, _, err := irbCmd(t, "", "--work", t.TempDir(), "--history-size", "0"); !errors.Is(err, fail) {
		t.Errorf("session: %v", err)
	}
	fakeSession(t, nil)
	dir := t.TempDir()
	// The history file is a directory: it cannot be read, the session keeps
	// none, and the directory is left alone.
	_, errs, err := irbCmd(t, "exit\n", "--work", dir, "--history", dir)
	if err != nil || !strings.Contains(errs, "this session keeps none") {
		t.Errorf("history read: %v %q", err, errs)
	}
	// The history cannot be saved: a note, not a failure.
	seam(t, &createTemp, func(string, string) (*os.File, error) { return nil, errors.New("disk full") })
	_, errs, err = irbCmd(t, "exit\n", "--work", dir, "--history", filepath.Join(dir, "h"))
	if err != nil || !strings.Contains(errs, "notebook irb: history: disk full") {
		t.Errorf("history save: %v %q", err, errs)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// --save into a directory that is not there.
	if _, _, err := irbCmd(t, "", "--work", dir, "--history-size", "0", "--save", filepath.Join(file, "s.md")); err == nil {
		t.Error("--save under a file")
	}
	// A stream that fails.
	root := RootCmd()
	root.SetIn(iotestErr{})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"irb", "--work", dir, "--history-size", "0"})
	if err := root.Execute(); err == nil || err.Error() != "read failed" {
		t.Errorf("read: %v", err)
	}
	// A terminal that cannot be put in raw mode (a pipe said to be one).
	seam(t, &isTerminal, func(*os.File) bool { return true })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	root = RootCmd()
	root.SetIn(r)
	root.SetOut(w)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"irb", "--work", dir, "--history-size", "0"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "raw mode") {
		t.Errorf("raw mode: %v", err)
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestTTYReaderHelpers(t *testing.T) {
	r := &ttyReader{s: &kernel.Session{}, line: 5}
	if got := r.prompts([]string{"def f", "1", "end"}); !slices.Equal(got, []string{"irb(main):005> ", "irb(main):006* ", "irb(main):007* "}) {
		t.Errorf("prompts %q", got)
	}
	if got := r.complete("e", "", ""); !slices.Equal(got, []string{"exit"}) {
		t.Errorf("commands %q", got)
	}
	if got := r.complete("q", "x = ", ""); got != nil {
		t.Errorf("not at the start: %q", got)
	}
}

func TestComplete(t *testing.T) {
	for src, want := range map[string]bool{
		"":                  true,
		"  \n":              true,
		"x = 1\n":           true,
		"def f\n":           false,
		"def f\n1\nend\n":   true,
		"\"abc\n":           false, // the parser takes it; irb's lexer sees it open
		"<<~E\nabc\n":       false,
		"[1, 2,\n":          false,
		"1 +\n":             false,
		"x ? 1\n":           false, // the parser says it ends at the end; IRB's lexer does not
		"a = 1 \\\n":        false,
		"end\n":             true, // a syntax error, for Ruby to report
		"puts 1)\n":         true,
		"foo(1,\n":          false,
		"%w[a b\n":          false,
		"[1].each do |x|\n": false,
	} {
		if got := complete(src); got != want {
			t.Errorf("complete(%q) = %v", src, got)
		}
	}
	if err := parse(`"\`); err == nil || !strings.Contains(err.Error(), "the Ruby parser failed") {
		t.Errorf("parser panic: %v", err)
	}
}

func TestPrompts(t *testing.T) {
	got := prompts([]string{`s = "a`, `b"`, "[1,", "2]"}, 9)
	want := []string{"irb(main):009> ", `irb(main):010" `, "irb(main):011> ", "irb(main):012* "}
	if !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
}

func TestFigure(t *testing.T) {
	svg := map[string][]byte{"image/svg+xml": []byte("<svg/>")}
	r := &repl{}
	if got := r.figure(map[string][]byte{"text/plain": []byte("x")}); got != "" {
		t.Errorf("no figure: %q", got)
	}
	if got := r.figure(svg); got != "  (a figure: --svg-dir saves it as SVG)" {
		t.Errorf("no --svg-dir: %q", got)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.svgDir = filepath.Join(file, "figs")
	if got := r.figure(svg); !strings.HasPrefix(got, "  (the figure was not saved: ") {
		t.Errorf("under a file: %q", got)
	}
	r.svgDir, r.figures = dir, 0
	if err := os.Mkdir(filepath.Join(dir, "figure-1.svg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := r.figure(svg); !strings.HasPrefix(got, "  (the figure was not saved: ") {
		t.Errorf("a directory in the way: %q", got)
	}
	if got := r.figure(svg); got != "  (saved as "+filepath.Join(dir, "figure-2.svg")+")" {
		t.Errorf("saved: %q", got)
	}
}

func TestSessionNotebook(t *testing.T) {
	cells := func(inputs ...string) []string {
		var out []string
		for _, c := range format.Parse(sessionNotebook(inputs, 0)).Cells {
			if c.Kind == format.Code {
				out = append(out, strings.TrimSuffix(c.Source, "\n"))
			}
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		inputs []string
		want   []string
	}{
		{"one cell per input", []string{"a = 1", "b = a + 1", "b"}, []string{"a = 1", "b = a + 1", "b"}},
		{"a reassignment joins the cells in between", []string{"a = 1", "b = 2", "a = 3", "c = 4"}, []string{"a = 1\nb = 2\na = 3", "c = 4"}},
		{"an op-assignment too", []string{"a = 1", "a += 1"}, []string{"a = 1\na += 1"}},
		{"a method defined again", []string{"def f = 1", "f", "def f = 2"}, []string{"def f = 1\nf\ndef f = 2"}},
		{"_ reads the input before", []string{"1 + 1", "_ * 3", "x = _"}, []string{"_ = begin\n1 + 1\nend\n_ = begin\n_ * 3\nend\nx = _"}},
		{"_ in the first input is nil", []string{"_"}, []string{"_ = nil\n_"}},
		{"a cycle makes one cell", []string{"def f = g", "def g = f"}, []string{"def f = g\ndef g = f"}},
	} {
		if got := cells(tc.inputs...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q", tc.name, got)
		}
	}
	if nb := sessionNotebook([]string{"1"}, 3); !strings.Contains(nb, "The 3 inputs that raised an exception are left out.") {
		t.Errorf("%s", nb)
	}
	if nb := sessionNotebook(nil, 0); strings.Contains(nb, "```") {
		t.Errorf("an empty session: %s", nb)
	}
	// An input holding a fence gets a longer one.
	if nb := sessionNotebook([]string{"s = <<~M\n```\nM"}, 0); !strings.Contains(nb, "````ruby\n") {
		t.Errorf("fence: %s", nb)
	}
}

func TestHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "irb_history")
	open := func(size int) *history {
		t.Helper()
		h, err := (&irbOptions{history: path, historySize: size}).openHistory()
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	h := open(3)
	if len(h.entries) != 0 {
		t.Errorf("a new file has %q", h.entries)
	}
	if err := h.save(); err != nil { // nothing added: nothing written
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("written without an input: %v", err)
	}
	for _, in := range []string{"a = 1", "  ", "def f\n  1\nend", "b = 2", "c = 3"} {
		h.add(in)
	}
	if err := h.save(); err != nil {
		t.Fatal(err)
	}
	// The last three, a multi-line input in irb's format; readable by its owner only.
	b, _ := os.ReadFile(path)
	if string(b) != "def f\\\n  1\\\nend\nb = 2\nc = 3\n" {
		t.Errorf("file %q", b)
	}
	if st, err := os.Stat(path); err != nil || goruntime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v %v", st.Mode(), err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || goruntime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v %v", st.Mode(), err)
	}
	// Two sessions: the second one's save keeps what the first saved meanwhile.
	h1, h2 := open(10), open(10)
	if !slices.Equal(h1.entries, []string{"def f\n  1\nend", "b = 2", "c = 3"}) {
		t.Errorf("read back %q", h1.entries)
	}
	h1.add("one")
	h2.add("two")
	if h1.save() != nil || h2.save() != nil {
		t.Fatal("save")
	}
	if got := open(10).entries; !slices.Equal(got[len(got)-2:], []string{"one", "two"}) {
		t.Errorf("merged %q", got)
	}
	// The line editor gets the entries.
	rh := reline.NewHistory(-1)
	open(2).load(rh)
	if rh.Size() != 2 {
		t.Errorf("loaded %d", rh.Size())
	}
	// An over-large file is not read, and an empty one has nothing.
	if err := os.WriteFile(path, bytes.Repeat([]byte("x\n"), maxHistoryFile/2+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := open(10); len(h.entries) != 0 {
		t.Errorf("a huge file gave %d entries", len(h.entries))
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if h := open(10); len(h.entries) != 0 {
		t.Errorf("an empty file gave %q", h.entries)
	}
}

func TestHistoryFailures(t *testing.T) {
	// No history: size 0, or no config directory.
	h, err := (&irbOptions{historySize: 0}).openHistory()
	if err != nil || h.path != "" {
		t.Errorf("size 0: %+v %v", h, err)
	}
	h.add("x")
	if err := h.save(); err != nil || len(h.added) != 0 {
		t.Errorf("without a file: %v %q", err, h.added)
	}
	seam(t, &userConfigDir, func() (string, error) { return "", errors.New("no config dir") })
	if h, err := (&irbOptions{historySize: 5}).openHistory(); err != nil || h.path != "" {
		t.Errorf("no config dir: %+v %v", h, err)
	}
	dir := t.TempDir()
	seam(t, &userConfigDir, func() (string, error) { return dir, nil })
	if h, err := (&irbOptions{historySize: 5}).openHistory(); err != nil || h.path != filepath.Join(dir, "go-notebook", "irb_history") {
		t.Errorf("default path: %+v %v", h, err)
	}
	// Saving fails: the file became unreadable, its directory cannot hold a
	// temporary file, the write fails, the rename fails.
	path := filepath.Join(dir, "h")
	h = &history{path: path, size: 5, added: []string{"x"}}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.save(); err == nil {
		t.Error("reading a directory")
	}
	h.path = filepath.Join(dir, "missing", "h")
	fail := errors.New("disk full")
	seam(t, &createTemp, func(string, string) (*os.File, error) { return nil, fail })
	if err := h.save(); !errors.Is(err, fail) {
		t.Errorf("temporary file: %v", err)
	}
	createTemp = os.CreateTemp
	seam(t, &writeHistory, func(string, []byte, os.FileMode) error { return fail })
	if err := h.save(); !errors.Is(err, fail) {
		t.Errorf("write: %v", err)
	}
	writeHistory = os.WriteFile
	seam(t, &renameHistory, func(string, string) error { return fail })
	if err := h.save(); !errors.Is(err, fail) {
		t.Errorf("rename: %v", err)
	}
	// None of them left a temporary file behind.
	if left, _ := filepath.Glob(filepath.Join(dir, "missing", ".irb_history-*")); len(left) != 0 {
		t.Errorf("left %q", left)
	}
	// The directory cannot be made.
	renameHistory = os.Rename
	seam(t, &mkdirAll, func(string, os.FileMode) error { return fail })
	if err := h.save(); !errors.Is(err, fail) {
		t.Errorf("directory: %v", err)
	}
}

// An interrupted line (Ctrl-C at the prompt) is dropped and the next read.
type scriptedReader struct{ steps []error }

func (r *scriptedReader) readInput(int) (string, error) {
	err := r.steps[0]
	r.steps = r.steps[1:]
	return "", err
}

func TestIrbInterruptedLine(t *testing.T) {
	fakeSession(t, nil)
	r := &scriptedReader{steps: []error{terminal.ErrInterrupt, terminal.ErrInterrupt, io.EOF}}
	seam(t, &newReader, func(*irbOptions, *cobra.Command, *history, *kernel.Session) inputReader { return r })
	if _, _, err := irbCmd(t, "", "--work", t.TempDir(), "--history-size", "0"); err != nil || len(r.steps) != 0 {
		t.Errorf("%v, %d steps left", err, len(r.steps))
	}
}

// Without --work, the worker is built under the user cache directory.
func TestIrbDefaultWorkDir(t *testing.T) {
	cache := t.TempDir()
	for _, v := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData", "home"} {
		t.Setenv(v, cache)
	}
	var got string
	seam(t, &newSession, func(opt kernel.Options, _, _ io.Writer) (*kernel.Session, error) {
		got = opt.Dir
		return &kernel.Session{}, nil
	})
	if _, _, err := irbCmd(t, "", "--history-size", "0"); err != nil || !strings.HasSuffix(got, filepath.Join("go-notebook", "irb")) {
		t.Errorf("%v, work %q", err, got)
	}
	// A pipe is no terminal.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) {
		t.Error("a pipe taken for a terminal")
	}
}

func TestSharedWriter(t *testing.T) {
	if w := sharedWriter(os.Stderr); w != os.Stderr {
		t.Error("a file is shared behind a lock")
	}
	var b bytes.Buffer
	if _, ok := sharedWriter(&b).(*lockedWriter); !ok {
		t.Error("a buffer is shared without a lock")
	}
}
