package analysis

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/go-embedded-julia/julia/expr"
	"github.com/go-embedded-julia/julia/syntax"
)

// juliaLimits bound the parse of a Julia cell: a notebook may come from
// anyone, and the parser must neither run long nor nest without end on it.
var juliaLimits = syntax.Limits{MaxBytes: 4 << 20, MaxDepth: 1000}

// AnalyzeJuliaCell analyses the source of Julia code cell number index, with
// go-embedded-julia's parser (a port of JuliaSyntax, the parser of Julia
// 1.12, which gives the Expr trees julia itself gives).
//
// The names follow Julia's scope rules ("Scope of Variables" in the Julia
// manual, 1.12) as they apply to the top level of a notebook, which Julia
// treats as an interactive context:
//
//   - the top level, and if, begin and other blocks in it, are the global
//     scope: an assignment there (x = 1, a, b = …, x::Int = 1, global x = 1)
//     defines a global, a notebook variable;
//   - function, macro, struct, let, do, -> and comprehension or generator
//     bodies are hard local scopes: an assignment there makes a local, and the
//     names read there that no enclosing scope binds are globals the cell uses;
//   - for, while and try bodies at the top level are soft scopes: an
//     assignment there to a name that is a global already (defined earlier in
//     the cell, or by another cell) assigns that global, as in the REPL and
//     IJulia; otherwise it makes a local of the loop.
//
// Functions (function f, f(x) = …) and macros (@m) are in Funcs; constants,
// structs, abstract and primitive types, modules and the names using and
// import bind are in Consts; all are in Defines. Names starting with an
// underscore stay private to the cell, as in the other languages.
func AnalyzeJuliaCell(index int, src string) *Cell {
	c := &Cell{Index: index, Source: src, Lang: Julia, Imports: map[string]string{}}
	tree, err := parseJulia(src)
	if err != nil {
		c.Errors = append(c.Errors, Error{Cell: index, Msg: juliaLimitMessage(err)})
		return c
	}
	top, ok := tree.(*expr.Expr) // ParseAllLimits returns (toplevel …)
	if !ok {
		top = expr.New("toplevel")
	}
	for _, a := range top.Args {
		if e, ok := a.(*expr.Expr); ok && (e.Head == "error" || e.Head == "incomplete") {
			pos, msg := juliaError(src, e)
			c.Errors = append(c.Errors, Error{Cell: index, Pos: pos, Msg: msg})
			return c
		}
	}
	w := &juliaWalker{cell: c, uses: map[string]bool{}, defined: map[string]bool{}}
	if err := w.walkTop(top.Args); err != nil {
		c.Defines, c.Funcs, c.Consts, c.Assigns = nil, nil, nil, nil
		c.Errors = append(c.Errors, Error{Cell: index, Msg: err.Error()})
		return c
	}
	for u := range w.uses {
		c.Uses = append(c.Uses, u)
	}
	sort.Strings(c.Uses)
	return c
}

// walkTop walks the top-level statements. A tree of a shape the walker does
// not expect is an error of the cell, never a crash of the notebook: the
// fuzz test looks for them.
func (w *juliaWalker) walkTop(stmts []any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("the names of this Julia cell could not be worked out (an unexpected tree); please report it")
		}
	}()
	for _, a := range stmts {
		w.stmt(a, nil)
	}
	return nil
}

// juliaParse is the parser, a seam for the tests.
var juliaParse = syntax.ParseAllLimits

// parseJulia parses a cell; a panic of the parser is an error of the cell.
func parseJulia(src string) (tree any, err error) {
	defer func() {
		if r := recover(); r != nil {
			tree, err = nil, errors.New("the Julia parser failed on this cell")
		}
	}()
	return juliaParse([]byte(src), "cell.jl", juliaLimits)
}

// juliaLimitMessage says why a cell could not be parsed at all.
func juliaLimitMessage(err error) string {
	switch {
	case errors.Is(err, syntax.ErrLimit):
		return "the Julia cell is too large or too deeply nested to parse (" + err.Error() + ")"
	case errors.Is(err, syntax.ErrParserFailed):
		return "the Julia parser cannot parse this cell (julia would fall back to its older parser here)"
	}
	return err.Error()
}

