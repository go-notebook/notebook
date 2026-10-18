# Design

## What exists, and what each gets wrong

| | execution | state between cells | order | speed |
|---|---|---|---|---|
| Jupyter (Python) | interpreter, one process | values, hidden and mutable | whatever order the cells were clicked in | interpreted; numpy in C |
| gophernotes | gomacro interpreter | values | clicked order | interpreted Go; generics partial |
| GoNB | compiles the cell plus the declarations it memorised, then runs it | **declarations only — values are computed again on every run** (it ships a `cache` package as the workaround) | clicked order | native |
| marimo (Python) | interpreter | values | **reactive graph from static analysis**; one definition per name | interpreted |

None has native speed, kept values and a reproducible order together. go-notebook is built to have all three.

## Decisions

### A notebook is a Markdown file

Its ` ```go ` blocks are the cells; everything else is prose, kept byte for byte (`format.Parse(src).String() == src` for every input; it is fuzzed). GitHub and every Markdown viewer show a notebook as a document, and a diff of a notebook is a diff of text. No output is ever written into it.

A valid Go file was considered first and rejected. Go allows no statements at the top level, and `x := load(path)` or `fmt.Println(x)` at the top level of a cell is most of what a notebook is. Wrapping every cell in a function costs the user more than editor support on the raw file gains. The kernel produces the valid Go program instead; see below.

### A cell is Go with top-level statements

```go
import "math"
func sq(x float64) float64 { return x * x }
y := sq(math.Pi)
y
```

Top-level `func`, `type` and `import` are declarations of the notebook. The rest is the cell's statements, run in order. The names a cell defines at the top level (`:=`, `var`, `const`, `func`, `type`) are notebook variables, readable from every cell. A final expression is the cell's display value. A call returning `(value, error)` shows the value, and a non-nil error fails the cell, so its readers do not run on a value that is not there. Other calls with several results, and `(int, error)` — the byte counts of `fmt.Println` and `Write` —, are run for their effect and not shown.

Three rules make the order a function of the code, as in marimo:

- **One definition per name.** Go already forbids two package-level declarations of a name, which gives this rule for free.
- **No assignment to another cell's variable.** The dependency graph would not see the change. Mutation through a method or an element cannot be seen statically and is not refused (marimo makes the same choice).
- **Private names stay in their cell**: a name starting with `_`, and `err`, which nearly every cell declares and none means to share.

### Dependencies come from syntax; types come from go/types

`analysis` finds each cell's definitions and free identifiers from the parse alone, so it works on code that does not compile yet. Those give the graph, its order and its errors (cycles, double definitions, foreign assignments).

`gen` writes the program. Each cell becomes a function. Its variables become package-level variables, which needs their types: `gen` type-checks the cells one by one in dependency order to learn them. The cells' text is kept as it is and edited in place: `:=` becomes `= `, `var x T` becomes `x = *new(T)` so that running the cell again resets it, constants move to package level, and the display value is wrapped in `_k.Show(...)`. `/*line nb.md:L:C*/` directives point every line back into the notebook, so compiler errors and panic stacks name the notebook's own lines.

A variable whose type cannot be written outside its package (an unexported type) cannot be shared. The error says so and suggests a private name.

### Native code, kept values

The kernel compiles the program into a worker process. Go's build cache makes rebuilding after a one-cell edit cost about a tenth of a second here. The kernel then does the least work that brings the results up to date:

- **A cell's key** is a hash of its source, the import paths of the packages it refers to, the Go toolchain's version, the work module's `go.sum` when the cell uses a package outside the standard library, and the keys of the cells it reads. Its position is not part of it, so moving cells runs nothing; neither does an import another cell adds. Upgrading Go, or a dependency, runs again what it could change.
- **An unchanged key keeps its result.** After a rebuild, the variables the cell made are handed to the new worker, not computed again.
- **Values are encoded** with their own `MarshalBinary` when they have one, and with gob when every field they can hold is exported (gob would silently drop the others). Anything else is **volatile**: its cell runs again, but only when a cell that reads it has to run.
- **Results and values are kept on disk** in the work directory, so a new kernel, or the next `notebook run`, starts where the last one stopped.
- **A failing cell stops its readers**, which report why. A worker that dies (`os.Exit`, a crash outside the recovered call) is restarted on the next run.

This assumes a cell computes the same values from the same inputs. A cell that reads the clock or an unseeded generator should be run explicitly (`Kernel.RunCell`).

### Ruby cells

A ` ```ruby ` block is a Ruby cell. The Ruby is [go-embedded-ruby/ruby](https://github.com/go-embedded-ruby/ruby) ("rbgo"), a bytecode VM written in Go, with the [go-scikit bindings](https://github.com/go-scikit/ruby) registered: `Stats`, `Special`, `Optimize`, `Integrate`, `DataFrame`, `Plot` over the same Go libraries a Go cell imports, and rbgo's own `NDArray`. A notebook with Ruby cells adds `github.com/go-scikit/ruby` to the work module, and the worker is built with `-tags rbgo_core` (the Ruby language and its numeric stack, no gems). The worker holds one interpreter, so the Ruby cells share one top-level binding.

- **Names.** `analysis` reads a Ruby cell with rbgo's own parser, [go-ruby-parser](https://github.com/go-ruby-parser/parser). The cell defines the locals it assigns at its top level, and the methods, constants, classes and modules it defines there. It uses the names it reads before assigning them: a local, a method called without a receiver, a constant. The rules of Go cells apply too: one definition per name, no assignment to another cell's variable, and `_x` stays in its cell.
- **Across languages only variables are shared.** A Go cell sees a Ruby cell's top-level locals, but not its methods, constants or classes. It does not see a local named like a Go keyword or predeclared identifier either (`len`, `type`, `int`): that local stays Ruby's. A Ruby cell sees a Go cell's variables, but not its funcs or types. A Go variable whose name starts with an upper-case letter is a Ruby constant there.
- **Go to Ruby**: before a Ruby cell runs, each Go variable it reads is set as a Ruby local, converted by rbgo:
  - numbers, strings and booleans as such;
  - slices as Arrays, maps as Hashes;
  - an `*ndarray.Array` as the NDArray itself, not a copy;
  - any other value (a `*plot.Figure`, a struct) as an object that comes back to Go as itself.
- **Ruby to Go**: after a Ruby cell runs, each local it assigns is stored in a Go variable whose type is the type of its value ([cells.GoValue](https://github.com/go-scikit/ruby/tree/main/cells)):

  | Ruby value | Go type |
  | --- | --- |
  | nil | `any` (nil) |
  | true, false | `bool` |
  | Integer | `int64` (`*big.Int` when it does not fit) |
  | Float | `float64` |
  | String | `string` |
  | Symbol | `ruby.Symbol` |
  | Complex | `complex128` |
  | Rational | `*big.Rat` |
  | NDArray | `*ndarray.Array`, the same array |
  | Array of all Floats, all Integers, all Strings, all booleans | `[]float64`, `[]int64`, `[]string`, `[]bool` |
  | any other Array | `[]any` |
  | Hash with String keys | `map[string]any` |
  | Hash with Symbol keys | `map[ruby.Symbol]any` |
  | any other Hash | `map[any]any` |
  | a go-scikit object | the Go value it wraps: `*plot.Figure`, `stats.TTestResult`, `*optimize.Result`, … |
  | any other Ruby object | `ruby.Value` (go-embedded-ruby), which a Go cell can `Call` |
- **The type comes from the value, so it is known once the cell has run.** The worker reports the type of each variable a Ruby cell assigns, and the kernel keeps these types in the work directory. A Go cell that reads a Ruby variable of a type not known yet is deferred, together with whatever reads it: the program is built without it, the Ruby cell runs, and the deferred cells are compiled with the type and run, all in the same `Run`. A later change of type does the same for the Go cells that read the variable. A variable that is nil, or has no nameable Go type, is an `any`.
- **Keys and keeping**: a Ruby cell's key is its source and the keys of the cells it reads, plus `go.sum`, which pins rbgo and the bindings. Ruby locals are kept like Go variables, through their Go holders: a new worker sets them back into its interpreter before the first cell that reads them. A cell that defines methods or constants is volatile, because they live only in the interpreter: a new worker runs it again before a cell that needs them.
- **Display and errors**: a cell that ends in an expression shows its value. A figure shows itself (SVG), and anything else shows its `inspect`, so an NDArray appears in NumPy's form. An exception fails the cell with `file:line: message (Class)` at the notebook's line, and the backtrace frames in the notebook.

### Output capture

What a cell prints, through `os.Stdout`, the `println` builtin or C code, is captured by pointing file descriptors 1 and 2 at a pipe for the duration of the cell. The protocol replies travel on a duplicate of the original standard output, so a cell can never forge one. Systems without `dup2` (Windows, js, wasip1) swap `os.Stdout` and `os.Stderr` instead, and do not capture `println`.

### Math in prose

`$...$` and `$$...$$` in prose are typeset by [go-tex/math](https://github.com/go-tex/math) into SVG glyph outlines: TeX's layout from the OpenType MATH table (STIX Two Math), with no MathJax, no script and no network, so the exported page stays static. An inline formula sits on the text's baseline (go-tex returns its depth). Dollar signs follow pandoc's rule, so "costs $20 and $30" stays text. A formula go-tex cannot typeset is shown as its source, marked, with the reason as a tooltip.

### Editing in the browser

`notebook serve` shows the notebook and lets the page change it: double-click a cell (or its edit button) to edit it, Ctrl/⌘+Enter to save, Escape to cancel, and buttons to add a code or text cell or delete one. Each edit rewrites the Markdown file — through `format`, so everything else stays byte for byte — and the watch loop runs what changed, as for an edit made in any other editor. The file stays the only source of truth.

Writing the notebook is running code, so the edit endpoints take two guards. Every change carries the session's random token in a header: only the page served here knows it, and another site can neither read it nor send a custom header without a CORS preflight this server never grants. And the Host must be a loopback name, which defeats DNS rebinding.

### Display

`runtime.Displayer` is a MIME bundle: `Display() map[string][]byte` with `text/plain`, `text/html`, `image/svg+xml`, `image/png`... Values without one show their `%v`.

## Not yet

- A Jupyter wire-protocol front end (JupyterLab, VS Code), and a go-widgets native and wasm UI.
- Running independent branches of the graph concurrently.
- `MarshalBinary` on go-ndarray arrays, so that arrays are kept rather than volatile.
- Ruby cells in a Jupyter front end; a Ruby cell cannot call a Go func of the notebook (funcs are not values Ruby can see).
