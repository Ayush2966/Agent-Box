//go:build !linux

package proc

import "syscall"

// Own process group so Stop can signal the whole tree.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
