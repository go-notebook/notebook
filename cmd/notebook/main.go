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
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-notebook/notebook/export"
	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/kernel"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notebook:", err)
		os.Exit(1)
	}
}

const usage = `usage: notebook run|html|watch|serve [flags] notebook.md

  run    run the notebook and print each cell's results
  html   run it and write the results as a page next to it (or -o)
  watch  run it again every time the file changes, keeping the page current
  serve  run it again every time the file changes, showing it live in a browser (-addr)
`

// run is the command, on explicit streams.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 {
		return errors.New(usage)
	}
	verb := args[0]
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "the HTML file to write (html, watch)")
	work := fs.String("work", "", "the work directory (default: under the user cache directory)")
	src := fs.String("runtime-src", "", "use the go-notebook source tree at this path instead of a released runtime")
	interval := fs.Duration("interval", 300*time.Millisecond, "how often watch and serve look at the file")
	addr := fs.String("addr", "127.0.0.1:8888", "where serve listens")
	timeout := fs.Duration("timeout", 0, "stop a cell that runs longer than this (0: no limit)")
	memory := fs.Int64("memory", 0, "stop a cell whose worker holds more than this many MiB (0: no limit)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(usage)
	}
	path := fs.Arg(0)
	k, err := newKernel(path, *work, *src, *timeout, *memory<<20)
	if err != nil {
		return err
	}
	defer k.Close()
	if *out == "" {
		*out = strings.TrimSuffix(path, filepath.Ext(path)) + ".html"
	}
	switch verb {
	case "run":
		nb, rep, err := once(k, path)
		if err != nil {
			return err
		}
		printReport(stdout, nb, rep)
		return failed(rep)
	case "html":
		nb, rep, err := once(k, path)
		if err != nil {
			return err
		}
		if err := writeHTML(*out, path, nb, rep); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", *out)
		return failed(rep)
	case "watch":
		page := func(nb *format.Notebook, rep *kernel.Report) error { return writeHTML(*out, path, nb, rep) }
		return watch(k, path, *interval, stdout, stopWatch, page)
	case "serve":
		return serve(k, path, *addr, *interval, stdout, stopWatch)
	}
	return errors.New(usage)
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
	// NOTEBOOK_REPLACE points modules of the work module at local
	// checkouts — module=dir pairs separated by commas — to run a notebook
	// against a library being changed.
	for _, pair := range strings.Split(os.Getenv("NOTEBOOK_REPLACE"), ",") {
		if mod, d, ok := strings.Cut(pair, "="); ok {
			opt.Replace[mod], _ = filepath.Abs(d) // fails only without a working directory
		}
	}
	return kernel.New(opt)
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
