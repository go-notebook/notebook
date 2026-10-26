package kernel

import "syscall"

// ownProcessGroup puts a session's worker in a process group of its own,
// which the console's Ctrl-C does not reach.
func ownProcessGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
