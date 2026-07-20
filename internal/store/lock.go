// Package store implements the never-duplicate storage engine: identity keys,
// the segment lifecycle, the merge-compact, and the process-wide lock guards.
package store

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
)

// Lock file names within a dataset directory. None is ever renamed or unlinked,
// because unlinking a held flock file detaches it from its inode and a rename
// detaches held flocks onto a dead inode.
const (
	DatasetLockFile  = ".dataset.lock"
	ActivityLockFile = ".activity.lock"
)

// Each lock is a process-wide guard per path: a sync primitive for goroutine
// exclusion layered over a single flock for cross-process exclusion. The
// registries below hand out one guard instance per path so the MCP server's
// long-lived process shares them correctly.
var (
	registryMu sync.Mutex
	dsGuards   = map[string]*DatasetLock{}
	actGuards  = map[string]*ActivityLock{}
	dlGuards   = map[string]*DownloadLock{}
)

// DatasetLockFor returns the process-wide per-dataset guard for a dataset dir.
func DatasetLockFor(datasetDir string) *DatasetLock {
	key := filepath.Clean(datasetDir)
	registryMu.Lock()
	defer registryMu.Unlock()
	if g, ok := dsGuards[key]; ok {
		return g
	}
	g := &DatasetLock{flock: flock.New(filepath.Join(key, DatasetLockFile))}
	dsGuards[key] = g
	return g
}

// ActivityLockFor returns the process-wide whole-fetch activity guard for a
// dataset dir.
func ActivityLockFor(datasetDir string) *ActivityLock {
	key := filepath.Clean(datasetDir)
	registryMu.Lock()
	defer registryMu.Unlock()
	if g, ok := actGuards[key]; ok {
		return g
	}
	g := &ActivityLock{flock: flock.New(filepath.Join(key, ActivityLockFile))}
	actGuards[key] = g
	return g
}

// DownloadLockFor returns the process-wide per-download guard for a lock file
// (seg_<type>_<run>.lock and friends) under a dataset's segments directory.
// Unlike the dataset-keyed guards, download guards are keyed per-run, so the
// registry entry is ref-counted and evicted on the last Unlock to keep a
// long-lived MCP server's map from growing unboundedly across runs.
func DownloadLockFor(datasetDir, lockName string) *DownloadLock {
	path := filepath.Join(filepath.Clean(datasetDir), SegmentsDir, lockName)
	registryMu.Lock()
	defer registryMu.Unlock()
	if g, ok := dlGuards[path]; ok {
		return g
	}
	g := &DownloadLock{path: path, flock: flock.New(path)}
	dlGuards[path] = g
	return g
}

// DownloadLock is the exclusive per-download guard held for a fetch command's
// lifetime; acquisition is always non-blocking (a second same-run command fails
// fast rather than racing the segment).
type DownloadLock struct {
	path  string
	mu    sync.Mutex
	flock *flock.Flock
	held  bool
	refs  int // registry references, guarded by registryMu
}

// TryLock acquires the guard without blocking, creating the segments directory
// so the lock file can be made.
func (l *DownloadLock) TryLock() (bool, error) {
	if !l.mu.TryLock() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		l.mu.Unlock()
		return false, err
	}
	ok, err := l.flock.TryLock()
	if err != nil || !ok {
		l.mu.Unlock()
		return false, err
	}
	l.held = true
	registryMu.Lock()
	l.refs++
	registryMu.Unlock()
	return true, nil
}

// Unlock releases the guard and evicts its registry entry once no holder
// remains, so per-run lock keys do not accumulate over the process lifetime.
func (l *DownloadLock) Unlock() {
	if !l.held {
		return
	}
	l.held = false
	l.flock.Unlock()
	registryMu.Lock()
	l.refs--
	// Only evict when this exact guard is still the registered one; a concurrent
	// DownloadLockFor may have already replaced it after an earlier eviction.
	if l.refs == 0 && dlGuards[l.path] == l {
		delete(dlGuards, l.path)
	}
	registryMu.Unlock()
	l.mu.Unlock()
}

// Held reports whether this process holds the guard.
func (l *DownloadLock) Held() bool { return l.held }

// DatasetLock is the exclusive per-dataset guard (mutex + flock). The mutex
// serializes goroutines so the single flock is only ever entered by one at a
// time, and inner helpers assert the guard rather than re-acquiring.
type DatasetLock struct {
	mu    sync.Mutex
	flock *flock.Flock
	held  bool
}

// Lock blocks until the guard is held.
func (l *DatasetLock) Lock() error {
	l.mu.Lock()
	if err := l.flock.Lock(); err != nil {
		l.mu.Unlock()
		return err
	}
	l.held = true
	return nil
}

// TryLock acquires the guard without blocking; the bool reports success.
func (l *DatasetLock) TryLock() (bool, error) {
	if !l.mu.TryLock() {
		return false, nil
	}
	ok, err := l.flock.TryLock()
	if err != nil || !ok {
		l.mu.Unlock()
		return false, err
	}
	l.held = true
	return true, nil
}

// Unlock releases the guard.
func (l *DatasetLock) Unlock() {
	l.held = false
	l.flock.Unlock()
	l.mu.Unlock()
}

// Held reports whether this process holds the guard (for inner-helper assertions).
func (l *DatasetLock) Held() bool { return l.held }

// ActivityLock is the whole-fetch guard: a reader-counted RW guard over one
// flock. Fetches take it shared for their lifetime; mutating dataset commands
// take it exclusively, non-blocking. Two files rather than one because a merging
// fetch already holds the shared lock and a shared-to-exclusive upgrade on a
// single file would self-deadlock.
type ActivityLock struct {
	mu        sync.Mutex
	flock     *flock.Flock
	readers   int
	writeHeld bool
}

// TryRLock acquires the shared lock without blocking; the first in-process
// reader takes the flock, later readers just increment the count.
func (l *ActivityLock) TryRLock() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writeHeld {
		return false, nil
	}
	if l.readers == 0 {
		ok, err := l.flock.TryRLock()
		if err != nil || !ok {
			return false, err
		}
	}
	l.readers++
	return true, nil
}

// RUnlock releases one shared holder; the last reader releases the flock.
func (l *ActivityLock) RUnlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readers == 0 {
		return
	}
	l.readers--
	if l.readers == 0 {
		l.flock.Unlock()
	}
}

// TryLock acquires the exclusive lock without blocking.
func (l *ActivityLock) TryLock() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readers > 0 || l.writeHeld {
		return false, nil
	}
	ok, err := l.flock.TryLock()
	if err != nil || !ok {
		return false, err
	}
	l.writeHeld = true
	return true, nil
}

// Unlock releases the exclusive lock.
func (l *ActivityLock) Unlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.writeHeld {
		return
	}
	l.writeHeld = false
	l.flock.Unlock()
}
