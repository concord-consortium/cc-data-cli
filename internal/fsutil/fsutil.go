// Package fsutil provides the shared sensitive-file write helpers: an atomic
// 0600 write, a 0600 pre-create, and robustio-style rename/open wrappers that
// retry the transient Windows sharing violations a fixed-name replace can hit.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic0600 writes data to path via a same-directory 0600 temp file,
// fsyncs it, and atomically renames it into place, so the destination is never
// created with a broader mode even transiently.
func WriteFileAtomic0600(path string, data []byte) error {
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
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return RenameAtomic(tmp, path)
}

// PreCreate0600 ensures path exists with mode 0600, creating it empty if absent
// (used to hand readline a history file with the right mode before it opens it).
func PreCreate0600(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// The 0o600 above only applies on creation; tighten a pre-existing file
	// (which may be 0644) so the caller always gets a 0600 mode.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// EnsureDir creates a directory tree with 0700 (its contents are sensitive).
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}
