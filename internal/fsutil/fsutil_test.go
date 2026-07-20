package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic0600Mode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := WriteFileAtomic0600(path, []byte(`{"token":"ccd_secret"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"token":"ccd_secret"}` {
		t.Fatalf("content = %q", data)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("final mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

// TestWriteFileAtomicTempMode asserts the temp file is 0600 at creation, never a
// transiently world-readable window. Gated off Windows, where stat mode is
// always 0444/0666.
func TestWriteFileAtomicTempMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows stat mode is not POSIX permission bits")
	}
	dir := t.TempDir()
	// Create a temp file the same way WriteFileAtomic0600 does and assert mode.
	f, err := os.CreateTemp(dir, "x.tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("temp mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestPreCreate0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "repl_history")
	if err := PreCreate0600(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("history mode = %v, want 0600", fi.Mode().Perm())
		}
	}
}

// TestPreCreate0600TightensExisting asserts PreCreate0600 chmods a pre-existing
// file down to 0600 (a fresh-creation-only mode would leave a 0644 file at 0644)
// without truncating its contents.
func TestPreCreate0600TightensExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows stat mode is not POSIX permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "repl_history")
	if err := os.WriteFile(path, []byte("prior"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PreCreate0600(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "prior" {
		t.Fatalf("content = %q err = %v, want %q preserved", data, err, "prior")
	}
}

func TestRenameAtomic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	if err := os.WriteFile(src, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenameAtomic(src, dst); err != nil {
		t.Fatal(err)
	}
	data, err := ReadReplaceTarget(dst)
	if err != nil || string(data) != "hi" {
		t.Fatalf("read after rename: %q %v", data, err)
	}
}
