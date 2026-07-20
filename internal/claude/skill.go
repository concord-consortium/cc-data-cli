// Package claude installs and maintains the Claude Code skill and CLAUDE.md
// pointer, plus the every-invocation freshness check that replaces installer hooks.
package claude

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed skill/SKILL.md
var skillBody string

var stampRe = regexp.MustCompile(`cc-data skill version:\s*(\S+)`)

// homeDir is a seam so tests can pin the home directory.
var defaultHomeDir = os.UserHomeDir
var homeDir = defaultHomeDir

// SkillDir returns ~/.claude/skills/cc-data.
func SkillDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", "cc-data"), nil
}

func skillPath() (string, error) {
	dir, err := SkillDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SKILL.md"), nil
}

// stampedContent returns the skill body with the version stamp appended.
func stampedContent(version string) string {
	return strings.TrimRight(skillBody, "\n") + fmt.Sprintf("\n\n<!-- cc-data skill version: %s -->\n", version)
}

// WriteSkill writes the skill file (and its directory) stamped with version.
func WriteSkill(version string) error {
	dir, err := SkillDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "SKILL.md")
	return os.WriteFile(path, []byte(stampedContent(version)), 0o644)
}

// installedStamp returns the version stamp of the installed skill, or "" if the
// skill is not installed.
func installedStamp() (string, bool) {
	path, err := skillPath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	m := stampRe.FindSubmatch(data)
	if m == nil {
		return "", true // installed but unstamped
	}
	return string(m[1]), true
}

// MaybeRefresh is the every-invocation freshness check: if the skill directory
// exists and the stamp differs from the binary version, silently rewrite the
// skill and pointer. It is a no-op when the skill is not installed.
func MaybeRefresh(version string) {
	stamp, installed := installedStamp()
	if !installed {
		return
	}
	if stamp == version {
		return
	}
	_ = WriteSkill(version)
	_ = AddPointer()
}

// Install writes the skill and the CLAUDE.md pointer.
func Install(version string) error {
	if err := WriteSkill(version); err != nil {
		return err
	}
	return AddPointer()
}

// Uninstall removes the skill directory and the CLAUDE.md pointer.
func Uninstall() error {
	dir, err := SkillDir()
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return RemovePointer()
}
