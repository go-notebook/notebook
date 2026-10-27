package gen

import "github.com/go-notebook/notebook/analysis"

// JuliaNotRun is analysis.JuliaNotRun, the failure of every Julia cell.
const JuliaNotRun = analysis.JuliaNotRun

func (g *generator) isJulia(i int) bool { return g.cells[i].Lang == analysis.Julia }

// deferJulia makes Julia cell i a stub that fails with JuliaNotRun. Its
// names are checked (analysis), and the cells that read them are deferred
// in turn, each saying which cell blocks it.
func (g *generator) deferJulia(i int) {
	g.prog.Deferred[i] = JuliaNotRun
}
