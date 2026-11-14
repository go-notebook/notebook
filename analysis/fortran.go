package analysis

import (
	"fmt"
	"strconv"
)

// Fortran is the language of ```fortran cells.
const Fortran = "fortran"

// FortranNotRun is the failure of every Fortran cell: go-embedded-fortran
// parses and checks Fortran, but its evaluator has no procedures, real(4) or
// modules yet.
const FortranNotRun = "Fortran cells are not run yet (go-embedded-fortran: the evaluator is incomplete)"

// FortranBlocked is the failure of a cell that reads, directly or through
// other cells, what Fortran cell c defines.
func FortranBlocked(c int) string {
	return "blocked: it depends on cell " + strconv.Itoa(c) + ", a Fortran cell, and Fortran cells are not run yet"
}

// fortranParser is the Fortran front end, set by SetFortranParser from
// analysis/parsers. This package never imports go-embedded-fortran, so that a
// program that does not import analysis/parsers does not carry it
// (TestNoEmbeddedFortranImport). The state it threads is opaque here; nil is
// the state before the first Fortran cell.
var fortranParser func(index int, src string, state any) (*Cell, any)

// SetFortranParser links the Fortran front end: it analyses Fortran cell
// index in the session state, and returns the state for the next Fortran
// cell, which is state itself when the cell has errors. Import
// github.com/go-notebook/notebook/analysis/parsers rather than calling it.
func SetFortranParser(f func(index int, src string, state any) (*Cell, any)) { fortranParser = f }

// fortranLang is the Fortran language. Its cells are not independent: a cell
// sees the modules, types, procedures and declarations of the Fortran cells
// before it (Sequential). A cell's procedures are in Funcs, and its
// parameters, derived types and modules in Consts; like C's functions they
// stay Fortran's, and only the variables cross to the other languages.
type fortranLang struct{ localsLang }

func (fortranLang) Lexer() string { return "fortran" }

// Begin is the state before the first Fortran cell: nothing is defined yet.
func (fortranLang) Begin() any { return nil }

// AnalyzeNext analyses Fortran cell index after the earlier Fortran cells,
// whose session is state, and returns the state for the next one. A cell with
// an error leaves the state as it was.
func (fortranLang) AnalyzeNext(state any, index int, src string) (*Cell, any) {
	if fortranParser == nil {
		return &Cell{Index: index, Source: src, Lang: Fortran, Imports: map[string]string{},
			Errors: []Error{{Cell: index, Msg: ParserMissing("Fortran")}}}, state
	}
	return fortranParser(index, src, state)
}

// fortranStandalone is a Fortran cell analysed on its own, as the first
// Fortran cell: the path of Language.Analyze.
func fortranStandalone(index int, src string) *Cell {
	c, _ := fortranLang{}.AnalyzeNext(nil, index, src)
	return c
}

func fortranReads(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, a Fortran cell, and Fortran cells are not run yet", c)
}

func fortranReadsBlocked(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, which is blocked by a Fortran cell", c)
}
