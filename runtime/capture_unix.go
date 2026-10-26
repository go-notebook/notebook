//go:build unix

package runtime

import (
	"os"

	"golang.org/x/sys/unix"
)

// dup and dup2 are the system calls, variables so a test can make them fail.
var (
	dup  = unix.Dup
	dup2 = unix.Dup2
)

// protocolOut returns a stream for the replies that a cell's output cannot
// reach: a duplicate of the original standard output, so that file
// descriptor 1 can be handed to each cell in turn.
func protocolOut() *os.File {
	fd, err := dup(1)
	if err != nil {
		return os.Stdout
	}
	return os.NewFile(uintptr(fd), "protocol")
}

// openNull opens the null device, a seam for the tests.
var openNull = func() (*os.File, error) { return os.Open(os.DevNull) }

// protocolIn returns a stream for the requests of a session's worker: a
// duplicate of the original standard input, whose file descriptor 0 then
// reads the null device, so that the interpreter reading its standard input
// gets an end of file rather than the protocol.
func protocolIn() *os.File {
	fd, err := dup(0)
	if err != nil {
		return os.Stdin
	}
	in := os.NewFile(uintptr(fd), "protocol")
	if null, err := openNull(); err == nil {
		_ = dup2(int(null.Fd()), 0)
		_ = null.Close()
	}
	return in
}

// strayToStderr points file descriptor 1 at the standard error, once the
// protocol has its own duplicate of it: what a session's worker writes there
// outside the interpreter reaches the kernel's error stream.
func strayToStderr() { _ = dup2(2, 1) }

// redirect points file descriptors 1 and 2 at w, so that everything a cell
// writes — through os.Stdout, the println builtin, or C code — is captured,
// and returns the function that puts them back.
func redirect(w *os.File) (restore func(), err error) {
	saved1, err := dup(1)
	if err != nil {
		return nil, err
	}
	saved2, err := dup(2)
	if err != nil {
		_ = unix.Close(saved1)
		return nil, err
	}
	_ = dup2(int(w.Fd()), 1)
	_ = dup2(int(w.Fd()), 2)
	return func() {
		_ = dup2(saved1, 1)
		_ = dup2(saved2, 2)
		_ = unix.Close(saved1)
		_ = unix.Close(saved2)
	}, nil
}
