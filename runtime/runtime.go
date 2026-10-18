// Package runtime is linked into every compiled notebook. The kernel starts
// the notebook's binary as a worker and drives it through Serve: it asks the
// worker to run a cell, to hand over the value of a notebook variable, or to
// take one back after a rebuild, so that cells whose code and inputs did not
// change are not run again.
//
// The protocol is one JSON object per line on the worker's standard input
// and output. What a cell prints goes to a pipe of its own, so it can never
// be mistaken for a reply.
package runtime

import (
	"bufio"
	"bytes"
	"encoding"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime/debug"
	"sync"
	"time"
)

// Displayer is implemented by values that know how to show themselves: the
// result maps a MIME type ("text/plain", "text/html", "image/svg+xml",
// "image/png", ...) to the bytes of that representation.
type Displayer interface {
	Display() map[string][]byte
}

// Cell is handed to each compiled cell; the generated code calls Show with
// the cell's final expression.
type Cell struct {
	shown []map[string][]byte
	notes map[string]string
}

// Note records a fact about the cell for the kernel: the Go type a Ruby
// variable has now, which the next program declares it with.
func (c *Cell) Note(name, value string) {
	if c.notes == nil {
		c.notes = map[string]string{}
	}
	c.notes[name] = value
}

// CellError is implemented by a panic value that is a cell's own failure,
// reported as it is rather than with the stack of the worker: a Ruby
// exception at its line in the notebook, a deferred cell.
type CellError interface {
	CellError() string
}

// Deferred fails a cell that cannot be compiled yet, with the reason.
type Deferred string

// CellError is the reason.
func (d Deferred) CellError() string { return string(d) }

// Show records the display of each value. A nil interface — the nil error
// of a call run for its effect — shows nothing.
func (c *Cell) Show(vs ...any) {
	for _, v := range vs {
		if v != nil {
			c.shown = append(c.shown, Bundle(v))
		}
	}
}

// Result shows v, the value of a call that also returned an error, or, when
// err is not nil, fails the cell with it, so the cells that read this one do
// not run on a value that is not there.
func (c *Cell) Result(v any, err error) {
	if err != nil {
		panic(fmt.Sprintf("error: %v", err))
	}
	c.Show(v)
}

// Bundle returns the MIME bundle of a value: its own when it implements
// Displayer, else its fmt %v text. A Displayer without text/plain (a figure
// that is only SVG and PNG) gets its type, <*plot.Figure>, rather than the %v
// of its innards.
func Bundle(v any) map[string][]byte {
	if d, ok := v.(Displayer); ok {
		b := d.Display()
		if _, ok := b["text/plain"]; !ok {
			b["text/plain"] = []byte(fmt.Sprintf("<%T>", v))
		}
		return b
	}
	return map[string][]byte{"text/plain": []byte(fmt.Sprintf("%v", v))}
}

// Program is what the generated main hands to Serve.
type Program struct {
	// Cells holds the compiled cells by their index among the notebook's
	// code cells; a cell that defines nothing and shows nothing still has
	// an entry.
	Cells []func(*Cell)

	// Vars maps each notebook variable to a pointer to it.
	Vars map[string]any
}

