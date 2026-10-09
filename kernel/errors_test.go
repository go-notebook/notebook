package kernel

import (
	"bufio"
	"errors"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/go-notebook/notebook/runtime"
)

// TestPersistAcrossKernels runs a notebook, then opens a second kernel on
// the same work directory: nothing runs again, and a changed leaf runs with
// its input read back from disk.
func TestPersistAcrossKernels(t *testing.T) {
	k := newKernel(t)
	cells := codes("x := 6\n", "y := x * 7\ny\n")
	if _, err := k.Run(cells); err != nil {
		t.Fatal(err)
	}
	_ = k.Close()
	k2, err := New(Options{Dir: k.opt.Dir, Replace: k.opt.Replace})
	if err != nil {
		t.Fatal(err)
	}
	defer k2.Close()
	rep, err := k2.Run(cells)
	if err != nil || ran(rep) != ".." || shown(rep.Results[1]) != "42" {
		t.Fatalf("second kernel: %s %+v %v", ran(rep), rep.Results, err)
	}
	cells[1].Source = "y := x * 8\ny\n"
	rep, _ = k2.Run(cells)
	if ran(rep) != ".R" || shown(rep.Results[1]) != "48" {
		t.Errorf("leaf change on the second kernel: %s %q", ran(rep), shown(rep.Results[1]))
	}

	// A corrupt cache entry is a miss; a corrupt value makes its cell run.
	k3, _ := New(Options{Dir: k.opt.Dir, Replace: k.opt.Replace})
	defer k3.Close()
	matches, _ := filepath.Glob(filepath.Join(k.opt.Dir, "cache", "*", "*.json"))
	for _, m := range matches {
		_ = os.WriteFile(m, []byte("not json"), 0o644)
	}
	rep, err = k3.Run(cells)
	if err != nil || ran(rep) != "RR" {
		t.Errorf("corrupt cache: %s %v", ran(rep), err)
	}
	k3.values["x"] = value{data: []byte{9}, key: k3.values["x"].key}
	k3.loaded = map[string]bool{}
	cells[1].Source = "y := x * 9\ny\n"
	rep, _ = k3.Run(cells)
	if ran(rep) != "RR" || shown(rep.Results[1]) != "54" {
		t.Errorf("corrupt value: %s %q", ran(rep), shown(rep.Results[1]))
	}
}

func TestNewErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o644)
	if _, err := New(Options{Dir: filepath.Join(file, "sub")}); err == nil {
		t.Error("a work directory under a file was accepted")
	}
	k, err := New(Options{Dir: t.TempDir()})
	if err != nil || k.opt.Name != "notebook.md" {
		t.Errorf("default name: %v %v", k.opt.Name, err)
	}
	if !strings.Contains(k.goMod(), "require github.com/go-notebook/notebook v0.0.0\n") {
		t.Errorf("goMod without a version:\n%s", k.goMod())
	}
	k.opt.RuntimeVersion = "v1.2.3"
	if !strings.Contains(k.goMod(), "v1.2.3") {
		t.Errorf("goMod with a version:\n%s", k.goMod())
	}
}

// seam replaces a package variable for the rest of the test.
func seam[T any](t *testing.T, v *T, with T) {
	old := *v
	*v = with
	t.Cleanup(func() { *v = old })
}

func TestModuleErrors(t *testing.T) {
	k := newKernel(t)
	fail := errors.New("injected")
	cells := codes("x := 1\n")

	seam(t, &writeFile, func(string, []byte, os.FileMode) error { return fail })
	if _, err := k.Run(cells); !errors.Is(err, fail) {
		t.Errorf("go.mod write: %v", err)
	}
	calls := 0
	writeFile = func(name string, b []byte, m os.FileMode) error {
		if calls++; calls == 2 {
			return fail
		}
		return os.WriteFile(name, b, m)
	}
	if _, err := k.Run(cells); !errors.Is(err, fail) {
		t.Errorf("deps.go write: %v", err)
	}
	writeFile = os.WriteFile

	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	if _, err := k.Run(codes("import \"example.invalid/nope\"\nnope.X()\n")); err == nil || !strings.Contains(err.Error(), "go mod tidy") {
		t.Errorf("tidy: %v", err)
	}

	seam(t, &loadPackages, func(*packages.Config, ...string) ([]*packages.Package, error) { return nil, fail })
	if _, err := k.Run(cells); !errors.Is(err, fail) {
		t.Errorf("load: %v", err)
	}
	loadPackages = func(*packages.Config, ...string) ([]*packages.Package, error) {
		return []*packages.Package{{PkgPath: "p", Errors: []packages.Error{{Msg: "broken"}}}}, nil
	}
	if _, err := k.Run(cells); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("package errors: %v", err)
	}
	loadPackages = packages.Load

	im := &importer{pkgs: map[string]*types.Package{}}
	if _, err := im.Import("fmt"); err == nil {
		t.Error("an unloaded package imported")
	}
}

func TestBuildAndStartErrors(t *testing.T) {
	k := newKernel(t)
	fail := errors.New("injected")
	cells := codes("x := 1\nx\n")

	seam(t, &goBuild, func(string, string) ([]byte, error) { return []byte("compiler says no"), fail })
	if rep, err := k.Run(cells); err == nil || !strings.Contains(err.Error(), "compiler says no") || rep == nil {
		t.Errorf("build: %v", err)
	}
	goBuild = func(string, string) ([]byte, error) { return nil, nil } // builds nothing
	if _, err := k.Run(cells); err == nil {
		t.Error("starting a missing worker succeeded")
	}
	// The imports are unchanged, so only main.go is written.
	k.src = nil
	seam(t, &writeFile, func(string, []byte, os.FileMode) error { return fail })
	if _, err := k.Run(cells); !errors.Is(err, fail) {
		t.Errorf("main.go write: %v", err)
	}
}

func TestRunCellDiagnostics(t *testing.T) {
	k := newKernel(t)
	rep, err := k.RunCell(codes("x := nope\n"), 0)
	if err != nil || len(rep.Diags) == 0 {
		t.Errorf("RunCell with an error: %+v %v", rep, err)
	}
}

// fakeWorker makes the kernel talk to canned replies.
func fakeWorker(k *Kernel, replies string) {
	k.in = nopCloser{io.Discard}
	k.out = bufio.NewScanner(strings.NewReader(replies))
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

func TestCallErrors(t *testing.T) {
	k := &Kernel{}
	fakeWorker(k, "not json\n")
	if r := k.call(runtime.Request{Op: "run"}); !strings.HasPrefix(r.Error, "bad reply") {
		t.Errorf("bad reply: %q", r.Error)
	}
	fakeWorker(k, "")
	k.in = failWriter{}
	if r := k.call(runtime.Request{Op: "run"}); !strings.Contains(r.Error, "worker stopped") {
		t.Errorf("write failure: %q", r.Error)
	}
	if k.get("x") != nil {
		t.Error("get from a dead worker returned data")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (failWriter) Close() error              { return nil }

func TestSaveFailure(t *testing.T) {
	dir := t.TempDir()
	k := &Kernel{opt: Options{Dir: dir}, values: map[string]value{}}
	_ = os.WriteFile(filepath.Join(dir, "cache"), nil, 0o644) // a file where the directory goes
	k.save("abcdef", Result{}, nil)
	if _, err := os.Stat(k.cachePath("abcdef")); err == nil {
		t.Error("saved under a file")
	}
}
