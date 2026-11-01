// Package kernel runs notebooks.
//
// A Kernel owns a work directory holding a Go module: the generated program
// of the notebook, compiled into a worker process it drives (see package
// runtime). Each Run takes the notebook's current code cells and does the
// least work that brings every result up to date:
//
//   - A cell's key is a hash of its source, of the notebook's imports and of
//     the keys of the cells it reads. A cell whose key has not changed keeps
//     its result and is not run.
//   - When the program changes the worker is rebuilt and restarted. The
//     values of unchanged cells are handed to the new worker instead of
//     being computed again. A value that cannot be encoded (a func, a
//     channel, unexported state) is volatile: its cell runs again, but only
//     when a cell that reads it has to run.
//   - A cell that fails stops the cells that read it, which report why.
//
// Results and values are also kept on disk in the work directory, so a new
// Kernel on the same directory picks up where the last one stopped.
//
// This assumes a cell computes the same values from the same inputs, which
// a cell reading the clock or an unseeded generator does not; such a cell
// should be run explicitly (RunCell).
package kernel

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-notebook/notebook/analysis"
	"github.com/go-notebook/notebook/gen"
	"github.com/go-notebook/notebook/runtime"
)

// Options configure a Kernel.
type Options struct {
	// Dir is the work directory. It is created if needed.
	Dir string

	// Name is the notebook's file name, used in error positions.
	Name string

	// Replace adds replace directives to the work module, mapping a module
	// path to a local directory (for a runtime or library under
	// development).
	Replace map[string]string

	// RuntimeVersion is the version of github.com/go-notebook/notebook the
	// work module requires. Empty means v0.0.0, which needs a Replace.
	RuntimeVersion string

	// RubyVersion is the version of github.com/go-scikit/ruby the work
	// module requires when the notebook has Ruby cells; empty means
	// DefaultRubyVersion.
	RubyVersion string

	// Versions maps a language to the version of its runtime module the work
	// module requires (analysis.Runtime), overriding the language's default.
	// RubyVersion, when set, wins for Ruby.
	Versions map[string]string

	// CellTimeout bounds how long one cell may run: a cell still running
	// after it — an endless loop, in Go or in Ruby — has its worker killed
	// and fails, and the next Run starts a new worker. Zero means no bound.
	CellTimeout time.Duration

	// CellMemory bounds, in bytes, the heap a worker's cells may hold. The
	// Go garbage collector keeps under it as long as it can; a cell that
	// holds more has its worker stopped and fails. Zero means no bound.
	CellMemory int64
}

// Result is the outcome of one code cell.
type Result struct {
	Cell    int
	Output  string              // what it printed
	Display []map[string][]byte // what it showed
	Error   string              // a compile error, a panic, or a failed input
	Ran     bool                // it ran in this Run
	Kept    bool                // its result was kept from before; neither means it did not run
	Took    time.Duration       // how long it ran
}

// Report is the outcome of a Run.
type Report struct {
	Results []Result // one per code cell, in document order
	Diags   []gen.Diag
	Built   bool          // whether the worker was rebuilt
	Build   time.Duration // how long generating and building took
}

// Kernel runs one notebook.
type Kernel struct {
	opt Options

	src     []byte // the program the worker runs
	imports string

	worker *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Scanner
	loaded map[string]bool // variables the running worker holds

	keys    []string
	results map[string]Result // by cell key
	values  map[string]value  // by variable
	types   *importer
	env     keyEnv

	// ruby reports Ruby cells in the notebook; rubyTypes are the Go types
	// their variables had when they last ran (gen.Options.RubyTypes), kept in
	// the work directory, and typesChanged that a cell of this Run changed
	// one.
	langs        []string // the languages of the cells that link a Go package (analysis.Runtime), sorted
	rubyTypes    map[string]string
	typesChanged bool
	waiting      bool            // a cell of this Run was deferred, or reads a type that changed
	exitCode     int             // the exit status of the last worker that ended
	changed      map[string]bool // the Ruby variables whose type this Run changed
}

// value is a kept variable: its encoding, or nil if it is volatile, and the
// key of the cell that made it.
type value struct {
	data []byte
	key  string
}

// New prepares a kernel.
func New(opt Options) (*Kernel, error) {
	if opt.Name == "" {
		opt.Name = "notebook.md"
	}
	if err := os.MkdirAll(opt.Dir, 0o755); err != nil {
		return nil, err
	}
	k := &Kernel{opt: opt, results: map[string]Result{}, values: map[string]value{}, rubyTypes: map[string]string{}}
	if b, err := os.ReadFile(k.typesPath()); err == nil {
		_ = json.Unmarshal(b, &k.rubyTypes)
	}
	return k, nil
}

// Close stops the worker.
func (k *Kernel) Close() error {
	return k.stop()
}

