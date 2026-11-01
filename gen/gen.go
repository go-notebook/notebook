// Package gen turns a notebook's code cells into a Go program: the worker
// the kernel compiles and drives.
//
// Each cell becomes a function. The names a cell defines at its top level
// become package-level variables — so every cell, and every function, can
// read them, and their values outlive the call — which needs their types:
// gen type-checks the cells one by one in dependency order to learn them.
// The cells' own text is kept, edited in place (":=" becomes "= ", a final
// expression is wrapped in a call that shows it), under //line directives
// that point back into the notebook, so the compiler's errors and a panic's
// stack name the notebook's lines.
package gen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// RuntimePath is the import path of the package the program links.
const RuntimePath = "github.com/go-notebook/notebook/runtime"

// Code is one code cell: its source and the 1-based line of the notebook
// where that source starts.
type Code struct {
	Source string
	Line   int
	// Lang is the cell's language: "go" (or ""), "ruby" or "julia".
	Lang string
}

// Var is a notebook variable as the program declares it.
type Var struct {
	Name string
	Type string // Go syntax, qualified with the program's import names
	Cell int
	// Ruby marks a Ruby cell's variable. Its Go holder is named Ident when
	// its name is no Go identifier (len, type): Go cells do not see it.
	Ruby  bool
	Ident string
}

// ident is the Go identifier of the variable's holder.
func (v Var) ident() string {
	if v.Ident != "" {
		return v.Ident
	}
	return v.Name
}

// Diag is a problem in the notebook, at a cell and a position in it.
type Diag struct {
	Cell      int // -1 when it is not inside a cell
	Line, Col int // in the notebook
	Msg       string
}

func (d Diag) String() string {
	if d.Line == 0 {
		return fmt.Sprintf("cell %d: %s", d.Cell, d.Msg)
	}
	return fmt.Sprintf("%d:%d: cell %d: %s", d.Line, d.Col, d.Cell, d.Msg)
}

// Program is a generated worker.
type Program struct {
	Source []byte
	Vars   []Var
	Graph  *analysis.Graph
	// Imports maps every package the program imports (but the runtime) to
	// the name it is imported under.
	Imports map[string]string
	// Diags are the notebook's errors. A program with diagnostics is still
	// returned, but will not compile.
	Diags []Diag
	// Deferred maps a cell that cannot be compiled yet to why: it reads a
	// Ruby cell's variable whose Go type is known only once that cell has
	// run (see Options.RubyTypes). It is emitted as a stub that says so.
	Deferred map[int]string
}

// Generate builds the program for a notebook's code cells. path is the
// notebook's file name, used in //line directives; imp resolves the
// packages the cells import.
func Generate(path string, cells []Code, imp types.Importer) *Program {
	return GenerateWith(path, cells, imp, Options{})
}

// GenerateWith is Generate with options.
func GenerateWith(path string, cells []Code, imp types.Importer, opt Options) *Program {
	srcs := make([]analysis.Source, len(cells))
	for i, c := range cells {
		srcs[i] = analysis.Source{Lang: c.Lang, Source: c.Source}
	}
	g := &generator{path: path, cells: cells, imp: imp, opt: opt, unknown: map[string]bool{}, blockedBy: map[int]string{},
		prog: &Program{Graph: analysis.AnalyzeSources(srcs), Deferred: map[int]string{}}}
	g.prog.Imports = map[string]string{}
	for _, e := range g.prog.Graph.Errors {
		g.diagAt(e.Cell, e.Pos, e.Msg)
	}
	if len(g.prog.Diags) > 0 {
		return g.prog
	}
	g.parseCells()
	g.collectImports()
	if len(g.prog.Diags) > 0 {
		return g.prog
	}
	for _, i := range g.prog.Graph.Order {
		switch b := g.backendOf(i); {
		case g.checkOnly(i):
			g.prog.Deferred[i] = analysis.RunOf(g.cells[i].Lang).NotRun
		case b != nil:
			b.infer(g, i)
		case g.waits(i):
			g.deferCell(i)
		default:
			g.infer(i)
		}
	}
	if len(g.prog.Diags) > 0 {
		return g.prog
	}
	g.prog.Source = g.emit(true)
	g.check()
	return g.prog
}

