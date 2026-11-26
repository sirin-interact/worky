//go:build !windows

package project

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return nil }
