package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// BuildDirName is where build writes by default: inside the package, and skipped by
// Collect like every dot path, so one build's zip never reaches the next.
const BuildDirName = ".cc-data-build"

// CheckBuildOutput refuses an explicit build output that is a directory, that package run would
// empty, or that a later build would collect.
func CheckBuildOutput(dir, out string) error {
	if info, err := os.Stat(out); err == nil && info.IsDir() {
		return fmt.Errorf("--out %s is a directory; name the zip file to write", out)
	}
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
	if strings.SplitN(filepath.ToSlash(rel), "/", 2)[0] == RunDirName {
		return fmt.Errorf("--out %s is inside %s, which package run empties", out, RunDirName)
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
	if filepath.Base(dir) != BuildDirName {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	} else if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	} else if err := ensureRealDir(dir); err != nil {
		return err
	} else if err := ignoreAll(dir); err != nil {
		return err
	}
	return replaceFile(out, archive)
}

// replaceFile writes data beside path and renames it into place, so a link already at path is
// replaced rather than followed.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".build-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return fsutil.RenameAtomic(tmp.Name(), path)
}
