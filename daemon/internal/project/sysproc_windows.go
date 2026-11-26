package project

import "syscall"

// sysProcAttr keeps git from opening a console window when the daemon has
// none of its own.
func sysProcAttr() *syscall.SysProcAttr {
	const createNoWindow = 0x08000000
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
