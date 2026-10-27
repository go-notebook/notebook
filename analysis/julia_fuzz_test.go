//go:build !nojulia

package analysis

import (
	"strings"
	"testing"
)

// bigJuliaCell is the corpus repeated to about 100 KB, a large notebook cell.
func bigJuliaCell() string {
	var b strings.Builder
	for b.Len() < 100<<10 {
		for _, s := range juliaCorpus {
			if !strings.HasPrefix(s, "module") { // a module must be at the top level of its own
				b.WriteString(s + "\n")
			}
		}
	}
	return b.String()
}

// BenchmarkAnalyzeJuliaCell is the whole analysis; BenchmarkParseJuliaCell
// the parse alone, so that the walk's share shows.
func BenchmarkAnalyzeJuliaCell(b *testing.B) {
	src := bigJuliaCell()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		AnalyzeJuliaCell(0, src)
	}
}

func BenchmarkParseJuliaCell(b *testing.B) {
	src := bigJuliaCell()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		_, _ = parseJulia(src)
	}
}

// FuzzAnalyzeJuliaCell: any text is analysed without a panic, and no tree
// the parser gives reaches the walker's last-resort recovery.
func FuzzAnalyzeJuliaCell(f *testing.F) {
	for _, s := range juliaCorpus {
		f.Add(s)
	}
	for _, s := range []string{"0:0:<", "x = (1,", "function", "struct", "do", "let", "for", "@m x", "a::", "(;a, b) = nt"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		c := AnalyzeJuliaCell(0, src)
		for _, e := range c.Errors {
			if strings.Contains(e.Msg, "unexpected tree") {
				t.Fatalf("%q: the walker panicked", src)
			}
		}
	})
}
