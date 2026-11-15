package jcells

import (
	"context"
	"math"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/go-embedded-julia/julia/interp"

	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/runtime"
)

func notes() (map[string]string, func(string, string)) {
	m := map[string]string{}
	return m, func(n, t string) { m[n] = t }
}

// Every type of the table crosses, both ways: Julia values come out as Go
// values, copied, and Go values go in as Julia ones.
func TestVariablesCross(t *testing.T) {
	s := sharedSession(t)
	var i, f, b, str, vf, vi, mf, mi, any1 any
	var iv int64
	n, note := notes()
	eval1 := func(src string, out ...Var) {
		t.Helper()
		if _, err := s.Eval(context.Background(), Cell{Source: src, Out: out, Note: note}); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
	}
	eval1("x_i = 7\nx_f = 2.5\nx_b = true\nx_s = \"hé\"\nx_vf = [1.5, 2.5]\nx_vi = [1, 2, 3]\nx_mf = [1.0 2.0 3.0; 4.0 5.0 6.0]\nx_mi = [1 2; 3 4]\nx_iv = 3\n",
		Var{Name: "x_i", Ptr: &i}, Var{Name: "x_f", Ptr: &f}, Var{Name: "x_b", Ptr: &b}, Var{Name: "x_s", Ptr: &str},
		Var{Name: "x_vf", Ptr: &vf}, Var{Name: "x_vi", Ptr: &vi}, Var{Name: "x_mf", Ptr: &mf}, Var{Name: "x_mi", Ptr: &mi},
		Var{Name: "x_iv", Ptr: &iv}, Var{Name: "x_absent", Ptr: &any1})
	if i != int64(7) || f != 2.5 || b != true || str != "hé" || iv != 3 || any1 != nil {
		t.Fatalf("scalars: %v %v %v %v %v %v", i, f, b, str, iv, any1)
	}
	if got := vf.([]float64); len(got) != 2 || got[1] != 2.5 {
		t.Fatalf("vector %v", vf)
	}
	if got := vi.([]int64); len(got) != 3 || got[2] != 3 {
		t.Fatalf("vector %v", vi)
	}
	m := mf.(interp.Matrix[float64])
	if m.Rows != 2 || m.Cols != 3 || m.At(1, 2) != 6 || m.Data[1] != 4 || m.Data[2] != 2 { // column-major: (1,0) is 4.0 at Data[1], (0,1) is 2.0 at Data[2]
		t.Fatalf("matrix %+v", m)
	}
	if got := mi.(interp.Matrix[int64]); got.At(0, 1) != 2 || got.At(1, 0) != 3 {
		t.Fatalf("matrix %+v", got)
	}
	want := map[string]string{
		"x_i": "int64", "x_f": "float64", "x_b": "bool", "x_s": "string", "x_vf": "[]float64", "x_vi": "[]int64", "x_iv": "int64",
		"x_mf": "{github.com/go-embedded-julia/julia/interp}.Matrix[float64]", "x_mi": "{github.com/go-embedded-julia/julia/interp}.Matrix[int64]",
	}
	for k, v := range want {
		if n[k] != v {
			t.Errorf("note %s = %q, want %q", k, n[k], v)
		}
	}
	if _, ok := n["x_absent"]; ok {
		t.Error("a variable the cell did not assign is noted")
	}
	// the Go values go in: a copy, as Julia variables
	in := []Var{{Name: "g_i", Ptr: &i}, {Name: "g_f", Ptr: &f}, {Name: "g_vf", Ptr: &vf}, {Name: "g_mf", Ptr: &mf}, {Name: "g_s", Ptr: &str}}
	out, err := s.Eval(context.Background(), Cell{Source: "(g_i + 1, g_f * 2, sum(g_vf), size(g_mf), g_s)", In: in})
	if err != nil || out.Stdout != "(8, 5.0, 4.0, (2, 3), \"hé\")\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
	// an int goes in as an Int64, and a holder that holds nothing is skipped
	var plain int = 5
	var empty any
	out, err = s.Eval(context.Background(), Cell{Source: "p_n * 2", In: []Var{{Name: "p_n", Ptr: &plain}, {Name: "p_empty", Ptr: &empty}}})
	if err != nil || out.Stdout != "10\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
}

