package kernel

import (
	"fmt"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
)

// analyzeImports returns the import paths the cells declare.
func analyzeImports(srcs []string) []string {
	var out []string
	for i, src := range srcs {
		for _, p := range analysis.AnalyzeCell(i, src).Imports {
			out = append(out, p)
		}
	}
	return out
}

// prepareModule makes the work directory a module that can build the
// notebook's imports, fetching what it lacks (go mod tidy), and loads the
// imported packages' types for the generator. It does nothing when the
// imports have not changed.
func (k *Kernel) prepareModule(paths []string) error {
	if k.ruby {
		paths = append(paths, gen.RubyPath)
	}
	sig := strings.Join(paths, "\n")
	if k.types != nil && sig == k.imports {
		return nil
	}
	if err := writeFile(filepath.Join(k.opt.Dir, "go.mod"), []byte(k.goMod()), 0o644); err != nil {
		return err
	}
	var deps strings.Builder
	deps.WriteString("package main\n\nimport (\n")
	fmt.Fprintf(&deps, "\t_ %q\n", gen.RuntimePath)
	for _, p := range paths {
		fmt.Fprintf(&deps, "\t_ %q\n", p)
	}
	deps.WriteString(")\n")
	if err := writeFile(filepath.Join(k.opt.Dir, "deps.go"), []byte(deps.String()), 0o644); err != nil {
		return err
	}
	// main.go from another set of imports could stop tidy; it is rewritten
	// before the next build anyway.
	_ = os.Remove(filepath.Join(k.opt.Dir, "main.go"))
	k.src = nil
	if out, err := k.goCmd("mod", "tidy").CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %v\n%s", err, out)
	}
	imp, err := loadImporter(k.opt.Dir, paths)
	if err != nil {
		return err
	}
	toolchain, err := goVersion(k)
	if err != nil {
		return err
	}
	sums, _ := os.ReadFile(filepath.Join(k.opt.Dir, "go.sum")) // none when only the standard library is used
	k.types, k.imports = imp, sig
	k.env = keyEnv{toolchain: toolchain, sums: string(sums)}
	return nil
}

// keyEnv is what, besides the code, decides what a cell computes: the Go
// toolchain that compiles it and the versions of the modules it links.
type keyEnv struct {
	toolchain string // go env GOVERSION
	sums      string // the work module's go.sum
}

// goVersion asks the go command its version; a seam for tests.
var goVersion = func(k *Kernel) (string, error) {
	out, err := k.goCmd("env", "GOVERSION").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// goMod writes the work module's go.mod.
func (k *Kernel) goMod() string {
	v := k.opt.RuntimeVersion
	if v == "" {
		v = "v0.0.0"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "module nbwork\n\ngo 1.27.2\n\nrequire %s %s\n", modulePath, v)
	if k.ruby {
		rv := k.opt.RubyVersion
		if rv == "" {
			rv = DefaultRubyVersion
		}
		fmt.Fprintf(&b, "\nrequire %s %s\n", gen.RubyModule, rv)
	}
	var mods []string
	for m := range k.opt.Replace {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	for _, m := range mods {
		fmt.Fprintf(&b, "\nreplace %s => %s\n", m, k.opt.Replace[m])
	}
	return b.String()
}

// tagsFlag builds the worker with rbgo's core build: the Ruby language and
// its numeric stack, without the gem bindings a notebook does not use (a
// fifth of the size). A worker without Ruby cells does not see it.
const tagsFlag = "-tags=rbgo_core"

// DefaultRubyVersion is the version of github.com/go-scikit/ruby a work
// module requires for its Ruby cells, unless Options.RubyVersion says
// otherwise.
const DefaultRubyVersion = "v0.1.0"

// modulePath is this module's path, which the runtime package belongs to.
const modulePath = "github.com/go-notebook/notebook"

// goCmd returns a go command run in the work directory, outside any
// workspace.
func (k *Kernel) goCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("go", args...)
	cmd.Dir = k.opt.Dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod "+tagsFlag)
	return cmd
}

// loadPackages is packages.Load, a seam for tests.
var loadPackages = packages.Load

// importer serves the types of the packages a notebook imports and of
// their dependencies.
type importer struct {
	pkgs map[string]*types.Package
}

// Import implements types.Importer.
func (im *importer) Import(path string) (*types.Package, error) {
	if p, ok := im.pkgs[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("package %q is not imported by the notebook", path)
}

// loadImporter loads the packages and everything they depend on.
func loadImporter(dir string, paths []string) (*importer, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedImports | packages.NeedDeps,
		Dir:  dir,
		Env:  append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod "+tagsFlag),
	}
	roots, err := loadPackages(cfg, append([]string{gen.RuntimePath}, paths...)...)
	if err != nil {
		return nil, err
	}
	im := &importer{pkgs: map[string]*types.Package{}}
	var errs []string
	packages.Visit(roots, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs = append(errs, e.Error())
		}
		if p.Types != nil {
			im.pkgs[p.PkgPath] = p.Types
		}
	})
	if len(errs) > 0 {
		return nil, fmt.Errorf("loading imports:\n%s", strings.Join(errs, "\n"))
	}
	return im, nil
}
