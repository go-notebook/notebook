//go:build unix

package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

// A session's worker, in this process: the system calls that would move its
// file descriptors 0 and 1 are recorded instead, and the protocol runs over
// pipes.
func TestServeInterpreterProcess(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var moved [][2]int
	dup = func(fd int) (int, error) {
		switch fd {
		case 0:
			return unix.Dup(int(inR.Fd()))
		case 1:
			return unix.Dup(int(outW.Fd()))
		}
		return unix.Dup(fd)
	}
	dup2 = func(from, to int) error { moved = append(moved, [2]int{from, to}); return nil }
	exited := make(chan int, 1)
	exitProcess = func(c int) { exited <- c }
	t.Cleanup(func() {
		dup, dup2, exitProcess = unix.Dup, unix.Dup2, os.Exit
		_ = outW.Close()
		_ = outR.Close()
	})

	_, _ = inW.WriteString(`{"op":"eval","source":"x","line":1}` + "\n" + `{"op":"exit"}` + "\n")
	_ = inW.Close()
	ServeInterpreter(func(o, e io.Writer) Interpreter { return &fakeInterp{out: o, errOut: e} })
	// The protocol holds a duplicate of outW, open until the process ends:
	// read the one reply, not up to an end.
	sc := bufio.NewScanner(outR)
	var r Reply
	if !sc.Scan() || json.Unmarshal(sc.Bytes(), &r) != nil || r.Inspect != `"x"` {
		t.Errorf("reply %q", sc.Text())
	}
	// fd 0 was pointed at the null device, fd 1 at fd 2.
	if len(moved) != 2 || moved[0][1] != 0 || moved[1] != [2]int{2, 1} {
		t.Errorf("moved %v", moved)
	}
	if c := <-exited; c != 0 {
		t.Errorf("exit %d", c)
	}
}

func TestProtocolInWithoutNullDevice(t *testing.T) {
	openNull = func() (*os.File, error) { return nil, errors.New("no null device") }
	var moved []int
	dup2 = func(from, to int) error { moved = append(moved, to); return nil }
	t.Cleanup(func() {
		openNull = func() (*os.File, error) { return os.Open(os.DevNull) }
		dup2 = unix.Dup2
	})
	f := protocolIn()
	defer f.Close()
	if slices.Contains(moved, 0) {
		t.Error("fd 0 moved without a null device")
	}
}

// failAfter makes the dup system call fail after n successful calls, until
// the test ends.
func failAfter(t *testing.T, n int) {
	t.Helper()
	calls := 0
	dup = func(fd int) (int, error) {
		calls++
		if calls > n {
			return -1, errors.New("no dup")
		}
		return unix.Dup(fd)
	}
	t.Cleanup(func() { dup = unix.Dup })
}

func TestDupFailures(t *testing.T) {
	failAfter(t, 0)
	if f := protocolOut(); f == nil {
		t.Error("protocolOut gave nil")
	}
	if f := protocolIn(); f != os.Stdin {
		t.Error("protocolIn without dup is not the standard input")
	}
	if _, err := redirect(nil); err == nil {
		t.Error("first dup failure not reported")
	}
	failAfter(t, 1)
	if _, err := redirect(nil); err == nil {
		t.Error("second dup failure not reported")
	}
}
