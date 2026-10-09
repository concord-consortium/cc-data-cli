//go:build windows

package packages

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// On Windows, which has no release, exec.CommandContext kills only the entrypoint, and nothing
// is killed after it exits.
func killGroup(cmd *exec.Cmd) {}

func reapGroup(cmd *exec.Cmd) {}

// exposeCCData puts the running binary's own folder first on the package's PATH when it is named
// cc-data, since creating a link needs a privilege on Windows. Under any other name the package's
// cc-data calls reach whatever is on PATH, and the warning names it.
func exposeCCData(_, exe string, lookPath func(string) (string, error)) (string, string, error) {
	if exe != "" && strings.EqualFold(strings.TrimSuffix(filepath.Base(exe), ".exe"), "cc-data") {
		return filepath.Dir(exe), "", nil
	}
	found, err := lookPath("cc-data")
	if err != nil {
		return "", "this binary is not named cc-data and no cc-data is on PATH, so the package's own cc-data calls will fail", nil
	}
	return "", fmt.Sprintf("this binary is not named cc-data, so the package's own cc-data calls reach %s", found), nil
}
