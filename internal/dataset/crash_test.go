package dataset

import (
	"fmt"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// TestCrashInjectionConverges aborts after each of the five durable writes,
// resumes, and asserts content converges with no lost records.
func TestCrashInjectionConverges(t *testing.T) {
	for n := 1; n <= 5; n++ {
		t.Run(fmt.Sprintf("boundary%d", n), func(t *testing.T) {
			d := newDataset(t)
			// Clean first merge: run A with e1, e2.
			segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{
				rec("s", "e1", "q1", "", "A"),
				rec("s", "e2", "q2", "", "A"),
			})
			if _, err := d.MergeCompact("answers", 584, segA); err != nil {
				t.Fatal(err)
			}

			// Run B updates e2 and adds e3; crash after boundary n.
			segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{
				rec("s", "e2", "q2", "", "B"),
				rec("s", "e3", "q3", "", "B"),
			})
			crashAt(t, n)
			func() {
				defer func() { recover() }()
				d.MergeCompact("answers", 612, segB)
			}()
			testHookAfterWrite = func(int) {}

			// Resume: re-run the merge against the surviving segment.
			segResume := store.OpenSegment(d.Dir, "answers", 612)
			if _, err := d.MergeCompact("answers", 612, segResume); err != nil {
				t.Fatalf("resume after boundary %d: %v", n, err)
			}

			assertDistinct(t, d, "answers")
			ids := storeIdentities(t, d, "answers")
			if len(ids) != 3 {
				t.Fatalf("boundary %d: expected e1,e2,e3 = 3 identities, got %d", n, len(ids))
			}
			// e2 should carry B's data (segment beat store).
			assertData(t, d, "answers", "s", "e2", "q2", "", "B")
		})
	}
}

func crashAt(t *testing.T, n int) {
	t.Helper()
	testHookAfterWrite = func(k int) {
		if k == n {
			panic(fmt.Sprintf("simulated crash after write %d", n))
		}
	}
	t.Cleanup(func() { testHookAfterWrite = func(int) {} })
}

func assertData(t *testing.T, d *Dataset, typ, sk, re, qid, hid, wantData string) {
	t.Helper()
	m, _ := d.ReadManifest()
	st := m.Stores[typ]
	data := readData(t, d.Path(st.File), typ, store.Identity{SourceKey: sk, RemoteEndpoint: re, QuestionID: qid, HistoryID: hid})
	if data != wantData {
		t.Fatalf("record data = %q, want %q", data, wantData)
	}
}

// TestMembershipWriteFailureAborts asserts a failed membership write aborts
// before the manifest repoint and the segment survives so resume converges.
func TestMembershipWriteFailureAborts(t *testing.T) {
	d := newDataset(t)
	segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	d.MergeCompact("answers", 584, segA)

	segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{rec("s", "e2", "q2", "", "B")})
	// Make the membership write fail by pointing the boundary hook to panic
	// exactly like a mid-write failure right after the store rename.
	crashAt(t, 1) // abort right after the store rename, before membership
	func() {
		defer func() { recover() }()
		d.MergeCompact("answers", 612, segB)
	}()
	testHookAfterWrite = func(int) {}

	// Manifest still names the old store; segment survives.
	m, _ := d.ReadManifest()
	if m.Stores["answers"].Version != 1 {
		t.Fatalf("manifest should still name v1, got v%d", m.Stores["answers"].Version)
	}
	segResume := store.OpenSegment(d.Dir, "answers", 612)
	if !segResume.Exists() {
		t.Fatal("segment must survive an aborted merge")
	}
	if _, err := d.MergeCompact("answers", 612, segResume); err != nil {
		t.Fatal(err)
	}
	if len(storeIdentities(t, d, "answers")) != 2 {
		t.Fatal("resume should recover both records")
	}
}