// juliaError turns julia's ParseError of an (error …) or (incomplete …)
// node into an offset of the cell and julia's messages:
//
//	ParseError:
//	# Error @ cell.jl:2:8
//	x = (1,
//	#      └ ── Expected `)` or `,`
func juliaError(src string, e *expr.Expr) (int, string) {
	text := ""
	for _, a := range e.Args {
		if pe, ok := a.(*expr.ParseError); ok {
			text = pe.Msg
			break
		}
		if s, ok := a.(string); ok && text == "" {
			text = s
		}
	}
	pos, line, col := 0, 0, 0
	var msgs []string
	for _, l := range strings.Split(text, "\n") {
		if at, ok := strings.CutPrefix(l, "# Error @ cell.jl:"); ok && line == 0 {
			lc := strings.SplitN(at, ":", 2)
			line, _ = strconv.Atoi(lc[0])
			if len(lc) > 1 {
				col, _ = strconv.Atoi(lc[1])
			}
		}
		if _, m, ok := strings.Cut(l, "── "); ok && strings.HasPrefix(l, "#") {
			msgs = append(msgs, m)
		}
	}
	if line > 0 {
		pos = lineColOffset(src, line, col)
	}
	if len(msgs) == 0 {
		msgs = []string{strings.TrimSpace(strings.TrimPrefix(text, "ParseError:"))}
	}
	return pos, "syntax error: " + strings.Join(msgs, "; ")
}

// lineColOffset is the byte offset of 1-based line and column (bytes) in
// src, clamped to it.
func lineColOffset(src string, line, col int) int {
	off := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(src[off:], '\n')
		if i < 0 {
			return len(src)
		}
		off += i + 1
	}
	return min(off+max(col-1, 0), len(src))
}

// jlScope is a local scope: the names it binds, and whether it is hard.
type jlScope struct {
	parent *jlScope
	vars   map[string]bool
	hard   bool
}

func (s *jlScope) bound(name string) bool {
	for ; s != nil; s = s.parent {
		if s.vars[name] {
			return true
		}
	}
	return false
}

// inHard reports whether a hard scope encloses s, s included.
func (s *jlScope) inHard() bool {
	for ; s != nil; s = s.parent {
		if s.hard {
			return true
		}
	}
	return false
}

func newScope(parent *jlScope, hard bool) *jlScope {
	return &jlScope{parent: parent, vars: map[string]bool{}, hard: hard}
}

type juliaWalker struct {
	cell    *Cell
	uses    map[string]bool
	defined map[string]bool // globals defined so far in this cell
}

// juliaKeywords are symbols the parser leaves in a tree that are no names:
// end and begin inside an index.
var juliaKeywords = map[string]bool{"end": true, "begin": true}

func (w *juliaWalker) use(name string) {
	if len(name) > 1 && name[0] == '.' {
		name = name[1:] // a dotted operator, .* or .+: it broadcasts * or +
	}
	if name != "" && !w.defined[name] && !Private(name) && !juliaKeywords[name] {
		w.uses[name] = true
	}
}

func (w *juliaWalker) read(name string, s *jlScope) {
	if !s.bound(name) {
		w.use(name)
	}
}

// define records a global the cell defines; list is Funcs or Consts, or nil
// for a variable.
func (w *juliaWalker) define(name string, list *[]string) {
	if name == "" || w.defined[name] {
		return
	}
	w.defined[name] = true
	if Private(name) {
		return
	}
	w.cell.Defines = append(w.cell.Defines, name)
	if list != nil {
		*list = append(*list, name)
	}
}

// assign is `name = …` in scope s (nil: the global scope).
func (w *juliaWalker) assign(name string, s *jlScope) {
	switch {
	case s == nil:
		w.define(name, nil)
	case s.bound(name):
	case s.inHard():
		s.vars[name] = true
	case w.defined[name]:
		// A soft scope at the top level: the cell's own global.
	default:
		// A soft scope at the top level: the global if another cell defines
		// it (an assignment the graph refuses), a local of the loop if none.
		w.use(name)
		if !Private(name) {
			w.cell.Assigns = append(w.cell.Assigns, Use{Name: name})
		}
	}
}

