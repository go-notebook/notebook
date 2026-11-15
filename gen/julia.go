package gen

import (
	"fmt"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// JuliaPath is the import path of the package that runs Julia cells: this
// module's jcells, over go-embedded-julia's kernel.
const JuliaPath = analysis.JuliaPath

// juliaBackend generates the Julia cells: calls of the notebook's Julia
// session, which holds their globals. Nothing crosses to the other languages
// (see analysis.Isolated), so a cell declares no variable but a marker,
// volatile like R's: a new worker has an empty Julia session, and the cells
// that define what a stale cell reads run again in it.
type juliaBackend struct{}

// Infer never defers a cell: a Julia cell reads only Julia cells (Isolated),
// and a Julia cell is never deferred itself.
func (juliaBackend) Infer(c *Context, i int) {
	c.Declare(Var{Name: juliaState(i), Cell: i, Type: "func()"})
	c.Done(i)
}

func (juliaBackend) Write(c *Context, b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_jcs.Run(_jc.Cell{Source: %q})\n}\n\n", c.FuncName(k), c.Code(k).Source)
}

func (juliaBackend) Runtime() (name, prelude string) { return "_jc", "var _jcs = _jc.New()\n\n" }

// juliaState names the marker of Julia cell i.
func juliaState(i int) string { return fmt.Sprintf("_nbj%d", i) }

func init() { RegisterBackend(analysis.Julia, juliaBackend{}) }
