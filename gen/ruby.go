package gen

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// RubyPath is the import path of the package that runs Ruby cells: the
// go-scikit Ruby bindings' cells, over the pure-Go Ruby go-embedded-ruby.
const RubyPath = analysis.RubyPath

// RubyModule is the module RubyPath belongs to.
const RubyModule = analysis.RubyModule

// rubyBackend generates the Ruby cells: calls of the Ruby session.
type rubyBackend struct{}

func (rubyBackend) Infer(c *Context, i int) { c.g.inferRuby(i) }

func (rubyBackend) Write(c *Context, b *strings.Builder, k int) { c.g.writeRubyCell(b, k) }

func (rubyBackend) Runtime() (name, prelude string) { return "_rb", "var _rbs = _rb.New()\n\n" }

func init() { RegisterBackend(analysis.Ruby, rubyBackend{}) }

// Options configure GenerateWith.
type Options struct {
	// RubyTypes maps a Ruby cell's variable to the Go type its value had the
	// last time the cell ran, as the cells package names it
	// ("[]float64", "*{github.com/go-plot/plot}.Figure"). A Go cell sees the
	// variable with that type. A variable not in it is declared any, and the
	// Go cells that read it are deferred until it is known.
	RubyTypes map[string]string
}

// varOf returns the notebook variable named name, if the program has one.
func (g *generator) varOf(name string) (Var, bool) {
	for _, v := range g.prog.Vars {
		if v.Name == name {
			return v, true
		}
	}
	return Var{}, false
}

// inputs are the variables of other cells a cell reads, in the order of its
// uses.
func (g *generator) inputs(i int) []Var {
	gr := g.prog.Graph
	var out []Var
	for _, u := range gr.Cells[i].Uses {
		o, ok := gr.Owner[u]
		if !ok || o == i || !analysis.Shared(gr.Cells[o], gr.Cells[i], u) {
			continue
		}
		if v, ok := g.varOf(u); ok {
			out = append(out, v)
		}
	}
	return out
}

// inferRuby declares the variables of Ruby cell i: each top-level local it
// assigns, with the Go type it had when it last ran, and, when it defines
// methods or constants, a volatile marker, so that a new worker runs the cell
// again before a cell that needs them.
func (g *generator) inferRuby(i int) {
	if why := g.waitsOnDeferred(i); why != "" {
		g.deferCell(i)
		return
	}
	c := g.prog.Graph.Cells[i]
	for _, name := range c.Defines {
		if contains(c.Funcs, name) || contains(c.Consts, name) {
			continue
		}
		v := Var{Name: name, Cell: i, Ruby: true, Type: "any"}
		if !analysis.GoName(name) {
			v.Ident = "_nbrb_" + name
		}
		if t, ok := g.opt.RubyTypes[name]; ok {
			if r := g.resolveType(t); r != "" {
				v.Type = r
			}
		} else {
			g.unknown[name] = true
		}
		g.prog.Vars = append(g.prog.Vars, v)
	}
	if len(c.Funcs)+len(c.Consts) > 0 {
		g.prog.Vars = append(g.prog.Vars, Var{Name: rubyDefs(i), Cell: i, Ruby: true, Type: "func()"})
	}
	g.done = append(g.done, i)
}

// rubyDefs names the marker of a Ruby cell's methods and constants.
func rubyDefs(i int) string { return fmt.Sprintf("_nbrbdefs%d", i) }

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

var pkgRef = regexp.MustCompile(`\{([^{}]+)\}`)

// resolveType writes a Go type in the cells package's syntax with the
// program's import names, importing the packages it names; "" when one of
// them is not among the packages the program can see.
func (g *generator) resolveType(t string) string {
	ok := true
	out := pkgRef.ReplaceAllStringFunc(t, func(m string) string {
		path := m[1 : len(m)-1]
		if _, err := g.imp.Import(path); err != nil {
			ok = false
			return ""
		}
		if n, found := g.names[path]; found {
			return n
		}
		g.synthetic++
		n := fmt.Sprintf("_nbimp%d", g.synthetic)
		g.names[path], g.byName[n] = n, path
		return n
	})
	if !ok {
		return ""
	}
	return out
}

// waits reports why Go cell i cannot be compiled yet: it reads a Ruby
// variable whose type is not known, or a variable of a deferred cell.
func (g *generator) waits(i int) bool {
	for _, v := range g.inputs(i) {
		if v.Ruby && g.unknown[v.Name] {
			g.prog.Deferred[i] = fmt.Sprintf("not run yet: it reads %s, whose Go type is known once cell %d has run", v.Name, v.Cell)
			return true
		}
	}
	if why := g.waitsOnDeferred(i); why != "" {
		g.prog.Deferred[i] = why
		return true
	}
	return false
}

// waitsOnDeferred says which deferred cell cell i reads from, if one.
func (g *generator) waitsOnDeferred(i int) string {
	for _, d := range g.prog.Graph.Deps[i] {
		if _, ok := g.prog.Deferred[d]; ok {
			if g.checkOnly(d) {
				l := g.cells[d].Lang
				g.blockedBy[i] = l
				return analysis.RunOf(l).Reads(d)
			}
			if l, ok := g.blockedBy[d]; ok {
				g.blockedBy[i] = l
				return analysis.RunOf(l).ReadsBlocked(d)
			}
			return fmt.Sprintf("not run yet: it reads cell %d, which waits for a Ruby cell", d)
		}
	}
	return ""
}

// deferCell marks cell i deferred; its variables are not declared.
func (g *generator) deferCell(i int) {
	if _, ok := g.prog.Deferred[i]; !ok {
		g.prog.Deferred[i] = g.waitsOnDeferred(i)
	}
}

// writeDeferred writes a deferred cell: a stub that fails with the reason.
func (g *generator) writeDeferred(b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\tpanic(_nb.Deferred(%q))\n}\n\n", cellFunc(k), g.prog.Deferred[k])
}

// writeRubyCell writes Ruby cell k: a call of the Ruby session, which sets
// the variables the cell reads, evaluates it, and stores the locals it
// assigns into their Go holders.
func (g *generator) writeRubyCell(b *strings.Builder, k int) {
	c := g.prog.Graph.Cells[k]
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_rbs.Run(_rb.Cell{\n\t\tSource: %q,\n\t\tFile: %q, Line: %d, Display: %v,\n",
		cellFunc(k), g.cells[k].Source, g.path, g.cells[k].Line, c.RubyDisplay)
	b.WriteString("\t\tIn: []_rb.Var{")
	for i, v := range g.inputs(k) {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "{Name: %q, Ptr: &%s, IfAbsent: %v}", v.Name, v.ident(), v.Ruby)
	}
	b.WriteString("},\n\t\tOut: []_rb.Var{")
	n := 0
	for _, v := range g.prog.Vars {
		if v.Cell != k || v.Name == rubyDefs(k) {
			continue
		}
		if n > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "{Name: %q, Ptr: &%s}", v.Name, v.ident())
		n++
	}
	b.WriteString("},\n\t}, _k.Show, _k.Note)\n}\n\n")
}

// onlyDeferred reports whether the package imported as name is referred to
// by deferred cells only (and is no type a variable is declared with).
func (g *generator) onlyDeferred(name string) bool {
	if len(g.prog.Deferred) == 0 {
		return false
	}
	for i, c := range g.parsed {
		if g.prog.Deferred[i] == "" && c.refs[name] {
			return false
		}
	}
	for _, v := range g.prog.Vars {
		if strings.Contains(v.Type, name+".") {
			return false
		}
	}
	return true
}
