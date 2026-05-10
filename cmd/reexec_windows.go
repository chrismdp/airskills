//go:build windows

package cmd

import (
	"os"
	"os/exec"
)

// reExec is the Windows equivalent of POSIX exec. Windows has no true
// exec syscall, so we spawn a child process with inherited stdio, wait
// for it to finish, and exit the parent with the child's exit code.
// The parent shim lingers, but the user-visible behaviour matches
// (single command, single exit code).
func reExec(execPath string, args []string, env []string) error {
	cmd := exec.Command(execPath, args[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
