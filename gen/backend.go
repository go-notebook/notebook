package gen

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/go-notebook/notebook/analysis"
)

// A Backend generates the code of a non-Go language's cells that run. Go cells
// need none; a language that does not run (analysis.Run.Runs false) is
// check-only and needs none either: its cells are stubs that fail with its
// Run.NotRun. A language that runs registers its Backend under its name
// (RegisterBackend), and implements analysis.Runtime for the package the
// program imports: no edit of gen is needed.
type Backend interface {
	// Infer declares the variables of cell i (Context.Declare), or defers it
	// (Context.Defer), and marks it done (Context.Done).
	Infer(c *Context, i int)

	// Write writes cell k's function, named Context.FuncName(k), whose
	// parameter is a *_nb.Cell: see Ruby's in ruby.go.
	Write(c *Context, b *strings.Builder, k int)

	// Runtime is the import name the generated program gives the language's
	// package (analysis.Runtime), and the declarations that follow the
	// program's variables.
	Runtime() (importName, prelude string)
}

// Context is what a Backend sees of the program being generated.
type Context struct{ g *generator }

// Path is the notebook's path, for position directives.
func (c *Context) Path() string { return c.g.path }

// Code is cell i's source, language and line.
func (c *Context) Code(i int) Code { return c.g.cells[i] }

// Cell is cell i's analysis.
func (c *Context) Cell(i int) *analysis.Cell { return c.g.prog.Graph.Cells[i] }

// Options are the options GenerateWith was given.
func (c *Context) Options() Options { return c.g.opt }

// FuncName is the name of cell i's function in the program.
func (c *Context) FuncName(i int) string { return cellFunc(i) }

// Inputs are the variables of other cells that cell i reads, in the order of
// its uses.
func (c *Context) Inputs(i int) []Var { return c.g.inputs(i) }

// Vars are the variables declared so far.
func (c *Context) Vars() []Var { return c.g.prog.Vars }

// Declare adds a variable to the program.
func (c *Context) Declare(v Var) { c.g.prog.Vars = append(c.g.prog.Vars, v) }

// Unknown records that the Go type of variable name is not known yet: the Go
// cells that read it are deferred.
func (c *Context) Unknown(name string) { c.g.unknown[name] = true }

// ResolveType writes a Go type, in the syntax of Options.RubyTypes
// ("*{github.com/go-plot/plot}.Figure"), with the program's import names; ""
// when it names a package the program cannot see.
func (c *Context) ResolveType(t string) string { return c.g.resolveType(t) }

// WaitsOnDeferred says why cell i must wait for a deferred cell it reads, ""
// when it need not.
func (c *Context) WaitsOnDeferred(i int) string { return c.g.waitsOnDeferred(i) }

// Defer defers cell i with a reason: its code is not emitted and it fails
// with the reason when run.
func (c *Context) Defer(i int, reason string) { c.g.prog.Deferred[i] = reason }

// Done marks the variables of cell i as known.
func (c *Context) Done(i int) { c.g.done = append(c.g.done, i) }

var (
	backendMu sync.RWMutex
	backends  = map[string]Backend{}
)

// RegisterBackend registers the Backend of a language that runs. It panics on
// an empty or duplicate name.
func RegisterBackend(lang string, b Backend) {
	backendMu.Lock()
	defer backendMu.Unlock()
	if lang == "" {
		panic("gen: RegisterBackend with no language")
	}
	if _, dup := backends[lang]; dup {
		panic(fmt.Sprintf("gen: backend for %q registered twice", lang))
	}
	backends[lang] = b
}

// UnregisterBackend removes a backend, for tests.
func UnregisterBackend(lang string) {
	backendMu.Lock()
	delete(backends, lang)
	backendMu.Unlock()
}

func backendFor(lang string) Backend {
	backendMu.RLock()
	defer backendMu.RUnlock()
	return backends[lang]
}

// backendOf returns the backend of cell i's language, nil for Go and for
// check-only languages.
func (g *generator) backendOf(i int) Backend { return backendFor(g.cells[i].Lang) }

// checkOnly reports whether cell i's language does not run.
func (g *generator) checkOnly(i int) bool { return !analysis.RunOf(g.cells[i].Lang).Runs }

// used is a backend in use by the program, with the package it imports.
type used struct {
	name, path, prelude string
}

// runtimes lists, by language name, the backends the cells use.
func (g *generator) runtimes() []used {
	var langs []string
	seen := map[string]bool{}
	for i := range g.cells {
		l := g.cells[i].Lang
		if !seen[l] && backendFor(l) != nil {
			seen[l] = true
			langs = append(langs, l)
		}
	}
	sort.Strings(langs)
	out := make([]used, 0, len(langs))
	for _, l := range langs {
		name, prelude := backendFor(l).Runtime()
		out = append(out, used{name, analysis.Lookup(l).(analysis.Runtime).Runtime().Path, prelude})
	}
	return out
}
