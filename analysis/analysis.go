// Package analysis finds what each code cell of a notebook defines and uses,
// and orders the cells by those dependencies.
//
// A cell is Go with statements allowed at the top level, as in a REPL:
//
//	import "math"
//	func sq(x float64) float64 { return x * x }
//	y := sq(math.Pi)
//	y
//
// Its top-level func, type and import declarations are declarations of the
// notebook; everything else is a statement of the cell, run in order. The
// names a cell defines at its top level (by :=, var, const, func or type) are
// the notebook's variables: every cell can read them, and exactly one cell
// may define each — the rule that makes the execution order a function of
// the code, not of the order cells were run in. Names starting with an
// underscore, and err, stay local to their cell (see Private). A cell may not assign to a
// variable another cell defines, for the same reason: the change would be
// invisible to the dependency graph. (Mutation through a method or a slice
// element cannot be seen statically and is not refused, as in marimo.)
//
// The analysis is syntactic: it needs no type information and works on code
// that does not compile yet. A cell's final expression statement is its
// display value.
package analysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Chunk is a run of a cell's text that is either one top-level declaration
// (func, type or import) or a sequence of statements.
type Chunk struct {
	Decl       bool
	Start, End int // byte offsets in the cell's source
}

// Cell is the analysis of one code cell.
type Cell struct {
	Index  int    // position among the notebook's code cells
	Source string // the cell's source
	Lang   string // Go, or Ruby (see AnalyzeRubyCell)

	// Dynamic marks a cell whose names are not statically resolvable (see
	// Names).
	Dynamic bool

	Chunks []Chunk

	// Defines lists the notebook variables the cell defines: those of its
	// declarations, then those of its statements, each in order of
	// appearance. Funcs and Types are the subsets declared with func and
	// type. Imports maps a package name to its import path.
	Defines []string
	Funcs   []string
	Types   []string
	Imports map[string]string

	// Consts are the constants, classes and modules a Ruby cell defines.
	Consts []string

	// RubyDisplay reports that a Ruby cell ends in an expression, whose value
	// it shows. (A Go cell's is Display.)
	RubyDisplay bool

	// Uses lists the free identifiers the cell reads, sorted: notebook
	// variables, package names other cells import, builtins, and names
	// that are not defined anywhere (the compiler reports those).
	Uses []string

	// OwnPackages lists, sorted, the names of the packages the cell imports
	// itself and refers to.
	OwnPackages []string

	// Display is the cell's final expression statement, if any, and its
	// byte range in Source.
	Display                  ast.Expr
	DisplayStart, DisplayEnd int

	// Assigns lists the free identifiers the cell assigns to (=, op=, ++,
	// --), with the offset of each assignment.
	Assigns []Use

	// Errors are syntax errors, with offsets in Source.
	Errors []Error
}

// Use is a name and the offset in a cell's source where it appears.
type Use struct {
	Name string
	Pos  int
}

// Error is a problem found in a cell, at a byte offset of its source.
type Error struct {
	Cell int
	Pos  int
	Msg  string
}

func (e Error) Error() string { return fmt.Sprintf("cell %d: offset %d: %s", e.Cell, e.Pos, e.Msg) }

// AnalyzeCell analyses the source of code cell number index.
func AnalyzeCell(index int, src string) *Cell {
	c := &Cell{Index: index, Source: src, Lang: Go, Imports: map[string]string{}}
	c.Chunks = chunk(src)
	uses := map[string]bool{}
	var stmtMask []byte
	for _, ch := range c.Chunks {
		if ch.Decl {
			c.analyzeDecl(ch, uses)
		}
	}
	stmtMask = mask(src, c.Chunks, false)
	c.analyzeStmts(string(stmtMask), uses)
	for _, d := range c.Defines {
		delete(uses, d)
	}
	for name := range c.Imports {
		if uses[name] {
			c.OwnPackages = append(c.OwnPackages, name)
		}
		delete(uses, name)
	}
	sort.Strings(c.OwnPackages)
	for u := range uses {
		c.Uses = append(c.Uses, u)
	}
	sort.Strings(c.Uses)
	return c
}

// tok is a scanned token and its offset.
type tok struct {
	off int
	t   token.Token
}

// chunk splits src into declaration and statement chunks at the top-level
// statement boundaries the Go scanner's semicolon insertion gives.
func chunk(src string) []Chunk {
	fset := token.NewFileSet()
	f := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(f, []byte(src), nil, 0)
	var toks []tok
	for {
		pos, t, _ := s.Scan()
		if t == token.EOF {
			break
		}
		toks = append(toks, tok{f.Offset(pos), t})
	}
	var out []Chunk
	depth, start, decl := 0, -1, false
	for i, t := range toks {
		if start < 0 && depth == 0 && t.t != token.SEMICOLON {
			start = t.off
			decl = isDecl(toks[i:])
		}
		switch t.t {
		case token.LPAREN, token.LBRACE, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACE, token.RBRACK:
			depth--
		case token.SEMICOLON:
			if depth <= 0 && start >= 0 {
				end := t.off
				if t.off < len(src) && src[t.off] == ';' { // a written semicolon, not an inserted one
					end++
				}
				out = appendChunk(out, Chunk{decl, start, end})
				start, depth = -1, 0
			}
		}
	}
	if start >= 0 {
		out = appendChunk(out, Chunk{decl, start, len(src)})
	}
	return out
}

