//go:build !unix

package runtime

import "testing"

// failAfter has nothing to break without file descriptor duplication.
func failAfter(t *testing.T, n int) { t.Skip("no dup on this system") }
