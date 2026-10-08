//go:build !windows

package packages

import (
	"os/exec"
	"syscall"
)

// killGroup runs the package in its own process group and kills the whole group on timeout,
// so a child the entrypoint started (cc-data, a shell) does not outlive the bound.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// reapGroup kills whatever is left of the package's process group once the entrypoint has
// exited. The group outlives its leader while any member does, so the kill still finds it.
func reapGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
