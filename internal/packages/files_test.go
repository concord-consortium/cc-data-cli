package packages

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const goodManifest = `{
  "name": "wildfire-responses",
  "title": "Wildfire responses",
  "version": "0.1.0",
  "urls": {"all": [], "any": [], "none": []},
  "clue_prepull": false,
  "entrypoint": "run.py",
  "expected_duration_seconds": 300
}`

// writeTree writes each path's content under dir, creating parents; a path ending in "/" is an
// empty directory.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skipOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}

func TestReadManifestNeedsWhatTheRunActsOn(t *testing.T) {
	files := map[string]bool{"run.py": true}
	if _, err := ReadManifest([]byte(goodManifest), files); err != nil {
		t.Fatalf("good manifest refused: %v", err)
	}
	if _, err := ReadManifest([]byte(goodManifest), map[string]bool{"other.py": true}); err == nil {
		t.Error("a missing entrypoint was accepted")
	}
	for _, duration := range []string{`"300"`, `1.5`, `0`} {
		m := strings.Replace(goodManifest, `"expected_duration_seconds": 300`, `"expected_duration_seconds": `+duration, 1)
		if _, err := ReadManifest([]byte(m), files); err == nil {
			t.Errorf("duration %s was accepted", duration)
		}
	}
}

func TestCollectLeavesOutWhatBuildMustNotShip(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"manifest.json":               goodManifest,
		"run.py":                      "print(1)",
		"lib/q.sql":                   "SELECT 1",
		"lib/y.pyc":                   "x",
		".git/HEAD":                   "ref",
		".cc-data-run/out/display.md": "x",
		".cc-data-build/p-0.1.0.zip":  "x",
		"__pycache__/cache.txt":       "x",
		"local-data/answers.csv":      "x",
		".DS_Store":                   "x",
	})
	files, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(files.Paths, ","); got != "lib/q.sql,manifest.json,run.py" {
		t.Errorf("collected %s", got)
	}
}

func TestCollectRefusesALink(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"manifest.json": goodManifest, "run.py": "print(1)"})
	if err := os.Symlink(filepath.Join(dir, "run.py"), filepath.Join(dir, "alias.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(dir); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("err = %v, want a symbolic-link refusal", err)
	}
}

func TestZipIsReproducible(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"manifest.json": goodManifest, "run.py": "print(1)"})
	build := func() string {
		files, err := Collect(dir)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Zip(files)
		if err != nil {
			t.Fatal(err)
		}
		return Checksum(b)
	}
	first := build()
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "run.py"), later, later); err != nil {
		t.Fatal(err)
	}
	if second := build(); second != first {
		t.Errorf("checksum changed with an mtime: %s then %s", first, second)
	}
}

func TestModesSurviveTheZip(t *testing.T) {
	skipOnWindows(t, "Windows records no execute bit")
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"manifest.json": goodManifest, "run.py": "print(1)", "run.sh": "#!/bin/sh\n"})
	if err := os.Chmod(filepath.Join(dir, "run.py"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "manifest.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Zip(files)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]os.FileMode{}
	for _, f := range r.File {
		modes[f.Name] = f.Mode().Perm()
	}
	if modes["run.py"] != 0o755 || modes["manifest.json"] != 0o644 {
		t.Errorf("zipped modes = %v, want run.py 0755 and manifest.json 0644", modes)
	}
	if err := files.CheckEntrypointMode("run.sh"); err == nil {
		t.Error("a 0644 run.sh entrypoint passed")
	}
	if err := os.Chmod(filepath.Join(dir, "run.py"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err = Collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.CheckEntrypointMode("run.py"); err != nil {
		t.Errorf("a 0644 run.py was refused: %v", err)
	}
}

func TestDisplayCapEdges(t *testing.T) {
	if DisplayLimitBytes != 65536 {
		t.Fatalf("DisplayLimitBytes = %d, want 65536", DisplayLimitBytes)
	}
	out := t.TempDir()
	write := func(n int) {
		if err := os.WriteFile(filepath.Join(out, "display.md"), bytes.Repeat([]byte("x"), n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(DisplayLimitBytes)
	if res, err := ReadResult(out); err != nil || res.DisplayBytes != DisplayLimitBytes {
		t.Errorf("at the cap: %+v, %v", res, err)
	}
	write(DisplayLimitBytes + 1)
	var refused *OutputRefused
	if _, err := ReadResult(out); !errors.As(err, &refused) {
		t.Errorf("one byte over the cap: err = %v, want OutputRefused", err)
	}
}

func TestResultReadsOnlyTheRunnersKeys(t *testing.T) {
	out := t.TempDir()
	writeTree(t, out, map[string]string{
		"display.md":  "# Class\n",
		"summary.txt": "  3 answers from 2 learners  \r\nsecond line\n",
		"counts.json": `{"answers": 3, "logs": 7, "log_freshness_at": "2026-10-08T00:00:00Z", "learners": 2}`,
	})
	res, err := ReadResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary == nil || *res.Summary != "3 answers from 2 learners" {
		t.Errorf("summary = %v", res.Summary)
	}
	if len(res.Counts) != 3 || res.Counts["answers"] != float64(3) || res.Counts["learners"] != nil {
		t.Errorf("counts = %v", res.Counts)
	}
}

func TestResultRefusesALinkedDisplay(t *testing.T) {
	skipOnWindows(t, "links need privileges on Windows")
	out := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(out, "display.md")); err != nil {
		t.Fatal(err)
	}
	var refused *OutputRefused
	if _, err := ReadResult(out); !errors.As(err, &refused) {
		t.Errorf("err = %v, want OutputRefused", err)
	}
}
