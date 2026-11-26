//go:build !windows

package session

import "syscall"

// sysProcAttr makes the child the leader of a new session with the PTY as its
// controlling terminal, so job control and signals from the keyboard work.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true}
}
