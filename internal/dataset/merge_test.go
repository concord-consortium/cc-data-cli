package dataset

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func newDataset(t *testing.T) *Dataset {
	t.Helper()
	root := t.TempDir()
	d, err := Create(root, Ref{Portal: "learn.concord.org", Name: "ds"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rec(sk, re, qid, hid, data string) []byte {
	m := map[string]any{"source_key": sk, "remote_endpoint": re, "question_id": qid, "data": data}
	if hid != "" {
		m["history_id"] = hid
	}
	b, _ := json.Marshal(m)
	return b
}

// writeFinishedSegment writes records to a segment and a finished cursor.
func writeFinishedSegment(t *testing.T, d *Dataset, typ string, run int, recs [][]byte) *store.Segment {
	t.Helper()
	seg := store.OpenSegment(d.Dir, typ, run)
	if err := seg.AppendPage(recs, time.Unix(int64(run), 0).UTC(), run); err != nil {
		t.Fatal(err)
	}
	if err := seg.WriteCursor(&store.Cursor{NextPageToken: nil, Pages: 1, Items: len(recs)}); err != nil {
		t.Fatal(err)
	}
	return seg
}

func storeIdentities(t *testing.T, d *Dataset, typ string) []store.Identity {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	st, ok := m.Stores[typ]
	if !ok {
		return nil
	}
	f, err := os.Open(d.Path(st.File))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ids []store.Identity
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, err := store.IdentityFromRecord(typ, []byte(line))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func readData(t *testing.T, storeFile, typ string, want store.Identity) string {
	t.Helper()
	f, err := os.Open(storeFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, _ := store.IdentityFromRecord(typ, []byte(line))
		if id.Key(typ) != want.Key(typ) {
			continue
		}
		var obj map[string]any
		json.Unmarshal([]byte(line), &obj)
		if s, ok := obj["data"].(string); ok {
			return s
		}
		return ""
	}
	return ""
}

func assertDistinct(t *testing.T, d *Dataset, typ string) {
	t.Helper()
	ids := storeIdentities(t, d, typ)
	seen := map[string]bool{}
	for _, id := range ids {
		k := id.Key(typ)
		if seen[k] {
			t.Fatalf("duplicate identity in store: %+v", id)
		}
		seen[k] = true
	}
}

func TestFirstMergeBootstrap(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "a"),
		rec("s", "e2", "q2", "", "b"),
	})
	counts, err := d.MergeCompact("answers", 584, seg)
	if err != nil {
		t.Fatal(err)
	}
	if counts.New != 2 || counts.Updated != 0 || counts.Removed != 0 || counts.Fetched != 2 {
		t.Fatalf("first merge counts = %+v", counts)
	}
	m, _ := d.ReadManifest()
	st := m.Stores["answers"]
	if st.Version != 1 || st.Count != 2 || st.File != "answers.v1.jsonl" {
		t.Fatalf("store after first merge = %+v", st)
	}
	if st.Columns["data"] != "VARCHAR" {
		t.Fatalf("column map missing data: %+v", st.Columns)
	}
	// Segment removed after merge.
	if seg.Exists() {
		t.Fatal("segment should be removed after merge")
	}
}

func TestMergeOverlapUpdatesAndNew(t *testing.T) {
	d := newDataset(t)
	seg1 := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "old"),
		rec("s", "e2", "q2", "", "old"),
	})
	if _, err := d.MergeCompact("answers", 584, seg1); err != nil {
		t.Fatal(err)
	}
	// Run B overlaps e2/q2 (update) and adds e3/q3 (new).
	seg2 := writeFinishedSegment(t, d, "answers", 612, [][]byte{
		rec("s", "e2", "q2", "", "new"),
		rec("s", "e3", "q3", "", "new"),
	})
	counts, err := d.MergeCompact("answers", 612, seg2)
	if err != nil {
		t.Fatal(err)
	}
	if counts.New != 1 || counts.Updated != 1 {
		t.Fatalf("overlap counts = %+v", counts)
	}
	assertDistinct(t, d, "answers")
	if got := len(storeIdentities(t, d, "answers")); got != 3 {
		t.Fatalf("store should hold 3 identities, got %d", got)
	}
}

func TestNeverDuplicateAcceptance(t *testing.T) {
	d := newDataset(t)
	// Fetch A: a1, a2.
	segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	d.MergeCompact("answers", 584, segA)
	// Fetch overlapping B: a2, a3.
	segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{
		rec("s", "e2", "q2", "", "B"),
		rec("s", "e3", "q3", "", "B"),
	})
	d.MergeCompact("answers", 612, segB)
	// Re-fetch A with --refresh: a1 only (a2 dropped from A's membership).
	segA2 := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A2"),
	})
	counts, err := d.MergeCompact("answers", 584, segA2)
	if err != nil {
		t.Fatal(err)
	}
	// a2 is still covered by B, so it is not removed from the store.
	if counts.Removed != 0 {
		t.Fatalf("a2 still covered by B; removed should be 0, got %+v", counts)
	}
	assertDistinct(t, d, "answers")
	ids := storeIdentities(t, d, "answers")
	if len(ids) != 3 {
		t.Fatalf("store should hold a1,a2,a3 = 3 identities, got %d: %+v", len(ids), ids)
	}
}

func TestShrinkRefreshRemoves(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	d.MergeCompact("answers", 584, seg)
	// Refresh A with only e1 (e2 no longer permitted, covered by nothing).
	seg2 := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A2"),
	})
	counts, err := d.MergeCompact("answers", 584, seg2)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Removed != 1 {
		t.Fatalf("shrunk refresh should report 1 removed, got %+v", counts)
	}
	if len(storeIdentities(t, d, "answers")) != 1 {
		t.Fatal("store should hold only e1/q1 after shrink refresh")
	}
}

func TestMergeSortedOutput(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "z", "q", "", "1"),
		rec("s", "a", "q", "", "2"),
		rec("s", "m", "q", "", "3"),
	})
	d.MergeCompact("answers", 584, seg)
	ids := storeIdentities(t, d, "answers")
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = id.Key("answers")
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("store not sorted by identity key: %v", keys)
	}
}
