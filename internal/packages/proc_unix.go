//go:build !windows

package packages

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// killGroup runs the package in its own process group and kills the whole group when the run
// is canceled or times out, so a child the entrypoint started does not outlive it.
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

// exposeCCData links bin/cc-data to the running binary, whatever its name, and returns bin to put
// first on the package's PATH. Unlike the binary's own folder, which may be a shared bin, it puts
// nothing else ahead of the user's PATH.
func exposeCCData(bin, exe string, _ func(string) (string, error)) (string, string, error) {
	if exe == "" {
		return "", "", nil
	}
	if err := os.Symlink(exe, filepath.Join(bin, "cc-data")); err != nil {
		return "", "", err
	}
	return bin, "", nil
}
