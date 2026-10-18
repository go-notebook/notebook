package analysis

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-ruby-parser/parser"
	"github.com/go-ruby-parser/parser/ast"
)

// Languages of code cells.
const (
	Go   = "go"
	Ruby = "ruby"
)

// Source is a code cell's language and text.
type Source struct {
	Lang   string // Go or Ruby; "" is Go
	Source string
}

// AnalyzeRubyCell analyses the source of Ruby code cell number index, with
// go-ruby-parser, the front end of the Ruby that runs it.
//
// A Ruby cell's top level is a program evaluated in one binding shared by
// every Ruby cell, so the names it defines are of two kinds:
//
//   - the local variables it assigns at its top level (x = 1, a, b = …):
//     notebook variables, which Go cells can read too;
//   - the methods it defines at its top level with def (Funcs), and its
//     constants, classes and modules (Consts): visible to later Ruby cells
//     only, but defined by one cell, as any name is.
//
// Both are in Defines. The cell uses every name it reads that it has not
// assigned before: a local of the top level or of a block, a method called
// without a receiver, a constant. Names starting with an underscore stay
// private to the cell. The display is the value of the final statement when
// that statement is an expression rather than an assignment or a definition.
func AnalyzeRubyCell(index int, src string) *Cell {
	c := &Cell{Index: index, Source: src, Lang: Ruby, Imports: map[string]string{}}
	prog, err := parser.Parse(src)
	if err != nil {
		c.Errors = append(c.Errors, Error{Cell: index, Pos: rubyErrorPos(src, err.Error()), Msg: err.Error()})
		return c
	}
	w := &rubyWalker{cell: c, uses: map[string]bool{}, defined: map[string]bool{}}
	for _, n := range prog.Body {
		w.top(n)
	}
	for u := range w.uses {
		c.Uses = append(c.Uses, u)
	}
	sort.Strings(c.Uses)
	if k := len(prog.Body); k > 0 && rubyIsExpr(prog.Body[k-1]) {
		c.RubyDisplay = true
	}
	return c
}

