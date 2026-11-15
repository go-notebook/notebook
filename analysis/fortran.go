package analysis

// Fortran is the language of ```fortran cells.
const Fortran = "fortran"

// The package that runs Fortran cells in the worker: this module's fcells, over
// go-embedded-fortran's evaluator (eval.Session). It belongs to this module,
// whose version the work module already requires.
const (
	FortranPath   = "github.com/go-notebook/notebook/fcells"
	FortranModule = "github.com/go-notebook/notebook"
)

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
// parameters, derived types and modules in Consts.
//
// Fortran cells run in one session of the worker (fcells), which holds their
// modules and variables, so they share them with the other Fortran cells and
// with no cell of another language (Isolated), as R's do. The front end does
// know the Go type a variable would take (sema.GoType, bib §7), but nothing
// carries a value out of the session yet: claiming that variables cross would
// be claiming a value the Go cell cannot read.
type fortranLang struct{ builtinLang }

func (fortranLang) Lexer() string { return "fortran" }

func (fortranLang) Crosses(_, _ *Cell, _ string) bool { return false }

func (fortranLang) Isolated() bool { return true }

func (fortranLang) Runtime() Module { return Module{Path: FortranPath, Module: FortranModule} }

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
