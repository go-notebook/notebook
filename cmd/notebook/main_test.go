package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"runtime/debug"

	"github.com/go-notebook/notebook/format"
	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/kernel"
)

// main exits 1 on an error, and does not exit on success.
func TestMainExit(t *testing.T) {
	code := -1
	seam(t, &osExit, func(c int) { code = c })
	seam(t, &os.Args, []string{"notebook"})
	main()
	if code != 1 {
		t.Errorf("main without a verb: exit %d, want 1", code)
	}
	code = -1
	os.Args = []string{"notebook", "--version"}
	main()
	if code != -1 {
		t.Errorf("main --version: exit %d, want none", code)
	}
}

// notebook writes a notebook into a temporary directory.
func notebook(t *testing.T, src string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Go programs")
	}
	path := filepath.Join(t.TempDir(), "nb.md")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const good = "# Demo\n\n```go\nimport \"fmt\"\nx := 6\nfmt.Println(\"hi\")\n```\n\n```go\nx * 7\n```\n"

func TestRunAndHTML(t *testing.T) {
	path := notebook(t, good)
	work := filepath.Join(t.TempDir(), "work")
	var out, errs bytes.Buffer
	if err := run([]string{"run", "--work", work, path}, &out, &errs); err != nil {
		t.Fatalf("run: %v\n%s", err, errs.String())
	}
	if errs.Len() != 0 {
		t.Errorf("POSIX flags drew a note: %q", errs.String())
	}
	if s := out.String(); !strings.Contains(s, "── cell 0 (line 4, ran in") || !strings.Contains(s, "   hi\n") || !strings.Contains(s, "   42\n") {
		t.Errorf("run output:\n%s", s)
	}
	out.Reset()
	// The single-dash spelling of earlier releases still works, with a note.
	if err := run([]string{"run", "-work", work, path}, &out, &errs); err != nil || !strings.Contains(out.String(), "kept") {
		t.Errorf("second run: %v\n%s", err, out.String())
	}
	if !strings.Contains(errs.String(), "read -work as --work") {
		t.Errorf("no note for -work: %q", errs.String())
	}
	out.Reset()
	html := filepath.Join(t.TempDir(), "out.html")
	if err := run([]string{"html", "-w", work, "--output", html, path}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	page, _ := os.ReadFile(html)
	if !strings.Contains(string(page), "<h1>Demo</h1>") || !strings.Contains(out.String(), "wrote "+html) {
		t.Errorf("html: %q", out.String())
	}
	// The default page path is next to the notebook.
	if err := run([]string{"html", path, "--work=" + work}, &out, &errs); err != nil { // flags after the file, too
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".md") + ".html"); err != nil {
		t.Error(err)
	}
}