// Run brings the results of the given code cells up to date.
func (k *Kernel) Run(cells []gen.Code) (*Report, error) {
	rep, err := k.run(cells)
	if err != nil || !k.typesChanged || !k.waiting {
		return rep, err
	}
	// A Ruby cell gave a variable a Go type the program did not declare it
	// with — the first time it ran, or since it changed — and Go cells wait
	// for it: they are compiled again with the type, and only they run. (A
	// type no Go cell waits for is declared on the next Run, when the
	// program is built anyway.)
	first := rep
	rep, err = k.run(cells)
	if rep != nil {
		rep.Build += first.Build
		rep.Built = true
		// One Run, seen once: a cell that ran in the first pass ran.
		for i, r := range first.Results {
			if r.Ran && i < len(rep.Results) && rep.Results[i].Kept {
				rep.Results[i].Ran, rep.Results[i].Kept, rep.Results[i].Took = true, false, r.Took
			}
		}
	}
	return rep, err
}

// run is one pass of Run.
func (k *Kernel) run(cells []gen.Code) (*Report, error) {
	start := time.Now()
	k.typesChanged, k.waiting, k.changed = false, false, map[string]bool{}
	k.langs = runtimeLangs(cells)
	rep := &Report{}
	paths := importPaths(cells)
	if err := k.prepareModule(paths); err != nil {
		return nil, err
	}
	prog := gen.GenerateWith(k.opt.Name, cells, k.types, gen.Options{RubyTypes: k.rubyTypes})
	rep.Diags = prog.Diags
	k.waiting = len(prog.Deferred) > 0
	rep.Results = make([]Result, len(cells))
	for i := range rep.Results {
		rep.Results[i].Cell = i
	}
	if len(prog.Diags) > 0 {
		for _, d := range prog.Diags {
			if d.Cell >= 0 {
				rep.Results[d.Cell].Error = strings.TrimSpace(rep.Results[d.Cell].Error + "\n" + d.String())
			}
		}
		for i := range rep.Results {
			if rep.Results[i].Error == "" {
				rep.Results[i].Error = "not run: the notebook has errors"
			}
		}
		rep.Build = time.Since(start)
		return rep, nil
	}
	if string(prog.Source) != string(k.src) {
		if err := k.build(prog.Source); err != nil {
			rep.Build = time.Since(start)
			return rep, err
		}
		rep.Built = true
	}
	if k.worker == nil {
		if err := k.start(); err != nil {
			return nil, err
		}
	}
	rep.Build = time.Since(start)
	k.keys = cellKeys(cells, prog, k.env)
	k.execute(prog, rep, nil)
	k.waiting = k.waiting || readsChanged(prog, k.changed)
	return rep, nil
}

// RunCell runs cell i again even if nothing changed, and every cell that
// reads it, after a Run.
func (k *Kernel) RunCell(cells []gen.Code, i int) (*Report, error) {
	rep, err := k.Run(cells)
	if err != nil || len(rep.Diags) > 0 {
		return rep, err
	}
	prog := gen.GenerateWith(k.opt.Name, cells, k.types, gen.Options{RubyTypes: k.rubyTypes})
	force := map[int]bool{}
	for _, c := range prog.Graph.Downstream(i) {
		force[c] = true
	}
	rep.Results = make([]Result, len(cells))
	k.execute(prog, rep, force)
	return rep, nil
}

// execute runs or restores every cell in order.
func (k *Kernel) execute(prog *gen.Program, rep *Report, force map[int]bool) {
	g := prog.Graph
	varsOf := make([][]string, len(g.Cells))
	for _, v := range prog.Vars {
		varsOf[v.Cell] = append(varsOf[v.Cell], v.Name)
	}
	// stale cells must run; need is everything they read, transitively.
	stale := map[int]bool{}
	for i := range g.Cells {
		if !k.load(k.keys[i]) || force[i] {
			stale[i] = true
		}
	}
	var staleList []int
	for i := range stale {
		staleList = append(staleList, i)
	}
	need := map[int]bool{}
	for _, i := range g.Upstream(staleList...) {
		need[i] = true
	}
	failed := map[int]int{} // cell -> the failed cell that blocks it
	for _, i := range g.Order {
		key := k.keys[i]
		if f, ok := blocked(g.Deps[i], failed); ok {
			failed[i] = f
			why := fmt.Sprintf("not run: cell %d failed", f)
			if r := analysis.RunOf(g.Cells[f].Lang); !r.Runs {
				why = r.Blocked(f)
			}
			rep.Results[i] = Result{Cell: i, Error: why}
			continue
		}
		if r := analysis.RunOf(g.Cells[i].Lang); !r.Runs {
			// Parsed and checked, never run (Run.NotRun): the cells that
			// read it are blocked.
			failed[i] = i
			rep.Results[i] = Result{Cell: i, Error: r.NotRun}
			continue
		}
		if !need[i] {
			rep.Results[i] = k.kept(i, key)
			continue
		}
		if !stale[i] && k.restore(varsOf[i], key) {
			rep.Results[i] = k.kept(i, key)
			continue
		}
		res := k.runCell(i)
		res.Ran = true
		rep.Results[i] = res
		if res.Error != "" {
			failed[i] = i
			delete(k.results, key)
			continue
		}
		k.results[key] = res
		for _, name := range varsOf[i] {
			k.values[name] = value{data: k.get(name), key: key}
			k.loaded[name] = true
		}
		k.save(key, res, varsOf[i])
	}
	for i := range g.Cells {
		rep.Results[i].Cell = i
	}
}

