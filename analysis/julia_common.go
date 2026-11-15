package analysis

// Julia is the language of ```julia cells.
const Julia = "julia"

// The package that runs Julia cells in the worker: this module's jcells, over
// go-embedded-julia's kernel (interp.Kernel). It belongs to this module, whose
// version the work module already requires.
const (
	JuliaPath   = "github.com/go-notebook/notebook/jcells"
	JuliaModule = "github.com/go-notebook/notebook"
)
