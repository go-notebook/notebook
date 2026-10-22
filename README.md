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
```

Edit the `.md` in any editor, or in the page `serve` shows (double-click a cell, Ctrl/⌘+Enter to save): the file is the only source of truth, and only what changed runs again.

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

A notebook runs with your rights, in either language. Every cell runs in a worker process of its own: `-timeout 30s` kills a worker whose cell runs longer, and `-memory 512` stops one whose cells hold more than 512 MiB of Go heap; the cell fails, and the next run starts a new worker. Both are off by default ([what a cell can do, and what stops it](docs/design.md#what-a-cell-can-do-and-what-stops-it)).

## How it works

`format` parses the Markdown and `analysis` builds the dependency graph from the syntax. `gen` writes the Go program, type-checking cell by cell to learn the types of shared variables. `kernel` compiles it into a worker, drives it through `runtime`, and keeps results and values under a hash of each cell's code and inputs. `export` writes the HTML page. The reasons for each choice, and what is not done yet, are in [docs/design.md](docs/design.md); the [documentation](https://go-notebook.github.io/docs/) covers the cell rules, the command and the kernel in more detail.

It is part of an ecosystem built for it: [go-ndarray](https://github.com/go-ndarray/ndarray) (N-dimensional arrays in thirteen dtypes, with FFT, linear algebra, and random numbers bit-identical to NumPy's for the same seed), [go-plot](https://github.com/go-plot/plot) (figures, with TeX labels), and, in progress, [go-numerics](https://github.com/go-numerics) (special functions, optimisation, integration), [go-statistics](https://github.com/go-statistics) and [go-dataframe](https://github.com/go-dataframe).

## License

BSD-3-Clause. See [LICENSE](LICENSE).
