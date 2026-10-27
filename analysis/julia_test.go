//go:build !nojulia

package analysis

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-embedded-julia/julia/expr"
	"github.com/go-embedded-julia/julia/syntax"
)

func names(l []string) string { return strings.Join(l, ",") }

func TestAnalyzeJuliaCell(t *testing.T) {
	for _, tc := range []struct {
		src                          string
		defines, uses, funcs, consts string
		assigns                      string
	}{
		// Definitions of each kind, and their lists.
		{src: "f(x) = x\nmacro m(e) e end\nconst K = 1\nstruct S end", defines: "f,@m,K,S", funcs: "f,@m", consts: "K,S"},
		{src: "abstract type A{T} <: B{T} end", defines: "A", uses: "B", consts: "A"},
		{src: "primitive type W <: Unsigned 16 end", defines: "W", uses: "Unsigned", consts: "W"},
		{src: "module M end\nusing .M\nimport A.B as C\nusing D: e as f", defines: "M,C,f", consts: "M,C,f"},
		{src: "using X: y", defines: "y", consts: "y"},
		// Private names stay in the cell.
		{src: "_tmp = 1\n_f(x) = x\ny = _tmp", defines: "y"},
		// A struct's fields are no names; its type parameters bind; new is local.
		{src: "struct P{T<:Real}\n  x::T\n  const y::Int\n  z\n  P(x) = new{typeof(x)}(x, 1, w)\nend",
			defines: "P", uses: "Int,Real,typeof,w", consts: "P"},
		// Callables, constructors with type parameters, functions of Base.
		{src: "(p::P)(x) = p.v + x\nP{T}(x) where T = x\nBase.show(io, p) = 1", uses: "+,Base,P"},
		// Where clauses: bounds read, variables bound.
		{src: "f(x::T, y::S) where {T<:Number, Integer<:S<:Real} = (x, y, U)", defines: "f", uses: "Integer,Number,Real,U", funcs: "f"},
		{src: "g(x::T) where T >: Int = x", defines: "g", uses: "Int", funcs: "g"},
		{src: "v = Vector{T} where T", defines: "v", uses: "Vector"},
		// Keyword parameters and defaults see the parameters before them.
		{src: "h(a, b=a; k=b, kw...) = (a, b, k, kw, z)", defines: "h", uses: "z", funcs: "h"},
		// Local functions; a function with no methods; anonymous functions.
		{src: "function out()\n  function inner end\n  in2(x) = x\n  inner\nend", defines: "out", funcs: "out"},
		{src: "function (a, b)\n  a + b + c\nend", uses: "+,c"},
		{src: "map((x, y) -> x * y + k, xs, ys)", uses: "*,+,k,map,xs,ys"},
		{src: "f = x -> begin\n  y = x\n  y\nend", defines: "f"},
		// Destructuring, typed assignment, index and field targets.
		{src: "(; a, b) = nt\nc, d... = xs\nm[1] = 2\no.p = 3", defines: "a,b,c,d", uses: "m,nt,o,xs"},
		// Updating operators: the operator is called; another cell's global is
		// assigned, not defined; in a hard scope, a local.
		{src: "x .+= 1", uses: "+,x", assigns: "x"},
		{src: "x = 1\nx += 1", defines: "x", uses: "+"},
		{src: "function f()\n  n += 1\nend", defines: "f", uses: "+", funcs: "f"},
		{src: "a[1] += 1", uses: "+,a"},
		// Soft scope: a global of the cell, of another cell, or a loop local.
		{src: "s = 0\nfor i in 1:3\n  s = s + i\n  t = i\nend", defines: "s", uses: "+,:,t", assigns: "t"},
		// global and local.
		{src: "function f()\n  global g = 1\nend", defines: "f", uses: "g", funcs: "f"},
		{src: "for i in 1:2\n  global h\n  h = i\nend", defines: "h", uses: ":"},
		{src: "local l = 1", uses: "l"},
		{src: "let\n  local q = 1\n  q + r\nend", uses: "+,r"},
		{src: "global x::Int, y = 1", defines: "x,y", uses: "Int"},
		// let: bindings read before they are bound; a function binding.
		{src: "let f(x) = x + 1, y\n  f(y)\nend", uses: "+"},
		{src: "let x::Int = x\nend", uses: "Int,x"},
		// Loops: nested iterators, destructuring, `in`.
		{src: "for (k, v) in d, w ∈ v\n  k, w\nend", uses: "d"},
		{src: "while ok()\n  local z = 1\nend", uses: "ok"},
		// Generators and comprehensions: filters, nesting, typed.
		{src: "Int[x for x in xs if x > lo]", uses: ">,Int,lo,xs"},
		{src: "[(i, j) for i in 1:n for j in 1:i if j < m]", uses: ":,<,m,n"},
		// try: catch variable, finally, else.
		{src: "try\n  a()\ncatch\n  b()\nelse\n  c()\nfinally\n  d()\nend", uses: "a,b,c,d"},
		{src: "try\n  a()\ncatch err\n  err\nend", uses: "a"},
		// do blocks, quotes with interpolation, macros, strings, keywords.
		{src: "open(f) do io\n  write(io, data)\nend", uses: "data,f,open,write"},
		{src: "q = quote\n  a + $b\nend\nr = :(x; $(y + 1))", defines: "q,r", uses: "+,b,y"},
		{src: "@show x\n@m.n y\nCore.@doc z", uses: "@show,Core,m,x,y,z"},
		{src: "f(a; k=b)\ng(; c)\nh(d=1)\n(e=2, f)", uses: "a,b,c,f,g,h"},
		{src: "a[end] + b[begin]", uses: "+,a,b"},
		{src: "f.(x) .* y.z", uses: "*,f,x,y"},
		{src: "export f\npublic g\nbreak", uses: ""},
		// Statements after a definition read it: not a use of the cell.
		{src: "x = 1\ny = x", defines: "x,y"},
		// if/begin are the global scope.
		{src: "if c\n  d = 1\nelseif e\n  f = 2\nend", defines: "d,f", uses: "c,e"},
	} {
		c := AnalyzeJuliaCell(0, tc.src)
		if len(c.Errors) > 0 {
			t.Errorf("%q: %v", tc.src, c.Errors)
			continue
		}
		var assigns []string
		for _, a := range c.Assigns {
			assigns = append(assigns, a.Name)
		}
		if names(c.Defines) != tc.defines || names(c.Uses) != tc.uses || names(c.Funcs) != tc.funcs ||
			names(c.Consts) != tc.consts || names(assigns) != tc.assigns {
			t.Errorf("%q:\n defines %q uses %q funcs %q consts %q assigns %q", tc.src,
				names(c.Defines), names(c.Uses), names(c.Funcs), names(c.Consts), names(assigns))
		}
	}
}

