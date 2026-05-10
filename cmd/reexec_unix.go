//go:build !windows

package cmd

import "syscall"

// reExec replaces the current process with a fresh instance of the
// airskills binary at execPath. On POSIX, syscall.Exec swaps the
// process image in place, so the new binary inherits stdin/stdout/
// stderr and the parent shell waits on the same PID. Returns an
// error only if the kernel rejects the exec (e.g. binary not
// executable); on success it does not return.
func reExec(execPath string, args []string, env []string) error {
	return syscall.Exec(execPath, args, env)
}
