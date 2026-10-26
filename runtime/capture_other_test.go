//go:build !unix

package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

// A session's worker, in this process: os.Stdin and os.Stdout are pipes,
// which ServeInterpreter takes for the protocol before it replaces them.
func TestServeInterpreterProcess(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	exited := make(chan int, 1)
	exitProcess = func(c int) { exited <- c }
	t.Cleanup(func() { os.Stdin, os.Stdout, exitProcess = stdin, stdout, os.Exit })

	_, _ = inW.WriteString(`{"op":"eval","source":"x","line":1}` + "\n" + `{"op":"exit"}` + "\n")
	_ = inW.Close()
	ServeInterpreter(func(o, e io.Writer) Interpreter { return &fakeInterp{out: o, errOut: e} })
	if os.Stdin == inR || os.Stdout != os.Stderr {
		t.Error("os.Stdin and os.Stdout were not replaced")
	}
	_ = outW.Close()
	sc := bufio.NewScanner(outR)
	var reps []Reply
	for sc.Scan() {
		var r Reply
		_ = json.Unmarshal(sc.Bytes(), &r)
		reps = append(reps, r)
	}
	if len(reps) != 1 || reps[0].Inspect != `"x"` {
		t.Errorf("replies %+v", reps)
	}
	if c := <-exited; c != 0 {
		t.Errorf("exit %d", c)
	}
}

func TestProtocolInWithoutNullDevice(t *testing.T) {
	openNull = func() (*os.File, error) { return nil, errors.New("no null device") }
	stdin := os.Stdin
	t.Cleanup(func() {
		openNull = func() (*os.File, error) { return os.Open(os.DevNull) }
		os.Stdin = stdin
	})
	if f := protocolIn(); f != stdin || os.Stdin != stdin {
		t.Error("os.Stdin replaced without a null device")
	}
}
