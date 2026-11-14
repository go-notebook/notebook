package parsers

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-embedded-fortran/fortran/sema"
	"github.com/go-embedded-fortran/fortran/syntax"
	"github.com/go-notebook/notebook/analysis"
)

// fortranMaxBytes bounds the source of a Fortran cell: a notebook may come
// from anyone. The front end bounds nesting, continuation lines, symbols and
// the errors it reports itself (fortranSession); INCLUDE is refused, as the
// session gives it no file system.
const fortranMaxBytes = 1 << 20

// fortranSession is the session whose cells the notebook analyses in order
// (bib/fortran.md §7): implicit typing off, the front end's default limits
// with the nesting of scopes and folding at 200, as the parser's.
var fortranSession = sema.NewSession(sema.SessionOptions{Limits: func() sema.Limits {
	l := sema.DefaultLimits()
	l.MaxDepth = 200
	return l
}()})

// fortranAnalyze replaces the front end in the tests; nil means the session.
var fortranAnalyze func(s *sema.Session, state any, index int, src string) (*sema.CellInfo, any)

// analyzeFortranCell analyses Fortran cell index in the session state (nil, or
// what the previous Fortran cell returned) with go-embedded-fortran. What a
// cell defines is Defines: the variables, which cross to the other languages;
// its procedures are also in Funcs, and its parameters, derived types and
// modules in Consts, which stay Fortran's. Uses are the names of earlier
// Fortran cells it reads. A cell with errors defines nothing and returns state
// as it was.
func analyzeFortranCell(index int, src string, state any) (*analysis.Cell, any) {
	c := &analysis.Cell{Index: index, Source: src, Lang: analysis.Fortran, Imports: map[string]string{}}
	if len(src) > fortranMaxBytes {
		c.Errors = append(c.Errors, analysis.Error{Cell: index, Msg: fmt.Sprintf("the cell is %d bytes; the limit is %d", len(src), fortranMaxBytes)})
		return c, state
	}
	ci, next, err := analyzeFortranSafe(state, index, src)
	if err != nil {
		c.Errors = append(c.Errors, analysis.Error{Cell: index, Msg: err.Error()})
		return c, state
	}
	for _, e := range ci.Errors {
		c.Errors = append(c.Errors, analysis.Error{Cell: index, Pos: fortranOffset(src, e.Pos), Msg: e.Msg})
	}
	if len(c.Errors) > 0 {
		return c, state
	}
	add := func(dst *[]string, names []string) {
		for _, n := range names {
			if !slices.Contains(c.Defines, n) {
				c.Defines = append(c.Defines, n)
			}
			*dst = append(*dst, n)
		}
	}
	for _, n := range ci.Defines {
		c.Defines = append(c.Defines, n)
		// A named constant is a parameter: it does not cross (bib §7).
		if s := ci.Vars[n]; s != nil && s.Kind == sema.SymParam {
			c.Consts = append(c.Consts, n)
		}
	}
	add(&c.Funcs, ci.Procedures)
	add(&c.Consts, ci.Types)
	add(&c.Consts, ci.Modules)
	c.Uses = append([]string(nil), ci.Uses...)
	slices.Sort(c.Uses)
	return c, next
}

// fortranOffset is the byte offset of a front end position (line and column,
// both 1-based, the column in bytes) in src, clamped to the cell; a position
// without a line is the start.
func fortranOffset(src string, p syntax.Position) int {
	if p.Line <= 1 {
		return min(max(p.Col-1, 0), len(src))
	}
	off := 0
	for l := 1; l < p.Line; l++ {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return min(off+max(p.Col-1, 0), len(src))
}

// analyzeFortranSafe runs the front end; a panic of it is an error of the
// cell, never a crash of the notebook (the fuzz test looks for them).
func analyzeFortranSafe(state any, index int, src string) (ci *sema.CellInfo, next any, err error) {
	defer func() {
		if r := recover(); r != nil {
			ci, next, err = nil, state, errors.New("the Fortran parser failed on this cell; please report it")
		}
	}()
	analyze := fortranAnalyze
	if analyze == nil {
		analyze = func(s *sema.Session, state any, index int, src string) (*sema.CellInfo, any) {
			if state == nil {
				state = s.Begin()
			}
			return s.AnalyzeNext(state, index, src)
		}
	}
	ci, next = analyze(fortranSession, state, index, src)
	return ci, next, nil
}

// analyzeFortranAlone is a cell analysed as the first Fortran cell of a
// notebook.
func analyzeFortranAlone(index int, src string) *analysis.Cell {
	c, _ := analyzeFortranCell(index, src, nil)
	return c
}