func TestFailures(t *testing.T) {
	var out, errs bytes.Buffer
	for _, args := range [][]string{nil, {"run"}, {"dance", "x.md"}, {"run", "-nope", "x.md"}} {
		if err := run(args, &out, &errs); err == nil {
			t.Errorf("run(%q) succeeded", args)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.md")
	for _, verb := range []string{"run", "html"} {
		if err := run([]string{verb, "-work", t.TempDir(), missing}, &out, &errs); err == nil {
			t.Errorf("%s of a missing file succeeded", verb)
		}
	}
	bad := notebook(t, "```go\nx := 1\n```\n```go\nx := 2\n```\n```go\npanic(\"no\")\n```\n")
	out.Reset()
	err := run([]string{"run", "-work", t.TempDir(), bad}, &out, &errs)
	if err == nil || !strings.Contains(out.String(), "error: ") || !strings.Contains(out.String(), "(line 2, not run)") || strings.Contains(out.String(), "kept") {
		t.Errorf("failing notebook: %v\n%s", err, out.String())
	}
	if err := run([]string{"html", "-work", t.TempDir(), bad}, &out, &errs); err == nil {
		t.Error("html of a failing notebook reported success")
	}
	if err := run([]string{"html", "-work", t.TempDir(), "-o", filepath.Join(missing, "x.html"), notebook(t, good)}, &out, &errs); err == nil {
		t.Error("html into a missing directory succeeded")
	}
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, nil, 0o644)
	if err := run([]string{"run", "-work", filepath.Join(file, "w"), bad}, &out, &errs); err == nil {
		t.Error("a work directory under a file was accepted")
	}
	// A notebook whose imports cannot be fetched fails before any cell.
	t.Setenv("GOPROXY", "off")
	nofetch := notebook(t, "```go\nimport \"example.invalid/x\"\nx.Y()\n```\n")
	if err := run([]string{"run", "-work", t.TempDir(), nofetch}, &out, &errs); err == nil || !strings.Contains(err.Error(), "tidy") {
		t.Errorf("unfetchable import: %v", err)
	}
	if err := run([]string{"html", "-work", t.TempDir(), nofetch}, &out, &errs); err == nil {
		t.Error("html of an unfetchable notebook succeeded")
	}
}

func TestWatch(t *testing.T) {
	path := notebook(t, good)
	k, err := newKernel(path, filepath.Join(t.TempDir(), "w"), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	out := &syncBuffer{}
	stop := make(chan struct{})
	done := make(chan error)
	html := filepath.Join(t.TempDir(), "w.html")
	go func() { done <- watch(k, path, 10*time.Millisecond, out, stop, toFile(path, html)) }()
	waitFor(t, out, "2 of 2 cells ran")
	// Change the file: the leaf runs again.
	later := time.Now().Add(2 * time.Second)
	_ = os.WriteFile(path, []byte(strings.Replace(good, "x * 7", "x * 8", 1)), 0o644)
	_ = os.Chtimes(path, later, later)
	waitFor(t, out, "1 of 2 cells ran")
	// A failing cell and an unwritable page are reported, not fatal.
	_ = os.WriteFile(path, []byte(good+"```go\npanic(1)\n```\n"), 0o644)
	later = later.Add(time.Second)
	_ = os.Chtimes(path, later, later)
	waitFor(t, out, "cell(s) failed")
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// An unwritable page and an unreadable notebook.
	out.Reset()
	stop = make(chan struct{})
	go func() {
		done <- watch(k, path, 10*time.Millisecond, out, stop, toFile(path, filepath.Join(path, "x.html")))
	}()
	waitFor(t, out, "page: ")
	close(stop)
	<-done
	out.Reset()
	stop = make(chan struct{})
	t.Setenv("GOPROXY", "off")
	broken := notebook(t, "```go\nimport \"example.invalid/x\"\nx.Y()\n```\n")
	go func() { done <- watch(k, broken, 10*time.Millisecond, out, stop, toFile(broken, html)) }()
	waitFor(t, out, "tidy")
	close(stop)
	<-done
}

// toFile is watch's page writer for the watch verb.
func toFile(path, out string) func(*format.Notebook, *kernel.Report) error {
	return func(nb *format.Notebook, rep *kernel.Report) error { return writeHTML(out, path, nb, rep) }
}

// syncBuffer is a buffer the watch loop writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// waitFor waits until the watch output contains want.
func waitFor(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watch never said %q:\n%s", want, out.String())
}

func TestWatchVerb(t *testing.T) {
	path := notebook(t, good)
	stop := make(chan struct{})
	close(stop)
	seam(t, &stopWatch, (<-chan struct{})(stop))
	var out bytes.Buffer
	if err := run([]string{"watch", "-work", t.TempDir(), path}, &out, &out); err != nil || !strings.Contains(out.String(), "cells ran") {
		t.Errorf("watch verb: %v %q", err, out.String())
	}
}

// Without --work the work directory is under the user cache directory.
func TestDefaultWorkDir(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData", "home"} {
		t.Setenv(v, home)
	}
	k, err := newKernel("nb.md", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Close()
}

func TestNoCacheDir(t *testing.T) {
	for _, v := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData", "home"} {
		t.Setenv(v, "")
	}
	if _, err := newKernel("nb.md", "", "", 0, 0); err == nil {
		t.Error("no cache directory, yet a kernel")
	}
}

func TestPrintGlobalDiag(t *testing.T) {
	var out bytes.Buffer
	printReport(&out, format.Parse(""), &kernel.Report{Diags: []gen.Diag{{Cell: -1, Msg: "internal: x"}}})
	if !strings.Contains(out.String(), "error: cell -1: internal: x") {
		t.Errorf("global diag: %q", out.String())
	}
}

// seam replaces a package variable for the rest of the test.
func seam[T any](t *testing.T, v *T, with T) {
	old := *v
	*v = with
	t.Cleanup(func() { *v = old })
}

func TestRuntimeSource(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{Main: debug.Module{Version: v}}, true }
	}
	seam(t, &readBuildInfo, info("v1.2.3"))
	if v, dir := runtimeSource(); v != "v1.2.3" || dir != "" {
		t.Errorf("release: %q %q", v, dir)
	}
	k, err := newKernel("nb.md", t.TempDir(), "", time.Minute, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Close()
	readBuildInfo = info("v0.0.0-2026-abc+dirty")
	seam(t, &thisFile, func() string { return "/nowhere/cmd/notebook/main.go" })
	if v, dir := runtimeSource(); v != "v0.0.0-2026-abc+dirty" || dir != "" {
		t.Errorf("pseudo-version without a tree: %q %q", v, dir)
	}
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	if v, dir := runtimeSource(); v != "" || dir != "" {
		t.Errorf("no build info: %q %q", v, dir)
	}
}