// Request is a command from the kernel.
type Request struct {
	Op   string `json:"op"` // "run", "get", "set" or "exit"
	Cell int    `json:"cell,omitempty"`
	Name string `json:"name,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// Reply answers a Request.
type Reply struct {
	Cell    int                 `json:"cell,omitempty"`
	Name    string              `json:"name,omitempty"`
	Output  string              `json:"output,omitempty"`  // what the cell printed
	Display []map[string][]byte `json:"display,omitempty"` // what it showed
	Error   string              `json:"error,omitempty"`   // a panic, with its stack
	Took    time.Duration       `json:"took,omitempty"`    // how long the cell ran, in the worker
	Notes   map[string]string   `json:"notes,omitempty"`   // what the cell noted (Cell.Note)
	Data    []byte              `json:"data,omitempty"`    // an encoded value
	// Volatile reports a value that cannot be encoded (a func, a channel,
	// unexported state): the cell that makes it has to run again instead.
	Volatile bool `json:"volatile,omitempty"`
}

// Serve answers the kernel until it says exit or closes the input.
func Serve(p Program) {
	watchMemory(os.Getenv(MemoryEnv), 20*time.Millisecond, os.Exit)
	serve(p, os.Stdin, protocolOut())
}

// serve is Serve on explicit streams.
func serve(p Program, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<30)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req Request
		var rep Reply
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			rep.Error = "bad request: " + err.Error()
			_ = enc.Encode(rep)
			continue
		}
		switch req.Op {
		case "run":
			rep = run(p, req.Cell)
		case "get":
			rep = get(p, req.Name)
		case "set":
			rep = set(p, req.Name, req.Data)
		case "exit":
			return
		default:
			rep.Error = "unknown op " + req.Op
		}
		_ = enc.Encode(rep)
	}
}

// pipe and redirectOutput are os.Pipe and redirect, variables so a test can
// make them fail on every system.
var (
	pipe           = os.Pipe
	redirectOutput = redirect
)

// stdMu serialises the redirection of os.Stdout and os.Stderr.
var stdMu sync.Mutex

// run runs one cell, capturing what it prints and recovering a panic.
func run(p Program, i int) (rep Reply) {
	rep.Cell = i
	if i < 0 || i >= len(p.Cells) {
		rep.Error = fmt.Sprintf("no cell %d", i)
		return rep
	}
	stdMu.Lock()
	defer stdMu.Unlock()
	r, w, err := pipe()
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	restore, err := redirectOutput(w)
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		rep.Error = err.Error()
		return rep
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	c := &Cell{}
	start := time.Now()
	func() {
		defer func() {
			if v := recover(); v != nil {
				if ce, ok := v.(CellError); ok {
					rep.Error = ce.CellError()
					return
				}
				rep.Error = fmt.Sprintf("panic: %v\n\n%s", v, debug.Stack())
			}
		}()
		p.Cells[i](c)
	}()
	rep.Took = time.Since(start)
	restore()
	_ = w.Close()
	<-done
	_ = r.Close()
	rep.Output = buf.String()
	rep.Display = c.shown
	rep.Notes = c.notes
	return rep
}

// get encodes a variable's value.
func get(p Program, name string) (rep Reply) {
	rep.Name = name
	ptr, ok := p.Vars[name]
	if !ok {
		rep.Error = "no variable " + name
		return rep
	}
	data, err := Encode(ptr)
	if err != nil {
		rep.Volatile = true
		rep.Error = err.Error()
		return rep
	}
	rep.Data = data
	return rep
}

// set decodes data into a variable.
func set(p Program, name string, data []byte) (rep Reply) {
	rep.Name = name
	ptr, ok := p.Vars[name]
	if !ok {
		rep.Error = "no variable " + name
		return rep
	}
	if err := Decode(ptr, data); err != nil {
		rep.Error = err.Error()
	}
	return rep
}

// The first byte of an encoding says how the rest was made.
const (
	tagNil    byte = iota // a nil pointer, map, slice or interface: nothing follows
	tagBinary             // the value's own MarshalBinary
	tagGob                // encoding/gob
)

// Encode encodes the value ptr points to: nil as nil, a value with its own
// MarshalBinary with it, anything else with gob when every field it can hold
// is exported (gob would silently drop the others). Anything else is an
// error, which makes the value volatile.
func Encode(ptr any) ([]byte, error) {
	v := reflect.ValueOf(ptr).Elem()
	if isNil(v) {
		return []byte{tagNil}, nil
	}
	m, ok := v.Interface().(encoding.BinaryMarshaler)
	if !ok {
		m, ok = v.Addr().Interface().(encoding.BinaryMarshaler)
	}
	if ok {
		data, err := m.MarshalBinary()
		if err != nil {
			return nil, err
		}
		return append([]byte{tagBinary}, data...), nil
	}
	if why := unkeepable(v.Type(), map[reflect.Type]bool{}); why != "" {
		return nil, fmt.Errorf("cannot keep a %v: %s", v.Type(), why)
	}
	b := bytes.NewBuffer([]byte{tagGob})
	if err := gob.NewEncoder(b).EncodeValue(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Decode is the inverse of Encode.
func Decode(ptr any, data []byte) error {
	v := reflect.ValueOf(ptr).Elem()
	if len(data) == 0 {
		return errors.New("empty encoding")
	}
	tag, data := data[0], data[1:]
	switch tag {
	case tagNil:
		v.SetZero()
		return nil
	case tagBinary:
		if v.Kind() == reflect.Pointer && v.Type().Implements(unmarshaler) {
			nv := reflect.New(v.Type().Elem())
			if err := nv.Interface().(encoding.BinaryUnmarshaler).UnmarshalBinary(data); err != nil {
				return err
			}
			v.Set(nv)
			return nil
		}
		if u, ok := v.Addr().Interface().(encoding.BinaryUnmarshaler); ok {
			return u.UnmarshalBinary(data)
		}
		return fmt.Errorf("a %v cannot decode itself", v.Type())
	case tagGob:
		return gob.NewDecoder(bytes.NewReader(data)).DecodeValue(v)
	}
	return fmt.Errorf("unknown encoding tag %d", tag)
}

var unmarshaler = reflect.TypeFor[encoding.BinaryUnmarshaler]()

// isNil reports whether v is a nil pointer, map, slice or interface.
func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// unkeepable says why values of type t cannot be encoded faithfully, or ""
// if they can.
func unkeepable(t reflect.Type, seen map[reflect.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	if t.Implements(reflect.TypeFor[encoding.BinaryMarshaler]()) {
		return ""
	}
	switch t.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer, reflect.Uintptr:
		return "it holds a " + t.Kind().String()
	case reflect.Interface:
		return "it holds an interface, whose dynamic types are not known in advance"
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return unkeepable(t.Elem(), seen)
	case reflect.Map:
		if why := unkeepable(t.Key(), seen); why != "" {
			return why
		}
		return unkeepable(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				return "its field " + f.Name + " is unexported"
			}
			if why := unkeepable(f.Type, seen); why != "" {
				return why
			}
		}
	}
	return ""
}
