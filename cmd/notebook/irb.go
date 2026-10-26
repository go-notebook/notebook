package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	irb "github.com/go-ruby-irb/irb"
	"github.com/go-ruby-parser/parser"
	"github.com/go-ruby-reline/reline/terminal"

	"github.com/go-notebook/notebook/kernel"
)

// irbOptions are the flags of notebook irb.
type irbOptions struct {
	options
	save, svgDir, history string
	historySize           int
}

// irbVerb is notebook irb: an interactive Ruby session.
func irbVerb() *cobra.Command {
	o := &irbOptions{}
	cmd := &cobra.Command{
		Use:   "irb [flags]",
		Short: "Start an interactive Ruby session, as irb, on the notebook's Ruby and bindings",
		Long: `irb starts an interactive Ruby session: rbgo with the go-scikit bindings
(Stats, Special, Optimize, Integrate, DataFrame, Plot, NDArray), as in a
` + "```ruby" + ` cell, but line by line, as irb. Any local may be assigned again, _ is
the last value, and an exception is shown without ending the session. exit,
quit or Ctrl-D ends it; Ctrl-C abandons the line being typed, or stops the
input that is running.

Each input runs in a worker process, so --timeout and --memory stop it
without losing the prompt; the worker's state (locals, methods, classes) is
lost then, which the session says. --save writes the session as a notebook
that notebook run runs.`,
		Example: `  notebook irb
  notebook irb --timeout 30s --memory 2048 --save session.md --svg-dir figures
  notebook irb < script.rb`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return o.run(cmd) },
	}
	f := cmd.Flags()
	f.StringVarP(&o.work, "work", "w", "", "the work directory of the Ruby worker (default: under the user cache directory)")
	f.StringVar(&o.src, "runtime-src", "", "use the go-notebook source tree at this path instead of a released runtime")
	f.DurationVarP(&o.timeout, "timeout", "t", 0, "stop an input that runs longer than this (0: no limit)")
	f.Int64VarP(&o.memory, "memory", "m", 0, "stop an input whose worker holds more than this many MiB (0: no limit)")
	f.StringVarP(&o.save, "save", "s", "", "write the session, at its end, as a notebook of ```ruby cells to this file")
	f.StringVar(&o.svgDir, "svg-dir", "", "save each figure an input shows as an SVG file in this directory")
	f.StringVar(&o.history, "history", "", "the history file (default: irb_history under the user config directory's go-notebook)")
	f.IntVar(&o.historySize, "history-size", irb.DefaultEntryLimit, "how many inputs the history keeps (0: no history file)")
	return cmd
}

// Seams the tests replace: the session, whether a stream is a terminal, and
// the reader of the inputs.
var (
	newSession = kernel.NewSession
	isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
	newReader  = (*irbOptions).reader
)