func TestRuntimeSourceFromTree(t *testing.T) {
	v, dir := runtimeSource()
	if dir == "" || v != "" {
		t.Errorf("from a checkout: %q %q", v, dir)
	}
	for v, want := range map[string]bool{"v1.2.3": true, "v0.0.0-2026-abc": false, "v1.0.0+dirty": false, "(devel)": false} {
		if released(v) != want {
			t.Errorf("released(%q) = %v", v, !want)
		}
	}
	k, err := newKernel("nb.md", t.TempDir(), "/elsewhere", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Close()
}

// A limit out of range is refused before anything runs: -memory is in MiB,
// and a byte count past an int64 would wrap to no limit.
func TestLimitsOutOfRange(t *testing.T) {
	for _, args := range [][]string{
		{"run", "-memory", "-1", "nb.md"},
		{"run", "-memory", "8796093022208", "nb.md"}, // 2^43 MiB = 2^63 bytes
		{"run", "-timeout", "-1s", "nb.md"},
		{"run", "--memory", "-1", "nb.md"},
		{"run", "-m", "8796093022208", "nb.md"},
		{"html", "--timeout=-1ns", "nb.md"},
		{"watch", "--interval", "0s", "nb.md"},
		{"serve", "-interval", "-1s", "nb.md"},
	} {
		var out, errOut bytes.Buffer
		err := run(args, &out, &errOut)
		name, _, _ := strings.Cut(strings.TrimLeft(args[1], "-"), "=")
		if name == "m" {
			name = "memory"
		}
		if err == nil || !strings.Contains(err.Error(), "--"+name) {
			t.Errorf("%v: got %v, want an error naming --%s", args, err, name)
		}
	}
}

// NOTEBOOK_REPLACE: module=dir pairs, a relative dir made absolute, a pair
// without "=" ignored; and newKernel reads it.
func TestNotebookReplace(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "rb")
	got := map[string]string{"github.com/go-notebook/notebook": "/runtime"}
	addReplaces(got, "github.com/go-scikit/ruby="+abs+",nonsense,example.com/rel=rel/dir")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"github.com/go-notebook/notebook": "/runtime",
		"github.com/go-scikit/ruby":       abs,
		"example.com/rel":                 filepath.Join(wd, "rel", "dir"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for m, d := range want {
		if got[m] != d {
			t.Errorf("%s: got %q, want %q", m, got[m], d)
		}
	}
	empty := map[string]string{}
	addReplaces(empty, "")
	if len(empty) != 0 {
		t.Errorf("empty variable: %v", empty)
	}

	t.Setenv("NOTEBOOK_REPLACE", "github.com/go-scikit/ruby="+abs)
	k, err := newKernel("nb.md", t.TempDir(), "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.Close()
}
