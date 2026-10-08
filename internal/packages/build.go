package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildDirName is where build writes by default: inside the package, and skipped by
// Collect like every dot path, so one build's zip never reaches the next.
const BuildDirName = ".cc-data-build"

// CheckOutsidePackage refuses an explicit build output that a later build would collect.
func CheckOutsidePackage(dir, out string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absDir, absOut)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	if excluded(filepath.ToSlash(rel), false) || excludedAncestor(filepath.ToSlash(rel)) {
		return nil
	}
	return fmt.Errorf("--out %s is inside the package, where the next build would include it", out)
}

func excludedAncestor(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if excluded(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return false
}

// WriteBuild writes the archive, creating its directory, and ignores the default build
// directory for git.
func WriteBuild(out string, archive []byte) error {
	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if filepath.Base(dir) == BuildDirName {
		if err := ignoreAll(dir); err != nil {
			return err
		}
	}
	return os.WriteFile(out, archive, 0o644)
}
