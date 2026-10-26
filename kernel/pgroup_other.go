//go:build !unix && !windows

package kernel

import "syscall"

// ownProcessGroup has nothing to set on systems without process groups.
func ownProcessGroup() *syscall.SysProcAttr { return nil }
