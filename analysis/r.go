package analysis

import (
	"errors"
	"sort"
	"strings"

	"github.com/go-embedded-r/r/syntax"
)

// R is the language of ```r cells.
const R = "r"

// The package that runs R cells in the worker: this module's rcells, over
// go-embedded-r's evaluator. It belongs to this module, whose version the work
// module already requires.
const (
	RPath   = "github.com/go-notebook/notebook/rcells"
	RModule = "github.com/go-notebook/notebook"
)

// rLimits bound the parse of an R cell: a notebook may come from anyone, and
// the parser must neither run long nor nest without end on it.
var rLimits = syntax.Limits{MaxBytes: 4 << 20, MaxDepth: 1000, MaxNodes: 1 << 20}

// rParse is the parser, a seam for the tests.
var rParse = syntax.ParseAllLimits

// AnalyzeRCell analyses the source of R code cell number index, with
// go-embedded-r's parser (a port of R 4.6's gram.y, which gives the trees
// parse() gives) and its TopLevelNames rules: what a cell assigns at the top
// level (x <- v, v -> x, x = v, assign("x", v), the variable of a top-level
// for) it defines; the free variables of its code and of its functions'
// bodies it uses. A function assigned at the top level (f <- function(x) …)
// is in Funcs: like a Ruby method or a Julia function it stays R's. A cell
// whose names cannot be worked out statically (<<-, eval, assign or get with
// a computed name, attach, library…) is Dynamic.
func AnalyzeRCell(index int, src string) *Cell {
	c := &Cell{Index: index, Source: src, Lang: R, Imports: map[string]string{}}
	names, funcs, err := rNames(src)
	if err != nil {
		var se *syntax.Error
		switch {
		case errors.As(err, &se):
			c.Errors = append(c.Errors, Error{Cell: index, Pos: rOffset(src, se.Line, se.Col), Msg: "syntax error: " + se.Message})
		case errors.Is(err, syntax.ErrLimit):
			c.Errors = append(c.Errors, Error{Cell: index, Msg: "the R cell is too large or too deeply nested to parse (" + err.Error() + ")"})
		default:
			c.Errors = append(c.Errors, Error{Cell: index, Msg: err.Error()})
		}
		return c
	}
	c.Defines, c.Funcs, c.Dynamic = names.Assigned, funcs, names.Dynamic
	c.Uses = append([]string(nil), names.Read...)
	sort.Strings(c.Uses)
	return c
}

// rNames parses a cell and works out its names; a panic of the parser or of
// the names pass is an error of the cell, never a crash of the notebook (the
// fuzz test looks for them).
func rNames(src string) (names *syntax.Names, funcs []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			names, funcs, err = nil, nil, errors.New("the R parser failed on this cell; please report it")
		}
	}()
	f, err := rParse(src, rLimits)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range f.Exprs {
		if name, ok := rFuncDef(e); ok && !contains(funcs, name) {
			funcs = append(funcs, name)
		}
	}
	return syntax.NamesOf(f.Exprs), funcs, nil
}

// rFuncDef reports the name a top-level f <- function(…) … or
// f = function(…) … assigns a function to.
func rFuncDef(e syntax.Node) (string, bool) {
	call, ok := e.(*syntax.Call)
	if !ok || len(call.Args) != 2 {
		return "", false
	}
	if op := call.FunName(); op != "<-" && op != "=" {
		return "", false
	}
	sym, ok := call.Args[0].Value.(*syntax.Sym)
	if !ok {
		return "", false
	}
	if _, ok := call.Args[1].Value.(*syntax.Function); !ok {
		return "", false
	}
	return sym.Name, true
}

// rOffset is the byte offset of an R position in src: line from 1, column
// from 1 in characters with a tab advancing to the next multiple of 8, 0 for
// the end of the line (syntax.Pos). It is clamped to src.
func rOffset(src string, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	end := strings.IndexByte(src[off:], '\n')
	if end < 0 {
		end = len(src) - off
	}
	if col <= 0 {
		return off + end
	}
	c := 0
	for i, r := range src[off : off+end] {
		if r == '\t' {
			c = (c/8 + 1) * 8
		} else {
			c++
		}
		if c >= col {
			return off + i
		}
	}
	return off + end
}
