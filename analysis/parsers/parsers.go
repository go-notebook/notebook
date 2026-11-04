// Package parsers links the parsers of go-notebook's check-only cell
// languages (go-embedded-julia's for ```julia cells, go-embedded-r's for
// ```r cells, go-embedded-c's for ```c cells) into a program. Import it for its side effect:
//
//	import _ "github.com/go-notebook/notebook/analysis/parsers"
//
// The kernel, the generator, the exporter and the notebook command import it
// already. A program that imports the analysis package alone and never
// analyses a Julia or an R cell can leave it out and carry neither parser:
// those cells then fail with analysis.ParserMissing.
package parsers

import "github.com/go-notebook/notebook/analysis"

func init() { analysis.EnableParsers() }