// at is argument i of e, nil when e has fewer; from is the arguments from i
// on. The parser gives odd arities for odd code (`struct <:() end`), and
// every access goes through them.
func at(e *expr.Expr, i int) any {
	if i < len(e.Args) {
		return e.Args[i]
	}
	return nil
}

func from(e *expr.Expr, i int) []any {
	if i < len(e.Args) {
		return e.Args[i:]
	}
	return nil
}

// stmt walks a statement: at the top level (s == nil) or in a block.
func (w *juliaWalker) stmt(a any, s *jlScope) { w.walk(a, s) }

// walk walks any value of a tree in scope s.
func (w *juliaWalker) walk(a any, s *jlScope) {
	switch v := a.(type) {
	case expr.Symbol:
		w.read(string(v), s)
	case *expr.Expr:
		w.expr(v, s)
	}
}

func (w *juliaWalker) walkAll(args []any, s *jlScope) {
	for _, a := range args {
		w.walk(a, s)
	}
}

func (w *juliaWalker) expr(e *expr.Expr, s *jlScope) {
	switch e.Head {
	case "=":
		w.assignment(e, s)
	case "const":
		w.constant(e, s)
	case "global":
		w.global(e, s)
	case "local":
		w.local(e, s)
	case "function":
		w.function(e, s)
	case "->":
		w.lambda(at(e, 0), from(e, 1), s)
	case "macro":
		w.macro(e, s)
	case "struct":
		w.structDef(e, s)
	case "abstract", "primitive":
		w.abstractDef(e, s)
	case "module":
		if name, ok := at(e, 1).(expr.Symbol); ok && s == nil {
			w.define(string(name), &w.cell.Consts)
		}
	case "using", "import":
		w.imports(e, s)
	case "let":
		w.let(e, s)
	case "for":
		w.forLoop(e, s)
	case "while":
		w.walk(at(e, 0), s)
		w.walkAll(from(e, 1), newScope(s, false))
	case "try":
		w.try(e, s)
	case "generator":
		w.generator(e, s)
	case "do":
		w.walk(at(e, 0), s)
		if lam, ok := at(e, 1).(*expr.Expr); ok { // (-> params body)
			w.lambda(at(lam, 0), from(lam, 1), s)
		}
	case "quote", "inert":
		w.quoted(e, s)
	case "macrocall":
		w.walk(at(e, 0), s)
		w.walkAll(from(e, 1), s)
	case ".":
		// a.b reads a; f.(x) reads f and x.
		w.walk(at(e, 0), s)
		for _, a := range from(e, 1) {
			if _, q := a.(expr.QuoteNode); !q {
				w.walk(a, s)
			}
		}
	case "tuple", "parameters", "call", "vect", "braces":
		w.arguments(e.Args, s)
	case "kw":
		w.walkAll(from(e, 1), s)
	case "where":
		inner := newScope(s, true)
		w.typeVars(from(e, 1), inner, s)
		w.walk(at(e, 0), inner)
	case "export", "public", "line", "meta", "symbolicgoto", "symboliclabel", "break", "continue":
	default:
		if opAssign(string(e.Head)) {
			w.opAssignment(e, s)
			return
		}
		w.walkAll(e.Args, s)
	}
}

// opAssign reports whether head is an updating operator (+=, .*=, …). The
// comparisons that also end in = (==, <=, …) are calls, never heads, but are
// refused all the same.
func opAssign(head string) bool {
	switch head {
	case "==", "!=", "===", "!==", "<=", ">=", ".==", ".!=", ".<=", ".>=":
		return false
	}
	return strings.HasSuffix(head, "=") && len(head) > 1
}

// generator is (generator body spec…): a hard scope where each spec — i =
// iter, or (filter cond spec…) — binds its variables, and the body is read.
func (w *juliaWalker) generator(e *expr.Expr, s *jlScope) {
	g := newScope(s, true)
	for _, sp := range from(e, 1) {
		w.genSpec(sp, g)
	}
	w.walk(at(e, 0), g)
}