func TestJuliaErrors(t *testing.T) {
	c := AnalyzeJuliaCell(3, "y = 2\nx = (1,")
	if len(c.Errors) != 1 || c.Errors[0].Cell != 3 || c.Errors[0].Pos != 13 || c.Errors[0].Msg != "syntax error: Expected `)` or `,`" {
		t.Errorf("%+v", c.Errors)
	}
	// A cell larger than the limit, nested deeper than it, or that the
	// parser itself cannot handle.
	big := AnalyzeJuliaCell(0, strings.Repeat("x = 1\n", juliaLimits.MaxBytes/6+1))
	deep := AnalyzeJuliaCell(0, strings.Repeat("(", 5000)+strings.Repeat(")", 5000))
	failed := AnalyzeJuliaCell(0, "0:0:<")
	for name, c := range map[string]*Cell{"big": big, "deep": deep} {
		if len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "too large or too deeply nested") {
			t.Errorf("%s: %+v", name, c.Errors)
		}
	}
	if len(failed.Errors) != 1 || !strings.Contains(failed.Errors[0].Msg, "cannot parse this cell") {
		t.Errorf("parser failure: %+v", failed.Errors)
	}
}

func TestJuliaParserSeams(t *testing.T) {
	old := juliaParse
	t.Cleanup(func() { juliaParse = old })
	juliaParse = func([]byte, string, syntax.Limits) (any, error) { panic("boom") }
	if c := AnalyzeJuliaCell(0, "x"); len(c.Errors) != 1 || c.Errors[0].Msg != "the Julia parser failed on this cell" {
		t.Errorf("panic: %+v", c.Errors)
	}
	juliaParse = func([]byte, string, syntax.Limits) (any, error) { return nil, errors.New("other") }
	if c := AnalyzeJuliaCell(0, "x"); len(c.Errors) != 1 || c.Errors[0].Msg != "other" {
		t.Errorf("other error: %+v", c.Errors)
	}
	// Something that is not (toplevel …) has no statements.
	juliaParse = func([]byte, string, syntax.Limits) (any, error) { return 42, nil }
	if c := AnalyzeJuliaCell(0, "x"); len(c.Errors) != 0 || len(c.Defines) != 0 {
		t.Errorf("not a toplevel: %+v", c)
	}
	// An error node holding a plain string, or no location.
	juliaParse = func([]byte, string, syntax.Limits) (any, error) {
		return expr.New("toplevel", expr.New("error", "plain message")), nil
	}
	if c := AnalyzeJuliaCell(0, "x"); len(c.Errors) != 1 || c.Errors[0].Msg != "syntax error: plain message" || c.Errors[0].Pos != 0 {
		t.Errorf("plain: %+v", c.Errors)
	}
	// A tree the walker does not expect: the cell fails, the notebook goes on.
	juliaParse = func([]byte, string, syntax.Limits) (any, error) {
		return expr.New("toplevel", &expr.Expr{Head: "=", Args: []any{expr.Symbol("x"), nil}}, expr.New("block", (*expr.Expr)(nil))), nil
	}
	if c := AnalyzeJuliaCell(0, "x"); len(c.Errors) != 1 || !strings.Contains(c.Errors[0].Msg, "unexpected tree") || len(c.Defines) != 0 {
		t.Errorf("unexpected: %+v", c)
	}
}

