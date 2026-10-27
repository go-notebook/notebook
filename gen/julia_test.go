package gen

import (
	"strings"
	"testing"
)

// A Julia cell is a stub that fails with JuliaNotRun; every cell that reads
// it, in any language and through any chain, is deferred saying so; the rest
// compiles and runs.
func TestGenerateJulia(t *testing.T) {
	im := newRubyImporter(t)
	cells := rubyCodes(
		Code{Source: "n := 10\n"},
		Code{Source: "xs = [i^2 for i in 1:n]\ntotal = sum(xs)\n", Lang: "julia"},
		Code{Source: "t := total + 1\n"},
		Code{Source: "u = t * 2\n", Lang: "ruby"},
		Code{Source: "println(total)\n", Lang: "julia"},
		Code{Source: "m := n * 2\nm\n"},
	)
	p := GenerateWith("nb.md", cells, im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	want := map[int]string{
		1: JuliaNotRun,
		2: "not run: it reads cell 1, a Julia cell, and Julia cells are not run yet",
		3: "not run: it reads cell 2, which is blocked by a Julia cell",
		4: JuliaNotRun,
	}
	for i, w := range want {
		if p.Deferred[i] != w {
			t.Errorf("cell %d: %q, want %q", i, p.Deferred[i], w)
		}
	}
	if _, ok := p.Deferred[5]; ok || len(p.Deferred) != 4 {
		t.Errorf("deferred %v", p.Deferred)
	}
	src := string(p.Source)
	if !strings.Contains(src, `panic(_nb.Deferred("Julia cells are parsed and checked, but not run yet`) || !strings.Contains(src, "m =  n * 2") {
		t.Errorf("program:\n%s", src)
	}
	// A syntax error in a Julia cell is a diagnostic at its line.
	p = GenerateWith("nb.md", rubyCodes(Code{Source: "y = 2\nx = (1,\n", Lang: "julia"}), im, Options{})
	if len(p.Diags) != 1 || p.Diags[0].Line != 3 || p.Diags[0].Col != 8 || p.Diags[0].Msg != "syntax error: Expected `)` or `,`" {
		t.Errorf("diags %+v", p.Diags)
	}
}
