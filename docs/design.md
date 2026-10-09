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

Top-level `func`, `type` and `import` are declarations of the notebook. The rest is the cell's statements, run in order. The names a cell defines at the top level (`:=`, `var`, `const`, `func`, `type`) are notebook variables, readable from every cell. A final expression is the cell's display value; a call with several results (`fmt.Println`'s `n, err`) is run for its effect and not shown.

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

- **A cell's key** is a hash of its source, the import paths of the packages it refers to, and the keys of the cells it reads. Its position is not part of it, so moving cells runs nothing; neither does an import another cell adds.
- **An unchanged key keeps its result.** After a rebuild, the variables the cell made are handed to the new worker, not computed again.
- **Values are encoded** with their own `MarshalBinary` when they have one, and with gob when every field they can hold is exported (gob would silently drop the others). Anything else is **volatile**: its cell runs again, but only when a cell that reads it has to run.
- **Results and values are kept on disk** in the work directory, so a new kernel, or the next `notebook run`, starts where the last one stopped.
- **A failing cell stops its readers**, which report why. A worker that dies (`os.Exit`, a crash outside the recovered call) is restarted on the next run.

This assumes a cell computes the same values from the same inputs. A cell that reads the clock or an unseeded generator should be run explicitly (`Kernel.RunCell`).

### Output capture

What a cell prints, through `os.Stdout`, the `println` builtin or C code, is captured by pointing file descriptors 1 and 2 at a pipe for the duration of the cell. The protocol replies travel on a duplicate of the original standard output, so a cell can never forge one. Systems without `dup2` (Windows, js, wasip1) swap `os.Stdout` and `os.Stderr` instead, and do not capture `println`.

### Display

`runtime.Displayer` is a MIME bundle: `Display() map[string][]byte` with `text/plain`, `text/html`, `image/svg+xml`, `image/png`... Values without one show their `%v`.

## Not yet

- A Jupyter wire-protocol front end (JupyterLab, VS Code), and a go-widgets native and wasm UI.
- Running independent branches of the graph concurrently.
- Real LaTeX in prose through go-tex; for now `$...$` stays as text.
- `MarshalBinary` on go-ndarray arrays, so that arrays are kept rather than volatile.
- Module versions in the cell key (today a `go get -u` does not invalidate results).
