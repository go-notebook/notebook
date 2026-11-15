<p align="center"><img src="https://raw.githubusercontent.com/go-notebook/brand/main/social/go-notebook.png" alt="go-notebook/notebook" width="720"></p>

# notebook — go-notebook

[![Docs](https://img.shields.io/badge/docs-hugo%20%2B%20hextra-047857)](https://go-notebook.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27.2%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](.github/workflows/ci.yml)

**Go notebooks with native speed, kept values and a reproducible order.**

🌐 **[Website](https://go-notebook.github.io)** · 📚 **[Documentation](https://go-notebook.github.io/docs/)**

A notebook is a Markdown file whose ` ```go ` blocks are cells — and ` ```ruby ` blocks too, in a pure-Go Ruby with the same numerical libraries, sharing their variables with the Go cells. The kernel:

- **compiles it** into a real Go program, so cells run at native speed;
- **orders the cells by what they use**, worked out from the code itself as marimo does for Python, not by the order you ran them in;
- **runs only what changed.** A cell whose code and inputs did not change keeps its result, and its values are handed over instead of being computed again. That holds across rebuilds and across runs: results and values are kept on disk.

````markdown
# Spectrum

```go
import "math"

n := 1_000_000
```

The sum below runs once; editing the last cell does not run it again.

```go
sum := 0.0
for i := 1; i <= n; i++ {
	sum += 1 / float64(i*i)
}
```

```go
math.Sqrt(6 * sum)
```
````

```console
$ notebook run spectrum.md
── cell 0 (line 4, ran in 11µs)
── cell 1 (line 12, ran in 2.065ms)
── cell 2 (line 19, ran in 30µs)
   3.1415916986605086
$ # change the last cell to math.Pi - math.Sqrt(6*sum), then:
$ notebook run spectrum.md
── cell 0 (line 4, kept)
── cell 1 (line 12, kept)
── cell 2 (line 19, ran in 48µs)
   9.54929284535666e-07
```

The second run is a new process: `sum` comes back from the kernel's store, it is not summed again.

## Use

```sh
go install github.com/go-notebook/notebook/cmd/notebook@latest

notebook run analysis.md     # run it, print each cell's results
notebook html analysis.md    # run it, write analysis.html (no scripts, no external resources; $\TeX$ math typeset by go-tex)
notebook watch analysis.md   # run it on every save and keep analysis.html current
notebook serve analysis.md   # the same in your browser at http://127.0.0.1:8888: edit cells there too
notebook irb                 # an interactive Ruby session, as irb, on the same Ruby and bindings
```

Edit the `.md` in any editor, or in the page `serve` shows (double-click a cell, Ctrl/⌘+Enter to save): the file is the only source of truth, and only what changed runs again.

Flags are POSIX, and each verb lists its own with `notebook <verb> --help`:

| Flag | Verbs | |
|---|---|---|
| `-o`, `--output FILE` | html, watch | the page to write (default: the notebook's name with `.html`) |
| `--addr HOST:PORT` | serve | where to listen (default `127.0.0.1:8888`) |
| `--interval D` | watch, serve | how often the file is looked at (default `300ms`) |
| `-t`, `--timeout D` | all | stop a cell that runs longer (default: no limit) |
| `-m`, `--memory MiB` | all | stop a cell whose worker holds more heap (default: no limit) |
| `-w`, `--work DIR` | all | the work directory (default: under the user cache directory) |
| `--runtime-src DIR` | all | run against a go-notebook source tree instead of a released runtime |
| `-s`, `--save FILE` | irb | write the session, at its end, as a notebook that `notebook run` runs |
| `--svg-dir DIR` | irb | save each figure an input shows as an SVG file there |
| `--history FILE`, `--history-size N` | irb | the history file and how many inputs it keeps (default 1000; 0: none) |

For `irb`, `--timeout` and `--memory` apply to each input.

Releases up to v0.3 read flags with one dash (`-timeout 30s`); that spelling is still accepted, with a note on stderr.

[examples/spectrum.md](examples/spectrum.md) finds two tones in Gaussian noise with [go-ndarray](https://github.com/go-ndarray/ndarray) (arrays, `fft.RFFT`, a seeded generator that gives NumPy's numbers for the same seed) and draws the signal and its spectrum with [go-plot](https://github.com/go-plot/plot).

## Cells

A cell is Go with statements allowed at the top level:

```go
import "strings"

func words(s string) []string { return strings.Fields(s) }

w := words("to be or not to be")
len(w) // the final expression is shown
```

- Names defined at the top level are shared with every cell. Each name is defined by **one** cell, and a cell may not assign to another cell's variable. That is what makes the order a function of the code.
- Names starting with `_`, and `err`, stay local to their cell.
- A final call returning `(value, error)` shows the value; a non-nil error fails the cell.
- A value with a `Display() map[string][]byte` method shows itself as HTML, SVG or PNG.
- Errors point at the notebook's own lines. A panic stops the cells that read the failing one, and they say why.

## Ruby cells

A ` ```ruby ` block runs in [rbgo](https://github.com/go-embedded-ruby/ruby), a Ruby written in Go, with the [go-scikit bindings](https://github.com/go-scikit/ruby): `Stats`, `Special`, `Optimize`, `Integrate`, `DataFrame` and `Plot` over the same Go libraries, and rbgo's `NDArray`. Ruby cells share one interpreter, so their top-level locals persist from cell to cell. The two languages share their variables:

- A Go variable a Ruby cell reads becomes a Ruby local of the same name. A `[]float64` becomes an Array, and an `*ndarray.Array` stays the same NDArray.
- A Ruby local a Go cell reads becomes a Go variable typed from its value: a Float is a `float64`, an Array of Floats a `[]float64`, an NDArray an `*ndarray.Array`, a fitted `Stats::Gamma` the `stats.Gamma` itself.

Dependencies, caching and re-runs work across languages as for Go alone: a Ruby cell re-runs only when it or a cell it reads changes. The last expression shows its `inspect`, and a `Plot` figure shows as SVG. [examples/mixed.md](examples/mixed.md) draws data in Go and fits and plots it in Ruby; the exact rules are in [docs/design.md](docs/design.md#ruby-cells).

Ruby cells also run in the browser, with no server, in the [playground](https://github.com/go-notebook/playground): the same rbgo and bindings compiled to WebAssembly, loaded only when the open notebook has a Ruby cell. Try it on [go-notebook's own playground](https://go-notebook.github.io/playground/) (Go and Ruby notebooks), and the Ruby examples on [go-statistics](https://go-statistics.github.io/playground/#5-ruby) and [go-numerics](https://go-numerics.github.io/playground/#6-ruby).

## Julia cells

A ` ```julia ` block is a Julia cell. It is parsed with [go-embedded-julia](https://github.com/go-embedded-julia/julia)'s parser, a pure-Go port of JuliaSyntax that gives the same trees as julia 1.12 on its whole test corpus, and run by its interpreter (go-embedded-julia v0.17.0 in this version), with no external `julia`. Its names join the notebook's dependency graph under the same rules as the other cells:

- **Syntax errors** are reported at the notebook's line and column, with julia's own message (``5:8: cell 0: syntax error: Expected `)` or `,` ``).
- **Names** follow Julia's scope rules for an interactive context ("Scope of Variables" in the Julia manual): top-level assignments, `function`, `const`, `struct`, `abstract type`, `module`, `macro`, `using`/`import` define; `let`, functions and comprehensions are hard scopes; `for`/`while`/`try` at the top level are soft scopes that assign an existing global, as in the REPL and IJulia. 61 cells are checked against julia 1.12.6 itself, which lowers them.
- **Running.** The worker of a notebook with Julia cells imports `jcells`, which owns one go-embedded-julia `interp.Kernel` for the notebook: the globals, functions and structs a cell defines are there in the next cell, what a cell prints is its own output, and the value of its last expression is shown as the REPL shows it (nothing for `nothing`, or after a trailing semicolon). A failure is julia's own text after `ERROR:`, with what the cell printed before it; the session goes on.
- **The image.** The kernel boots from go-embedded-julia's prebuilt Base image, which is not part of the module: `jcells` fetches release `v0.17.0`'s image (25,638,782 bytes) on the first Julia cell a worker runs, checks it against the SHA-256 it pins (`jcells.Digest`, `d243d2b7…c2f6ec`, from that release's notes and `.sha256` asset), loads nothing else, and keeps it in `os.UserCacheDir()/go-embedded-julia/images`, hashed again each time. The fetch is bounded to five minutes and tried once: a machine that is offline and has no cached image fails each Julia cell of the run at once with *the Julia kernel could not start*; the other cells run. The cold start is about a second. Moving to another release is changing the version and the digest together.
- **Limits** for untrusted cells, R's (`runtime.RLimits`, from `NOTEBOOK_R_LIMITS`; zero is the default, `-1` no bound): 30 s of time, 50,000,000 statements, a memory budget of 512 MiB, and 16 MiB of output per stream. The memory budget counts the elements of the arrays and dictionary tables a cell allocates, in total and garbage included, at 16 bytes each (33,554,432 elements): a floor, not the heap, because a boxed value costs more and strings are not counted; one array may not have more than 2^24 elements whatever the budget. A cell over a limit is stopped, with the kernel's own words, and the notebook's other cells run. The time limit and the worker's `--timeout` end a running cell: the generated program has no context to cancel (`jcells.Session.Eval` takes one, for an embedder). The sandbox is the interpreter's default: its file system holds julia's own tree, and its environment is empty.
- **Julia variables cross, as Ruby's do** (go-embedded-julia's `Kernel.Get` and `Kernel.Set`, #60). A variable a Julia cell defines is read by the Go, Ruby and Julia cells after it, with the Go type its value had when the cell last ran (the kernel learns it, and compiles the Go cells that read it afterwards, as for Ruby); a Go or Ruby variable is given to the Julia cells that read it. Every conversion copies. Functions, macros, constants and types stay in Julia.

  | Julia | Go |
  | --- | --- |
  | `Int64` | `int64` (a Go `int` goes in as an `Int64`) |
  | `Float64` | `float64` |
  | `Bool` | `bool` |
  | `String` | `string` |
  | `Vector{Float64}`, `Vector{Int64}` | `[]float64`, `[]int64` |
  | `Matrix{Float64}`, `Matrix{Int64}` | `interp.Matrix[float64]`, `interp.Matrix[int64]` (`github.com/go-embedded-julia/julia/interp`), column-major: element (i, j) at `Data[i+j*Rows]`, or `At(i, j)` |

  A Go `[]int` goes in as a `Vector{Int64}` (widened on a 32-bit machine). Floats cross with every bit (NaN payloads, `-0.0`, the infinities), strings byte for byte (invalid UTF-8 too), and empty vectors and matrices with a zero row count cross; a matrix with zero columns (3 by 0) does not come out of go-embedded-julia's `Get` yet and stays in Julia (`TestEmptyArraysCross` pins it). The arrays a Julia cell reads from Go are bounded before they are copied: one array has at most 2^24 elements (the kernel's own cap, which it checks only after copying a slice to 16 bytes an element), and all the arrays a cell reads together stay within its memory budget; a cell over either fails with a message that says so.

  Nothing else crosses. A Julia variable of another type (a `Dict`, a struct, a `Vector{String}`) stays in Julia: the Julia cells read it as before, and a Go cell that reads it is not run, with *it reads d, a Julia variable of type Dict{Int64, Int64}, which does not cross to Go* (and the cells that read that cell wait with it). A Go variable of another type than the table's (`[]string`, a map, a struct) fails the Julia cell that reads it, with *the Julia cell reads words, a Go []string that Julia cannot take*; nothing panics. A Julia cell shows text only.
- **A new worker starts with an empty kernel.** The kernel keeps its globals from cell to cell and not from worker to worker (a Go cell changed, `--timeout`): the Julia cells run again in the new one, and the variables that cross come back from their Go holders.

Known limitations of go-embedded-julia v0.17.0:

- **Base image gaps.** `A * x` of a Float64 matrix needs BLAS ([go-embedded-julia#67](https://github.com/go-embedded-julia/julia/issues/67)) and `uppercase` or `lowercase` of a non-ASCII string needs utf8proc ([#65](https://github.com/go-embedded-julia/julia/issues/65)); both fail with *not ported yet* (`jcells.TestKnownGapsOfTheImage` pins them; Int matrix products and ASCII case conversion work).
- **One release per process** ([#49](https://github.com/go-embedded-julia/julia/issues/49)): a process that loads two different image releases is refused; a worker loads one.
- The playground's WebAssembly core does not run Julia cells; that is a separate question, pending with go-embedded-julia.

In the HTML export, Julia and R cells are highlighted with [go-rouge](https://github.com/go-rouge/rouge)'s `julia` and `r` lexers (go-rouge v0.8.0 and later, transcribed from Rouge v5.1.0); both languages implement `analysis.Highlighter`. Since go-rouge v0.9.0 a lexer match is bounded to 16 MiB of stack: on a pathological cell the rest of it becomes plain text.

## R cells

A ` ```r ` block is an R cell (parsed since v0.8.0, run after v0.10.0). R is [go-embedded-r](https://github.com/go-embedded-r/r) (v0.13.1), a pure-Go R: no external `R` is started. Since v0.5.0 it has environments as values, the apply family and the string functions; since v0.6.0 its regular expressions are a port of R's TRE (`perl=TRUE` and approximate matching are refused); since v0.13.0 a cell can use files and connections (`readLines`/`writeLines`, `file.*`, `saveRDS`/`readRDS`, `source`, `sink`/`capture.output`). Those files live in a private in-memory file system of at most 64 MiB per notebook: no host file is reachable, and `Sys.getenv` sees no host variable. Its parser, a port of R 4.6's grammar that gives the same trees and tokens as R 4.6.2 on all of R's own `src/library` and `tests` (932 files), checks the cell; its evaluator runs it.

```r
xs <- c(2.1, 3.4, 1.9, 5.6)
total <- sum(xs)
total / length(xs)
```

- **One session.** The R cells of a notebook share one R session in the worker, so a variable assigned in one cell is there in the next, as in an R console. A cell re-runs only when it or an R cell it reads changes; after the worker is rebuilt, the R cells that a stale cell reads run again in the new session first.
- **Output.** What a cell prints is its output, as an R console shows it: `print`, `cat`, and the value of every visible top-level expression. An invisible value (`x <- 1`, `invisible(x)`) prints nothing. `message()` and the cell's warnings go to the standard error, the warnings in R's form (`Warning message:` then `In f(x) : text`; `Warning messages:` and a numbered line each when there are several). An error fails the cell as `Error in f(x) : text`, after the output written before it, and the cells that read it do not run. A cell that goes over a limit fails the same way.
- **Syntax errors** are reported at the notebook's line and column, with R's own message (`5:8: cell 0: syntax error: unexpected symbol`).
- **Names** follow R's scoping at the top level: `x <- v`, `x = v`, `v -> x`, `assign("x", v)`, `x$a <- v`, `names(x) <- v` and a top-level `for` variable define; the free variables of the cell and of its functions' bodies are read. A cell whose names cannot be known from its text (`<<-`, `eval`, `assign`/`get` with a computed name, `attach`, `library`, `source`) is **dynamic**: it depends on every earlier cell, and every later cell that reads a name depends on it.
- **R variables stay in R.** They are not shared with Go, Ruby, Julia, C or Fortran cells in either direction yet: a Go cell that reads an R name finds it undefined, and an R cell does not see a Go variable (`analysis.Isolated`). A cell shows text only, no figure.
- **Limits.** go-embedded-r (since v0.4.0) bounds every evaluation by default, and a notebook may come from anyone, so the kernel gives each R cell its own bounds (`kernel.Options.R`; a zero field is the default, `-1` is no bound):

  | Limit | Default | Ends a cell with |
  |---|---|---|
  | allocation, `MaxAlloc` | 512 MiB of vectors in total over the cell, garbage included | `Error: memory limit exceeded: more than 536870912 bytes of vectors allocated` |
  | calls and loop iterations, `MaxSteps` | 50 million (16.6 million iterations of `for (i in 1:n) s <- s + i`, 12.5 million of the same in a `while`) | `Error: evaluation step limit reached` |
  | time, `Timeout` | 30 s | `stopped: the R cell ran longer than 30s, the limit` |
  | depth, vector length | R's 5000 evaluations; 2^26 elements | `Error: evaluation nested too deeply: …`; `Error in numeric(2^27) : cannot allocate vector of length 134217728` |

  The session and its variables survive a limit. `--timeout` and `--memory` apply as to every cell, and kill the worker, session included. Each limit and its reasons are in [docs/design.md](docs/design.md#r-cells); the allocation bomb test (`rcells.TestAllocBomb`) runs in CI by itself under `GOMEMLIMIT` and `ulimit -v`.

The evaluator is linked only into a worker that has R cells: `analysis`, `gen`, `kernel`, `export` and the command do not import it (`analysis.TestNoEvaluatorImport`), so a WebAssembly core that analyses cells carries no more than before: 4,987,833 bytes without `analysis/parsers` and 10,567,013 with it at v0.10.0, 4,987,322 and 10,567,902 with this change (probe analysing a Go, a C and an R cell, go 1.27.3, `GOOS=js GOARCH=wasm`, `-s -w`).

## C cells: checked, not run yet

A ` ```c ` block is a C cell. It is checked with [go-embedded-c](https://github.com/go-embedded-c/c)'s front end (cc/v4 behind the `cell` package), which accepts what C allows at file scope and statements alike:

- **Cells are not independent.** A C cell sees the typedefs, macros, tags and declarations of the C cells before it, in notebook order (`analysis.Sequential`): `myint v = N;` is fine in a cell after the one that has `typedef int myint;` and `#define N 3`, and an error alone. A cell with an error leaves nothing behind for the next ones.
- **Errors** are reported at the notebook's line and column, with cc/v4's message. An error outside the cell (in a header) is reported at the start of the cell and says so. `#include` takes the C library's headers and clang's.
- **Names**: variables, functions, typedefs, `struct`/`union`/`enum` tags (`struct s`), enumerators and macros define; the names a cell reads from earlier C cells are its uses, and give the graph its edges. Only **variables** cross to the other languages; functions, typedefs, tags, enumerators and macros stay C's. A name is defined by its definition (C11 6.7p5), or failing that by its first tentative definition (`int x;`, 6.9.2); a prototype, an `extern` object or a forward `struct s;` defines nothing but makes its cell read the name, so a prototype in one cell and its definition in a later one are fine. A second definition (`int x = 1;` then `int x = 2;`) is refused as go-notebook's rule of one definition per name, which C itself would not apply across cells.
- **Limits**: a cell is at most 1 MiB, nests brackets at most 256 deep, and a macro expansion that would produce more than 65536 tokens (`#define D(a) ((a)+(a))` nested 40 deep is 2^40) is refused, in well under a second.
- **Running** a C cell fails, clearly: *C cells are not run yet (go-embedded-c M2: code generation)*. The cells that read what it defines, C cells included, are **blocked**; the others run.

No external C compiler is started.

## Fortran cells

A ` ```fortran ` block is a Fortran cell. It is checked and run with [go-embedded-fortran](https://github.com/go-embedded-fortran/fortran) (its `sema` session for the names and the errors, its `eval.Session` for the running; free form, implicit typing off), and highlighted with go-rouge's `fortran` lexer. A cell is a program's loose material and units alike: declarations and statements at top level, subroutines, functions, modules, derived types, even a `program`.

- **Cells share one session.** A notebook with Fortran cells makes the worker import `github.com/go-notebook/notebook/fcells`, which owns one `eval.Session`: a Fortran cell sees the variables (with the values the earlier cells left), procedures, modules and derived types of the Fortran cells before it, in notebook order (`analysis.Sequential`). `n = n + 1` in a cell after the one that has `integer :: n = 3` changes the same `n`. A cell with an error leaves nothing behind for the next ones. Names are case-insensitive; the lower-case name is the one in the graph. Redefinition shadows in Fortran (a cell that runs again defines a new version that later cells see), and is refused here, between two cells of the notebook, as go-notebook's rule of one definition per name.
- **Names**: variables and named constants (`parameter`) are declared at top level, subroutines and functions, derived types and modules are defined; the names a cell reads from earlier Fortran cells are its uses, and give the graph its edges: a variable, a procedure, a derived type named in a declaration and a module named in `use` (go-embedded-fortran v0.17.0 reports the last two) all do.
- **Fortran variables stay in Fortran.** They are not shared with Go, Ruby, Julia, R or C cells in either direction yet (`analysis.Isolated`, as R's): a Go cell that reads a Fortran name finds it undefined, and a Fortran cell does not see a Go variable. The values table of go-scikit/roadmap `bib/fortran.md` §7 (`integer(k)` to `int8`..`int64`, `real(4|8)` to `float32|float64`, arrays to `*ndarray.Array` with 1-based bounds) is `sema.GoType`, and `eval.Session.Get` reads a scalar; carrying an array out of the session, and a Go value in, is not done, and until it is the language does not claim that variables cross. A cell shows text only.
- **Output and errors.** What a cell prints is its output (a record left open with `advance='no'` ends with the cell); standard error and the text of `STOP` are its standard error; `STOP` ends the cell, not the session. A refused cell or a failure is reported at `line L, column C` of the cell (`line 4, column 9: ...`), and a cell that fails while it runs keeps what it had done. Output is cut at 16 MiB in all, standard output and standard error together. The storage a session holds is a counted budget (`fcells.Options.MaxAlloc`, 512 MiB, R's bound): every variable, array, derived-type object and character component is charged before it is allocated and credited by `deallocate`, the return of a procedure and the reassignment of an allocatable array, so a bomb of many small objects fails as the cell, before it is allocated, as one big array does, and a loop that drops its arrays does not. No collection and no stop-the-world.
- **Limits.** A notebook may come from anyone. A cell may make at most 2^32 DO iterations and procedure calls (`fcells.Options.MaxSteps`; `stopped` with `step limit reached`), run 30 s (`Options.Timeout`: `stopped: the Fortran cell ran longer than 30s, the limit`), and the evaluator's own bounds apply: 2^28 elements per array, 2^26 characters per string, 2^15 nested calls. The step count, not a timer, is what ends a loop in WebAssembly, where no timer fires while the program runs. `--timeout` and `--memory` apply as to every cell and kill the worker, session included; the session and its variables survive a limit of fcells. `fcells.TestAllocBomb` and `TestLimits` run in CI by themselves under `GOMEMLIMIT` and `ulimit -v`. `INCLUDE` is refused (no file is read, and the evaluator has no file or environment access); a cell is limited to 1 MiB.
- **What the evaluator refuses** (it says so, positioned): type extension and `select type`, pointer components, vector subscripts, `elsewhere`, `dim=` and `mask=`, complex variables, character arrays, `read` from a unit, an array declared with an initialisation. They are go-embedded-fortran's list, not ours.

`analysis` does not import go-embedded-fortran (`TestNoEmbeddedFortranImport`): `analysis/parsers` links its front end through `analysis.SetFortranParser`, like the C front end. The evaluator is linked only into a worker that has Fortran cells: `analysis`, `gen`, `kernel`, `export` and the command do not import it (`analysis.TestNoEvaluatorImport`). `fcells` builds for `GOOS=js GOARCH=wasm`, as `rcells` does, and the [playground](https://github.com/go-notebook/playground) links it into its second core since v0.16.0 (the one with the parsers and R's evaluator): Fortran cells run in the browser, bounded by `engine.FortranOptions` (200 million steps, 10 s), the default core staying Go-only.

An example, `examples/fortran.md`, runs end to end: `notebook run examples/fortran.md`.

### The Julia, R, C and Fortran parsers are opt-in (v0.9.0)

The `gen`, `kernel` and `export` packages and the `notebook` command analyse Julia, R, C and Fortran cells as above. A program that imports `analysis` alone must also import `analysis/parsers` to get the same:

```go
import _ "github.com/go-notebook/notebook/analysis/parsers"
```

Without it, Julia, R, C and Fortran stay registered, with their names and run capability, and their cells fail with `analysis.ParserMissing`. The point is size: a WebAssembly program that never analyses those cells, such as the playground's default core, leaves out 1.44 MB of parser code. The parsers' package `init` tables, about 0.29 MB, still come in through `analysis`'s imports. Measured on a probe that analyses a Go and a C cell (go 1.27, `GOOS=js GOARCH=wasm`): 5.07 MB without `analysis/parsers`, 10.75 MB with it; v0.9.0 was 5.06 and 6.53 MB. `analysis` does not import go-embedded-c (`TestNoEmbeddedCImport`): the C front end is linked by `analysis/parsers` through `analysis.SetCParser`, so a program without the parsers is 0.01 MB larger than at v0.9.0. Natively on darwin/arm64: 4.18 MB without, 8.11 MB with (v0.9.0: 4.17 and 5.24).

## An interactive session: `notebook irb`

`notebook irb` is irb on the same Ruby: rbgo with the go-scikit bindings, line by line, with no notebook file.

```console
$ notebook irb --save session.md
irb(main):001> x = [2.1, 3.4, 1.9, 5.6]
=> [2.1, 3.4, 1.9, 5.6]
irb(main):002> fit = Stats::Normal.fit(x)
=> #<Stats::Normal loc=3.25, scale=1.4739402972983673>
irb(main):003> _.loc * 2
=> 6.5
irb(main):004> def sq(v)
irb(main):005*   v * v
irb(main):006* end
=> :sq
irb(main):007> raise "boom"
boom (RuntimeError)
	from (irb):7:in '<main>'
irb(main):008> exit
notebook irb: wrote session.md
```

- **Editing**: on a terminal, the line editor is [go-ruby-reline](https://github.com/go-ruby-reline/reline)'s port of Ruby's Reline (emacs and vi keys, history with Up/Down and Ctrl-R, bracketed paste). An input that is not finished — an open `def`, bracket or string — continues on the next line with irb's continuation prompt; rbgo's parser decides. Tab completes locals, constants (`Stats::Nor` → `Stats::Normal`) and the methods of a variable's value, without calling anything the word contains.
- **Results**: `=> inspect` after each input, `_` the last value, and an exception as `message (Class)` with its frames, the session going on. Any local may be assigned again: there is no dependency graph here. `exit`, `quit` or Ctrl-D ends the session; Ctrl-C abandons the line being typed, or stops the input that is running.
- **Limits**: each input runs in a worker process, so `--timeout` and `--memory` apply to each input as to a cell. Stopping an input (a limit, Ctrl-C) kills its worker: the next input gets a new one, and the session says that its locals, methods and classes are gone.
- **Figures**: a `Plot` figure is never printed as SVG; `--svg-dir figures` saves each one as `figures/figure-N.svg`.
- **`--save session.md`** writes the inputs that ran as ` ```ruby ` cells, grouped so that the notebook keeps the rules of cells (an input that assigns a name again joins the cell that defined it; one that reads `_` joins the one before), and `notebook run session.md` runs them again.
- **History**: `irb_history` under the user config directory's `go-notebook` (mode 0600; the inputs only, never their values; the last 1000), in irb's format.

When standard input is not a terminal, lines are read as they come and echoed after their prompt, so `notebook irb < script.rb` prints a transcript.

A notebook runs with your rights, in either language. Every cell runs in a worker process of its own: `--timeout 30s` kills a worker whose cell runs longer, and `--memory 512` stops one whose cells hold more than 512 MiB of Go heap; the cell fails, and the next run starts a new worker. Both are off by default ([what a cell can do, and what stops it](docs/design.md#what-a-cell-can-do-and-what-stops-it)).

## Adding a cell language

A cell language is one `analysis.Language` registered under the first word of its fence's info string; the graph, the kernel, the generator and the exporter look it up (`analysis.Lookup`) and never test for a language by name. A fence whose word is not registered stays prose.

```go
type Language interface {
	Name() string // the fence word: "go", "ruby", "julia", ...
	Analyze(src string, lim Limits) (Names, []Diagnostic, error)
	Crosses(owner, user *Cell, name string) bool // does a cell of another language see name?
	Run() Run // runs, or check-only with the user-facing reasons
}
type Names struct{ Defines, Uses []string; Dynamic bool }
```

1. **Implement `Language`** and call `analysis.Register` from `init`. `Analyze` must refuse a source over `lim.MaxBytes` (and nesting over `lim.MaxDepth`) with an error, never a panic; diagnostics carry a byte offset in the cell. `Crosses` is the owner's rule for what the other languages read: a Go cell sees a Ruby local, not a Ruby method.
2. **Dynamic cells.** Set `Names.Dynamic` for a cell whose names cannot be resolved statically (R's `<<-`, `assign`/`get` with computed names, `eval(parse())`, `attach`, `library()`). The graph makes it depend on every earlier cell, and every later cell that reads any name depend on it.
3. **Run capability.** `Run{Runs: true}` runs the cells. Otherwise the language is check-only: `NotRun` is the cell's failure and `Blocked`, `Reads`, `ReadsBlocked` the messages of the cells that read it (C today; Julia, R and Fortran run).
4. **Optional capabilities**, found by type assertion: `CellAnalyzer` (fill more of a `Cell` than `Names`), `NameChecker` (names this language cannot hold), `Highlighter` (the go-rouge lexer for the HTML export; none means escaped, uncoloured), `Runtime` (the Go module the worker links: its import path, module and default version, which the kernel adds to the work module), `TypeLearner` (variables typed when the cell has run, as Ruby's), `Isolated` (cells that share no variable with another language's, as R's: `Shared` is false across languages when either is isolated), `Sequential` (cells that depend on the earlier cells of the same language: `Begin() any` starts the notebook's state, `AnalyzeNext(state any, index int, src string) (*Cell, any)` analyses a cell in it and returns the state for the next one; the graph threads it through that language's cells in notebook order, and a cell with an error should return the state it was given; C's).
5. **Run emission.** A language that runs also registers a `gen.Backend` with `gen.RegisterBackend(name, backend)`, from its own package: `Infer` declares the cell's variables, `Write` writes its function, `Runtime` names the import and the prelude, and the package it imports comes from its `analysis.Runtime`. The `gen.Context` it receives exposes the cell, its inputs, the declared variables and type resolution. No edit of `gen` is needed; Ruby's backend (`gen/ruby.go`) and `TestRegisteredBackend` are the models.

Test it as `analysis/lang_test.go` does, with a fake language, and fuzz the dispatch (`FuzzDispatch`).

## How it works

`format` parses the Markdown and `analysis` builds the dependency graph from the syntax. `gen` writes the Go program, type-checking cell by cell to learn the types of shared variables. `kernel` compiles it into a worker, drives it through `runtime`, and keeps results and values under a hash of each cell's code and inputs. `export` writes the HTML page. The reasons for each choice, and what is not done yet, are in [docs/design.md](docs/design.md); the [documentation](https://go-notebook.github.io/docs/) covers the cell rules, the command and the kernel in more detail.

It is part of an ecosystem built for it: [go-ndarray](https://github.com/go-ndarray/ndarray) (N-dimensional arrays in thirteen dtypes, with FFT, linear algebra, and random numbers bit-identical to NumPy's for the same seed), [go-plot](https://github.com/go-plot/plot) (figures, with TeX labels), and, in progress, [go-numerics](https://github.com/go-numerics) (special functions, optimisation, integration), [go-statistics](https://github.com/go-statistics) and [go-dataframe](https://github.com/go-dataframe).

## License

BSD-3-Clause. See [LICENSE](LICENSE).
