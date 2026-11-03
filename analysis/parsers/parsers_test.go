package parsers

import (
	"testing"

	"github.com/go-notebook/notebook/analysis"
)

// Importing the package is enough: Julia and R cells are parsed, not refused.
func TestImportEnables(t *testing.T) {
	g := analysis.AnalyzeSources([]analysis.Source{{Lang: analysis.Julia, Source: "x = 1"}, {Lang: analysis.R, Source: "y <- 1"}})
	for i, want := range []string{"x", "y"} {
		c := g.Cells[i]
		if len(c.Errors) != 0 || len(c.Defines) != 1 || c.Defines[0] != want {
			t.Errorf("cell %d: %+v", i, c)
		}
	}
}