type generator struct {
	path  string
	cells []Code
	imp   types.Importer
	opt   Options
	prog  *Program
	// unknown are the Ruby variables whose Go type is not known yet.
	unknown map[string]bool
	// blockedBy maps the deferred cells that read a check-only cell, directly
	// or through another deferred cell, to that cell's language.
	blockedBy map[int]string

	parsed []*cellAST
	// names maps an import path to its name in the program; byName is the
	// reverse.
	names, byName map[string]string
	synthetic     int
	done          []int // cells whose variables are known, in order
}

// cellAST is a cell parsed for editing.
type cellAST struct {
	masked string     // the source with its declaration chunks blanked
	stmts  []ast.Stmt // top-level statements, positions in fset
	fset   *token.FileSet
	file   *token.File
	decls  []analysis.Chunk
	refs   map[string]bool // every free identifier, imports included

	display     ast.Expr
	displayVoid bool
	// displayResult: the display is a (value, error) pair, shown with
	// _k.Result — the value, or the error as the cell's failure.
	displayResult bool
	locals        map[string]string // private names a mixed := declares, with their types
}

const stmtPrefix = "package p;func _(){"

// off returns a cell-source offset for a position in a cellAST.
func (c *cellAST) off(p token.Pos) int { return c.fset.Position(p).Offset - len(stmtPrefix) }

func (g *generator) parseCells() {
	for i, code := range g.cells {
		ac := g.prog.Graph.Cells[i]
		c := &cellAST{decls: ac.Chunks, refs: map[string]bool{}, locals: map[string]string{}, display: ac.Display}
		if g.backendOf(i) != nil || g.checkOnly(i) {
			g.parsed = append(g.parsed, c)
			continue
		}
		c.masked = string(maskDecls(code.Source, ac.Chunks))
		c.fset = token.NewFileSet()
		f, _ := parser.ParseFile(c.fset, "", stmtPrefix+c.masked+"\n}", 0)
		c.file = c.fset.File(f.Pos())
		if fn, ok := f.Decls[0].(*ast.FuncDecl); ok {
			c.stmts = fn.Body.List
		}
		for _, id := range f.Unresolved {
			c.refs[id.Name] = true
		}
		for _, ch := range ac.Chunks {
			if ch.Decl {
				df, _ := parser.ParseFile(token.NewFileSet(), "", "package p;"+code.Source[ch.Start:ch.End], 0)
				for _, id := range df.Unresolved {
					c.refs[id.Name] = true
				}
			}
		}
		g.parsed = append(g.parsed, c)
	}
}

