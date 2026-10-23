// Command notebook runs Go notebooks: Markdown files whose ```go blocks are
// cells.
//
//	notebook run analysis.md            run it, print each cell's results
//	notebook html analysis.md           run it, write analysis.html
//	notebook watch analysis.md          run it again on every save, keeping analysis.html current
//	notebook serve analysis.md          the same, shown live at http://127.0.0.1:8888
//
// Results are kept between invocations in a work directory under the user's
// cache directory, so a second run only runs the cells that changed.
//
// Flags are POSIX: --timeout 5s, -t 5s. The single-dash long spelling of
// earlier releases (-timeout 5s) is still read, with a note on stderr.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-notebook/notebook/export"
	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/kernel"
)

// osExit is os.Exit, replaced by tests.
var osExit = os.Exit

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notebook:", err)
		osExit(1)
	}
}

// run is the command, on explicit streams: the arguments are read with the
// single-dash long flags of earlier releases rewritten, then executed.
func run(args []string, stdout, stderr io.Writer) error {
	root := RootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(legacy(root, args, stderr))
	return root.Execute()
}

// options are the flags of the verbs; each verb registers those it reads.
type options struct {
	out, work, src, addr string
	interval, timeout    time.Duration
	memory               int64
	watching             bool // the verb reads --interval
}

// RootCmd returns the notebook command with its four verbs.
func RootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "notebook",
		Short: "Run Go notebooks: Markdown files whose ```go and ```ruby blocks are cells",
		Long: `notebook runs Go notebooks: Markdown files whose ` + "```go and ```ruby" + ` blocks are cells.

Results are kept between invocations in a work directory under the user's
cache directory, so a second run only runs the cells that changed.`,
		Example: `  notebook run analysis.md
  notebook html -o report.html analysis.md
  notebook serve --addr 127.0.0.1:9000 --timeout 30s --memory 2048 analysis.md`,
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("missing command: run, html, watch or serve (see notebook --help)")
		},
	}
	root.AddCommand(
		verb("run", "Run the notebook and print each cell's results", "",
			func(cmd *cobra.Command, k *kernel.Kernel, path string, o *options) error {
				nb, rep, err := once(k, path)
				if err != nil {
					return err
				}
				printReport(cmd.OutOrStdout(), nb, rep)
				return failed(rep)
			}),
		verb("html", "Run the notebook and write its results as a page next to it (or --output)", "o",
			func(cmd *cobra.Command, k *kernel.Kernel, path string, o *options) error {
				nb, rep, err := once(k, path)
				if err != nil {
					return err
				}
				if err := writeHTML(o.out, path, nb, rep); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", o.out)
				return failed(rep)
			}),
		verb("watch", "Run the notebook again every time it changes, keeping its page current", "oi",
			func(cmd *cobra.Command, k *kernel.Kernel, path string, o *options) error {
				page := func(nb *format.Notebook, rep *kernel.Report) error { return writeHTML(o.out, path, nb, rep) }
				return watch(k, path, o.interval, cmd.OutOrStdout(), stopWatch, page)
			}),
		verb("serve", "Run the notebook again every time it changes, showing it live in a browser", "ai",
			func(cmd *cobra.Command, k *kernel.Kernel, path string, o *options) error {
				return serve(k, path, o.addr, o.interval, cmd.OutOrStdout(), stopWatch)
			}),
	)
	return root
}

// verb builds one verb. Every verb takes the work directory, the runtime
// source and the cell limits; extra lists the flags it adds: o (--output),
// i (--interval), a (--addr).
func verb(name, short, extra string,
	do func(cmd *cobra.Command, k *kernel.Kernel, path string, o *options) error) *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use:   name + " [flags] notebook.md",
		Short: short,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("%s takes one notebook file, got %d argument(s)", name, len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			k, err := o.kernel(path)
			if err != nil {
				return err
			}
			defer k.Close()
			if o.out == "" {
				o.out = strings.TrimSuffix(path, filepath.Ext(path)) + ".html"
			}
			return do(cmd, k, path, o)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.work, "work", "w", "", "the work directory (default: under the user cache directory)")
	f.StringVar(&o.src, "runtime-src", "", "use the go-notebook source tree at this path instead of a released runtime")
	f.DurationVarP(&o.timeout, "timeout", "t", 0, "stop a cell that runs longer than this (0: no limit)")
	f.Int64VarP(&o.memory, "memory", "m", 0, "stop a cell whose worker holds more than this many MiB (0: no limit)")
	if strings.Contains(extra, "o") {
		f.StringVarP(&o.out, "output", "o", "", "the HTML file to write (default: the notebook's name with .html)")
	}
	if strings.Contains(extra, "i") {
		o.watching = true
		f.DurationVar(&o.interval, "interval", 300*time.Millisecond, "how often the file is looked at")
	}
	if strings.Contains(extra, "a") {
		f.StringVar(&o.addr, "addr", "127.0.0.1:8888", "where to listen")
	}
	return cmd
}

