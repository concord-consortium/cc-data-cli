package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// CurrentStore returns the manifest's current store for a type (a zero Store
// when none exists yet).
func (d *Dataset) CurrentStore(typ string) (Store, error) {
	m, err := d.ReadManifest()
	if err != nil {
		return Store{}, err
	}
	return m.Stores[typ], nil
}

// membershipUnionMulti loads every membership set for the type except the runs
// being merged (whose identity sets replace them wholesale), unions them with
// all the new identities, and returns the covered key set.
func (d *Dataset) membershipUnionMulti(m *Manifest, typ string, idsByRun map[int][]store.Identity) (map[string]bool, error) {
	covered := map[string]bool{}
	for key, ref := range m.Membership {
		t, r, ok := parseMembershipKey(key)
		if !ok || t != typ {
			continue
		}
		if _, merging := idsByRun[r]; merging {
			continue
		}
		// Fail closed: a missing membership file must not silently drop this run's
		// identities from covered, or the next merge would delete still-owned
		// records (the sibling ScanRunAttachments fails closed for the same reason).
		ids, err := store.ReadMembershipFile(d.Path(ref.File))
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			covered[id.Key(typ)] = true
		}
	}
	for _, ids := range idsByRun {
		for _, id := range ids {
			covered[id.Key(typ)] = true
		}
	}
	return covered, nil
}

// WriteMembership writes a run's membership version file.
func (d *Dataset) WriteMembership(typ string, runID, version int, ids []store.Identity) error {
	file := fmt.Sprintf("members_%s_%d.v%d.jsonl", typ, runID, version)
	return store.WriteMembershipFile(d.Path(file), ids)
}

// MergeCompact merges the segment into a new store version under the per-dataset
// lock, following the load-bearing durable-write order: store rename, membership
// write, manifest repoint, cursor merged_as, segment removal last.
func (d *Dataset) MergeCompact(typ string, runID int, seg *store.Segment) (store.MergeCounts, error) {
	lock := d.Lock()
	if err := lock.Lock(); err != nil {
		return store.MergeCounts{}, err
	}
	defer lock.Unlock()
	return d.mergeUnderLock(typ, runID, seg)
}

func (d *Dataset) mergeUnderLock(typ string, runID int, seg *store.Segment) (store.MergeCounts, error) {
	// A resume whose segment is already gone has nothing to merge.
	if !seg.Exists() {
		return store.MergeCounts{}, nil
	}
	// Already-merged short-circuit: a resume that finds merged_as set at or below
	// the current store version means the merge already landed (versions only
	// grow, and merged_as is written only after this merge's own repoint).
	if c, ok, err := seg.ReadCursor(); err == nil && ok && c.MergedAs != 0 {
		if cur, cerr := d.CurrentStore(typ); cerr == nil && c.MergedAs <= cur.Version {
			seg.Remove()
			return store.MergeCounts{}, nil
		}
	}
	for {
		m, err := d.ReadManifest()
		if err != nil {
			return store.MergeCounts{}, err
		}
		cur := m.Stores[typ]
		next := cur.Version + 1
		tmp := d.Path(fmt.Sprintf("%s.v%d.jsonl.tmp", typ, next))

		primaryIdx, err := seg.BuildIndex()
		if err != nil {
			return store.MergeCounts{}, err
		}

		// Sweep other finished-but-unmerged segments of this type whose
		// per-download lock is free, folding them into one store rewrite.
		swept := d.discoverSweep(typ, runID, cur.Version)
		byRun := map[int]*store.SegmentIndex{runID: primaryIdx}
		idsByRun := map[int][]store.Identity{runID: primaryIdx.Identities()}
		for _, s := range swept {
			byRun[s.run] = s.idx
			idsByRun[s.run] = s.idx.Identities()
		}

		covered, err := d.membershipUnionMulti(m, typ, idsByRun)
		if err != nil {
			closeAll(byRun)
			releaseSwept(swept)
			return store.MergeCounts{}, err
		}

		var src store.MergeSource
		if len(swept) == 0 {
			src = store.NewSingleSource(primaryIdx, runID)
		} else {
			ms, merr := store.NewMultiSource(byRun)
			if merr != nil {
				closeAll(byRun)
				releaseSwept(swept)
				return store.MergeCounts{}, merr
			}
			src = ms
		}

		oldPath := ""
		if cur.File != "" {
			oldPath = d.Path(cur.File)
		}
		result, err := store.StreamMerge(oldPath, typ, tmp, src, covered)
		closeAll(byRun)
		if err != nil {
			os.Remove(tmp)
			releaseSwept(swept)
			return store.MergeCounts{}, err
		}

		// Rebase if a concurrent merge moved the pointer (belt and braces: the
		// whole merge runs under the lock, so the pointer cannot actually move).
		cur2, err := d.CurrentStore(typ)
		if err != nil {
			os.Remove(tmp)
			releaseSwept(swept)
			return store.MergeCounts{}, err
		}
		if cur2.Version != cur.Version {
			os.Remove(tmp)
			releaseSwept(swept)
			continue
		}

		// Durable-write order is load-bearing: store rename, membership writes,
		// manifest repoint, cursor merged_as, segment removal last.
		final := d.Path(fmt.Sprintf("%s.v%d.jsonl", typ, next))
		if err := os.Rename(tmp, final); err != nil {
			releaseSwept(swept)
			return store.MergeCounts{}, err
		}
		testHookAfterWrite(1)
		for run, ids := range idsByRun {
			if err := d.WriteMembership(typ, run, next, ids); err != nil {
				releaseSwept(swept)
				return store.MergeCounts{}, err // abort before the repoint: segments survive, resume re-merges
			}
		}
		testHookAfterWrite(2)
		m.Stores[typ] = Store{File: filepath.Base(final), Version: next, Count: result.Total, Columns: result.Columns}
		for run := range idsByRun {
			m.SetMembershipRef(typ, run, next)
		}
		d.recordSweptDownloads(m, typ, swept, result)
		if err := d.WriteManifest(m); err != nil {
			releaseSwept(swept)
			return store.MergeCounts{}, err
		}
		testHookAfterWrite(3)
		for run := range idsByRun {
			d.cleanupOldVersions(typ, run, next)
		}
		for run := range byRun {
			store.OpenSegment(d.Dir, typ, run).MarkMerged(next)
		}
		testHookAfterWrite(4)
		for run := range byRun {
			store.OpenSegment(d.Dir, typ, run).Remove()
		}
		testHookAfterWrite(5)
		releaseSwept(swept)

		primary := result.PerRun[runID]
		primary.Removed = result.Removed
		return primary, nil
	}
}

