//go:build windows

package packages

import "os/exec"

// On Windows, which has no release, exec.CommandContext kills only the entrypoint, and nothing
// is killed after it exits.
func killGroup(cmd *exec.Cmd) {}

func reapGroup(cmd *exec.Cmd) {}