// kernel checks the limits and opens the notebook's kernel.
func (o *options) kernel(path string) (*kernel.Kernel, error) {
	// In MiB: a value whose byte count overflows an int64 would wrap, to no
	// limit or to a tiny one.
	if o.memory < 0 || o.memory > math.MaxInt64>>20 {
		return nil, fmt.Errorf("--memory %d: out of range (0 to %d MiB)", o.memory, int64(math.MaxInt64>>20))
	}
	if o.timeout < 0 {
		return nil, fmt.Errorf("--timeout %s: negative", o.timeout)
	}
	// A ticker panics on a period that is not positive.
	if o.watching && o.interval <= 0 {
		return nil, fmt.Errorf("--interval %s: not positive", o.interval)
	}
	return newKernel(path, o.work, o.src, o.timeout, o.memory<<20)
}

// version is this build's module version, for --version.
func version() string {
	if bi, ok := readBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// legacy rewrites the single-dash long flags earlier releases accepted
// (-timeout 5s, -work=dir), read by the standard flag package, into the
// double-dash spelling pflag requires, and says so once on warn. pflag reads
// one dash as one letter: left alone, -timeout would be -t with the value
// "imeout".
//
// Only a name the verb registers is rewritten; anything else is left for
// pflag to report in its own words. The value of a flag that takes one is
// copied as it is, even when it starts with a dash, and so is everything
// after a bare "--". No long name is made only of registered shorthand
// letters (a test holds that), so a rewrite never changes what a cluster
// such as -ot would have meant.
func legacy(root *cobra.Command, args []string, warn io.Writer) []string {
	cmd, _, err := root.Find(args)
	if err != nil {
		return args // an unknown verb: cobra says so
	}
	cmd.InitDefaultHelpFlag()
	cmd.InitDefaultVersionFlag()
	flags := cmd.Flags()
	out := make([]string, 0, len(args))
	var rewritten []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			out = append(out, arg)
			continue
		}
		body, double := strings.CutPrefix(arg[1:], "-")
		name, _, inline := strings.Cut(body, "=")
		f := flags.Lookup(name)
		switch {
		case double:
		case len(name) == 1:
			f = flags.ShorthandLookup(name)
		case f != nil:
			arg = "-" + arg
			rewritten = append(rewritten, name)
		}
		out = append(out, arg)
		if f != nil && !inline && f.NoOptDefVal == "" && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	if len(rewritten) > 0 {
		fmt.Fprintf(warn, "notebook: read -%s as --%s; flags take two dashes now\n",
			strings.Join(rewritten, ", -"), strings.Join(rewritten, ", --"))
	}
	return out
}

// newKernel opens the kernel for a notebook, with its work directory.
func newKernel(path, work, src string, timeout time.Duration, memory int64) (*kernel.Kernel, error) {
	if work == "" {
		abs, _ := filepath.Abs(path) // fails only without a working directory; the name still keys the cache
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(abs))
		work = filepath.Join(cache, "go-notebook", hex.EncodeToString(sum[:8]))
	}
	opt := kernel.Options{Dir: work, Name: filepath.Base(path), CellTimeout: timeout, CellMemory: memory}
	version, dir := runtimeSource()
	if src != "" {
		dir = src
	}
	opt.Replace = map[string]string{}
	if dir != "" {
		opt.Replace["github.com/go-notebook/notebook"] = dir
	} else {
		opt.RuntimeVersion = version
	}
	addReplaces(opt.Replace, os.Getenv("NOTEBOOK_REPLACE"))
	return kernel.New(opt)
}

// addReplaces reads NOTEBOOK_REPLACE, which points modules of the work
// module at local checkouts — module=dir pairs separated by commas — to run
// a notebook against a library being changed. A pair without "=" is
// ignored; a relative directory is made absolute.
func addReplaces(replace map[string]string, env string) {
	for _, pair := range strings.Split(env, ",") {
		if mod, d, ok := strings.Cut(pair, "="); ok {
			replace[mod], _ = filepath.Abs(d) // fails only without a working directory
		}
	}
}

// Seams for tests: where watch stops (never, from the command line), the
// build information, and this file's path in the source tree.
var (
	stopWatch     <-chan struct{}
	readBuildInfo = debug.ReadBuildInfo
	thisFile      = func() string { _, file, _, _ := goruntime.Caller(0); return file }
)

