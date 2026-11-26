package session

import "syscall"

// sysProcAttr needs nothing on Windows: the pseudoconsole is attached when the
// process is created.
func sysProcAttr() *syscall.SysProcAttr {
	return nil
}
