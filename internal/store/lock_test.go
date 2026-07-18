package store

import (
	"sync"
	"testing"
	"time"
)

func TestActivityLockMatrix(t *testing.T) {
	dir := t.TempDir()
	l := ActivityLockFor(dir)

	// Two concurrent fetches hold the shared lock together.
	ok1, err := l.TryRLock()
	if err != nil || !ok1 {
		t.Fatalf("first reader failed: %v", err)
	}
	ok2, err := l.TryRLock()
	if err != nil || !ok2 {
		t.Fatalf("second reader failed: %v", err)
	}

	// A mutator's exclusive TryLock fails while either reader is held.
	got, _ := l.TryLock()
	if got {
		t.Fatal("exclusive lock should fail while readers hold")
	}

	// Reader count releases the flock only when the last reader exits.
	l.RUnlock()
	if got, _ := l.TryLock(); got {
		t.Fatal("exclusive lock should still fail with one reader remaining")
	}
	l.RUnlock()

	// Now exclusive succeeds and a reader is blocked.
	ok, err := l.TryLock()
	if err != nil || !ok {
		t.Fatalf("exclusive lock should succeed after readers exit: %v", err)
	}
	if r, _ := l.TryRLock(); r {
		t.Fatal("read lock should fail while a mutator holds the exclusive lock")
	}
	l.Unlock()
}

func TestDatasetLockInProcessExclusion(t *testing.T) {
	dir := t.TempDir()
	l := DatasetLockFor(dir)

	if err := l.Lock(); err != nil {
		t.Fatal(err)
	}
	if !l.Held() {
		t.Fatal("Held should be true after Lock")
	}

	acquired := make(chan struct{})
	go func() {
		l2 := DatasetLockFor(dir) // same process-wide guard
		l2.Lock()
		close(acquired)
		l2.Unlock()
	}()

	select {
	case <-acquired:
		t.Fatal("second Lock acquired while first is held; guard is not exclusive")
	case <-time.After(50 * time.Millisecond):
	}
	l.Unlock()
	<-acquired // now it can proceed
}

func TestDownloadLockSameRunExclusion(t *testing.T) {
	dir := t.TempDir()
	l := DownloadLockFor(dir, "seg_answers_584.lock")
	ok, err := l.TryLock()
	if err != nil || !ok {
		t.Fatalf("first download lock should succeed: %v", err)
	}
	// A second command fetching the same download fails fast.
	if got, _ := DownloadLockFor(dir, "seg_answers_584.lock").TryLock(); got {
		t.Fatal("second same-run download lock should fail")
	}
	// A different download (job-qualified) proceeds.
	other, err := DownloadLockFor(dir, "seg_report_job_584_2.lock").TryLock()
	if err != nil || !other {
		t.Fatalf("different download lock should succeed: %v", err)
	}
	l.Unlock()
}

func TestDatasetLockSameInstanceIsSingleton(t *testing.T) {
	dir := t.TempDir()
	first := DatasetLockFor(dir)
	second := DatasetLockFor(dir)
	if first != second {
		t.Fatal("DatasetLockFor should return the same guard per path")
	}
}

func TestDatasetLockTryLock(t *testing.T) {
	dir := t.TempDir()
	l := DatasetLockFor(dir)
	ok, err := l.TryLock()
	if err != nil || !ok {
		t.Fatalf("TryLock should succeed on a free lock: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if got, _ := DatasetLockFor(dir).TryLock(); got {
			t.Error("TryLock should fail while the guard is held")
		}
	}()
	wg.Wait()
	l.Unlock()
}