// Trees the parser gives only for odd code, or never: the walker reads what
// it can and binds nothing it should not.
func TestJuliaWalkerShapes(t *testing.T) {
	sym := func(s string) expr.Symbol { return expr.Symbol(s) }
	walk := func(e *expr.Expr) *Cell {
		c := &Cell{Lang: Julia}
		w := &juliaWalker{cell: c, uses: map[string]bool{}, defined: map[string]bool{}}
		w.walk(e, nil)
		for u := range w.uses {
			c.Uses = append(c.Uses, u)
		}
		slices.Sort(c.Uses)
		return c
	}
	for name, tc := range map[string]struct {
		e             *expr.Expr
		defines, uses string
	}{
		"kw outside a call":       {expr.New("kw", sym("k"), sym("v")), "", "v"},
		"const without a value":   {expr.New("const", sym("q")), "q", ""},
		"unnamed typed parameter": {expr.New("=", expr.New("call", sym("f"), expr.New("::", sym("T"))), sym("y")), "f", "T,y"},
		"odd parameter":           {expr.New("=", expr.New("call", sym("f"), expr.New("ref", sym("a"))), sym("b")), "f", "a,b"},
		"return type":             {expr.New("=", expr.New("::", expr.New("call", sym("f")), sym("R")), sym("x")), "f", "R,x"},
		"no signature":            {expr.New("function", sym("x"), expr.New("block", sym("y"))), "", "x,y"},
		"non-call signature":      {expr.New("function", expr.New("vect", sym("a")), sym("b")), "", "a,b"},
		"braces of type vars":     {expr.New("where", sym("V"), expr.New("braces", sym("T"), expr.New("<:", sym("S"), sym("U")))), "", "U,V"},
		"odd type var":            {expr.New("where", sym("V"), expr.New("call", sym("g"), sym("h"))), "", "V,g,h"},
		"block of parameters":     {expr.New("->", expr.New("block", sym("x"), expr.LineNumberNode{}), expr.New("call", sym("x"), sym("z"))), "", "z"},
		"struct without a body":   {expr.New("struct", false, sym("S")), "S", ""},
		"odd struct name":         {expr.New("struct", false, expr.New("call", sym("n")), expr.New("block")), "", "n"},
		"odd import":              {expr.New("using", sym("x"), expr.New(":", sym("a"), sym("b")), expr.New(".")), "", ""},
		"odd iteration":           {expr.New("for", sym("it"), sym("body")), "", "body,it"},
	} {
		c := walk(tc.e)
		if names(c.Defines) != tc.defines || names(c.Uses) != tc.uses {
			t.Errorf("%s: defines %q uses %q", name, names(c.Defines), names(c.Uses))
		}
	}
	for _, h := range []string{"==", "!=", ".<=", "="} {
		if opAssign(h) {
			t.Errorf("%s is an updating operator", h)
		}
	}
}

func TestLineColOffset(t *testing.T) {
	src := "ab\ncd\n"
	for _, tc := range []struct{ line, col, want int }{{1, 1, 0}, {2, 2, 4}, {9, 1, 6}, {2, 0, 3}, {2, 99, 6}} {
		if got := lineColOffset(src, tc.line, tc.col); got != tc.want {
			t.Errorf("%d:%d = %d, want %d", tc.line, tc.col, got, tc.want)
		}
	}
}

func TestJuliaGraph(t *testing.T) {
	g := AnalyzeSources([]Source{
		{Lang: Julia, Source: "x = 1\nf(y) = y"},
		{Lang: Julia, Source: "for i in 1:3\n  x = x + i\nend"}, // assigns cell 0's global
		{Lang: Go, Source: "z := x"},                            // reads the Julia variable
		{Lang: Ruby, Source: "w = f(1)"},                        // f is a Julia function: not shared
		{Lang: Julia, Source: "v = z + w"},                      // reads the Go and Ruby variables
	})
	if len(g.Errors) != 1 || g.Errors[0].Cell != 1 || !strings.Contains(g.Errors[0].Msg, "cannot assign to x, which cell 0 defines") {
		t.Errorf("errors %+v", g.Errors)
	}
	if !slices.Equal(g.Deps[2], []int{0}) || len(g.Deps[3]) != 0 || !slices.Equal(g.Deps[4], []int{2, 3}) {
		t.Errorf("deps %v", g.Deps)
	}
	if JuliaBlocked(4) != "blocked: it depends on cell 4, a Julia cell, and Julia cells are not run yet" {
		t.Error(JuliaBlocked(4))
	}
	// A Julia variable named like a Go keyword stays out of Go's sight.
	g = AnalyzeSources([]Source{{Lang: Julia, Source: "type = 1"}, {Lang: Go, Source: "_ = 1"}})
	if !Shared(g.Cells[0], &Cell{Lang: Ruby}, "type") || Shared(g.Cells[0], g.Cells[1], "type") {
		t.Error("type crosses to Go")
	}
}
