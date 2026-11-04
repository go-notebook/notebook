package gen

import (
	"fmt"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// RPath is the import path of the package that runs R cells: this module's
// rcells, over go-embedded-r's evaluator.
const RPath = analysis.RPath

// rBackend generates the R cells: calls of the notebook's R session, which
// holds their variables. Nothing crosses to the other languages (see
// analysis.Isolated), so a cell declares no variable but a marker, volatile like
// a Ruby method's: a new worker has an empty R session, and the cells that
// define what a stale cell reads run again in it.
type rBackend struct{}

func (rBackend) Infer(c *Context, i int) {
	if c.WaitsOnDeferred(i) != "" {
		c.Defer(i, c.WaitsOnDeferred(i))
		return
	}
	c.Declare(Var{Name: rState(i), Cell: i, Type: "func()"})
	c.Done(i)
}

func (rBackend) Write(c *Context, b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_rcs.Run(_rc.Cell{Source: %q})\n}\n\n", c.FuncName(k), c.Code(k).Source)
}

func (rBackend) Runtime() (name, prelude string) { return "_rc", "var _rcs = _rc.New()\n\n" }

// rState names the marker of R cell i.
func rState(i int) string { return fmt.Sprintf("_nbr%d", i) }

func init() { RegisterBackend(analysis.R, rBackend{}) }
