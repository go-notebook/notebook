package analysis

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/go-embedded-c/c/cell"
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

// cLimits bound the analysis of a C cell: a notebook may come from anyone, and
// the front end must neither run long, nest without end nor expand a macro
// without end on it.
var cLimits = cell.Limits{MaxBytes: 1 << 20, MaxDepth: cell.MaxDepth, MaxTokens: 1 << 16}

// cAnalyze replaces the front end in the tests; nil means cell.Analyze. It is
// not initialised to it, which would link go-embedded-c into a program that
// does not enable the parsers.
var cAnalyze func([]byte, cell.Context, cell.Limits) *cell.Analysis

// cParser is the C front end, set by EnableParsers (see lang.go); the state it
// threads is a cell.Context, opaque to the rest of the package so that a
// program without the parser does not carry go-embedded-c.
var cParser func(index int, src string, state any) (*Cell, any)

// cLang is the C language. Its cells are not independent: a cell sees the
// typedefs, macros and declarations of the C cells before it (Sequential).
// Like Ruby's methods and R's functions, what is not a variable (functions,
// typedefs, struct, union and enum tags, enumerators, macros) stays C's;
// the variables cross.
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

// cStandalone is a C cell analysed on its own through the linked parser (or
// failed with ParserMissing): Language.Analyze's path.
func cStandalone(index int, src string) *Cell {
	c, _ := cLang{}.AnalyzeNext(nil, index, src)
	return c
}

// AnalyzeCCell analyses the source of C code cell number index on its own, as
// the first C cell of a notebook, with go-embedded-c (cc/v4 checks it). A
// notebook's cells are analysed in sequence by the graph; see analyzeCCell.
func AnalyzeCCell(index int, src string) *Cell {
	c, _ := analyzeCCell(index, src, nil)
	return c
}

// analyzeCCell is the parser EnableParsers links: it analyses a cell in the
// context state (nil, or the cell.Context the previous cell returned), and
// returns the context for the next cell. What a cell defines is Defines; its
// functions are also in Funcs, and its typedefs, tags, enumerators and macros
// in Consts, which do not cross to the other languages. Uses are the names of
// earlier C cells it reads.
func analyzeCCell(index int, src string, state any) (*Cell, any) {
	c := &Cell{Index: index, Source: src, Lang: C, Imports: map[string]string{}}
	ctx, _ := state.(cell.Context)
	a, err := cAnalyzeSafe(src, ctx)
	if err != nil {
		c.Errors = append(c.Errors, Error{Cell: index, Msg: err.Error()})
		return c, state
	}
	for _, d := range a.Diagnostics {
		pos, msg := d.Offset, d.Msg
		if pos < 0 || pos > len(src) { // outside the cell: a header, say
			pos, msg = 0, "outside the cell (in a header): "+d.Msg
		}
		c.Errors = append(c.Errors, Error{Cell: index, Pos: pos, Msg: msg})
	}
	if len(c.Errors) > 0 {
		return c, state
	}
	for _, n := range a.Defines {
		if contains(c.Defines, n.Name) {
			continue
		}
		c.Defines = append(c.Defines, n.Name)
		switch n.Kind {
		case cell.Variable:
		case cell.Function:
			c.Funcs = append(c.Funcs, n.Name)
		default:
			c.Consts = append(c.Consts, n.Name)
		}
	}
	c.Uses = append([]string(nil), a.Uses...)
	return c, ctx.Extend(a)
}

// cAnalyzeSafe runs the front end; a panic of it is an error of the cell,
// never a crash of the notebook (the fuzz test looks for them).
func cAnalyzeSafe(src string, ctx cell.Context) (a *cell.Analysis, err error) {
	defer func() {
		if r := recover(); r != nil {
			a, err = nil, errors.New("the C parser failed on this cell; please report it")
		}
	}()
	analyze := cAnalyze
	if analyze == nil {
		analyze = cell.Analyze
	}
	return analyze([]byte(src), ctx, cLimits), nil
}

func cReads(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, a C cell, and C cells are not run yet", c)
}

func cReadsBlocked(c int) string {
	return fmt.Sprintf("not run: it reads cell %d, which is blocked by a C cell", c)
}