// runtimeSource returns the released version of this module the work module
// should require, or, for a build from a source tree, that tree. A build
// from a checkout is stamped with a pseudo-version ("v0.0.0-...", perhaps
// "+dirty") that names no published runtime with the same code, so only a
// clean release tag is required from the module proxy.
func runtimeSource() (version, dir string) {
	tree := filepath.Clean(filepath.Join(filepath.Dir(thisFile()), "..", ".."))
	bi, ok := readBuildInfo()
	if ok && released(bi.Main.Version) {
		return bi.Main.Version, ""
	}
	if _, err := os.Stat(filepath.Join(tree, "runtime", "runtime.go")); err == nil {
		return "", tree
	}
	if ok {
		return bi.Main.Version, ""
	}
	return "", ""
}

// released reports whether v is a release tag: vX.Y.Z with no pre-release
// or build suffix.
func released(v string) bool {
	return strings.HasPrefix(v, "v") && !strings.ContainsAny(v, "-+")
}

// once reads and runs the notebook.
func once(k *kernel.Kernel, path string) (*format.Notebook, *kernel.Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	nb := format.Parse(string(b))
	var cells []gen.Code
	for _, c := range nb.Cells {
		if c.Kind == format.Code {
			cells = append(cells, gen.Code{Source: c.Source, Line: c.Line, Lang: c.Lang})
		}
	}
	rep, err := k.Run(cells)
	return nb, rep, err
}

// failed returns an error when a cell failed, so the command's status says so.
func failed(rep *kernel.Report) error {
	n := 0
	for _, r := range rep.Results {
		if r.Error != "" {
			n++
		}
	}
	if n > 0 || len(rep.Diags) > 0 {
		return fmt.Errorf("%d cell(s) failed", max(n, 1))
	}
	return nil
}

// printReport writes the results for a terminal.
func printReport(w io.Writer, nb *format.Notebook, rep *kernel.Report) {
	code := 0
	for _, c := range nb.Cells {
		if c.Kind != format.Code {
			continue
		}
		r := rep.Results[code]
		state := "not run"
		switch {
		case r.Ran:
			state = "ran in " + r.Took.Round(time.Microsecond).String()
		case r.Kept:
			state = "kept"
		}
		fmt.Fprintf(w, "── cell %d (line %d, %s)\n", code, c.Line, state)
		if r.Output != "" {
			fmt.Fprint(w, indent(r.Output))
		}
		for _, d := range r.Display {
			fmt.Fprint(w, indent(string(d["text/plain"])+"\n"))
		}
		if r.Error != "" {
			fmt.Fprint(w, indent("error: "+r.Error+"\n"))
		}
		code++
	}
	for _, d := range rep.Diags {
		if d.Cell < 0 {
			fmt.Fprintf(w, "error: %s\n", d)
		}
	}
}

// indent prefixes every line of s.
func indent(s string) string {
	s = strings.TrimRight(s, "\n")
	return "   " + strings.ReplaceAll(s, "\n", "\n   ") + "\n"
}

// writeHTML writes the page. A nil notebook (the run itself failed) writes
// nothing.
func writeHTML(out, path string, nb *format.Notebook, rep *kernel.Report) error {
	if nb == nil {
		return nil
	}
	var b bytes.Buffer
	_ = export.HTML(&b, filepath.Base(path), nb, rep) // a buffer does not fail
	return os.WriteFile(out, b.Bytes(), 0o644)
}

// watch runs the notebook whenever its modification time changes, until stop
// is closed (never, from the command line), and hands every result to page.
func watch(k *kernel.Kernel, path string, interval time.Duration, w io.Writer, stop <-chan struct{},
	page func(*format.Notebook, *kernel.Report) error) error {
	var last time.Time
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if st, err := os.Stat(path); err == nil && !st.ModTime().Equal(last) {
			last = st.ModTime()
			nb, rep, err := once(k, path)
			switch {
			case err != nil:
				fmt.Fprintf(w, "%s  %v\n", time.Now().Format("15:04:05"), err)
				_ = page(nil, &kernel.Report{Diags: []gen.Diag{{Cell: -1, Msg: err.Error()}}})
			default:
				ran := 0
				for _, r := range rep.Results {
					if r.Ran {
						ran++
					}
				}
				status := "ok"
				if e := failed(rep); e != nil {
					status = e.Error()
				}
				if err := page(nb, rep); err != nil {
					status = "page: " + err.Error()
				}
				fmt.Fprintf(w, "%s  %d of %d cells ran, build %s: %s\n", time.Now().Format("15:04:05"),
					ran, len(rep.Results), rep.Build.Round(time.Millisecond), status)
			}
		}
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
	}
}
