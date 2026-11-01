package analysis

import (
	"fmt"
	"sort"
	"sync"
)

// A cell language is ONE registered Language: how its cells are analysed,
// which of their names the other languages see, and whether they run. The
// kernel, the generator, the exporter and the graph look a cell's language up
// by the info string of its fence (Lookup) and never test for a language by
// name. To add a language, implement Language (and, if it needs them, the
// optional interfaces below) and Register it; see "Adding a cell language" in
// the README.

// Limits bound the work Analyze may do on one cell. They protect the notebook
// from a hostile or accidental huge cell; an implementation returns an error
// when a limit is exceeded.
type Limits struct {
	MaxBytes int // the longest source, in bytes
	MaxDepth int // the deepest nesting of brackets and blocks
}

// DefaultLimits are the limits the graph passes to Analyze.
var DefaultLimits = Limits{MaxBytes: 16 << 20, MaxDepth: 10_000}

// Names are the notebook variables a cell defines and reads.
type Names struct {
	Defines, Uses []string

	// Dynamic marks a cell whose names cannot be resolved statically: it
	// assigns or reads names computed at run time (R's <<-, assign, get,
	// eval(parse()), attach, library()). The graph makes such a cell depend on
	// every earlier cell, and every later cell that reads any name depend on
	// it.
	Dynamic bool
}

// Diagnostic is a problem found in a cell, at a byte offset of its source.
// Analyze leaves Cell zero; the graph sets it.
type Diagnostic = Error

// Run says whether the cells of a language run, and what to tell when they
// do not.
type Run struct {
	// Runs is true when the language's cells execute.
	Runs bool

	// The rest is set when Runs is false: a cell is parsed and checked only.
	// NotRun is the cell's own failure. Blocked(c) is the failure of a cell
	// that reads, through the graph, what cell c defines; Reads(c) and
	// ReadsBlocked(c) say the same when the generator defers a Go cell that
	// reads cell c directly, or reads a cell blocked by cell c.
	NotRun       string
	Blocked      func(cell int) string
	Reads        func(cell int) string
	ReadsBlocked func(cell int) string
}

// Language is a cell language, keyed by the first word of its fence's info
// string.
type Language interface {
	// Name is the fence word: "go", "ruby", "julia"…
	Name() string

	// Analyze finds the names a cell defines and uses, and its syntax errors.
	// It returns an error, not a panic, when src exceeds lim.
	Analyze(src string, lim Limits) (Names, []Diagnostic, error)

	// Crosses reports whether cell user, of another language, sees name,
	// which cell owner, of this language, defines. Within a language every
	// name is shared and Crosses is not asked.
	Crosses(owner, user *Cell, name string) bool

	// Run is the run capability of the language.
	Run() Run
}

// CellAnalyzer is implemented by a language whose analysis fills more of a
// Cell than Names (chunks, funcs, types, the display…). The graph uses it in
// place of Analyze; the generator needs that detail for the languages it
// compiles or splices.
type CellAnalyzer interface {
	AnalyzeCell(index int, src string) *Cell
}

// NameChecker is implemented by a language that cannot hold every name: when
// a cell of another language defines a name, a cell of this language sees it
// only if ValidName holds (a Go variable cannot be named like a keyword).
type NameChecker interface {
	ValidName(name string) bool
}

// Highlighter is implemented by a language go-rouge has a lexer for; the
// others are shown escaped, uncoloured.
type Highlighter interface {
	Lexer() string
}

// Module is a Go module the worker needs to run a language's cells.
type Module struct {
	Path    string // the import path of the package the generated program imports
	Module  string // the module that holds Path
	Version string // the version the work module requires by default
}

// Runtime is implemented by a language that runs on a Go package the worker
// links.
type Runtime interface {
	Runtime() Module
}

// TypeLearner is implemented by a language whose variables have no static Go
// type: the kernel learns it when the cell runs and compiles the Go cells
// that read the variable afterwards.
type TypeLearner interface {
	LearnsTypes() bool
}

// LearnsTypes reports whether the language of the given name is a TypeLearner.
func LearnsTypes(name string) bool {
	t, ok := Lookup(name).(TypeLearner)
	return ok && t.LearnsTypes()
}

var (
	regMu sync.RWMutex
	reg   = map[string]Language{}
)

// Register adds a language. It panics on an empty or duplicate name, as
// database/sql does: registering is done once, at init.
func Register(l Language) {
	name := l.Name()
	regMu.Lock()
	defer regMu.Unlock()
	if name == "" {
		panic("analysis: Register of a language with no name")
	}
	if _, dup := reg[name]; dup {
		panic(fmt.Sprintf("analysis: language %q registered twice", name))
	}
	reg[name] = l
}

// Unregister removes a language, for tests that register a fake one.
func Unregister(name string) {
	regMu.Lock()
	delete(reg, name)
	regMu.Unlock()
}

