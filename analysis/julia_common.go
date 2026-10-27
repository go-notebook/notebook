package analysis

import "strconv"

// Julia is the language of ```julia cells.
const Julia = "julia"

// JuliaNotRun is the failure of every Julia cell, in the kernel and in the
// playground: go-embedded-julia parses Julia exactly as julia 1.12 does, but
// has no interpreter yet, and go-notebook runs no external julia.
const JuliaNotRun = "Julia cells are parsed and checked, but not run yet: execution arrives with go-embedded-julia's runtime (milestone M3)"

// JuliaBlocked is the failure of a cell that reads, directly or through
// other cells, what Julia cell julia defines.
func JuliaBlocked(julia int) string {
	return "blocked: it depends on cell " + strconv.Itoa(julia) + ", a Julia cell, and Julia cells are not run yet"
}
