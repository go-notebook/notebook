package gen

import (
	"fmt"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// FortranPath is the import path of the package that runs Fortran cells: this
// module's fcells, over go-embedded-fortran's evaluator.
const FortranPath = analysis.FortranPath

// fortranBackend generates the Fortran cells: calls of the notebook's Fortran
// session, which holds their modules and variables. Nothing crosses to the other
// languages (see analysis.Isolated), so a cell declares no variable but a
// marker, volatile like R's: a new worker has an empty Fortran session, and the
// cells that define what a stale cell reads run again in it.
type fortranBackend struct{}

// Infer never defers a cell: a Fortran cell reads only Fortran cells (Isolated),
// and a Fortran cell is never deferred itself.
func (fortranBackend) Infer(c *Context, i int) {
	c.Declare(Var{Name: fortranState(i), Cell: i, Type: "func()"})
	c.Done(i)
}

func (fortranBackend) Write(c *Context, b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_fcs.Run(_fc.Cell{Source: %q})\n}\n\n", c.FuncName(k), c.Code(k).Source)
}

func (fortranBackend) Runtime() (name, prelude string) { return "_fc", "var _fcs = _fc.New()\n\n" }

// fortranState names the marker of Fortran cell i.
func fortranState(i int) string { return fmt.Sprintf("_nbf%d", i) }

func init() { RegisterBackend(analysis.Fortran, fortranBackend{}) }