// maskDecls blanks the declaration chunks of src, keeping newlines.
func maskDecls(src string, chunks []analysis.Chunk) []byte {
	b := []byte(src)
	for _, ch := range chunks {
		if ch.Decl {
			for i := ch.Start; i < ch.End; i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return b
}

// collectImports gathers the cells' imports, refusing one name for two
// paths, and keeps those some cell refers to.
func (g *generator) collectImports() {
	g.names, g.byName = map[string]string{}, map[string]string{}
	for _, c := range g.prog.Graph.Cells {
		names := make([]string, 0, len(c.Imports))
		for n := range c.Imports {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			path := c.Imports[name]
			if p, ok := g.byName[name]; ok && p != path {
				g.diagAt(c.Index, 0, fmt.Sprintf("%s names both %q and %q", name, p, path))
				continue
			}
			used := false
			for _, pc := range g.parsed {
				used = used || pc.refs[name]
			}
			if used {
				g.byName[name] = path
				g.names[path] = name
			}
		}
	}
}

// qualifier names packages in type strings, importing those no cell imports
// under a generated name.
func (g *generator) qualifier(p *types.Package) string {
	if p.Path() == "main" {
		return ""
	}
	if n, ok := g.names[p.Path()]; ok {
		return n
	}
	g.synthetic++
	n := "_nbimp" + strconv.Itoa(g.synthetic)
	g.names[p.Path()] = n
	g.byName[n] = p.Path()
	return n
}

// infer type-checks cell i with the variables of the cells before it known,
// and records the types of the variables it defines and of the private
// names it must declare ahead of a mixed := statement.
func (g *generator) infer(i int) {
	src := g.emitCheck(i)
	fset := token.NewFileSet()
	// The cells parsed; what they assemble into does too. Should it not, the
	// final check reports it.
	f, _ := parser.ParseFile(fset, "check.go", src, parser.SkipObjectResolution)
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{},
		Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: g.imp, Error: func(error) {}}
	_, _ = conf.Check("main", fset, []*ast.File{f}, info)

	var body []ast.Stmt
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == cellFunc(i) {
			body = fd.Body.List
		}
	}
	obj := func(id *ast.Ident) types.Object {
		if o := info.Defs[id]; o != nil {
			return o
		}
		return info.Uses[id]
	}
	c := g.parsed[i]
	global := map[string]bool{}
	for _, n := range g.prog.Graph.Cells[i].Defines {
		global[n] = true
	}
	record := func(id *ast.Ident) {
		o := obj(id)
		if o == nil || id.Name == "_" {
			return
		}
		ts := g.typeString(i, id.Name, o.Type())
		if ts == "" {
			return
		}
		if global[id.Name] {
			for _, v := range g.prog.Vars {
				if v.Name == id.Name {
					return
				}
			}
			g.prog.Vars = append(g.prog.Vars, Var{Name: id.Name, Type: ts, Cell: i})
			return
		}
		if _, ok := c.locals[id.Name]; !ok {
			c.locals[id.Name] = ts
		}
	}
	for _, st := range body {
		switch st := st.(type) {
		case *ast.AssignStmt:
			if st.Tok != token.DEFINE {
				continue
			}
			mixed := false
			for _, l := range st.Lhs {
				if id, ok := l.(*ast.Ident); ok && global[id.Name] {
					mixed = true
				}
			}
			for _, l := range st.Lhs {
				if id, ok := l.(*ast.Ident); ok && (global[id.Name] || mixed) {
					record(id)
				}
			}
		case *ast.DeclStmt:
			gd, ok := st.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, s := range gd.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if global[n.Name] {
						record(n)
					}
				}
			}
		}
	}
	if n := len(body); n > 0 && c.display != nil {
		if es, ok := body[n-1].(*ast.ExprStmt); ok {
			if tv, ok := info.Types[es.X]; ok {
				// A call returning (value, error) shows the value, and fails
				// the cell with a non-nil error. Other calls with several
				// results, and (int, error) — the byte counts of
				// fmt.Println and Write —, are run for their effect: showing
				// (25, <nil>) would only be noise.
				tup, multi := tv.Type.(*types.Tuple)
				c.displayResult = multi && valueAndError(tup)
				c.displayVoid = tv.IsVoid() || tv.IsType() || (multi && !c.displayResult)
			} else {
				// Untyped (an error the final check reports, or a builtin
				// call without a value): do not wrap it.
				c.displayVoid = true
			}
		}
	}
	g.done = append(g.done, i)
}

// valueAndError reports whether a result tuple is (T, error) with T not
// int.
func valueAndError(t *types.Tuple) bool {
	if t.Len() != 2 || !types.Identical(t.At(1).Type(), types.Universe.Lookup("error").Type()) {
		return false
	}
	return !types.Identical(t.At(0).Type(), types.Typ[types.Int])
}

// typeString writes t for the program, or reports why it cannot.
func (g *generator) typeString(cell int, name string, t types.Type) string {
	if t == types.Typ[types.Invalid] {
		// The cell does not type-check; declaring the name lets the final
		// check report the cell's own error rather than a second one.
		return "any"
	}
	if why := unnameable(t); why != "" {
		g.diagAt(cell, 0, fmt.Sprintf("%s cannot be shared between cells: its type %s; make it private to the cell by naming it _%s", name, why, name))
		return ""
	}
	return types.TypeString(t, g.qualifier)
}

