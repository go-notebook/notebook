package analysis

import (
	"reflect"
	"testing"
)

func TestAnalyzeRubyCell(t *testing.T) {
	for _, c := range []struct {
		src                  string
		defines, uses, funcs []string
		consts               []string
		display              bool
	}{
		{"x = 1\ny = x + z", []string{"x", "y"}, []string{"z"}, nil, nil, false},
		{"a, b = f(1), g\na", []string{"a", "b"}, []string{"f", "g"}, nil, nil, true},
		{"xs.map { |v| v * k }", nil, []string{"k", "xs"}, nil, nil, true},
		{"def sq(n) = n * n * scale\nsq(w)", []string{"sq"}, []string{"scale", "w"}, []string{"sq"}, nil, true},
		{"K = 3\nclass Foo < Base\n  def bar = helper(K)\nend", []string{"K", "Foo"}, []string{"Base", "helper"}, nil, []string{"K", "Foo"}, false},
		{"_tmp = 2\nputs _tmp", nil, []string{"puts"}, nil, nil, true},
		{"if c\n  r = 1\nend\nr", []string{"r"}, []string{"c"}, nil, nil, true},
		{"n += 1", nil, []string{"n"}, nil, nil, false},
		{"[1].each { |i; j| j = i }\nlam = ->(q) { q + outer }", []string{"lam"}, []string{"lambda", "outer"}, nil, nil, false},
		{"module M\n  X = Y\nend", []string{"M"}, []string{"Y"}, nil, []string{"M"}, false},
		{"def kw(a:, b: c, **r, &blk) = a + b + d + blk.call + r.size", []string{"kw"}, []string{"c", "d"}, []string{"kw"}, nil, false},
	} {
		got := AnalyzeRubyCell(0, c.src)
		if len(got.Errors) > 0 {
			t.Fatalf("%q: %v", c.src, got.Errors)
		}
		if !reflect.DeepEqual(got.Defines, c.defines) || !reflect.DeepEqual(got.Uses, c.uses) ||
			!reflect.DeepEqual(got.Funcs, c.funcs) || !reflect.DeepEqual(got.Consts, c.consts) || got.RubyDisplay != c.display {
			t.Errorf("%q:\n defines %v uses %v funcs %v consts %v display %v", c.src, got.Defines, got.Uses, got.Funcs, got.Consts, got.RubyDisplay)
		}
	}
}

func TestAnalyzeRubyErrors(t *testing.T) {
	c := AnalyzeRubyCell(2, "x = 1\ny = (\n")
	if len(c.Errors) != 1 || c.Errors[0].Cell != 2 {
		t.Fatalf("errors = %v", c.Errors)
	}
	t.Log(c.Errors[0].Msg, c.Errors[0].Pos)
}

func TestMixedGraph(t *testing.T) {
	g := AnalyzeSources([]Source{
		{Go, "xs := []float64{1, 2, 3}\nfunc sq(x float64) float64 { return x * x }"},
		{Ruby, "m = xs.sum / xs.size\nlen = 3\ndef cube(v) = v ** 3\nsq(2)"},
		{Go, "y := m * 2\nn := len(xs)"},
		{Ruby, "cube(m) + len"},
	})
	if len(g.Errors) > 0 {
		t.Fatal(g.Errors)
	}
	want := [][]int{nil, {0}, {0, 1}, {1}}
	if !reflect.DeepEqual(g.Deps, want) {
		t.Fatalf("deps = %v, want %v", g.Deps, want)
	}
	for _, n := range []string{"len", "type", "x9", "1a", ""} {
		_ = GoName(n)
	}
	if GoName("len") || GoName("type") || !GoName("x9") || GoName("9x") || GoName("") || GoName("é") {
		t.Fatal("GoName")
	}
}
