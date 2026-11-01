package gen

import "github.com/go-notebook/notebook/analysis"

// JuliaNotRun is analysis.JuliaNotRun, the failure of every Julia cell. Julia
// is a check-only language (analysis.Run): its cells are stubs that fail with
// it, and the cells that read them say which cell blocks them.
const JuliaNotRun = analysis.JuliaNotRun
