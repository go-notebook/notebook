//go:build !unix

package runtime

import "os"

// protocolOut returns the original standard output: without file descriptor
// duplication, the cells' output is captured by swapping os.Stdout instead.
func protocolOut() *os.File { return os.Stdout }

// redirect points os.Stdout and os.Stderr at w. What bypasses them (the
// println builtin) is not captured on these systems.
func redirect(w *os.File) (restore func(), err error) {
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	return func() { os.Stdout, os.Stderr = stdout, stderr }, nil
}