func (w *juliaWalker) genSpec(sp any, g *jlScope) {
	if f, ok := sp.(*expr.Expr); ok && f.Head == "filter" {
		for _, inner := range from(f, 1) {
			w.genSpec(inner, g)
		}
		w.walk(at(f, 0), g)
		return
	}
	w.iteration(sp, g)
}

// arguments walks the arguments of a call or tuple: a keyword argument or a
// named-tuple field (k = v, (kw k v)) reads its value, not its name; `; k`
// in a call reads k.
func (w *juliaWalker) arguments(args []any, s *jlScope) {
	for _, a := range args {
		if e, ok := a.(*expr.Expr); ok && (e.Head == "=" || e.Head == "kw") && len(e.Args) == 2 {
			if _, named := at(e, 0).(expr.Symbol); named {
				w.walk(at(e, 1), s)
				continue
			}
		}
		w.walk(a, s)
	}
}

// assignment is (= lhs rhs): the value first, then what the left side binds.
func (w *juliaWalker) assignment(e *expr.Expr, s *jlScope) {
	lhs := at(e, 0)
	if isSignature(lhs) {
		w.method(lhs, from(e, 1), s, false)
		return
	}
	w.walkAll(from(e, 1), s)
	w.bindTarget(lhs, s, nil)
}

// bindTarget binds the left side of an assignment: names, x::T, tuples and
// (; a, b) destructuring; an index or field target reads instead.
func (w *juliaWalker) bindTarget(lhs any, s *jlScope, list *[]string) {
	switch v := lhs.(type) {
	case expr.Symbol:
		if list != nil && s == nil {
			w.define(string(v), list)
			return
		}
		w.assign(string(v), s)
	case *expr.Expr:
		switch v.Head {
		case "::":
			if len(v.Args) == 2 {
				w.walk(at(v, 1), s)
			}
			w.bindTarget(at(v, 0), s, list)
		case "tuple", "parameters":
			for _, a := range v.Args {
				w.bindTarget(a, s, list)
			}
		case "...":
			w.bindTarget(at(v, 0), s, list)
		default:
			w.walk(v, s)
		}
	}
}

// opAssignment is x += v: it reads x and assigns it, without defining it.
func (w *juliaWalker) opAssignment(e *expr.Expr, s *jlScope) {
	w.read(strings.TrimSuffix(string(e.Head), "="), s) // x += 1 is x = x + 1: it calls +
	w.walkAll(from(e, 1), s)
	name, ok := at(e, 0).(expr.Symbol)
	if !ok {
		w.walk(at(e, 0), s)
		return
	}
	switch {
	case s.bound(string(name)):
	case s != nil && s.inHard():
		s.vars[string(name)] = true
	case w.defined[string(name)]:
	default:
		w.use(string(name))
		if !Private(string(name)) {
			w.cell.Assigns = append(w.cell.Assigns, Use{Name: string(name)})
		}
	}
}

// constant is (const (= x v)) or (const x): a constant of the global scope.
func (w *juliaWalker) constant(e *expr.Expr, s *jlScope) {
	for _, a := range e.Args {
		if as, ok := a.(*expr.Expr); ok && as.Head == "=" {
			w.walkAll(from(as, 1), s)
			w.bindTarget(at(as, 0), s, &w.cell.Consts)
			continue
		}
		w.bindTarget(a, s, &w.cell.Consts)
	}
}

// global is (global x …) or (global (= x v)): at the top level, or in a soft
// scope of it, it defines; in a hard scope it names the global, defined only
// when the code runs.
func (w *juliaWalker) global(e *expr.Expr, s *jlScope) {
	top := s == nil || !s.inHard()
	for _, a := range e.Args {
		target, value := a, any(nil)
		if as, ok := a.(*expr.Expr); ok && as.Head == "=" {
			target, value = at(as, 0), at(as, 1)
		}
		if value != nil {
			w.walk(value, s)
		}
		if top {
			w.bindTarget(target, nil, nil) // global x, y = …; global x::T
			continue
		}
		w.walk(target, s)
	}
}

// local is (local x …) or (local (= x v)): new locals of the current scope.
func (w *juliaWalker) local(e *expr.Expr, s *jlScope) {
	for _, a := range e.Args {
		target := a
		if as, ok := a.(*expr.Expr); ok && as.Head == "=" {
			w.walkAll(from(as, 1), s)
			target = at(as, 0)
		}
		if s == nil {
			// local at the top level is an error of julia's; read what it names.
			w.walk(target, s)
			continue
		}
		w.bindLocal(target, s)
	}
}

