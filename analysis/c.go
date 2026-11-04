package analysis

import (
	"fmt"
	"strconv"
)

// C is the language of ```c cells.
const C = "c"

// CNotRun is the failure of every C cell: go-embedded-c parses and checks C,
// but has no code generation yet.
const CNotRun = "C cells are not run yet (go-embedded-c M2: code generation)"

// CBlocked is the failure of a cell that reads, directly or through other
// cells, what C cell c defines.
func CBlocked(c int) string {
	return "blocked: it depends on cell " + strconv.Itoa(c) + ", a C cell, and C cells are not run yet"
}

// cParser is the C front end, set by SetCParser from analysis/parsers. This
// package never imports go-embedded-c, so that a program that does not import
// analysis/parsers does not carry it (TestNoEmbeddedCImport). The state it
// threads is opaque here; nil is the state before the first C cell.
var cParser func(index int, src string, state any) (*Cell, any)

// SetCParser links the C front end: it analyses C cell index in the context
// state, and returns the cell and the context for the next C cell, which is
// state itself when the cell has errors. Import
// github.com/go-notebook/notebook/analysis/parsers rather than calling it.
func SetCParser(f func(index int, src string, state any) (*Cell, any)) { cParser = f }

// cLang is the C language. Its cells are not independent: a cell sees the
// typedefs, macros and declarations of the C cells before it (Sequential).
// A cell's functions are in Funcs, and its typedefs, tags, enumerators and
// macros in Consts; like Ruby's methods and R's functions they stay C's, and
// only the variables cross to the other languages.
type cLang struct{ localsLang }

func (cLang) Lexer() string { return "c" }

// Begin is the state before the first C cell: nothing is defined yet.
func (cLang) Begin() any { return nil }

// AnalyzeNext analyses C cell index after the earlier C cells, whose
// accumulated declarations are state, and returns the state for the next one.
// A cell with an error leaves the state as it was.
func (cLang) AnalyzeNext(state any, index int, src string) (*Cell, any) {
	if cParser == nil {
		return &Cell{Index: index, Source: src, Lang: C, Imports: map[string]string{},
			Errors: []Error{{Cell: index, Msg: ParserMissing("C")}}}, state
	}
	return cParser(index, src, state)
}

// cStandalone is a C cell analysed on its own, as the first C cell: the path
// of Language.Analyze.
func cStandalone(index int, src string) *Cell {
	c, _ := cLang{}.AnalyzeNext(nil, index, src)
	return c
}

func cReads(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, a C cell, and C cells are not run yet", c)
}

func cReadsBlocked(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, which is blocked by a C cell", c)
}
