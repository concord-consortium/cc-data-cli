//go:build windows

package packages

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExposeCCDataOnWindowsUsesTheBinarysFolderByName(t *testing.T) {
	other := func(string) (string, error) { return `C:\tools\cc-data.exe`, nil }
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	build := filepath.Join(`C:\`, "build")
	if dir, warning, err := exposeCCData("", filepath.Join(build, "cc-data.exe"), other); err != nil || dir != build || warning != "" {
		t.Errorf("cc-data.exe: %q, %q, %v", dir, warning, err)
	}
	if dir, warning, _ := exposeCCData("", filepath.Join(build, "main.exe"), other); dir != "" || !strings.Contains(warning, `C:\tools\cc-data.exe`) {
		t.Errorf("go run: %q, %q", dir, warning)
	}
	if _, warning, _ := exposeCCData("", filepath.Join(build, "main.exe"), none); !strings.Contains(warning, "will fail") {
		t.Errorf("no cc-data on PATH: %q", warning)
	}
}