// unnameable says why t cannot be written outside its package, or "". A
// named type is not looked into: its name is what the program writes.
func unnameable(t types.Type) string {
	switch t := t.(type) {
	case *types.Named:
		o := t.Obj()
		if o.Pkg() != nil && o.Pkg().Path() != "main" && !o.Exported() {
			return fmt.Sprintf("is %s.%s, which is not exported", o.Pkg().Name(), o.Name())
		}
		if ta := t.TypeArgs(); ta != nil {
			for i := range ta.Len() {
				if why := unnameable(ta.At(i)); why != "" {
					return why
				}
			}
		}
	case *types.Pointer:
		return unnameable(t.Elem())
	case *types.Slice:
		return unnameable(t.Elem())
	case *types.Array:
		return unnameable(t.Elem())
	case *types.Map:
		if why := unnameable(t.Key()); why != "" {
			return why
		}
		return unnameable(t.Elem())
	case *types.Chan:
		return unnameable(t.Elem())
	case *types.Signature:
		for _, tup := range []*types.Tuple{t.Params(), t.Results()} {
			for i := range tup.Len() {
				if why := unnameable(tup.At(i).Type()); why != "" {
					return why
				}
			}
		}
	case *types.Struct:
		for i := range t.NumFields() {
			if why := unnameable(t.Field(i).Type()); why != "" {
				return why
			}
		}
	}
	return ""
}

func cellFunc(i int) string { return "_nbcell" + strconv.Itoa(i) }

// emitCheck writes the program used to type-check cell i: the variables of
// the cells done so far, the declarations of those cells and of cell i, and
// cell i's statements unedited.
func (g *generator) emitCheck(i int) string {
	var b strings.Builder
	b.WriteString("package main\n\n")
	g.writeImports(&b, false)
	g.writeVars(&b)
	cells := append(append([]int(nil), g.done...), i)
	for _, k := range cells {
		g.writeDecls(&b, k)
		g.writeConsts(&b, k)
	}
	fmt.Fprintf(&b, "func %s() {\n%s\n}\n", cellFunc(i), g.parsed[i].masked)
	return b.String()
}

// emit writes the final program. With lines, //line directives map it back
// to the notebook.
func (g *generator) emit(lines bool) []byte {
	var b strings.Builder
	b.WriteString("// Code generated by go-notebook; DO NOT EDIT.\n\npackage main\n\n")
	g.writeImports(&b, true)
	g.writeVars(&b)
	for _, u := range g.runtimes() {
		b.WriteString(u.prelude)
	}
	for k := range g.cells {
		if g.prog.Deferred[k] != "" {
			continue // its declarations may read what is not known yet
		}
		g.writeDecls(&b, k)
		g.writeConsts(&b, k)
	}
	for k := range g.cells {
		switch bk := g.backendOf(k); {
		case g.prog.Deferred[k] != "":
			g.writeDeferred(&b, k)
		case bk != nil:
			bk.write(g, &b, k)
		default:
			g.writeCell(&b, k, lines)
		}
	}
	b.WriteString("func main() {\n\t_nb.Serve(_nb.Program{\n\t\tCells: []func(*_nb.Cell){")
	for k := range g.cells {
		if k > 0 {
			b.WriteString(", ")
		}
		b.WriteString(cellFunc(k))
	}
	b.WriteString("},\n\t\tVars: map[string]any{")
	for i, v := range g.prog.Vars {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q: &%s", v.Name, v.ident())
	}
	b.WriteString("},\n\t})\n}\n")
	for path, name := range g.names {
		g.prog.Imports[path] = name
	}
	return []byte(b.String())
}

