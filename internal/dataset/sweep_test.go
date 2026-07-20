package dataset

import (
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// TestBacklogSweepCollapsesLockFreeSegments leaves two runs' segments
// finished-but-unmerged with their per-download locks free, then one merge
// collapses both into a single new store version, rewriting the store once.
func TestBacklogSweepCollapsesLockFreeSegments(t *testing.T) {
	d := newDataset(t)
	// Two finished-but-unmerged segments (locks free: no live command holds them).
	writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	writeFinishedSegment(t, d, "answers", 612, [][]byte{
		rec("s", "e3", "q3", "", "B"),
	})

	// Merging run 584 sweeps run 612 into the same compact.
	seg := store.OpenSegment(d.Dir, "answers", 584)
	counts, err := d.MergeCompact("answers", 584, seg)
	if err != nil {
		t.Fatal(err)
	}
	if counts.New != 2 {
		t.Fatalf("primary run counts = %+v", counts)
	}

	m, _ := d.ReadManifest()
	st := m.Stores["answers"]
	// Store rewritten once: version 1 holds all three identities.
	if st.Version != 1 {
		t.Fatalf("store should be rewritten once (v1), got v%d", st.Version)
	}
	if st.Count != 3 {
		t.Fatalf("swept store should hold 3 identities, got %d", st.Count)
	}
	// Both memberships repointed to v1.
	if m.Membership[MembershipKey("answers", 584)].Version != 1 || m.Membership[MembershipKey("answers", 612)].Version != 1 {
		t.Fatalf("both memberships should be at v1: %+v", m.Membership)
	}
	// Both segments removed.
	if store.OpenSegment(d.Dir, "answers", 584).Exists() || store.OpenSegment(d.Dir, "answers", 612).Exists() {
		t.Fatal("swept segments should be removed")
	}
	assertDistinct(t, d, "answers")
}

// TestSweepSkipsLiveLockHeldSegment asserts a finished-but-unmerged segment whose
// per-download lock is held by a live command is NOT swept by another run's merge.
func TestSweepSkipsLiveLockHeldSegment(t *testing.T) {
	d := newDataset(t)
	writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	writeFinishedSegment(t, d, "answers", 612, [][]byte{rec("s", "e3", "q3", "", "B")})

	// A live command holds run 612's per-download lock.
	held := store.DownloadLockFor(d.Dir, store.OpenSegment(d.Dir, "answers", 612).LockName())
	ok, err := held.TryLock()
	if err != nil || !ok {
		t.Fatalf("could not hold run 612 lock: %v", err)
	}
	defer held.Unlock()

	seg := store.OpenSegment(d.Dir, "answers", 584)
	if _, err := d.MergeCompact("answers", 584, seg); err != nil {
		t.Fatal(err)
	}

	// Run 612's segment must NOT have been swept (still present, unmerged).
	if !store.OpenSegment(d.Dir, "answers", 612).Exists() {
		t.Fatal("live-lock-held segment must not be swept")
	}
	m, _ := d.ReadManifest()
	if _, ok := m.Membership[MembershipKey("answers", 612)]; ok {
		t.Fatal("run 612 should not have a membership yet; it was not swept")
	}
	if m.Stores["answers"].Count != 1 {
		t.Fatalf("store should hold only run 584's record, got %d", m.Stores["answers"].Count)
	}
}
