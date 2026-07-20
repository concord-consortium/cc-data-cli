package dataset

import (
	"errors"
	"io"
	"os"
	"testing"
)

// The tests in this file inject filesystem faults (an unreadable store, a missing
// membership file, an uncommitted store generation) and assert that the never-lose
// storage engine fails closed instead of silently dropping or corrupting records.
// A read error is injected by placing a directory where a store file is expected:
// os.Open succeeds, but the first Read returns a non-EOF error (EISDIR).

// #15: scanStore drives the attachment GC, which deletes any on-disk attachment
// not in the returned reference set. A read error swallowed as end-of-scan would
// yield a truncated set and delete valid files, so scanStore must return the
// error. A missing store must likewise fail closed rather than report zero refs.
func TestScanStoreFailsClosedOnUnreadableOrMissingStore(t *testing.T) {
	d := newDataset(t)

	// Unreadable store: a directory where the store file should be.
	const file = "answers.store.jsonl"
	if err := os.Mkdir(d.Path(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.scanStore("answers", file, nil); err == nil {
		t.Fatal("scanStore must return a read error, not a truncated (GC-driving) scan")
	} else if errors.Is(err, io.EOF) {
		t.Fatalf("a real read error must not be reported as EOF: %v", err)
	}

	// Missing store: a store named by the manifest must be on disk, so a missing
	// one is corruption and must fail closed rather than delete every attachment.
	if _, err := d.scanStore("answers", "does-not-exist.jsonl", nil); err == nil {
		t.Fatal("scanStore must fail closed on a missing store")
	}
}

// #25: scanStoreCountAndColumns feeds the reindexed manifest's row count and
// column map. Breaking on a non-EOF read error as if it were success would commit
// a truncated count and partial column map, corrupting manifest metadata.
func TestScanStoreCountAndColumnsPropagatesReadError(t *testing.T) {
	d := newDataset(t)
	p := d.Path("answers.v1.jsonl")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := scanStoreCountAndColumns(p)
	if err == nil {
		t.Fatal("scanStoreCountAndColumns must return a read error, not a truncated count as success")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("a real read error must not be reported as EOF: %v", err)
	}
}

// #21: a missing membership file for a run still referenced by the manifest must
// abort the merge (fail closed). Skipping it would drop that run's identities from
// `covered`, and the merge would then delete records the run still owns.
func TestMergeFailsClosedOnMissingMembership(t *testing.T) {
	d := newDataset(t)

	// Merge run 584; this writes its membership file and manifest entry.
	segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	if _, err := d.MergeCompact("answers", 584, segA); err != nil {
		t.Fatal(err)
	}

	// Simulate loss/corruption of run 584's membership file while the manifest
	// still references it.
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	ref := m.Membership[MembershipKey("answers", 584)]
	if ref.File == "" {
		t.Fatal("expected a membership file for run 584 after its merge")
	}
	if err := os.Remove(d.Path(ref.File)); err != nil {
		t.Fatal(err)
	}

	// Merging a different run reads run 584's (now missing) membership to build
	// `covered`; that must fail the merge, not silently proceed.
	segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{rec("s", "e2", "q2", "", "B")})
	if _, err := d.MergeCompact("answers", 612, segB); err == nil {
		t.Fatal("merge must fail closed when a referenced membership file is missing")
	}
}

// #24: reindex must not adopt an uncommitted store generation. A crash between the
// store rename and the membership write can leave answers.vN.jsonl on disk while
// membership is still vN-1; adopting vN would drop vN's identities (absent from
// any membership) on the next merge. Reindex must adopt the newest generation
// proven committed by a matching membership version.
func TestReindexSkipsUncommittedStoreGeneration(t *testing.T) {
	d := newDataset(t)

	// Committed generation: store v1 with membership v1.
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	if _, err := d.MergeCompact("answers", 584, seg); err != nil {
		t.Fatal(err)
	}

	// Uncommitted store v2 (as a crashed merge would leave it): the store file is
	// present, but there is no membership at v2.
	uncommitted := []byte(`{"source_key":"s","remote_endpoint":"e9","question_id":"q9","data":"Z"}` + "\n")
	if err := os.WriteFile(d.Path("answers.v2.jsonl"), uncommitted, 0o600); err != nil {
		t.Fatal(err)
	}

	os.Remove(d.Path("manifest.json"))
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Stores["answers"].Version; got != 1 {
		t.Fatalf("reindex must skip the uncommitted store generation and adopt v1, got v%d", got)
	}
	if got := m.Stores["answers"].Count; got != 2 {
		t.Fatalf("reindex should reflect the committed v1 count (2), got %d", got)
	}
}

// newestCommittedStoreVersion is the decision the #24 fix hinges on: pick the
// highest store version that has a membership file at the same version.
func TestNewestCommittedStoreVersion(t *testing.T) {
	versFiles := map[int]string{1: "answers.v1.jsonl", 2: "answers.v2.jsonl"}
	if got := newestCommittedStoreVersion(versFiles, map[int]bool{1: true}); got != 1 {
		t.Fatalf("v2 uncommitted -> want 1, got %d", got)
	}
	if got := newestCommittedStoreVersion(versFiles, map[int]bool{1: true, 2: true}); got != 2 {
		t.Fatalf("both committed -> want 2, got %d", got)
	}
	if got := newestCommittedStoreVersion(versFiles, map[int]bool{}); got != 0 {
		t.Fatalf("none committed -> want 0, got %d", got)
	}
}
