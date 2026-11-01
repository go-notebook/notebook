package gen

import (
	"sort"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// A backend generates the code of a non-Go language's cells that run. The Go
// cells need none; a language that does not run (analysis.Run.Runs false) is
// check-only and needs none either: its cells are stubs that fail with its
// Run.NotRun. A backend is registered under the language's name.
type backend interface {
	// infer declares the variables of cell i.
	infer(g *generator, i int)
	// write writes cell k's function.
	write(g *generator, b *strings.Builder, k int)
	// runtime is the import name the generated program gives the language's
	// package (analysis.Runtime), and the declarations that follow the
	// program's variables.
	runtime() (name, prelude string)
}

var backends = map[string]backend{}

// backendOf returns the backend of cell i's language, nil for Go and for
// check-only languages.
func (g *generator) backendOf(i int) backend { return backends[g.cells[i].Lang] }

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
		if backends[l] != nil && !seen[l] {
			seen[l] = true
			langs = append(langs, l)
		}
	}
	sort.Strings(langs)
	out := make([]used, 0, len(langs))
	for _, l := range langs {
		name, prelude := backends[l].runtime()
		out = append(out, used{name, analysis.Lookup(l).(analysis.Runtime).Runtime().Path, prelude})
	}
	return out
}
