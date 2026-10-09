//go:build unix

package runtime

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

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
	if _, err := redirect(nil); err == nil {
		t.Error("first dup failure not reported")
	}
	failAfter(t, 1)
	if _, err := redirect(nil); err == nil {
		t.Error("second dup failure not reported")
	}
}
