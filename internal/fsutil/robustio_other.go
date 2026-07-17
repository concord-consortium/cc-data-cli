//go:build !windows

package fsutil

import "os"

// RenameAtomic renames oldpath to newpath. On POSIX this is a plain atomic rename.
func RenameAtomic(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// OpenReplaceTarget opens a fixed-name file that another process may replace by
// rename. On POSIX this is a plain open.
func OpenReplaceTarget(path string) (*os.File, error) {
	return os.Open(path)
}

// ReadReplaceTarget reads a fixed-name replace-target file. On POSIX this is a
// plain read.
func ReadReplaceTarget(path string) ([]byte, error) {
	return os.ReadFile(path)
}
