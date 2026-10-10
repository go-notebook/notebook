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