// bindLocal binds names as locals of s: parameters, loop variables, let
// bindings.
func (w *juliaWalker) bindLocal(t any, s *jlScope) {
	switch v := t.(type) {
	case expr.Symbol:
		s.vars[string(v)] = true
	case *expr.Expr:
		switch v.Head {
		case "::":
			// The type is read where the where-variables are bound.
			if len(v.Args) == 2 {
				w.walk(at(v, 1), s)
				w.bindLocal(at(v, 0), s)
			} else {
				w.walk(at(v, 0), s) // ::T, an unnamed parameter
			}
		case "tuple", "parameters":
			for _, a := range v.Args {
				w.bindLocal(a, s)
			}
		case "...":
			w.bindLocal(at(v, 0), s)
		case "kw", "=":
			// A default value is evaluated in the function's scope, after
			// the parameters before it.
			w.walk(at(v, 1), s)
			w.bindLocal(at(v, 0), s)
		default:
			w.walk(v, s)
		}
	}
}

// isSignature reports whether the left side of `=` is a method signature:
// f(x) = …, f(x)::T = …, f(x) where T = ….
func isSignature(lhs any) bool {
	e, ok := lhs.(*expr.Expr)
	if !ok {
		return false
	}
	switch e.Head {
	case "call":
		return true
	case "where":
		return isSignature(at(e, 0))
	case "::":
		return len(e.Args) == 2 && isSignature(at(e, 0))
	}
	return false
}

// function is (function sig body…) or (function name) (a function with no
// methods yet).
func (w *juliaWalker) function(e *expr.Expr, s *jlScope) {
	if name, ok := at(e, 0).(expr.Symbol); ok && len(e.Args) == 1 {
		w.functionName(name, s)
		return
	}
	if t, ok := at(e, 0).(*expr.Expr); ok && t.Head == "tuple" {
		w.lambda(t, from(e, 1), s) // function (x, y) … end: anonymous
		return
	}
	w.method(at(e, 0), from(e, 1), s, false)
}

// method walks a method definition: its name is defined (or, for Base.show
// or (p::P)(x), read), its where-variables and parameters bound in a hard
// scope, its types and default values read, its body walked in that scope.
func (w *juliaWalker) method(sig any, body []any, s *jlScope, isMacro bool) {
	fn := newScope(s, true)
	for {
		e, ok := sig.(*expr.Expr)
		if !ok {
			break
		}
		if e.Head == "where" {
			w.typeVars(from(e, 1), fn, s)
			sig = at(e, 0)
			continue
		}
		if e.Head == "::" && len(e.Args) == 2 {
			w.walk(at(e, 1), fn) // the return type
			sig = at(e, 0)
			continue
		}
		break
	}
	call, ok := sig.(*expr.Expr)
	if !ok || call.Head != "call" {
		w.walk(sig, s)
		w.walkAll(body, fn)
		return
	}
	switch name := at(call, 0).(type) {
	case expr.Symbol:
		if isMacro {
			name = "@" + name
		}
		w.functionName(name, s)
	case *expr.Expr:
		if name.Head == "::" {
			// (p::P)(x): a callable P; p is a parameter.
			w.bindLocal(name, fn)
		} else if name.Head == "curly" {
			// P{T}(x): a constructor of P.
			w.walk(at(name, 0), s)
			w.walkAll(from(name, 1), fn)
		} else {
			w.walk(name, s) // Base.show: a method of a function of Base
		}
	}
	// The keyword parameters come first in the tree, but their defaults see
	// the positional parameters: those are bound first.
	var keywords []any
	for _, p := range from(call, 1) {
		if k, ok := p.(*expr.Expr); ok && k.Head == "parameters" {
			keywords = append(keywords, p)
			continue
		}
		w.bindLocal(p, fn)
	}
	for _, p := range keywords {
		w.bindLocal(p, fn)
	}
	w.walkAll(body, fn)
}

