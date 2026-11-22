//go:build !linux

package vm

import "syscall"

func procAttr(bool) *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }
