# notebook — go-notebook

[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.27.2%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](.github/workflows/ci.yml)

**Go notebooks with native speed, kept values and a reproducible order.**

A notebook is a Markdown file whose ` ```go ` blocks are cells. The kernel:

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
notebook html analysis.md    # run it, write analysis.html (no scripts, no external resources)
notebook watch analysis.md   # run it on every save and keep analysis.html current
```

Edit the `.md` in any editor; with `watch`, the page follows the file.

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
- A value with a `Display() map[string][]byte` method shows itself as HTML, SVG or PNG.
- Errors point at the notebook's own lines. A panic stops the cells that read the failing one, and they say why.

## How it works

`format` parses the Markdown and `analysis` builds the dependency graph from the syntax. `gen` writes the Go program, type-checking cell by cell to learn the types of shared variables. `kernel` compiles it into a worker, drives it through `runtime`, and keeps results and values under a hash of each cell's code and inputs. `export` writes the HTML page. The reasons for each choice, and what is not done yet, are in [docs/design.md](docs/design.md).

It is part of an ecosystem with [go-ndarray](https://github.com/go-ndarray/ndarray) (NumPy-style arrays in thirteen dtypes, `numpy.fft`); go-plot will provide the figures.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
