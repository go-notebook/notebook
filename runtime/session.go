package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sync"
	"time"
)

// Interpreter evaluates the inputs of an interactive session (notebook irb)
// one at a time, as irb does. go-scikit/ruby's cells.Session is one.
type Interpreter interface {
	// Eval evaluates src, which starts at line of file, and returns its
	// value's inspect and, for a value that shows itself (a figure), its MIME
	// bundle. An error that has a Class method names the exception.
	Eval(src, file string, line int) (inspect string, display map[string][]byte, err error)
	// Complete returns the completions of the word before the cursor.
	Complete(target string) []string
}

// SessionFile is the file name a session's inputs are evaluated under, as
// irb's are: backtraces read (irb):3.
const SessionFile = "(irb)"

// ServeInterpreter is the main of a session's worker: it answers the
// kernel's "eval" and "complete" requests with the interpreter open returns
// until the kernel says exit or closes the input, and ends the process at
// once when the input closes in the middle of an evaluation (the kernel is
// gone). What the interpreter prints, through out and errOut, is sent to the
// kernel as it is printed, ahead of the reply that ends the input. What the
// process writes to its standard output otherwise goes to its standard
// error, which the kernel shows; and the interpreter reads an empty standard
// input: the worker's own two are the protocol.
func ServeInterpreter(open func(out, errOut io.Writer) Interpreter) {
	watchMemory(os.Getenv(MemoryEnv), 20*time.Millisecond, os.Exit)
	in := protocolIn()
	out := protocolOut()
	strayToStderr()
	serveInterpreter(open, in, out, exitProcess)
}

// exitProcess is os.Exit, a seam for the tests.
var exitProcess = os.Exit

// session writes a worker's replies, one JSON object per line; what Ruby
// threads print reaches it from their own goroutines.
type session struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (s *session) send(r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(r) // a kernel that stopped reading is gone; the input's end stops the worker
}

// streamWriter sends what is written to it as output, in order.
type streamWriter struct{ s *session }

func (w streamWriter) Write(p []byte) (int, error) {
	w.s.send(Reply{Output: string(p), More: true})
	return len(p), nil
}

// serveInterpreter is ServeInterpreter on explicit streams; exit is called
// when the input ends.
func serveInterpreter(open func(out, errOut io.Writer) Interpreter, in io.Reader, out io.Writer, exit func(int)) {
	s := &session{enc: json.NewEncoder(out)}
	w := streamWriter{s}
	interp := open(w, w)
	reqs := make(chan Request)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 1<<20), 1<<30)
		for sc.Scan() {
			var req Request
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				req = Request{Op: "bad request: " + err.Error()}
			}
			reqs <- req
		}
		exit(0) // the kernel is gone: stop now, even in the middle of an input
		close(reqs)
	}()
	for req := range reqs {
		switch req.Op {
		case "eval":
			s.send(evaluate(interp, req))
		case "complete":
			s.send(complete(interp, req.Source))
		case "exit":
			return
		default:
			s.send(Reply{Error: "unknown op " + req.Op})
		}
	}
}

// evaluate runs one input; a Go panic in what it calls fails the input, not
// the worker.
func evaluate(interp Interpreter, req Request) (rep Reply) {
	start := time.Now()
	defer func() {
		rep.Took = time.Since(start)
		if v := recover(); v != nil {
			rep = Reply{Error: fmt.Sprintf("panic: %v\n\n%s", v, debug.Stack()), Took: time.Since(start)}
		}
	}()
	insp, disp, err := interp.Eval(req.Source, SessionFile, req.Line)
	if err != nil {
		rep.Error = err.Error()
		if c, ok := err.(interface{ Class() string }); ok {
			rep.Class = c.Class()
		}
		return rep
	}
	rep.Inspect = insp
	if disp != nil {
		rep.Display = []map[string][]byte{disp}
	}
	return rep
}

// complete asks for the completions of a word; a panic gives none.
func complete(interp Interpreter, target string) (rep Reply) {
	defer func() {
		if recover() != nil {
			rep = Reply{}
		}
	}()
	return Reply{Candidates: interp.Complete(target)}
}
