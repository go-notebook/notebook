package parsers

import (
	"errors"
	"slices"

	"github.com/go-embedded-c/c/cell"
	"github.com/go-notebook/notebook/analysis"
)

// cLimits bound the analysis of a C cell: a notebook may come from anyone, and
// the front end must neither run long, nest without end nor expand a macro
// without end on it.
var cLimits = cell.Limits{MaxBytes: 1 << 20, MaxDepth: cell.MaxDepth, MaxTokens: 1 << 16}

// cAnalyze replaces the front end in the tests; nil means cell.Analyze.
var cAnalyze func([]byte, cell.Context, cell.Limits) *cell.Analysis

// analyzeCCell analyses C cell index in the context state (nil, or the
// cell.Context the previous C cell returned) with go-embedded-c, which
// cc/v4 backs, and returns the context for the next cell. What a cell defines
// is Defines; its functions are also in Funcs, and its typedefs, tags,
// enumerators and macros in Consts, which do not cross to the other
// languages. Uses are the names of earlier C cells it reads. A cell with
// errors defines nothing and returns state as it was.
func analyzeCCell(index int, src string, state any) (*analysis.Cell, any) {
	c := &analysis.Cell{Index: index, Source: src, Lang: analysis.C, Imports: map[string]string{}}
	ctx, _ := state.(cell.Context)
	a, err := analyzeSafe(src, ctx)
	if err != nil {
		c.Errors = append(c.Errors, analysis.Error{Cell: index, Msg: err.Error()})
		return c, state
	}
	for _, d := range a.Diagnostics {
		pos, msg := d.Offset, d.Msg
		if pos < 0 || pos > len(src) { // outside the cell: a header, say
			pos, msg = 0, "outside the cell (in a header): "+d.Msg
		}
		c.Errors = append(c.Errors, analysis.Error{Cell: index, Pos: pos, Msg: msg})
	}
	if len(c.Errors) > 0 {
		return c, state
	}
	for _, n := range a.Defines {
		if slices.Contains(c.Defines, n.Name) {
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

// analyzeSafe runs the front end; a panic of it is an error of the cell,
// never a crash of the notebook (the fuzz test looks for them).
func analyzeSafe(src string, ctx cell.Context) (a *cell.Analysis, err error) {
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

// analyzeAlone is a cell analysed as the first C cell of a notebook.
func analyzeAlone(index int, src string) *analysis.Cell {
	c, _ := analyzeCCell(index, src, nil)
	return c
}
