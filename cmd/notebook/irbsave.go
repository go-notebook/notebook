package main

import (
	"fmt"
	"strings"

	irb "github.com/go-ruby-irb/irb"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/format"
)

// sessionNotebook writes the inputs that ran as a notebook of Ruby cells
// that notebook run runs. A session may assign a name again, a notebook may
// not — each name is defined by one cell — and a session reads `_`, which a
// cell keeps to itself. So the inputs are grouped: one cell per input, except
// that an input that defines or assigns a name an earlier cell defines is
// joined to that cell, with every input in between; and an input that reads
// `_` is joined to the one before it, which then assigns `_` its value
// (`_ = begin … end`), as irb did. Should the notebook still break a rule —
// a cycle between methods, say — all the inputs make one cell. failed
// inputs, which raised, are left out, and the text says how many.
func sessionNotebook(inputs []string, failed int) string {
	cells := groupInputs(inputs)
	if g := analysis.AnalyzeSources(rubySources(cells)); len(g.Errors) > 0 {
		cells = []string{strings.Join(wrapLast(inputs), "\n")}
	}
	intro := "# irb session\n\nThe inputs of a `notebook irb` session that ran, as Ruby cells: `notebook run` runs them again.\n"
	switch {
	case failed == 1:
		intro += "The input that raised an exception is left out.\n"
	case failed > 1:
		intro += fmt.Sprintf("The %d inputs that raised an exception are left out.\n", failed)
	}
	nb := &format.Notebook{Cells: []format.Cell{{Kind: format.Prose, Source: intro + "\n"}}}
	for i, c := range cells {
		if i > 0 {
			nb.Cells = append(nb.Cells, format.Cell{Kind: format.Prose, Source: "\n"})
		}
		nb.Cells = append(nb.Cells, format.NewCodeIn("ruby", c))
	}
	return nb.String()
}

// groupInputs makes the cells of sessionNotebook.
func groupInputs(inputs []string) []string {
	type group struct{ start, end int } // inputs[start:end]
	var groups []group
	owner := map[string]int{} // a name -> the group that defines it
	for i, src := range inputs {
		groups = append(groups, group{i, i + 1})
		cur := len(groups) - 1
		from := cur
		if i > 0 && readsLast(src) {
			from = cur - 1
		}
		names := definedNames(i, src)
		for _, n := range names {
			if g, ok := owner[n]; ok && g < from {
				from = g
			}
		}
		if from < cur {
			groups = append(groups[:from], group{groups[from].start, i + 1})
			for n, g := range owner {
				owner[n] = min(g, from)
			}
			cur = from
		}
		for _, n := range names {
			owner[n] = cur
		}
	}
	wrapped := wrapLast(inputs)
	cells := make([]string, len(groups))
	for i, g := range groups {
		cells[i] = strings.Join(wrapped[g.start:g.end], "\n")
	}
	return cells
}

// wrapLast is the inputs with each one whose `_` the next one reads wrapped
// to assign it. A first input that reads `_` reads nil, as in irb.
func wrapLast(inputs []string) []string {
	out := append([]string(nil), inputs...)
	for i := range inputs {
		switch {
		case !readsLast(inputs[i]):
		case i == 0:
			out[0] = "_ = nil\n" + out[0]
		default:
			out[i-1] = "_ = begin\n" + out[i-1] + "\nend"
		}
	}
	return out
}

// definedNames are the names an input defines or assigns at its top level.
func definedNames(i int, src string) []string {
	c := analysis.AnalyzeRubyCell(i, src)
	names := append([]string(nil), c.Defines...)
	for _, a := range c.Assigns {
		names = append(names, a.Name)
	}
	return names
}

// readsLast reports whether an input names `_`, irb's last value. (A block
// parameter named _ counts too: joining two inputs for nothing is harmless.)
func readsLast(src string) bool {
	for _, t := range irb.Lex(src) {
		if t.Event == irb.EvIdent && t.Tok == "_" {
			return true
		}
	}
	return false
}

func rubySources(cells []string) []analysis.Source {
	out := make([]analysis.Source, len(cells))
	for i, c := range cells {
		out[i] = analysis.Source{Lang: analysis.Ruby, Source: c}
	}
	return out
}