// run is the session.
func (o *irbOptions) run(cmd *cobra.Command) error {
	if err := o.check(); err != nil {
		return err
	}
	if o.historySize < 0 {
		return fmt.Errorf("--history-size %d: negative", o.historySize)
	}
	out, errOut := cmd.OutOrStdout(), sharedWriter(cmd.ErrOrStderr())
	work := o.work
	if work == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		work = filepath.Join(cache, "go-notebook", "irb")
	}
	hist, err := o.openHistory()
	if err != nil {
		// A history that cannot be read is not written over either.
		fmt.Fprintf(errOut, "notebook irb: history: %v; this session keeps none\n", err)
		hist = &history{}
	}
	s, err := newSession(kernelOptions(work, "irb", o.src, o.timeout, o.memory<<20), errOut, errOut)
	if err != nil {
		return err
	}
	defer s.Close()
	in := newReader(o, cmd, hist, s)
	r := &repl{s: s, out: out, svgDir: o.svgDir, line: 1}
	for {
		src, err := in.readInput(r.line)
		if errors.Is(err, terminal.ErrInterrupt) {
			continue
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
		hist.add(src)
		if !r.eval(src) {
			break
		}
	}
	if err := hist.save(); err != nil {
		fmt.Fprintf(errOut, "notebook irb: history: %v\n", err)
	}
	if o.save == "" {
		return nil
	}
	if err := os.WriteFile(o.save, []byte(sessionNotebook(r.inputs, r.failed)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(errOut, "notebook irb: wrote %s\n", o.save)
	return nil
}

// sharedWriter is w for the session's own messages and its worker's error
// stream at once: a file as it is (the worker writes to it directly), any
// other writer behind a lock (the worker's stream is copied by a goroutine).
func sharedWriter(w io.Writer) io.Writer {
	if f, ok := w.(*os.File); ok {
		return f
	}
	return &lockedWriter{w: w}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// repl evaluates inputs and shows their results, as irb does.
type repl struct {
	s       *kernel.Session
	out     io.Writer
	svgDir  string
	line    int      // the session line the next input starts at
	inputs  []string // the inputs that ran without an exception, for --save
	failed  int      // how many did not
	figures int
}

// commands end the session, as irb's do.
var exitCommands = map[string]bool{"exit": true, "quit": true, "exit!": true}

// eval evaluates one input; false ends the session.
func (r *repl) eval(src string) bool {
	trimmed := strings.TrimSpace(src)
	start := r.line
	r.line += strings.Count(src, "\n") + 1
	if trimmed == "" {
		return true
	}
	if exitCommands[trimmed] {
		return false
	}
	ev := r.evaluate(src, start)
	if ev.Notice != "" {
		fmt.Fprintf(r.out, "notebook irb: %s; the session's state was lost with it\n", ev.Notice)
	}
	switch {
	case ev.Class == "SystemExit":
		return false
	case ev.Error != "":
		r.failed++
		fmt.Fprintln(r.out, ev.Error)
		if ev.Lost {
			fmt.Fprintln(r.out, "notebook irb: the session's state (locals, methods, classes) is lost; the next input starts a new worker")
		}
	default:
		r.inputs = append(r.inputs, src)
		fmt.Fprintln(r.out, "=> "+ev.Inspect+r.figure(ev.Display))
	}
	return true
}

// evaluate runs the input with Ctrl-C (SIGINT, or the console's Ctrl-C)
// stopping it rather than the session.
func (r *repl) evaluate(src string, line int) kernel.Evaluation {
	sig := make(chan os.Signal, 1)
	notifyInterrupt(sig)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			r.s.Interrupt()
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(sig)
		close(done)
	}()
	return r.s.Eval(src, line, func(o string) { _, _ = io.WriteString(r.out, o) })
}

// notifyInterrupt sends Ctrl-C to c, a seam for the tests.
var notifyInterrupt = func(c chan<- os.Signal) { signal.Notify(c, os.Interrupt) }

// figure saves a figure's SVG in --svg-dir, or says how to; never prints it.
func (r *repl) figure(display map[string][]byte) string {
	svg, ok := display["image/svg+xml"]
	if !ok {
		return ""
	}
	if r.svgDir == "" {
		return "  (a figure: --svg-dir saves it as SVG)"
	}
	r.figures++
	path := filepath.Join(r.svgDir, fmt.Sprintf("figure-%d.svg", r.figures))
	if err := os.MkdirAll(r.svgDir, 0o755); err != nil {
		return "  (the figure was not saved: " + err.Error() + ")"
	}
	if err := os.WriteFile(path, svg, 0o644); err != nil {
		return "  (the figure was not saved: " + err.Error() + ")"
	}
	return "  (saved as " + path + ")"
}

// inputReader reads one whole input: its lines up to one that completes it.
type inputReader interface {
	readInput(line int) (string, error)
}

// reader is the line editor on a terminal, else a plain reader that echoes
// what it reads after its prompt, so a scripted session reads as a typed one.
func (o *irbOptions) reader(cmd *cobra.Command, hist *history, s *kernel.Session) inputReader {
	in, inOK := cmd.InOrStdin().(*os.File)
	out, outOK := cmd.OutOrStdout().(*os.File)
	if inOK && outOK && isTerminal(in) && isTerminal(out) {
		t := terminal.New(in, out)
		hist.load(t.History)
		return &ttyReader{t: t, s: s}
	}
	return &plainReader{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
}

// ttyReader reads with the line editor.
type ttyReader struct {
	t    *terminal.Terminal
	s    *kernel.Session
	line int // where the input being read starts
}

func (r *ttyReader) readInput(line int) (string, error) {
	r.line = line
	return r.t.Readline(terminal.Options{
		Prompt:     prompts([]string{""}, line)[0],
		Terminate:  complete,
		PromptProc: r.prompts,
		Complete:   r.complete,
		AddHistory: true,
	})
}

// prompts are the prompts of the lines being edited.
func (r *ttyReader) prompts(lines []string) []string { return prompts(lines, r.line) }

// complete gives the session's completions of target, and the commands when
// target starts the input.
func (r *ttyReader) complete(target, pre, _ string) []string {
	var out []string
	if strings.TrimSpace(pre) == "" {
		for c := range exitCommands {
			if strings.HasPrefix(c, target) && c != "exit!" {
				out = append(out, c)
			}
		}
		sort.Strings(out)
	}
	return append(out, r.s.Complete(target)...)
}

// plainReader reads lines from a stream that is not a terminal.
type plainReader struct {
	in  *bufio.Reader
	out io.Writer
}

func (r *plainReader) readInput(line int) (string, error) {
	var lines []string
	for {
		text, err := r.in.ReadString('\n')
		if text == "" && err != nil {
			if len(lines) > 0 {
				// The input ends in the middle of a statement: it is
				// evaluated as it is, and its SyntaxError shown.
				return strings.Join(lines, "\n"), nil
			}
			return "", err
		}
		text = strings.TrimRight(text, "\r\n")
		fmt.Fprintln(r.out, prompts(append(lines, text), line)[len(lines)]+text)
		lines = append(lines, text)
		src := strings.Join(lines, "\n")
		if complete(src + "\n") {
			return src, nil
		}
	}
}

// prompts are irb's default prompts for the lines of one input that starts
// at session line first: irb(main):001> for its first line, then the
// continuation prompt the constructs still open give (* for a statement,
// the quote for a string).
func prompts(lines []string, first int) []string {
	ctx := irb.PromptContext{IRBName: "irb", Main: "main", MainIns: "main"}
	out := make([]string, len(lines))
	for i := range lines {
		var opens []irb.Token
		cont := false
		if i > 0 {
			var verdict irb.Completeness
			verdict, opens = irb.CheckCode(strings.Join(lines[:i], "\n") + "\n")
			cont = verdict == irb.More
		}
		out[i] = irb.GeneratePrompt(irb.PromptDefault, ctx, opens, cont, first+i, true, false)
	}
	return out
}

// complete reports whether src, the lines typed so far, is a whole input
// that Enter evaluates, or goes on on another line. rbgo's parser
// (go-ruby-parser) decides: an input it finds ending in the middle of a
// construct continues. So does one IRB's lexer (go-ruby-irb's port of
// RubyLex) sees open where the parser accepts it as it is: an unterminated
// string, heredoc, regexp or =begin, a line continued with a backslash. Any
// other syntax error is evaluated, for Ruby to report it.
func complete(src string) bool {
	if strings.TrimSpace(src) == "" {
		return true
	}
	if verdict, _ := irb.CheckCode(src); verdict == irb.More {
		return false
	}
	err := parse(src)
	return err == nil || !(strings.Contains(err.Error(), "(EOF)") || strings.Contains(err.Error(), "unterminated"))
}

// parse is parser.Parse, a panic of the parser an error.
func parse(src string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the Ruby parser failed: %v", r)
		}
	}()
	_, err = parser.Parse(src)
	return err
}
