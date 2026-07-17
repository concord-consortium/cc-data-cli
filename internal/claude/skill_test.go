package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = defaultHomeDir })
	return home
}

func TestInstallWritesSkillAndPointer(t *testing.T) {
	home := setHome(t)
	if err := Install("1.2.3"); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "cc-data", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "cc-data skill version: 1.2.3") {
		t.Fatal("skill should carry the version stamp")
	}
	if !strings.Contains(string(skill), "name: cc-data") {
		t.Fatal("skill should have frontmatter")
	}
	md, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if !strings.Contains(string(md), pointerMarker) {
		t.Fatal("CLAUDE.md should carry the pointer")
	}
}

func TestStampCompareMatrix(t *testing.T) {
	home := setHome(t)
	Install("1.0.0")

	// Newer binary rewrites the stamp.
	MaybeRefresh("1.1.0")
	skill, _ := os.ReadFile(filepath.Join(home, ".claude", "skills", "cc-data", "SKILL.md"))
	if !strings.Contains(string(skill), "cc-data skill version: 1.1.0") {
		t.Fatal("newer binary should rewrite the stamp")
	}

	// Equal stamp: no change (mtime-independent check via content equality).
	MaybeRefresh("1.1.0")
	skill2, _ := os.ReadFile(filepath.Join(home, ".claude", "skills", "cc-data", "SKILL.md"))
	if string(skill) != string(skill2) {
		t.Fatal("equal stamp should not change the file")
	}
}

func TestMaybeRefreshSkipsWhenNotInstalled(t *testing.T) {
	home := setHome(t)
	MaybeRefresh("1.0.0")
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "cc-data")); !os.IsNotExist(err) {
		t.Fatal("MaybeRefresh must not create the skill for non-init users")
	}
}

func TestPointerIdempotent(t *testing.T) {
	setHome(t)
	if err := AddPointer(); err != nil {
		t.Fatal(err)
	}
	if err := AddPointer(); err != nil {
		t.Fatal(err)
	}
	path, _ := claudeMdPath()
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), pointerMarker) != 1 {
		t.Fatalf("pointer should appear exactly once, got %d", strings.Count(string(data), pointerMarker))
	}
	if err := RemovePointer(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), pointerMarker) {
		t.Fatal("pointer should be removed")
	}
}

func TestUninstallRemovesSkill(t *testing.T) {
	home := setHome(t)
	Install("1.0.0")
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "cc-data")); !os.IsNotExist(err) {
		t.Fatal("uninstall should remove the skill dir")
	}
}
