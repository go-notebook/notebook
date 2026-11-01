package gen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
)

// checkOnlyLang is a language that never runs and defines the variable named
// by its cell's source; it is not Julia, so nothing here is Julia's.
type checkOnlyLang struct{}

func (checkOnlyLang) Name() string { return "chk" }
func (checkOnlyLang) Analyze(src string, _ analysis.Limits) (analysis.Names, []analysis.Diagnostic, error) {
	return analysis.Names{Defines: []string{strings.TrimSpace(src)}}, nil, nil
}
func (checkOnlyLang) Crosses(_, _ *analysis.Cell, _ string) bool { return true }
func (checkOnlyLang) Run() analysis.Run {
	return analysis.Run{
		NotRun:       "chk cells are only checked",
		Blocked:      func(c int) string { return fmt.Sprintf("blocked by chk %d", c) },
		Reads:        func(c int) string { return fmt.Sprintf("reads chk %d", c) },
		ReadsBlocked: func(c int) string { return fmt.Sprintf("reads a cell blocked by chk %d", c) },
	}
}

// A check-only language other than Julia is deferred through the registry
// alone, with its own messages.
func TestGenerateCheckOnlyLanguage(t *testing.T) {
	analysis.Register(checkOnlyLang{})
	t.Cleanup(func() { analysis.Unregister("chk") })
	im := newRubyImporter(t)
	p := GenerateWith("nb.md", rubyCodes(
		Code{Source: "v", Lang: "chk"},
		Code{Source: "a := v\n"},
		Code{Source: "b := a\n"},
		Code{Source: "c := 1\n"},
	), im, Options{})
	if len(p.Diags) > 0 {
		t.Fatal(p.Diags)
	}
	want := map[int]string{0: "chk cells are only checked", 1: "reads chk 0", 2: "reads a cell blocked by chk 1"}
	for i, w := range want {
		if p.Deferred[i] != w {
			t.Errorf("cell %d: %q, want %q", i, p.Deferred[i], w)
		}
	}
	if _, ok := p.Deferred[3]; ok {
		t.Errorf("deferred %v", p.Deferred)
	}
}
