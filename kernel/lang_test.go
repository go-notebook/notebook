package kernel

import (
	"reflect"
	"strings"
	"testing"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
)

// rtLang is a language that links a Go package, as R and C will.
type rtLang struct{}

func (rtLang) Name() string { return "rt" }
func (rtLang) Analyze(string, analysis.Limits) (analysis.Names, []analysis.Diagnostic, error) {
	return analysis.Names{}, nil, nil
}
func (rtLang) Crosses(_, _ *analysis.Cell, _ string) bool { return false }
func (rtLang) Run() analysis.Run                          { return analysis.Run{Runs: true} }
func (rtLang) Runtime() analysis.Module {
	return analysis.Module{Path: "example.com/rt/cells", Module: "example.com/rt", Version: "v1.0.0"}
}

func TestRuntimeLanguages(t *testing.T) {
	analysis.Register(rtLang{})
	t.Cleanup(func() { analysis.Unregister("rt") })
	cells := []gen.Code{{Lang: "go"}, {Lang: "rt"}, {Lang: "ruby"}, {Lang: "rt"}, {Lang: "julia"}, {Lang: "nonesuch"}}
	if got := runtimeLangs(cells); !reflect.DeepEqual(got, []string{"rt", "ruby"}) {
		t.Fatalf("runtimeLangs = %v", got)
	}
	k := &Kernel{langs: []string{"rt", "ruby"}}
	mod := k.goMod()
	for _, want := range []string{"require example.com/rt v1.0.0\n", "require " + analysis.RubyModule + " " + DefaultRubyVersion + "\n"} {
		if !strings.Contains(mod, want) {
			t.Errorf("goMod lacks %q:\n%s", want, mod)
		}
	}
	// Versions override a language's default; RubyVersion wins for Ruby.
	k.opt.Versions = map[string]string{"rt": "v2.0.0", "ruby": "v0.3.0"}
	k.opt.RubyVersion = "v0.4.0"
	mod = k.goMod()
	for _, want := range []string{"require example.com/rt v2.0.0\n", "require " + analysis.RubyModule + " v0.4.0\n"} {
		if !strings.Contains(mod, want) {
			t.Errorf("goMod lacks %q:\n%s", want, mod)
		}
	}
	k.opt.RubyVersion = ""
	if !strings.Contains(k.goMod(), analysis.RubyModule+" v0.3.0\n") {
		t.Errorf("Versions[ruby] ignored:\n%s", k.goMod())
	}
}
