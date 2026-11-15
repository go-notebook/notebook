package gen

import (
	"fmt"
	"strings"

	"github.com/go-notebook/notebook/analysis"
)

// JuliaPath is the import path of the package that runs Julia cells: this
// module's jcells, over go-embedded-julia's kernel.
const JuliaPath = analysis.JuliaPath

// UnsupportedPrefix starts the note of a Julia variable whose type does not
// cross: UnsupportedPrefix + the Julia type (jcells.Unsupported; gen does not
// import jcells, which links the interpreter).
const UnsupportedPrefix = "!"

// juliaBackend generates the Julia cells: calls of the notebook's Julia
// session, which holds their globals, and takes and gives the variables that
// cross. Those are typed as Ruby's are (Options.RubyTypes, the Go type the
// value had when the cell last ran: int64, float64, bool, string, []float64,
// []int64, interp.Matrix[T]); a type that does not cross is noted with
// UnsupportedPrefix, its variable stays in Julia, and a Go cell that reads it is
// deferred. A cell also declares a marker, volatile like R's: a new worker has an
// empty kernel, and the cells that define what a stale cell reads (functions,
// structs, variables of types that do not cross) run again in it.
type juliaBackend struct{}

func (juliaBackend) Infer(c *Context, i int) {
	if why := c.WaitsOnDeferred(i); why != "" {
		c.Defer(i, why)
		return
	}
	cell := c.Cell(i)
	for _, name := range cell.Defines {
		if contains(cell.Funcs, name) || contains(cell.Consts, name) {
			continue
		}
		v := Var{Name: name, Cell: i, Julia: true, Type: "any"}
		if !analysis.GoName(name) {
			v.Ident = "_nbjl_" + name
		}
		switch t, ok := c.Options().RubyTypes[name]; {
		case !ok:
			c.Unknown(name)
		case strings.HasPrefix(t, UnsupportedPrefix):
			c.Unsupported(name, strings.TrimPrefix(t, UnsupportedPrefix))
		default:
			if r := c.ResolveType(t); r != "" {
				v.Type = r
			}
		}
		c.Declare(v)
	}
	c.Declare(Var{Name: juliaState(i), Cell: i, Type: "func()"})
	c.Done(i)
}

func (juliaBackend) Write(c *Context, b *strings.Builder, k int) {
	fmt.Fprintf(b, "func %s(_k *_nb.Cell) {\n\t_jcs.Run(_jc.Cell{\n\t\tSource: %q,\n\t\tIn: []_jc.Var{", c.FuncName(k), c.Code(k).Source)
	for i, v := range c.Inputs(k) {
		if i > 0 {
			b.WriteString(", ")
		}
		// a Julia cell's own variable is given from its holder only when the
		// kernel lacks it (a new worker); another language's always
		fmt.Fprintf(b, "{Name: %q, Ptr: &%s, IfAbsent: %v}", v.Name, v.ident(), v.Julia)
	}
	b.WriteString("},\n\t\tOut: []_jc.Var{")
	n := 0
	for _, v := range c.Vars() {
		if v.Cell != k || !v.Julia {
			continue
		}
		if n > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "{Name: %q, Ptr: &%s}", v.Name, v.ident())
		n++
	}
	b.WriteString("},\n\t\tNote: _k.Note,\n\t})\n}\n\n")
}

func (juliaBackend) Runtime() (name, prelude string) { return "_jc", "var _jcs = _jc.New()\n\n" }

// juliaState names the marker of Julia cell i.
func juliaState(i int) string { return fmt.Sprintf("_nbj%d", i) }

func init() { RegisterBackend(analysis.Julia, juliaBackend{}) }
