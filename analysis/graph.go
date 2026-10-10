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

// Analyze analyses the sources of a notebook's code cells, in document
// order, and builds their dependency graph.
func Analyze(sources []string) *Graph {
	g := &Graph{Owner: map[string]int{}}
	for i, src := range sources {
		c := AnalyzeCell(i, src)
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
			if o, ok := g.Owner[u]; ok && o != c.Index && !seen[o] {
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
