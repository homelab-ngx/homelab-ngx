//go:build linux

package vm

import "syscall"

// procAttr puts QEMU in a process group of its own. Unless it is to
// outlive this process (KEEP), the kernel kills it when this process ends,
// so a run that is killed leaves no machine behind.
func procAttr(detach bool) *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Setpgid: true}
	if !detach {
		a.Pdeathsig = syscall.SIGKILL
	}
	return a
}