// kept returns a cell's stored result, marked as not run.
func (k *Kernel) kept(i int, key string) Result {
	r := k.results[key]
	r.Cell, r.Ran, r.Kept = i, false, true
	return r
}

// blocked reports whether one of deps failed, and which.
func blocked(deps []int, failed map[int]int) (int, bool) {
	for _, d := range deps {
		if f, ok := failed[d]; ok {
			return f, true
		}
	}
	return 0, false
}

// restore hands a kept cell's variables to the worker. It fails, so the
// cell must run, when one of them is volatile or was made by another
// version of the cell.
func (k *Kernel) restore(names []string, key string) bool {
	for _, n := range names {
		if k.loaded[n] && k.values[n].key == key {
			continue
		}
		v, ok := k.values[n]
		if !ok || v.data == nil || v.key != key {
			return false
		}
		if r := k.call(runtime.Request{Op: "set", Name: n, Data: v.data}); r.Error != "" {
			return false
		}
		k.loaded[n] = true
	}
	return true
}

// runCell asks the worker to run cell i. Took is measured in the worker,
// around the cell alone: not the round trip, nor a fresh worker's start.
func (k *Kernel) runCell(i int) Result {
	var timedOut atomic.Bool
	if d := k.opt.CellTimeout; d > 0 {
		w := k.worker.Process
		t := time.AfterFunc(d, func() {
			timedOut.Store(true)
			_ = w.Kill()
		})
		defer t.Stop()
	}
	r := k.call(runtime.Request{Op: "run", Cell: i})
	if timedOut.Load() {
		r.Error = fmt.Sprintf("stopped: the cell ran longer than %s, the limit; its worker was killed", k.opt.CellTimeout)
	}
	k.learn(r.Notes)
	return Result{Cell: i, Output: r.Output, Display: r.Display, Error: r.Error, Took: r.Took}
}

// get fetches a variable's encoding, nil if it is volatile.
func (k *Kernel) get(name string) []byte {
	r := k.call(runtime.Request{Op: "get", Name: name})
	if r.Volatile || r.Error != "" {
		return nil
	}
	return r.Data
}

// call sends a request and reads the reply. A dead worker answers with an
// error, and is restarted on the next Run.
func (k *Kernel) call(req runtime.Request) runtime.Reply {
	b, _ := json.Marshal(req)
	if _, err := k.in.Write(append(b, '\n')); err != nil {
		return k.died(err)
	}
	if !k.out.Scan() {
		return k.died(k.out.Err())
	}
	var rep runtime.Reply
	if err := json.Unmarshal(k.out.Bytes(), &rep); err != nil {
		return runtime.Reply{Error: "bad reply: " + err.Error()}
	}
	return rep
}

// died handles a worker that stopped answering (a cell called os.Exit, or
// crashed outside the recovered call).
func (k *Kernel) died(err error) runtime.Reply {
	k.exitCode = 0
	_ = k.stop()
	k.src = nil
	if k.exitCode == runtime.ExitMemory {
		return runtime.Reply{Error: fmt.Sprintf("stopped: the cell went over its memory limit of %d bytes; its worker was killed", k.opt.CellMemory)}
	}
	msg := "the worker stopped"
	if err != nil {
		msg += ": " + err.Error()
	}
	return runtime.Reply{Error: msg}
}

// cellKeys computes each cell's key from its source, the import paths of
// the packages it refers to, the Go toolchain, the module versions when it
// uses a package outside the standard library, and the keys of the cells it
// reads. An import another cell adds or drops does not change it; a go
// version change, or a dependency upgrade, does.
func cellKeys(cells []gen.Code, prog *gen.Program, env keyEnv) []string {
	all := map[string]string{} // package name -> path, over every cell
	for _, c := range prog.Graph.Cells {
		for n, p := range c.Imports {
			all[n] = p
		}
	}
	keys := make([]string, len(cells))
	for _, i := range prog.Graph.Order {
		c := prog.Graph.Cells[i]
		var pkgs []string
		for _, n := range c.OwnPackages {
			pkgs = append(pkgs, c.Imports[n])
		}
		for _, u := range c.Uses {
			if p, ok := all[u]; ok {
				pkgs = append(pkgs, p)
			}
		}
		sort.Strings(pkgs)
		h := sha256.New()
		// The cell's position is not part of it: moving a cell runs nothing.
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", cells[i].Source, strings.Join(pkgs, ","), env.toolchain)
		if _, ok := analysis.Lookup(c.Lang).(analysis.Runtime); ok {
			// A cell of a language that links a Go package (Ruby's rbgo and
			// the go-scikit bindings) depends on its versions, in go.sum.
			fmt.Fprintf(h, "%s\x00%s\x00", c.Lang, env.sums)
		} else if !allStandard(pkgs) {
			// Any module could be behind any non-standard package,
			// transitively: the whole go.sum is the honest summary.
			fmt.Fprintf(h, "%s\x00", env.sums)
		}
		for _, d := range prog.Graph.Deps[i] {
			fmt.Fprintf(h, "%s\x00", keys[d])
		}
		keys[i] = hex.EncodeToString(h.Sum(nil))
	}
	return keys
}

// allStandard reports whether every path is in the standard library, whose
// first element has no dot.
func allStandard(paths []string) bool {
	for _, p := range paths {
		first, _, _ := strings.Cut(p, "/")
		if strings.Contains(first, ".") {
			return false
		}
	}
	return true
}

// importPaths returns the sorted import paths of the cells.
func importPaths(cells []gen.Code) []string {
	var srcs []string
	for _, c := range cells {
		if c.Lang == analysis.Go || c.Lang == "" {
			srcs = append(srcs, c.Source)
		}
	}
	set := map[string]bool{}
	for _, c := range analyzeImports(srcs) {
		set[c] = true
	}
	var out []string
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// build writes and compiles the program.
func (k *Kernel) build(src []byte) error {
	_ = k.stop()
	if err := writeFile(filepath.Join(k.opt.Dir, "main.go"), src, 0o644); err != nil {
		return err
	}
	if out, err := goBuild(k.opt.Dir, k.binary()); err != nil {
		k.src = nil
		return fmt.Errorf("build: %v\n%s", err, out)
	}
	k.src = src
	return nil
}

// binary is the worker's path.
func (k *Kernel) binary() string {
	return filepath.Join(k.opt.Dir, "worker"+exeSuffix)
}

// Seams a test replaces to reach the failures it cannot cause otherwise.
var (
	writeFile = os.WriteFile
	goBuild   = func(dir, out string) ([]byte, error) {
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS="+tagsFlag)
		return cmd.CombinedOutput()
	}
)

// start launches the worker.
func (k *Kernel) start() error {
	cmd := exec.Command(k.binary())
	cmd.Dir = k.opt.Dir
	cmd.Stderr = os.Stderr
	if k.opt.CellMemory > 0 {
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", runtime.MemoryEnv, k.opt.CellMemory))
	}
	// The pipes fail only when Stdin or Stdout is already set.
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	k.worker, k.in = cmd, in
	k.out = bufio.NewScanner(out)
	k.out.Buffer(make([]byte, 1<<20), 1<<30)
	k.loaded = map[string]bool{}
	return nil
}

// stop ends the worker, if any.
func (k *Kernel) stop() error {
	if k.worker == nil {
		return nil
	}
	_, _ = k.in.Write([]byte(`{"op":"exit"}` + "\n"))
	_ = k.in.Close()
	err := k.worker.Wait()
	k.worker = nil
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		k.exitCode = ee.ExitCode()
		return nil // it may have died first; that was reported
	}
	return err
}

// learn records the Go types a Ruby cell gave its variables.
func (k *Kernel) learn(notes map[string]string) {
	changed := false
	for name, typ := range notes {
		if k.rubyTypes[name] != typ {
			k.rubyTypes[name] = typ
			changed = true
		}
	}
	if !changed {
		return
	}
	for name := range notes {
		k.changed[name] = true
	}
	k.typesChanged = true
	b, _ := json.Marshal(k.rubyTypes)
	_ = writeFile(k.typesPath(), b, 0o644)
}

// typesPath is where the Ruby variables' Go types are kept.
func (k *Kernel) typesPath() string { return filepath.Join(k.opt.Dir, "ruby-types.json") }

// readsChanged reports whether a Go cell reads one of the variables whose
// type changed (those of a language that learns its types, Ruby): it was
// compiled against the old type.
func readsChanged(prog *gen.Program, changed map[string]bool) bool {
	for _, c := range prog.Graph.Cells {
		if analysis.LearnsTypes(c.Lang) {
			continue
		}
		for _, u := range c.Uses {
			if o, ok := prog.Graph.Owner[u]; ok && changed[u] && analysis.LearnsTypes(prog.Graph.Cells[o].Lang) {
				return true
			}
		}
	}
	return false
}