// functionName defines a function at the top level, or a local function.
func (w *juliaWalker) functionName(name expr.Symbol, s *jlScope) {
	if s == nil {
		w.define(string(name), &w.cell.Funcs)
		return
	}
	w.assign(string(name), s)
}

// typeVars binds the variables of a where clause in scope, reading their
// bounds in outer.
func (w *juliaWalker) typeVars(vars []any, scope, outer *jlScope) {
	for _, v := range vars {
		switch t := v.(type) {
		case expr.Symbol:
			scope.vars[string(t)] = true
		case *expr.Expr:
			// T <: Upper, Lower <: T <: Upper, (braces T S): bounds read.
			if t.Head == "braces" {
				w.typeVars(t.Args, scope, outer)
				continue
			}
			if name, ok := at(t, 0).(expr.Symbol); ok && (t.Head == "<:" || t.Head == ">:") {
				scope.vars[string(name)] = true
				w.walkAll(from(t, 1), outer)
				continue
			}
			if t.Head == "comparison" && len(t.Args) == 5 {
				w.walk(at(t, 0), outer)
				if name, ok := at(t, 2).(expr.Symbol); ok {
					scope.vars[string(name)] = true
				}
				w.walk(at(t, 4), outer)
				continue
			}
			w.walk(t, outer)
		}
	}
}

// lambda is params -> body: a hard scope binding the parameters.
func (w *juliaWalker) lambda(params any, body []any, s *jlScope) {
	fn := newScope(s, true)
	if p, ok := params.(*expr.Expr); ok && p.Head == "block" {
		// x -> … with a block of parameters and a line node
		for _, a := range p.Args {
			w.bindLocal(a, fn)
		}
	} else {
		w.bindLocal(params, fn)
	}
	w.walkAll(body, fn)
}

// macro is (macro (call m params…) body).
func (w *juliaWalker) macro(e *expr.Expr, s *jlScope) {
	w.method(at(e, 0), from(e, 1), s, true)
}

// structDef is (struct mutable name body): name is S, S{T}, or S <: Super.
func (w *juliaWalker) structDef(e *expr.Expr, s *jlScope) {
	inner := newScope(s, true)
	inner.vars["new"] = true // an inner constructor's new is no global
	w.typeName(at(e, 1), inner, s)
	body, ok := at(e, 2).(*expr.Expr) // (block …)
	if !ok {
		return
	}
	for _, f := range body.Args {
		switch v := f.(type) {
		case expr.Symbol: // a field without a type
		case *expr.Expr:
			if v.Head == "::" && len(v.Args) == 2 {
				w.walk(at(v, 1), inner) // a field's type, not its name
				continue
			}
			if v.Head == "const" { // const field (Julia 1.8)
				w.walkFieldTypes(v.Args, inner)
				continue
			}
			w.walk(v, inner) // an inner constructor
		}
	}
}

// walkFieldTypes reads the types of struct fields.
func (w *juliaWalker) walkFieldTypes(fields []any, s *jlScope) {
	for _, f := range fields {
		if v, ok := f.(*expr.Expr); ok && v.Head == "::" && len(v.Args) == 2 {
			w.walk(at(v, 1), s)
		}
	}
}

// typeName defines the type named by a struct or abstract type head, binds
// its parameters in inner and reads its supertype.
func (w *juliaWalker) typeName(head any, inner, s *jlScope) {
	switch v := head.(type) {
	case expr.Symbol:
		if s == nil {
			w.define(string(v), &w.cell.Consts)
		}
		inner.vars[string(v)] = true // its constructors' name, inside
	case *expr.Expr:
		switch v.Head {
		case "<:":
			w.typeName(at(v, 0), inner, s)
			w.walk(at(v, 1), inner)
		case "curly":
			w.typeName(at(v, 0), inner, s)
			w.typeVars(from(v, 1), inner, s)
		default:
			w.walk(v, s)
		}
	}
}

// abstractDef is (abstract name) or (primitive name bits).
func (w *juliaWalker) abstractDef(e *expr.Expr, s *jlScope) {
	inner := newScope(s, true)
	w.typeName(at(e, 0), inner, s)
	w.walkAll(from(e, 1), s)
}