// rubyErrorPos turns the front end's "parse error at line N: …" into an
// offset of the cell: the start of line N.
func rubyErrorPos(src, msg string) int {
	const pfx = "parse error at line "
	if !strings.HasPrefix(msg, pfx) {
		return 0
	}
	rest := msg[len(pfx):]
	n, err := strconv.Atoi(rest[:strings.IndexByte(rest+":", ':')])
	if err != nil {
		return 0
	}
	off := 0
	for line := 1; line < n; line++ {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return off
}

// rubyIsExpr reports whether a top-level statement is an expression whose
// value the cell shows.
func rubyIsExpr(n ast.Node) bool {
	switch n.(type) {
	case *ast.Assign, *ast.MultiAssign, *ast.OpAssign, *ast.ConstAssign, *ast.ScopedConstAssign,
		*ast.IvarAssign, *ast.GVarAssign, *ast.CVarAssign, *ast.MethodDef, *ast.ClassDef,
		*ast.ModuleDef, *ast.SingletonClassDef, *ast.Alias, *ast.Undef:
		return false
	}
	return true
}

type rubyWalker struct {
	cell    *Cell
	uses    map[string]bool
	defined map[string]bool // top-level locals assigned so far in this cell
}

// top walks one top-level statement: its assignments define, the rest uses.
func (w *rubyWalker) top(n ast.Node) {
	switch n := n.(type) {
	case *ast.Assign:
		w.expr(n.Value, nil)
		w.defineLocal(n.Name)
	case *ast.MultiAssign:
		for _, v := range n.Values {
			w.expr(v, nil)
		}
		for i, name := range n.Names {
			if n.Targets == nil || n.Targets[i] == nil {
				w.defineLocal(name)
			} else if _, local := n.Targets[i].(*ast.VarRef); local {
				w.defineLocal(name)
			} else {
				w.expr(n.Targets[i], nil)
			}
		}
	case *ast.OpAssign:
		w.expr(n.Value, nil)
		if !w.defined[n.Name] {
			w.use(n.Name)
			w.cell.Assigns = append(w.cell.Assigns, Use{Name: n.Name})
		}
	case *ast.MethodDef:
		if n.Recv == nil && !n.Singleton {
			w.defineOther(n.Name, &w.cell.Funcs)
		}
		w.body(n)
	case *ast.ConstAssign:
		w.expr(n.Value, nil)
		w.defineOther(n.Name, &w.cell.Consts)
	case *ast.ClassDef:
		w.defineOther(n.Name, &w.cell.Consts)
		if n.Super != "" {
			w.use(n.Super)
		}
		w.body(n)
	case *ast.ModuleDef:
		w.defineOther(n.Name, &w.cell.Consts)
		w.body(n)
	default:
		w.expr(n, nil)
	}
}

func (w *rubyWalker) defineLocal(name string) {
	if w.defined[name] {
		return
	}
	w.defined[name] = true
	if !Private(name) {
		w.cell.Defines = append(w.cell.Defines, name)
	}
}

func (w *rubyWalker) defineOther(name string, list *[]string) {
	if Private(name) || w.defined[name] {
		return
	}
	w.defined[name] = true
	w.cell.Defines = append(w.cell.Defines, name)
	*list = append(*list, name)
}

func (w *rubyWalker) use(name string) {
	if !w.defined[name] && !Private(name) {
		w.uses[name] = true
	}
}

// body walks the body of a def, class or module. It is a new scope, where
// the top level's locals are not visible, so its uses are the methods it calls
// without a receiver and the constants it names.
func (w *rubyWalker) body(n ast.Node) {
	inner := &rubyWalker{cell: w.cell, uses: map[string]bool{}, defined: map[string]bool{}}
	inner.fields(reflect.ValueOf(n).Elem(), map[string]bool{})
	for u := range inner.uses {
		w.use(u)
	}
}

// expr walks any node, by reflection over its fields — every Node, []Node and
// *Block it holds. locals are the names bound by the enclosing blocks or
// method (nil at the top level, where w.defined tracks them).
func (w *rubyWalker) expr(n ast.Node, locals map[string]bool) {
	switch n := n.(type) {
	case nil:
		return
	case *ast.VarRef:
		if !locals[n.Name] {
			w.use(n.Name)
		}
		return
	case *ast.ConstRef:
		w.use(n.Name)
		return
	case *ast.Call:
		if n.Recv == nil && !locals[n.Name] {
			w.use(n.Name)
		}
	case *ast.Assign:
		if locals != nil {
			locals[n.Name] = true
		} else {
			// Inside an if or a loop at the top level: still a top-level local.
			defer w.defineLocal(n.Name)
		}
	case *ast.MethodDef, *ast.ClassDef, *ast.ModuleDef:
		w.body(n)
		return
	}
	w.fields(reflect.ValueOf(n).Elem(), locals)
}

var (
	nodeType  = reflect.TypeFor[ast.Node]()
	blockType = reflect.TypeFor[*ast.Block]()
)

// fields walks the fields of a node's struct value.
func (w *rubyWalker) fields(v reflect.Value, locals map[string]bool) {
	if md, ok := v.Addr().Interface().(*ast.MethodDef); ok {
		// A method's parameters are its own locals.
		locals = map[string]bool{md.BlockParam: true, md.KwRest: true}
		for _, p := range md.Params {
			locals[p] = true
		}
		for _, p := range md.KwParams {
			locals[p.Name] = true
		}
	}
	for i := range v.NumField() {
		w.value(v.Field(i), locals)
	}
}

func (w *rubyWalker) value(f reflect.Value, locals map[string]bool) {
	switch {
	case f.Type() == blockType:
		if !f.IsNil() {
			w.block(f.Interface().(*ast.Block), locals)
		}
	case f.Type() == nodeType || f.Type().Implements(nodeType) && f.Kind() == reflect.Pointer:
		if !f.IsNil() {
			w.expr(f.Interface().(ast.Node), locals)
		}
	case f.Kind() == reflect.Slice:
		for j := range f.Len() {
			w.value(f.Index(j), locals)
		}
	case f.Kind() == reflect.Struct:
		for i := range f.NumField() {
			w.value(f.Field(i), locals)
		}
	}
}

// block walks a block: its parameters and block-locals shadow, in a scope that
// sees the enclosing one.
func (w *rubyWalker) block(b *ast.Block, locals map[string]bool) {
	scope := map[string]bool{b.BlockParam: true}
	for k, v := range locals {
		scope[k] = v
	}
	for _, p := range b.Params {
		scope[p] = true
	}
	for _, p := range b.Locals {
		scope[p] = true
	}
	for _, d := range b.Defaults {
		w.expr(d, scope)
	}
	for _, s := range b.Body {
		w.expr(s, scope)
	}
}
