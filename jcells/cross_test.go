package jcells

import (
	"context"
	"strings"
	"testing"

	"github.com/go-embedded-julia/julia/interp"

	"github.com/go-notebook/notebook/gen"
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
	for _, want := range []string{"the Julia cell reads bad_s, a Go []string that Julia cannot take", "[]string", "Julia takes int, int64, float64, bool, string, []float64, []int64 and interp.Matrix"} {
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
	eval(t, s, "ia_x = 1")
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
