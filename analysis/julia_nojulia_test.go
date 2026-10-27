//go:build nojulia

package analysis

import "testing"

// Without the parser, a Julia cell is an error of its own.
func TestNoJulia(t *testing.T) {
	c := AnalyzeJuliaCell(2, "x = 1")
	if len(c.Errors) != 1 || c.Errors[0].Msg != JuliaParserNotBuilt || c.Errors[0].Cell != 2 || c.Lang != Julia {
		t.Errorf("%+v", c)
	}
}
