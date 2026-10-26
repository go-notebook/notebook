//go:build !unix

package runtime

import "os"

// protocolOut returns the original standard output: without file descriptor
// duplication, the cells' output is captured by swapping os.Stdout instead.
func protocolOut() *os.File { return os.Stdout }

// openNull opens the null device, a seam for the tests.
var openNull = func() (*os.File, error) { return os.Open(os.DevNull) }

// protocolIn returns the original standard input, and makes os.Stdin the
// null device, so that the interpreter reading its standard input gets an
// end of file rather than the protocol.
func protocolIn() *os.File {
	in := os.Stdin
	if null, err := openNull(); err == nil {
		os.Stdin = null
	}
	return in
}

// strayToStderr makes os.Stdout the standard error, once the protocol holds
// the original: what a session's worker writes there outside the interpreter
// reaches the kernel's error stream.
func strayToStderr() { os.Stdout = os.Stderr }

// redirect points os.Stdout and os.Stderr at w. What bypasses them (the
// println builtin) is not captured on these systems.
func redirect(w *os.File) (restore func(), err error) {
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	return func() { os.Stdout, os.Stderr = stdout, stderr }, nil
}
