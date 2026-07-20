//go:build windows

package fsutil

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// Go opens files with FILE_SHARE_READ|FILE_SHARE_WRITE but not FILE_SHARE_DELETE,
// so MoveFileEx(REPLACE_EXISTING) onto a name any process holds open fails with a
// sharing violation, and a reader opening a file mid-replace can see a transient
// error. These wrappers retry those transient conditions with a short bounded
// backoff (reader windows are milliseconds), modeled on Go's cmd/internal/robustio.

const errSharingViolation syscall.Errno = 32 // ERROR_SHARING_VIOLATION

const (
	maxRetries   = 20
	retryBackoff = 5 * time.Millisecond
)

func isSharing(err error) bool {
	return errors.Is(err, errSharingViolation) || errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}

// renameTransient additionally treats a transient not-found as retryable, since a
// rename-replace can briefly unlink the destination path.
func renameTransient(err error) bool {
	return isSharing(err) || errors.Is(err, syscall.ERROR_FILE_NOT_FOUND)
}

func retry(op func() error, transient func(error) bool) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = op()
		if err == nil || !transient(err) {
			return err
		}
		time.Sleep(retryBackoff)
	}
	return err
}

// RenameAtomic renames oldpath to newpath, retrying the transient Windows
// sharing/access/not-found conditions a concurrent reader can provoke.
func RenameAtomic(oldpath, newpath string) error {
	return retry(func() error { return os.Rename(oldpath, newpath) }, renameTransient)
}

// OpenReplaceTarget opens a fixed-name replace-target file, retrying only the
// sharing/access violations a concurrent rename-replace can provoke. A genuine
// not-found returns immediately, so an absent file is not penalized.
func OpenReplaceTarget(path string) (*os.File, error) {
	var f *os.File
	err := retry(func() error {
		var e error
		f, e = os.Open(path)
		return e
	}, isSharing)
	return f, err
}

// ReadReplaceTarget reads a fixed-name replace-target file, retrying only the
// sharing/access violations a concurrent rename-replace can provoke.
func ReadReplaceTarget(path string) ([]byte, error) {
	var b []byte
	err := retry(func() error {
		var e error
		b, e = os.ReadFile(path)
		return e
	}, isSharing)
	return b, err
}
