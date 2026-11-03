package analysis

import (
	"strings"
	"testing"
)

// rCorpus are R cells of the shapes notebooks hold.
var rCorpus = []string{
	"x <- 1\ny = 2\n3 -> z",
	"f <- function(a, b = 2) { s <- a + b; s * k }",
	"h = \\(v) v^2",
	"for (i in seq_len(n)) total <- total + i",
	"if (x > 0) y <- 1 else y <- -1",
	"while (TRUE) break",
	"repeat { next }",
	"d <- data.frame(a = 1:3, b = c(\"x\", \"y\", \"z\"))",
	"d$a[2] <- 10L",
	"names(d)[1] <- 'A'",
	"m <- matrix(1:6, nrow = 2) %*% t(matrix(1:6, nrow = 2))",
	"x |> sqrt() |> sum()",
	"assign(\"v\", 1); get(\"v\")",
	"library(stats); require(utils)",
	"g <<- 3",
	"eval(parse(text = \"q <- 1\"))",
	"local({ tmp <- 1; tmp })",
	"`my var` <- 1",
	"s <- r\"(raw \\ string)\"",
	"z <- 1i + 2.5e-3 + 0x1F + NA_integer_",
	"# a comment\nx",
}

// BenchmarkAnalyzeRCell is the whole analysis of a large cell.
func BenchmarkAnalyzeRCell(b *testing.B) {
	var sb strings.Builder
	for sb.Len() < 100<<10 {
		for _, s := range rCorpus {
			sb.WriteString(s + "\n")
		}
	}
	src := sb.String()
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		AnalyzeRCell(0, src)
	}
}

// FuzzAnalyzeRCell: any text is analysed without a panic, the parser's or
// the names pass's, and an error's offset is inside the cell.
func FuzzAnalyzeRCell(f *testing.F) {
	for _, s := range rCorpus {
		f.Add(s)
	}
	for _, s := range []string{"x <- (1,", "function(", "if", "\"unterminated", "'\\q'", "5 ->", "`", "\t\ty y", "é é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		c := AnalyzeRCell(0, src)
		for _, e := range c.Errors {
			if strings.Contains(e.Msg, "R parser failed") {
				t.Fatalf("%q: the parser panicked", src)
			}
			if e.Pos < 0 || e.Pos > len(src) {
				t.Fatalf("%q: offset %d outside the cell", src, e.Pos)
			}
		}
	})
}