// imports defines the names using and import bind: the module of `using A`
// or `import A.B` (its last component), the names after `:`, an `as` alias.
func (w *juliaWalker) imports(e *expr.Expr, s *jlScope) {
	for _, a := range e.Args {
		path, ok := a.(*expr.Expr)
		if !ok {
			continue
		}
		if path.Head == ":" {
			// (: (. A) (. x) (. y)): the module is read, x and y bound.
			w.importPath(at(path, 0), s, false)
			for _, n := range from(path, 1) {
				w.importPath(n, s, true)
			}
			continue
		}
		w.importPath(path, s, true)
	}
}

func (w *juliaWalker) importPath(p any, s *jlScope, bind bool) {
	e, ok := p.(*expr.Expr)
	if !ok {
		return
	}
	switch e.Head {
	case "as":
		if alias, ok := at(e, 1).(expr.Symbol); ok && s == nil {
			w.define(string(alias), &w.cell.Consts)
		}
	case ".":
		if len(e.Args) == 0 || !bind || s != nil {
			return
		}
		if last, ok := e.Args[len(e.Args)-1].(expr.Symbol); ok && last != "." {
			w.define(string(last), &w.cell.Consts)
		}
	}
}

// let is (let bindings body): a hard scope; each binding's value is read
// before the name is bound, so `let x = x` reads the outer x.
func (w *juliaWalker) let(e *expr.Expr, s *jlScope) {
	inner := newScope(s, true)
	bindings := []any{at(e, 0)}
	if b, ok := at(e, 0).(*expr.Expr); ok && b.Head == "block" {
		bindings = b.Args
	}
	for _, b := range bindings {
		if as, ok := b.(*expr.Expr); ok && as.Head == "=" {
			if isSignature(at(as, 0)) {
				w.method(at(as, 0), from(as, 1), inner, false)
				continue
			}
			w.walkAll(from(as, 1), inner)
			w.bindLocal(at(as, 0), inner)
			continue
		}
		w.bindLocal(b, inner)
	}
	w.walkAll(from(e, 1), inner)
}

// forLoop is (for (= i iter) body) or (for (block (= i a) (= j b)) body):
// each iterator read before its variables are bound, in a soft scope.
func (w *juliaWalker) forLoop(e *expr.Expr, s *jlScope) {
	loop := newScope(s, false)
	specs := []any{at(e, 0)}
	if b, ok := at(e, 0).(*expr.Expr); ok && b.Head == "block" {
		specs = b.Args
	}
	for _, sp := range specs {
		w.iteration(sp, loop)
	}
	w.walkAll(from(e, 1), loop)
}

// iteration is one `i = iter` (or `i in iter`) of a loop or a generator.
func (w *juliaWalker) iteration(sp any, scope *jlScope) {
	as, ok := sp.(*expr.Expr)
	if !ok || (as.Head != "=" && as.Head != "in" && as.Head != "∈") || len(as.Args) != 2 {
		w.walk(sp, scope)
		return
	}
	w.walk(at(as, 1), scope)
	w.bindLocal(at(as, 0), scope)
}

// try is (try body catchvar catchbody [finally] [else]): soft scopes; the
// catch variable is a local of the catch block.
func (w *juliaWalker) try(e *expr.Expr, s *jlScope) {
	w.walk(at(e, 0), newScope(s, false))
	if len(e.Args) > 2 {
		catch := newScope(s, false)
		if v, ok := at(e, 1).(expr.Symbol); ok {
			catch.vars[string(v)] = true
		}
		w.walk(at(e, 2), catch)
	}
	for _, a := range e.Args[min(3, len(e.Args)):] {
		w.walk(a, newScope(s, false))
	}
}

// quoted walks a quotation: its symbols are data, but what $ interpolates
// is read.
func (w *juliaWalker) quoted(e *expr.Expr, s *jlScope) {
	for _, a := range e.Args {
		w.interpolations(a, s)
	}
}

func (w *juliaWalker) interpolations(a any, s *jlScope) {
	e, ok := a.(*expr.Expr)
	if !ok {
		return
	}
	if e.Head == "$" {
		w.walkAll(e.Args, s)
		return
	}
	for _, x := range e.Args {
		w.interpolations(x, s)
	}
}