// sweptSegment is a lock-free finished-but-unmerged sibling folded into a sweep.
type sweptSegment struct {
	run  int
	idx  *store.SegmentIndex
	lock *store.DownloadLock
}

// discoverSweep finds other finished-but-unmerged segments of the type whose
// per-download lock this process can acquire non-blocking.
func (d *Dataset) discoverSweep(typ string, primaryRun, curVersion int) []sweptSegment {
	entries, err := os.ReadDir(filepath.Join(d.Dir, store.SegmentsDir))
	if err != nil {
		return nil
	}
	prefix := fmt.Sprintf("seg_%s_", typ)
	var swept []sweptSegment
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		runStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		run, err := strconv.Atoi(runStr)
		if err != nil || run == primaryRun {
			continue
		}
		seg := store.OpenSegment(d.Dir, typ, run)
		c, ok, err := seg.ReadCursor()
		if err != nil || !ok || !c.Finished() {
			continue
		}
		if c.MergedAs != 0 && c.MergedAs <= curVersion {
			continue
		}
		lock := store.DownloadLockFor(d.Dir, seg.LockName())
		locked, lerr := lock.TryLock()
		if lerr != nil || !locked {
			continue // a live command holds it; never sweep a live segment
		}
		idx, ierr := seg.BuildIndex()
		if ierr != nil {
			lock.Unlock()
			continue
		}
		swept = append(swept, sweptSegment{run: run, idx: idx, lock: lock})
	}
	return swept
}

// recordSweptDownloads flips each swept run's Download entry to complete with its
// per-run counts (best-effort; a swept run's original fetch has exited).
func (d *Dataset) recordSweptDownloads(m *Manifest, typ string, swept []sweptSegment, result store.MergeResult) {
	for _, s := range swept {
		counts := result.PerRun[s.run]
		for i := range m.Downloads {
			dl := &m.Downloads[i]
			if dl.Type == typ && dl.RunID == s.run && !dl.Complete {
				dl.Complete = true
				c := counts
				dl.MergeCounts = &c
				break
			}
		}
	}
}

func closeAll(byRun map[int]*store.SegmentIndex) {
	for _, idx := range byRun {
		idx.Close()
	}
}

func releaseSwept(swept []sweptSegment) {
	for _, s := range swept {
		s.lock.Unlock()
	}
}

// testHookAfterWrite is a no-op in production; crash-injection tests override it
// to abort after each of the five durable writes.
var testHookAfterWrite = func(n int) {}

var storeVersionRe = regexp.MustCompile(`^(answers|history)\.v(\d+)\.jsonl$`)

// cleanupOldVersions removes superseded store versions for the type and old
// membership versions for the run; best-effort and idempotent.
func (d *Dataset) cleanupOldVersions(typ string, runID, keepStoreVersion int) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		return
	}
	memPrefix := fmt.Sprintf("members_%s_%d.v", typ, runID)
	for _, e := range entries {
		name := e.Name()
		if mm := storeVersionRe.FindStringSubmatch(name); mm != nil && mm[1] == typ {
			if v, _ := strconv.Atoi(mm[2]); v != keepStoreVersion {
				os.Remove(filepath.Join(d.Dir, name))
			}
			continue
		}
		if strings.HasPrefix(name, memPrefix) && strings.HasSuffix(name, ".jsonl") {
			if v := membershipVersion(name); v != 0 && v != keepStoreVersion {
				os.Remove(filepath.Join(d.Dir, name))
			}
		}
	}
}

func membershipVersion(name string) int {
	i := strings.LastIndex(name, ".v")
	if i < 0 {
		return 0
	}
	rest := strings.TrimSuffix(name[i+2:], ".jsonl")
	v, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return v
}

func parseMembershipKey(key string) (typ string, run int, ok bool) {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return "", 0, false
	}
	r, err := strconv.Atoi(key[i+1:])
	if err != nil {
		return "", 0, false
	}
	return key[:i], r, true
}
