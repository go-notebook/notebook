# Waiting times, drawn in Go and fitted in Ruby

A notebook can mix Go cells and Ruby cells. The Ruby is
[rbgo](https://github.com/go-embedded-ruby/ruby), a Ruby written in Go, with the
[go-scikit bindings](https://github.com/go-scikit/ruby) — so both languages run
the same libraries. Here Go draws the data, and Ruby fits a distribution to it and
draws the plot. Their variables are shared: what one language defines, the other reads.

Go draws 2,000 waiting times from a gamma distribution, with NumPy's generator and seed.

```go
import "github.com/go-ndarray/ndarray/random"

rng := random.DefaultRNG(7)
waits, _ := rng.Gamma(2.5, 1.2, 2000)
waits.Shape()
```

Ruby reads `waits` as the same NDArray, fits a gamma distribution with its
location held at zero, and tests the fit.

```ruby
fit = Stats::Gamma.fit(waits, fixed: {loc: 0})
shape, scale = fit.a, fit.scale
ks = Stats.ks_1samp(waits) { |x| fit.cdf(x) }
puts "shape #{shape.round(3)}, scale #{scale.round(3)}, KS p-value #{ks.pvalue.round(3)}"
fit
```

The histogram of the data and the fitted density.

```ruby
grid = NDArray.linspace(0, 15, 200)
fig, ax = Plot.subplots(figsize: [6, 3.5])
ax.hist(waits, bins: 40, density: true, alpha: 0.5, label: "data")
ax.plot(grid, fit.pdf(grid), color: "crimson", label: "gamma fit")
ax.set_xlabel("waiting time")
ax.legend
fig
```

Back in Go, the fitted `shape` and `scale` are `float64`s: the mean of the fitted
distribution, against the mean of the data.

```go
dataMean, _ := waits.Mean()
shape*scale - dataMean
```
