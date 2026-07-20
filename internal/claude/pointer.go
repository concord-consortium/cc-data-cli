package claude

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
)

// pointerMarker identifies the cc-data line in ~/.claude/CLAUDE.md so it can be
// added and removed idempotently.
const pointerMarker = "<!-- cc-data-skill -->"

const pointerLine = "- For downloading and querying Concord Consortium researcher data, use the cc-data skill (`~/.claude/skills/cc-data/SKILL.md`). " + pointerMarker

func claudeMdPath() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}

// AddPointer adds the one-line skill pointer to ~/.claude/CLAUDE.md if absent.
func AddPointer() error {
	path, err := claudeMdPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), pointerMarker) {
		return nil
	}
	content := string(data)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += pointerLine + "\n"
	return writeFileAtomic(path, []byte(content), fileMode(path, 0o644))
}

// RemovePointer removes the skill pointer line from ~/.claude/CLAUDE.md.
func RemovePointer() error {
	path, err := claudeMdPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, pointerMarker) {
			continue
		}
		kept = append(kept, l)
	}
	return writeFileAtomic(path, []byte(strings.Join(kept, "\n")), fileMode(path, 0o644))
}

// fileMode returns path's current permission bits, or def when it does not exist.
func fileMode(path string, def os.FileMode) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return def
}

// writeFileAtomic writes data to path via a same-directory temp file, fsyncs it,
// chmods it to mode, and atomically renames it into place, so an interrupt can
// never truncate CLAUDE.md to a partial read-modify-write result.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fsutil.RenameAtomic(tmp, path)
}