func (g *generator) writeImports(b *strings.Builder, runtime bool) {
	paths := make([]string, 0, len(g.names))
	for p := range g.names {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	b.WriteString("import (\n")
	if runtime {
		fmt.Fprintf(b, "\t_nb %q\n", RuntimePath)
		for _, u := range g.runtimes() {
			fmt.Fprintf(b, "\t%s %q\n", u.name, u.path)
		}
	}
	for _, p := range paths {
		name := g.names[p]
		if runtime && g.onlyDeferred(name) {
			name = "_" // only deferred cells, whose code is not emitted, use it
		}
		fmt.Fprintf(b, "\t%s %q\n", name, p)
	}
	b.WriteString(")\n\n")
}

func (g *generator) writeVars(b *strings.Builder) {
	if len(g.prog.Vars) == 0 {
		return
	}
	b.WriteString("var (\n")
	for _, v := range g.prog.Vars {
		fmt.Fprintf(b, "\t%s %s\n", v.ident(), v.Type)
	}
	b.WriteString(")\n\n")
}

// lineDirective points the next character at an offset of cell k.
func (g *generator) lineDirective(k, off int) string {
	src := g.cells[k].Source
	line := g.cells[k].Line + strings.Count(src[:off], "\n")
	col := off - (strings.LastIndexByte(src[:off], '\n') + 1) + 1
	return fmt.Sprintf("/*line %s:%d:%d*/", g.path, line, col)
}

// writeDecls writes cell k's declarations (other than imports).
func (g *generator) writeDecls(b *strings.Builder, k int) {
	src := g.cells[k].Source
	for _, ch := range g.parsed[k].decls {
		if !ch.Decl || strings.HasPrefix(strings.TrimSpace(src[ch.Start:ch.End]), "import") {
			continue
		}
		b.WriteString(g.lineDirective(k, ch.Start))
		b.WriteString(src[ch.Start:ch.End])
		b.WriteString("\n\n")
	}
}

// writeConsts writes cell k's top-level constants that are notebook
// variables, at package level.
func (g *generator) writeConsts(b *strings.Builder, k int) {
	c := g.parsed[k]
	for _, st := range c.stmts {
		if gd := constDecl(st); gd != nil && g.isGlobalConst(k, gd) {
			start, end := c.off(gd.Pos()), c.off(gd.End())
			b.WriteString(g.lineDirective(k, start))
			b.WriteString(g.cells[k].Source[start:end])
			b.WriteString("\n\n")
		}
	}
}

// constDecl returns st's const declaration, if it is one.
func constDecl(st ast.Stmt) *ast.GenDecl {
	if ds, ok := st.(*ast.DeclStmt); ok {
		if gd, ok := ds.Decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			return gd
		}
	}
	return nil
}

// isGlobalConst reports whether a const declaration defines notebook
// variables (any non-private name).
func (g *generator) isGlobalConst(k int, gd *ast.GenDecl) bool {
	for _, s := range gd.Specs {
		for _, n := range s.(*ast.ValueSpec).Names {
			if !analysis.Private(n.Name) && n.Name != "_" {
				return true
			}
		}
	}
	return false
}

// edit replaces [start, end) of a cell's statement text.
type edit struct {
	start, end int
	text       string
}

// writeCell writes cell k as a function.
func (g *generator) writeCell(b *strings.Builder, k int, lines bool) {
	c := g.parsed[k]
	global := map[string]bool{}
	for _, n := range g.prog.Graph.Cells[k].Defines {
		global[n] = true
	}
	var edits []edit
	declared := map[string]bool{}
	for _, st := range c.stmts {
		switch st := st.(type) {
		case *ast.AssignStmt:
			if st.Tok != token.DEFINE {
				continue
			}
			mixed := false
			for _, l := range st.Lhs {
				if id, ok := l.(*ast.Ident); ok && global[id.Name] {
					mixed = true
				}
			}
			if !mixed {
				for _, l := range st.Lhs {
					if id, ok := l.(*ast.Ident); ok {
						declared[id.Name] = true
					}
				}
				continue
			}
			var pre strings.Builder
			for _, l := range st.Lhs {
				id, ok := l.(*ast.Ident)
				if !ok || global[id.Name] || declared[id.Name] || id.Name == "_" {
					continue
				}
				if t, ok := c.locals[id.Name]; ok {
					fmt.Fprintf(&pre, "var %s %s; ", id.Name, t)
					declared[id.Name] = true
				}
			}
			if pre.Len() > 0 {
				p := c.off(st.Pos())
				edits = append(edits, edit{p, p, pre.String()})
			}
			p := c.off(st.TokPos)
			edits = append(edits, edit{p, p + 2, "= "})
		case *ast.DeclStmt:
			gd := st.Decl.(*ast.GenDecl)
			start, end := c.off(gd.Pos()), c.off(gd.End())
			orig := g.cells[k].Source[start:end]
			pad := strings.Repeat("\n", strings.Count(orig, "\n"))
			switch {
			case gd.Tok == token.CONST && g.isGlobalConst(k, gd):
				edits = append(edits, edit{start, end, pad})
			case gd.Tok == token.VAR && g.isGlobalVar(gd, global):
				edits = append(edits, edit{start, end, g.varAssign(k, gd) + pad})
			}
		}
	}
	if n := len(c.stmts); n > 0 && !c.displayVoid {
		if es, ok := c.stmts[n-1].(*ast.ExprStmt); ok {
			s, e := c.off(es.Pos()), c.off(es.End())
			show := "_k.Show("
			if c.displayResult {
				show = "_k.Result("
			}
			edits = append(edits, edit{s, s, show}, edit{e, e, ")"})
		}
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	text := c.masked
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		text = text[:e.start] + e.text + text[e.end:]
	}
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n", cellFunc(k))
	if lines {
		b.WriteString(g.lineDirective(k, 0))
	}
	b.WriteString(text)
	b.WriteString("\n")
	// A private name a cell defines but does not use must not stop the
	// build, as it would in ordinary Go: a notebook cell is often unfinished.
	for _, st := range c.stmts {
		if as, ok := st.(*ast.AssignStmt); ok && as.Tok == token.DEFINE {
			for _, l := range as.Lhs {
				if id, ok := l.(*ast.Ident); ok && !global[id.Name] && id.Name != "_" {
					fmt.Fprintf(b, "\t_ = %s\n", id.Name)
				}
			}
		}
	}
	b.WriteString("\t_ = _k\n}\n\n")
}

