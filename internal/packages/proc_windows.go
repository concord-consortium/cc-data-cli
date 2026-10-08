//go:build windows

package packages

import "os/exec"

// killGroup and reapGroup kill only the entrypoint on Windows, which has no release.
func killGroup(cmd *exec.Cmd) {}

func reapGroup(cmd *exec.Cmd) {}
