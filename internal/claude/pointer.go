package claude

import (
	"os"
	"path/filepath"
	"strings"
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
	return os.WriteFile(path, []byte(content), 0o644)
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
	return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644)
}