// The values are copies: a Go slice is not Julia's array, in either direction.
func TestCrossingCopies(t *testing.T) {
	s := sharedSession(t)
	var v any
	_, note := notes()
	if _, err := s.Eval(context.Background(), Cell{Source: "c_v = [1.0, 2.0]", Out: []Var{{Name: "c_v", Ptr: &v}}, Note: note}); err != nil {
		t.Fatal(err)
	}
	v.([]float64)[0] = 99 // the Go side changes its copy
	out, _ := s.Eval(context.Background(), Cell{Source: "c_v[1]"})
	if out.Stdout != "1.0\n" {
		t.Fatalf("julia saw the Go change: %q", out.Stdout)
	}
	g := []float64{1, 2}
	if _, err := s.Eval(context.Background(), Cell{Source: "g_c[1] = 5.0", In: []Var{{Name: "g_c", Ptr: &g}}}); err != nil {
		t.Fatal(err)
	}
	if g[0] != 1 {
		t.Fatalf("Go saw the julia change: %v", g)
	}
}

// A variable of a type that does not cross stays in Julia and is noted as such,
// with its Julia type; the cell itself does not fail.
func TestUnsupportedTypeStaysInJulia(t *testing.T) {
	s := sharedSession(t)
	var d, k any
	n, note := notes()
	_, err := s.Eval(context.Background(), Cell{Source: "u_d = Dict(1 => 2)\nu_k = [\"a\", \"b\"]", Out: []Var{{Name: "u_d", Ptr: &d}, {Name: "u_k", Ptr: &k}}, Note: note})
	if err != nil || d != nil || k != nil {
		t.Fatalf("%v %v %v", err, d, k)
	}
	if n["u_d"] != Unsupported+"Dict{Int64, Int64}" || n["u_k"] != Unsupported+"Vector{String}" {
		t.Fatalf("notes %v", n)
	}
	// it is still there for Julia, whose own cells read it as before (IfAbsent:
	// held, so not given again, even though its holder is empty)
	out, err := s.Eval(context.Background(), Cell{Source: "u_d[1]", In: []Var{{Name: "u_d", Ptr: &d, IfAbsent: true}}})
	if err != nil || out.Stdout != "2\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
	// a note-less cell (a nil Note) takes the same variables
	if _, err := s.Eval(context.Background(), Cell{Source: "u_n = 1", Out: []Var{{Name: "u_n", Ptr: &d}}}); err != nil {
		t.Fatal(err)
	}
}

// A Go value of a type Julia cannot take fails the cell that reads it, clearly,
// and does not panic.
func TestGoValueJuliaCannotTake(t *testing.T) {
	s := sharedSession(t)
	ss := []string{"a"}
	_, err := s.Eval(context.Background(), Cell{Source: "length(bad_s)", In: []Var{{Name: "bad_s", Ptr: &ss}}})
	msg := cellErr(t, err)
	for _, want := range []string{"the Julia cell reads bad_s, a Go []string that Julia cannot take", "[]string", "Julia takes int, int64, float64, bool, string, []int, []int64, []float64 and interp.Matrix"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
	// the session goes on
	if out := eval(t, s, "1 + 1"); out.Stdout != "2\n" {
		t.Fatalf("%+v", out)
	}
}

// IfAbsent: Julia's own variable is given from its Go holder only when the
// kernel lacks it (a new worker), never over what the kernel holds.
func TestIfAbsent(t *testing.T) {
	s := sharedSession(t)
	var kept any
	if _, err := s.Eval(context.Background(), Cell{Source: "ia_x = 1", Out: []Var{{Name: "ia_x", Ptr: &kept}}}); err != nil {
		t.Fatal(err)
	}
	var held any = int64(100)
	out, err := s.Eval(context.Background(), Cell{Source: "ia_x", In: []Var{{Name: "ia_x", Ptr: &held, IfAbsent: true}}})
	if err != nil || out.Stdout != "1\n" {
		t.Fatalf("the kernel's value was overwritten: %q %v", out.Stdout, err)
	}
	var lost any = int64(41)
	out, err = s.Eval(context.Background(), Cell{Source: "ia_y + 1", In: []Var{{Name: "ia_y", Ptr: &lost, IfAbsent: true}}})
	if err != nil || out.Stdout != "42\n" {
		t.Fatalf("a variable the kernel lacks was not given: %q %v", out.Stdout, err)
	}
}

// A variable that changes type: the holder keeps its value, and the note names
// the new type, so that the next program declares it.
func TestVariableChangesType(t *testing.T) {
	s := sharedSession(t)
	var n int64 = 5
	notesMap, note := notes()
	_, err := s.Eval(context.Background(), Cell{Source: "ct_n = 2.5", Out: []Var{{Name: "ct_n", Ptr: &n}}, Note: note})
	if err != nil || n != 5 || notesMap["ct_n"] != "float64" {
		t.Fatalf("%v %d %v", err, n, notesMap)
	}
}

func TestGoType(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{int64(1), "int64"}, {1.5, "float64"}, {true, "bool"}, {"s", "string"}, {[]float64{}, "[]float64"}, {[]int64{}, "[]int64"},
		{interp.Matrix[float64]{}, "{github.com/go-embedded-julia/julia/interp}.Matrix[float64]"},
		{interp.Matrix[int64]{}, "{github.com/go-embedded-julia/julia/interp}.Matrix[int64]"},
	} {
		if got, ok := GoType(tc.v); !ok || got != tc.want {
			t.Errorf("GoType(%T) = %q %v", tc.v, got, ok)
		}
	}
	if Unsupported != gen.UnsupportedPrefix {
		t.Error("gen and jcells disagree on the note of a type that does not cross")
	}
	if _, ok := GoType(map[string]int{}); ok {
		t.Error("a map has a type")
	}
}

