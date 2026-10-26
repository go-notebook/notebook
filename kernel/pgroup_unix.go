//go:build unix

package kernel

import "syscall"

// ownProcessGroup puts a session's worker in a process group of its own, out
// of the reach of the terminal's Ctrl-C.
func ownProcessGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
