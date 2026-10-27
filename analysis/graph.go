package analysis

import (
	"fmt"
	"sort"
	"strings"
)

// Graph is the dependency graph of a notebook's code cells.
type Graph struct {
	Cells []*Cell

	// Owner maps each notebook variable to the cell that defines it.
	Owner map[string]int

	// Deps[i] lists the cells cell i reads from, sorted; Dependents is the
	// reverse relation.
	Deps, Dependents [][]int

	// Order is an execution order: every cell after the cells it reads, and
	// otherwise in document order. It holds every cell when Errors is empty.
	Order []int

	// Errors lists syntax errors, names defined twice, assignments to
	// another cell's variable, and dependency cycles.
	Errors []Error
}

// Analyze analyses the sources of a notebook's Go code cells, in document
// order, and builds their dependency graph.
func Analyze(sources []string) *Graph {
	cells := make([]Source, len(sources))
	for i, s := range sources {
		cells[i] = Source{Lang: Go, Source: s}
	}
	return AnalyzeSources(cells)
}

// AnalyzeSources is Analyze for cells in Go and in Ruby. The two languages
// share their variables (see Shared), so a Go cell can read what a Ruby cell
// defines and the reverse, and the graph orders them together.
func AnalyzeSources(sources []Source) *Graph {
	g := &Graph{Owner: map[string]int{}}
	for i, s := range sources {
		var c *Cell
		switch s.Lang {
		case Ruby:
			c = AnalyzeRubyCell(i, s.Source)
		case Julia:
			c = AnalyzeJuliaCell(i, s.Source)
		default:
			c = AnalyzeCell(i, s.Source)
		}
		g.Cells = append(g.Cells, c)
		g.Errors = append(g.Errors, c.Errors...)
	}
	for _, c := range g.Cells {
		for _, name := range c.Defines {
			if prev, dup := g.Owner[name]; dup {
				g.Errors = append(g.Errors, Error{Cell: c.Index, Pos: 0,
					Msg: fmt.Sprintf("%s is already defined by cell %d; each name is defined by one cell", name, prev)})
				continue
			}
			g.Owner[name] = c.Index
		}
	}
	n := len(g.Cells)
	g.Deps = make([][]int, n)
	g.Dependents = make([][]int, n)
	for _, c := range g.Cells {
		seen := map[int]bool{}
		for _, u := range c.Uses {
			if o, ok := g.Owner[u]; ok && o != c.Index && !seen[o] && Shared(g.Cells[o], c, u) {
				seen[o] = true
				g.Deps[c.Index] = append(g.Deps[c.Index], o)
				g.Dependents[o] = append(g.Dependents[o], c.Index)
			}
		}
		sort.Ints(g.Deps[c.Index])
		for _, a := range c.Assigns {
			if o, ok := g.Owner[a.Name]; ok && o != c.Index {
				g.Errors = append(g.Errors, Error{Cell: c.Index, Pos: a.Pos,
					Msg: fmt.Sprintf("cannot assign to %s, which cell %d defines; define a new name instead", a.Name, o)})
			}
		}
	}
	for _, d := range g.Dependents {
		sort.Ints(d)
	}
	g.order()
	return g
}

// order computes a topological order, smallest ready cell first, and reports
// the cells left on cycles.
func (g *Graph) order() {
	n := len(g.Cells)
	indeg := make([]int, n)
	for i := range g.Deps {
		indeg[i] = len(g.Deps[i])
	}
	done := make([]bool, n)
	for len(g.Order) < n {
		next := -1
		for i := 0; i < n; i++ {
			if !done[i] && indeg[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		done[next] = true
		g.Order = append(g.Order, next)
		for _, d := range g.Dependents[next] {
			indeg[d]--
		}
	}
	if len(g.Order) == n {
		return
	}
	var cyc []string
	for i := 0; i < n; i++ {
		if !done[i] {
			cyc = append(cyc, fmt.Sprint(i))
		}
	}
	for i := 0; i < n; i++ {
		if !done[i] {
			g.Errors = append(g.Errors, Error{Cell: i,
				Msg: "cells " + strings.Join(cyc, ", ") + " depend on each other in a cycle"})
		}
	}
}

// Downstream returns the cells that must re-run when the given cells change:
// those cells and everything that depends on them, in execution order.
func (g *Graph) Downstream(changed ...int) []int {
	mark := make([]bool, len(g.Cells))
	var visit func(int)
	visit = func(i int) {
		if mark[i] {
			return
		}
		mark[i] = true
		for _, d := range g.Dependents[i] {
			visit(d)
		}
	}
	for _, c := range changed {
		visit(c)
	}
	var out []int
	for _, i := range g.Order {
		if mark[i] {
			out = append(out, i)
		}
	}
	return out
}

// Upstream returns the cells the given cells need, transitively, themselves
// included, in execution order.
func (g *Graph) Upstream(cells ...int) []int {
	mark := make([]bool, len(g.Cells))
	var visit func(int)
	visit = func(i int) {
		if mark[i] {
			return
		}
		mark[i] = true
		for _, d := range g.Deps[i] {
			visit(d)
		}
	}
	for _, c := range cells {
		visit(c)
	}
	var out []int
	for _, i := range g.Order {
		if mark[i] {
			out = append(out, i)
		}
	}
	return out
}

// Shared reports whether the cell user sees the name that cell owner defines.
// Within a language every name is shared. Across languages only variables are:
//
//   - a Go cell sees a Ruby cell's top-level local variables, but not its
//     methods, constants or classes, nor a local named like a Go keyword or a
//     predeclared identifier (len, int, type, …), which stays Ruby's;
//   - a Ruby cell sees a Go cell's variables, but not its funcs or types;
//   - a Julia cell's variables, not its functions, macros, constants or types,
//     are seen by the other languages as a Ruby cell's are, and a Julia cell
//     sees their variables. Julia cells do not run yet, so a cell that reads
//     one of their variables is blocked (see gen), but the edge is there.
func Shared(owner, user *Cell, name string) bool {
	if owner.Lang == user.Lang {
		return true
	}
	if owner.Lang == Ruby || owner.Lang == Julia {
		ok := !contains(owner.Funcs, name) && !contains(owner.Consts, name)
		return ok && (user.Lang != Go && user.Lang != "" || GoName(name))
	}
	return !contains(owner.Funcs, name) && !contains(owner.Types, name)
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// GoName reports whether a Ruby local can be a Go variable of the same name:
// an identifier that is neither a Go keyword nor predeclared.
func GoName(name string) bool {
	if goReserved[name] {
		return false
	}
	for i, r := range name {
		ok := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}
	return name != ""
}

var goReserved = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`break case chan const continue default defer else fallthrough for func go goto if
		import interface map package range return select struct switch type var
		any bool byte comparable complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string
		uint uint8 uint16 uint32 uint64 uintptr true false iota nil
		append cap clear close complex copy delete imag len make max min new panic print println real recover`) {
		goReserved[k] = true
	}
}
