//go:build nojulia

package analysis

// JuliaParserNotBuilt is the error of a Julia cell in a build without the
// Julia parser.
const JuliaParserNotBuilt = "Julia cells cannot be checked in this build: it was made with -tags nojulia, without go-embedded-julia's parser"

// AnalyzeJuliaCell, in a build with the tag nojulia, fails every Julia cell:
// the parser, a couple of megabytes, is left out. go-notebook's playground
// builds its Go-only WebAssembly core this way, and loads the core that has
// it only for a notebook with a Julia cell.
func AnalyzeJuliaCell(index int, src string) *Cell {
	return &Cell{Index: index, Source: src, Lang: Julia, Imports: map[string]string{},
		Errors: []Error{{Cell: index, Msg: JuliaParserNotBuilt}}}
}