// isGlobalVar reports whether a var declaration defines notebook variables.
func (g *generator) isGlobalVar(gd *ast.GenDecl, global map[string]bool) bool {
	for _, s := range gd.Specs {
		for _, n := range s.(*ast.ValueSpec).Names {
			if global[n.Name] {
				return true
			}
		}
	}
	return false
}

// varAssign rewrites a top-level var declaration of notebook variables as
// assignments: var x, y = 1, 2 becomes x, y = 1, 2, and var x T becomes
// x = *new(T), so that running the cell again resets it.
func (g *generator) varAssign(k int, gd *ast.GenDecl) string {
	c := g.parsed[k]
	src := g.cells[k].Source
	var parts []string
	for _, s := range gd.Specs {
		vs := s.(*ast.ValueSpec)
		var names []string
		for _, n := range vs.Names {
			names = append(names, n.Name)
		}
		var vals []string
		if len(vs.Values) > 0 {
			for _, v := range vs.Values {
				vals = append(vals, src[c.off(v.Pos()):c.off(v.End())])
			}
		} else {
			typ := src[c.off(vs.Type.Pos()):c.off(vs.Type.End())]
			for range vs.Names {
				vals = append(vals, "*new("+typ+")")
			}
		}
		parts = append(parts, strings.Join(names, ", ")+" = "+strings.Join(vals, ", "))
	}
	return strings.Join(parts, "; ")
}

// check type-checks the final program and reports its errors at notebook
// positions.
func (g *generator) check() {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, g.path, g.prog.Source, parser.SkipObjectResolution)
	if err != nil {
		g.prog.Diags = append(g.prog.Diags, Diag{Cell: -1, Msg: "internal: " + err.Error()})
		return
	}
	conf := types.Config{Importer: g.imp, Error: func(err error) {
		te := err.(types.Error)
		p := te.Fset.Position(te.Pos)
		g.prog.Diags = append(g.prog.Diags, Diag{Cell: g.cellAtLine(p.Line, p.Filename), Line: p.Line, Col: p.Column, Msg: te.Msg})
	}}
	_, _ = conf.Check("main", fset, []*ast.File{f}, nil)
}

// cellAtLine returns the code cell holding a notebook line, or -1.
func (g *generator) cellAtLine(line int, file string) int {
	if file != g.path {
		return -1
	}
	for k, c := range g.cells {
		if line >= c.Line && line < c.Line+strings.Count(c.Source, "\n")+1 {
			return k
		}
	}
	return -1
}

// diagAt records a diagnostic at an offset of cell k.
func (g *generator) diagAt(k, off int, msg string) {
	src := g.cells[k].Source
	off = min(max(off, 0), len(src))
	line := g.cells[k].Line + strings.Count(src[:off], "\n")
	col := off - (strings.LastIndexByte(src[:off], '\n') + 1) + 1
	g.prog.Diags = append(g.prog.Diags, Diag{Cell: k, Line: line, Col: col, Msg: msg})
}