// isDecl reports whether a top-level item starting with these tokens is a
// declaration of the notebook: import, type, or a named func or method —
// not a function literal called as a statement, func(){...}().
func isDecl(t []tok) bool {
	switch t[0].t {
	case token.IMPORT, token.TYPE:
		return true
	case token.FUNC:
		if len(t) > 1 && t[1].t == token.IDENT {
			return true
		}
		// func (recv) Name(...: a method. Skip the receiver's parentheses.
		depth := 0
		for j := 1; j < len(t); j++ {
			switch t[j].t {
			case token.LPAREN:
				depth++
			case token.RPAREN:
				depth--
				if depth == 0 {
					return j+1 < len(t) && t[j+1].t == token.IDENT
				}
			}
			if depth == 0 {
				return false
			}
		}
	}
	return false
}

// appendChunk adds ch, merging it into the previous chunk when both are
// statements.
func appendChunk(out []Chunk, ch Chunk) []Chunk {
	if n := len(out); n > 0 && !ch.Decl && !out[n-1].Decl {
		out[n-1].End = ch.End
		return out
	}
	return append(out, ch)
}

// mask returns src with every chunk whose Decl differs from keep blanked out,
// newlines kept, so offsets and lines stay where they were.
func mask(src string, chunks []Chunk, keep bool) []byte {
	b := []byte(src)
	for _, ch := range chunks {
		if ch.Decl != keep {
			for i := ch.Start; i < ch.End; i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return b
}

// keepOnly returns src with everything but the range [start, end) blanked.
func keepOnly(src string, start, end int) string {
	b := []byte(src)
	for i := range b {
		if (i < start || i >= end) && b[i] != '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

const (
	declPrefix = "package p;"
	stmtPrefix = "package p;func _(){"
)

// analyzeDecl records what one declaration chunk defines and uses.
func (c *Cell) analyzeDecl(ch Chunk, uses map[string]bool) {
	src := declPrefix + keepOnly(c.Source, ch.Start, ch.End)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.AllErrors) // never nil after a package clause
	c.addErrors(err, len(declPrefix))
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, "`\"")
		name := path[strings.LastIndexByte(path, '/')+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name != "_" && name != "." {
			c.Imports[name] = path
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name != "_" && d.Name.Name != "init" {
				c.define(d.Name.Name)
				c.Funcs = append(c.Funcs, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name != "_" {
					c.define(ts.Name.Name)
					c.Types = append(c.Types, ts.Name.Name)
				}
			}
		}
	}
	for _, id := range f.Unresolved {
		uses[id.Name] = true
	}
}

// analyzeStmts records what the cell's statements define and use. masked is
// the cell's source with its declaration chunks blanked.
func (c *Cell) analyzeStmts(masked string, uses map[string]bool) {
	if strings.TrimSpace(masked) == "" {
		return
	}
	src := stmtPrefix + masked + "\n}"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.AllErrors)
	c.addErrors(err, len(stmtPrefix))
	// The prefix always parses as a function declaration with a body, even
	// when the cell's statements do not.
	fn := f.Decls[0].(*ast.FuncDecl)
	off := func(p token.Pos) int { return fset.Position(p).Offset - len(stmtPrefix) }
	for _, st := range fn.Body.List {
		switch st := st.(type) {
		case *ast.AssignStmt:
			if st.Tok == token.DEFINE {
				for _, l := range st.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
						c.define(id.Name)
					}
				}
			}
		case *ast.DeclStmt:
			// var and const: a top-level type is a declaration chunk.
			for _, s := range st.Decl.(*ast.GenDecl).Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if n.Name != "_" {
						c.define(n.Name)
					}
				}
			}
		}
	}
	if n := len(fn.Body.List); n > 0 {
		if es, ok := fn.Body.List[n-1].(*ast.ExprStmt); ok {
			c.Display = es.X
			c.DisplayStart, c.DisplayEnd = off(es.Pos()), off(es.End())
		}
	}
	for _, id := range f.Unresolved {
		uses[id.Name] = true
	}
	// Assignments to free names: candidates for writing another cell's variable.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				for _, l := range n.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Obj == nil && id.Name != "_" {
						c.Assigns = append(c.Assigns, Use{id.Name, off(id.Pos())})
					}
				}
			}
		case *ast.IncDecStmt:
			if id, ok := n.X.(*ast.Ident); ok && id.Obj == nil {
				c.Assigns = append(c.Assigns, Use{id.Name, off(id.Pos())})
			}
		}
		return true
	})
}

// Private reports whether a name defined at a cell's top level stays local
// to the cell instead of becoming a notebook variable: a name starting with
// an underscore (marimo's convention), and err, which nearly every cell
// declares and none means to share.
func Private(name string) bool {
	return strings.HasPrefix(name, "_") || name == "err"
}

// define records a name the cell defines, once. Private names are not
// notebook variables.
func (c *Cell) define(name string) {
	if Private(name) {
		return
	}
	for _, d := range c.Defines {
		if d == name {
			return
		}
	}
	c.Defines = append(c.Defines, name)
}

// addErrors records parse errors, translating positions back to the cell.
func (c *Cell) addErrors(err error, prefix int) {
	list, ok := err.(scanner.ErrorList)
	if !ok {
		return
	}
	for _, e := range list {
		c.Errors = append(c.Errors, Error{Cell: c.Index, Pos: max(0, e.Pos.Offset-prefix), Msg: e.Msg})
	}
}
