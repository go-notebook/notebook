//go:build !nojulia

package analysis

import (
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// juliaCorpus are cells whose names julia itself resolves the same way
// (testdata/julia_names.jl): definitions of every kind, reads before and
// after assignment, and each scope construct of "Scope of Variables" in the
// Julia manual.
var juliaCorpus = []string{
	"x = 1",
	"y = x + 1",
	"x = 1\ny = x + 1",
	"y = x\nx = 2",
	"a, b = 1, 2",
	"(a, b) = t",
	"x::Int = 3",
	"global g = h",
	"if c\n  d = 1\nelse\n  e = 2\nend",
	"begin\n  p = q\nend",
	"f(x) = x + y",
	"f(x::T) where {T<:Number} = x * k",
	"function g(a::T; k=1) where T\n  local z = a\n  z + k + w\nend",
	"function h end",
	"function (x, y)\n  x + y + z\nend",
	"sq = x -> x^2 + c",
	"const C = 3",
	"const D, E = 1, 2",
	"struct P{T}\n  x::T\n  y::Vector{S}\nend",
	"mutable struct Q <: AbstractR\n  v::Int\n  Q() = new(0)\nend",
	"abstract type A <: B end",
	"primitive type Bits 8 end",
	"module M\n  x = 1\nend",
	"macro twice(e)\n  :($e; $e)\nend",
	"using LinearAlgebra",
	"using LinearAlgebra: norm, dot",
	"import Base.show",
	"import Statistics as St",
	"for i in 1:n\n  s = i\nend",
	"s = 0\nfor i in 1:n\n  s = s + i\nend",
	"for i in 1:3, j in 1:i\n  println(i, j, k)\nend",
	"t = 0\nwhile t < 10\n  t += 1\nend",
	"while true\n  u = 1\n  u > 0 && break\nend",
	"try\n  risky(z)\ncatch e\n  handle(e)\nfinally\n  cleanup()\nend",
	"let a = 1, b = a + c\n  a + b + d\nend",
	"let x = x\n  x + 1\nend",
	"[i^2 for i in 1:n if i > k]",
	"Dict(k => v for (k, v) in pairs)",
	"sum(x * w for x in xs)",
	"map(xs) do x\n  x + off\nend",
	"Base.show(io::IO, p::P) = print(io, p.x)",
	"(p::P)(z) = p.x + z + q",
	"total = sum(v .* weights)",
	"nt = (a=1, b=c)",
	"f(1; key=val)",
	"f(1; key)",
	"m = a[end] + a[begin]",
	"s = \"v = $v\"",
	"ex = :(a + $b)",
	"x = 1\nx = 2",
	"function outer()\n  inner(y) = y + base\n  inner(1)\nend",
	"function acc()\n  n = 0\n  for i in 1:3\n    n += i\n  end\n  n\nend",
	"h(a, b=a; k=b, kw...) = (a, b, k, kw, z)",
	"global x::Int, y = 1",
	"for i in 1:2\n  global h\n  h = i\nend",
	"(; a, b) = nt",
	"let f(x) = x + 1, y\n  f(y)\nend",
	"[(i, j) for i in 1:n for j in 1:i if j < m]",
	"f(::Int) = 1",
	"f(x)::R = x",
}

// julia returns the command that runs the reference julia, or skips: the
// executable in $JLGO_JULIA (go-embedded-julia's variable), else julia 1.12.6
// through pkgx.
func julia(t *testing.T) []string {
	t.Helper()
	if j := os.Getenv("JLGO_JULIA"); j != "" {
		return []string{j}
	}
	if _, err := exec.LookPath("pkgx"); err == nil && os.Getenv("NOTEBOOK_JULIA_ORACLE") != "" {
		return []string{"pkgx", "julia@1.12.6"}
	}
	t.Skip("no reference julia: set JLGO_JULIA, or NOTEBOOK_JULIA_ORACLE=1 with pkgx")
	return nil
}

// goNames is a cell's names as the oracle prints them: the defined names,
// and the used ones without the defined. julia sees the cell alone, where a
// loop's assignment to a name no cell defines makes a local of the loop: the
// names the analysis records as assigned in a soft scope (Assigns), which
// would be another cell's globals if one defined them, are locals there.
func goNames(src string) string {
	c := AnalyzeJuliaCell(0, src)
	if len(c.Errors) > 0 {
		return "error\t"
	}
	defs := append([]string(nil), c.Defines...)
	slices.Sort(defs)
	var uses []string
	for _, u := range c.Uses {
		assigned := slices.ContainsFunc(c.Assigns, func(a Use) bool { return a.Name == u })
		if !slices.Contains(defs, u) && !assigned {
			uses = append(uses, u)
		}
	}
	return strings.Join(defs, ",") + "\t" + strings.Join(uses, ",")
}

const juliaGolden = "testdata/julia_names.golden"

// The analysis gives the names the golden file holds, which julia gave.
func TestJuliaNamesGolden(t *testing.T) {
	b, err := os.ReadFile(juliaGolden)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(want) != len(juliaCorpus) {
		t.Fatalf("golden has %d lines, corpus %d", len(want), len(juliaCorpus))
	}
	for i, src := range juliaCorpus {
		if got := goNames(src); got != want[i] {
			t.Errorf("%q:\n  go    %q\n  julia %q", src, got, want[i])
		}
	}
}

// The golden file is what julia says (JULIA_GEN_GOLDEN=1 rewrites it).
func TestJuliaNamesOracle(t *testing.T) {
	cmd := julia(t)
	c := exec.Command(cmd[0], append(cmd[1:], "--startup-file=no", "testdata/julia_names.jl")...)
	c.Stdin = strings.NewReader(strings.Join(juliaCorpus, "\x00") + "\x00")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("julia: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(juliaCorpus) {
		t.Fatalf("julia gave %d lines for %d cells:\n%s", len(got), len(juliaCorpus), out)
	}
	if os.Getenv("JULIA_GEN_GOLDEN") != "" {
		if err := os.WriteFile(juliaGolden, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, src := range juliaCorpus {
		if g := goNames(src); g != got[i] {
			t.Errorf("%s:\n  go    %q\n  julia %q", strconv.Quote(src), g, got[i])
		}
	}
}