// Lookup returns the language of a fence word, nil when it is not one: such a
// block is prose. The empty word is Go.
func Lookup(name string) Language {
	if name == "" {
		name = Go
	}
	regMu.RLock()
	l := reg[name]
	regMu.RUnlock()
	return l
}

// Languages returns the registered language names, sorted.
func Languages() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for n := range reg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// RunOf is the run capability of the language of a cell; an unknown language
// runs (it is Go).
func RunOf(name string) Run {
	if l := Lookup(name); l != nil {
		return l.Run()
	}
	return Run{Runs: true}
}

// analyzeIn analyses cell index in language name with the registered
// implementation; an unknown language is analysed as Go, as before.
func analyzeIn(name string, index int, src string) *Cell {
	l := Lookup(name)
	if l == nil {
		l = Lookup(Go)
	}
	if len(src) > DefaultLimits.MaxBytes {
		return &Cell{Index: index, Lang: l.Name(), Imports: map[string]string{},
			Errors: []Error{{Cell: index, Msg: fmt.Sprintf("the cell is %d bytes; the limit is %d", len(src), DefaultLimits.MaxBytes)}}}
	}
	if ca, ok := l.(CellAnalyzer); ok {
		return ca.AnalyzeCell(index, src)
	}
	c := &Cell{Index: index, Source: src, Lang: l.Name(), Imports: map[string]string{}}
	names, diags, err := l.Analyze(src, DefaultLimits)
	c.Defines, c.Uses, c.Dynamic = names.Defines, names.Uses, names.Dynamic
	for _, d := range diags {
		d.Cell = index
		c.Errors = append(c.Errors, d)
	}
	if err != nil {
		c.Errors = append(c.Errors, Error{Cell: index, Msg: err.Error()})
	}
	return c
}

// builtinLang is the common part of the languages this package implements.
type builtinLang struct {
	name string
	cell func(index int, src string) *Cell
	run  Run
}

func (l builtinLang) Name() string { return l.name }
func (l builtinLang) Run() Run     { return l.run }
func (l builtinLang) AnalyzeCell(index int, src string) *Cell {
	return l.cell(index, src)
}

func (l builtinLang) Analyze(src string, lim Limits) (Names, []Diagnostic, error) {
	if len(src) > lim.MaxBytes {
		return Names{}, nil, fmt.Errorf("the cell is %d bytes; the limit is %d", len(src), lim.MaxBytes)
	}
	c := l.cell(0, src)
	return Names{Defines: c.Defines, Uses: c.Uses}, c.Errors, nil
}

// goLang: a Go cell's funcs and types stay Go's; its variables cross.
type goLang struct{ builtinLang }

func (goLang) Crosses(owner, _ *Cell, name string) bool {
	return !contains(owner.Funcs, name) && !contains(owner.Types, name)
}

// ValidName: a Go variable cannot be a keyword or a predeclared identifier.
func (goLang) ValidName(name string) bool { return GoName(name) }

func (goLang) Lexer() string { return "go" }

// localsLang is the part of Ruby and Julia: the other languages see a cell's
// top-level local variables, but not its methods, functions or constants.
type localsLang struct{ builtinLang }

func (localsLang) Crosses(owner, user *Cell, name string) bool {
	if contains(owner.Funcs, name) || contains(owner.Consts, name) {
		return false
	}
	if c, ok := Lookup(user.Lang).(NameChecker); ok {
		return c.ValidName(name)
	}
	return true
}

type rubyLang struct{ localsLang }

func (rubyLang) Lexer() string { return "ruby" }

func (rubyLang) LearnsTypes() bool { return true }

func (rubyLang) Runtime() Module {
	return Module{Path: RubyPath, Module: RubyModule, Version: DefaultRubyVersion}
}

// juliaLang: go-rouge has no Julia lexer, so it is not a Highlighter.
type juliaLang struct{ localsLang }

// The go-scikit Ruby bindings that run Ruby cells over go-embedded-ruby.
const (
	RubyPath           = "github.com/go-scikit/ruby/cells"
	RubyModule         = "github.com/go-scikit/ruby"
	DefaultRubyVersion = "v0.2.1"
)

func init() {
	Register(goLang{builtinLang{name: Go, cell: AnalyzeCell, run: Run{Runs: true}}})
	Register(rubyLang{localsLang{builtinLang{name: Ruby, cell: AnalyzeRubyCell, run: Run{Runs: true}}}})
	Register(juliaLang{localsLang{builtinLang{name: Julia, cell: AnalyzeJuliaCell, run: Run{
		NotRun:  JuliaNotRun,
		Blocked: JuliaBlocked,
		Reads: func(c int) string {
			return fmt.Sprintf("not run: it reads cell %d, a Julia cell, and Julia cells are not run yet", c)
		},
		ReadsBlocked: func(c int) string {
			return fmt.Sprintf("not run: it reads cell %d, which is blocked by a Julia cell", c)
		},
	}}}})
}