// Floats cross with every bit: a NaN payload, -0.0 and the infinities, in both
// directions, scalars and in vectors.
func TestFloatsCrossBitForBit(t *testing.T) {
	s := sharedSession(t)
	nan := math.Float64frombits(0x7ff8000000000123)
	snan := math.Float64frombits(0x7ff0000000000001)
	negz := math.Copysign(0, -1)
	vals := []float64{nan, snan, negz, math.Inf(1), math.Inf(-1), 1.5}
	var scalar, vec any = nan, vals
	_, err := s.Eval(context.Background(), Cell{
		Source: "bf_s = g_nan\nbf_v = g_vec\nbf_back = [reinterpret(UInt64, x) for x in g_vec]\nbf_nan = reinterpret(Float64, 0x7ff8000000000123)\nbf_negz = -0.0\nbf_inf = -Inf",
		In:     []Var{{Name: "g_nan", Ptr: &scalar}, {Name: "g_vec", Ptr: &vec}},
		Out:    nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	var s2, v2, nan2, negz2, inf2 any
	_, err = s.Eval(context.Background(), Cell{Source: "1", Out: []Var{{Name: "bf_s", Ptr: &s2}, {Name: "bf_v", Ptr: &v2}, {Name: "bf_nan", Ptr: &nan2}, {Name: "bf_negz", Ptr: &negz2}, {Name: "bf_inf", Ptr: &inf2}}})
	if err != nil {
		t.Fatal(err)
	}
	bits := math.Float64bits
	if bits(s2.(float64)) != bits(nan) || bits(nan2.(float64)) != 0x7ff8000000000123 || bits(negz2.(float64)) != bits(negz) || !math.IsInf(inf2.(float64), -1) {
		t.Fatalf("scalars: %x %x %x %v", bits(s2.(float64)), bits(nan2.(float64)), bits(negz2.(float64)), inf2)
	}
	got := v2.([]float64)
	for i := range vals {
		if bits(got[i]) != bits(vals[i]) {
			t.Errorf("vector[%d]: %x, want %x", i, bits(got[i]), bits(vals[i]))
		}
	}
}

// Empty vectors and matrices with a zero dimension cross, both ways.
func TestEmptyArraysCross(t *testing.T) {
	s := sharedSession(t)
	var ev, em, m03, mi any = []float64{}, []int64{}, interp.Matrix[float64]{Rows: 0, Cols: 3}, interp.Matrix[int64]{Rows: 1, Cols: 2, Data: []int64{7, 8}}
	out, err := s.Eval(context.Background(), Cell{Source: "(length(e_v), length(e_m), size(e_03), e_mi[1, 2])", In: []Var{{Name: "e_v", Ptr: &ev}, {Name: "e_m", Ptr: &em}, {Name: "e_03", Ptr: &m03}, {Name: "e_mi", Ptr: &mi}}})
	if err != nil || out.Stdout != "(0, 0, (0, 3), 8)\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
	var v, m, n30 any
	_, err = s.Eval(context.Background(), Cell{Source: "e_a = Float64[]\ne_b = zeros(Int64, 0, 3)\ne_c = zeros(3, 0)", Out: []Var{{Name: "e_a", Ptr: &v}, {Name: "e_b", Ptr: &m}, {Name: "e_c", Ptr: &n30}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := v.([]float64); !ok || len(got) != 0 {
		t.Fatalf("empty vector: %#v", v)
	}
	if got, ok := m.(interp.Matrix[int64]); !ok || got.Rows != 0 || got.Cols != 3 || len(got.Data) != 0 {
		t.Fatalf("0x3 matrix: %#v", m)
	}
	// A matrix with zero columns does not come out: go-embedded-julia's Get
	// refuses a 3 by 0 matrix (it checks the first dimension against the empty
	// memory), so the variable stays in Julia. Pinned so that a fix shows up.
	if n30 != nil {
		t.Fatalf("a 3x0 matrix crosses now (go-embedded-julia fixed it): %#v; remove this pin", n30)
	}
}

// A string that is not UTF-8 crosses byte for byte, both ways.
func TestInvalidUTF8Crosses(t *testing.T) {
	s := sharedSession(t)
	var in any = "\xff\xfeab"
	out, err := s.Eval(context.Background(), Cell{Source: "(sizeof(u_s), codeunits(u_s)[1], isvalid(u_s))", In: []Var{{Name: "u_s", Ptr: &in}}})
	if err != nil || out.Stdout != "(4, 0xff, false)\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
	var back any
	if _, err := s.Eval(context.Background(), Cell{Source: "u_t = String(UInt8[0xc3, 0x28, 0xff])", Out: []Var{{Name: "u_t", Ptr: &back}}}); err != nil {
		t.Fatal(err)
	}
	if back != "\xc3\x28\xff" {
		t.Fatalf("%q", back)
	}
}

// []int and int go in as Int64, a []int as an Int64 vector.
func TestIntSlicesCrossAsInt64(t *testing.T) {
	s := sharedSession(t)
	var xs any = []int{1, 2, 3}
	out, err := s.Eval(context.Background(), Cell{Source: "(typeof(i_xs), sum(i_xs))", In: []Var{{Name: "i_xs", Ptr: &xs}}})
	if err != nil || out.Stdout != "(Vector{Int64}, 6)\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
}

// The arrays a cell reads are bounded before they are copied: the kernel makes
// a []any of a slice, 16 bytes an element, before it checks the size, so a
// slice over the per-array cap is refused here, and the inputs together stay
// within the cell's memory budget.
func TestInputArraysAreBounded(t *testing.T) {
	s := bounded(t, runtime.RLimits{MaxAlloc: 16 * 1000}) // 1000 elements
	var big any = make([]float64, 1001)
	_, err := s.Eval(context.Background(), Cell{Source: "length(b_x)", In: []Var{{Name: "b_x", Ptr: &big}}})
	if msg := cellErr(t, err); !strings.Contains(msg, "more than its memory budget of 1000 array elements: b_x brings the total to 1001") {
		t.Fatalf("message %q", msg)
	}
	// 600 + 600 over 1000, each alone within it
	var a, b any = make([]int64, 600), interp.Matrix[float64]{Rows: 20, Cols: 30, Data: make([]float64, 600)}
	_, err = s.Eval(context.Background(), Cell{Source: "1", In: []Var{{Name: "b_a", Ptr: &a}, {Name: "b_b", Ptr: &b}}})
	if msg := cellErr(t, err); !strings.Contains(msg, "b_b brings the total to 1200") {
		t.Fatalf("message %q", msg)
	}
	// scalars and strings are not arrays: not counted
	var str any = strings.Repeat("x", 100000)
	if out, err := s.Eval(context.Background(), Cell{Source: "sizeof(b_s)", In: []Var{{Name: "b_s", Ptr: &str}}}); err != nil || out.Stdout != "100000\n" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
	// the session goes on
	if out := eval(t, s, "1 + 1"); out.Stdout != "2\n" {
		t.Fatalf("%+v", out)
	}
}

// One array over the VM's cap (2^24 elements) is refused with this package's
// message, without the copy: the heap the refusal allocates stays far under the
// 16 bytes an element the kernel's own copy would take (256 MiB here).
func TestOneInputArrayOverTheCapIsRefusedWithoutCopying(t *testing.T) {
	s := bounded(t, runtime.RLimits{MaxAlloc: runtime.Unbounded})
	big := make([]float64, (1<<24)+1)
	var v any = big
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	_, err := s.Eval(context.Background(), Cell{Source: "length(o_x)", In: []Var{{Name: "o_x", Ptr: &v}}})
	goruntime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("the refusal allocated %d MiB", alloc>>20)
	}
	if msg := cellErr(t, err); !strings.Contains(msg, "the Julia cell reads o_x, a Go []float64 of 16777217 elements; an array has at most 16777216 elements here") {
		t.Fatalf("message %q", msg)
	}
}
