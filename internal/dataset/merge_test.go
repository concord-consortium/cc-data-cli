package dataset

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func newDataset(t *testing.T) *Dataset {
	t.Helper()
	root := t.TempDir()
	d, err := Create(root, Ref{Portal: config.MustPortal("learn.concord.org"), Name: "ds"}, "")
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
	appendPage(t, seg, recs, time.Unix(int64(run), 0).UTC(), run)
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
		id, err := store.IdentityFromRecord(store.MustKind(typ), []byte(line))
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
		kind := store.MustKind(typ)
		id, _ := store.IdentityFromRecord(kind, []byte(line))
		if kind.Key(id) != kind.Key(want) {
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
		k := store.MustKind(typ).Key(id)
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
	if _, err := d.MergeCompact("answers", 584, segA); err != nil {
		t.Fatal(err)
	}
	// Fetch overlapping B: a2, a3.
	segB := writeFinishedSegment(t, d, "answers", 612, [][]byte{
		rec("s", "e2", "q2", "", "B"),
		rec("s", "e3", "q3", "", "B"),
	})
	if _, err := d.MergeCompact("answers", 612, segB); err != nil {
		t.Fatal(err)
	}
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
	m, _ := d.ReadManifest()
	if got := readData(t, d.Path(m.Stores["answers"].File), "answers", identityFor("answers", "s", "e2", "q2", "")); got != "B" {
		t.Fatalf("a2 = %q, want B's copy", got)
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
	if _, err := d.MergeCompact("answers", 584, seg); err != nil {
		t.Fatal(err)
	}
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
	if _, err := d.MergeCompact("answers", 584, seg); err != nil {
		t.Fatal(err)
	}
	ids := storeIdentities(t, d, "answers")
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = store.MustKind(store.TypeAnswers).Key(id)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("store not sorted by identity key: %v", keys)
	}
}

func identityFor(typ, sk, re, qid, hid string) store.Identity {
	vals := map[string]string{"source_key": sk, "remote_endpoint": re, "question_id": qid, "history_id": hid}
	k := store.MustKind(typ)
	id := make(store.Identity, len(k.Identity))
	for i, c := range k.Identity {
		id[i] = vals[c]
	}
	return id
}

func TestATornSegmentLineFailsTheMergeAndKeepsTheStore(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "history", 584, [][]byte{rec("s", "e1", "q1", "h1", "kept")})
	if _, err := d.MergeCompact("history", 584, seg); err != nil {
		t.Fatal(err)
	}
	before, _ := d.ReadManifest()
	storeBytes, err := os.ReadFile(d.Path(before.Stores["history"].File))
	if err != nil {
		t.Fatal(err)
	}

	whole := `{"entry_json":"` + strings.Repeat("x", 64) + `","history_id":"h2","question_id":"q1","remote_endpoint":"e1","source_key":"s"}`
	// Identity keys precede the cut, so only the whole-line JSON check can reject this line.
	torn := `{"history_id":"h2","question_id":"q1","remote_endpoint":"e1","source_key":"s","entry_json":"xx` + whole + "\n"
	seg2 := store.OpenSegment(d.Dir, "history", 612)
	if err := os.MkdirAll(filepath.Dir(seg2.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seg2.Path(), []byte(torn), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seg2.WriteCursor(&store.Cursor{Pages: 1, Items: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MergeCompact("history", 612, seg2); err == nil {
		t.Fatal("a torn segment line must fail the merge")
	}
	after, _ := d.ReadManifest()
	if after.Stores["history"].Version != before.Stores["history"].Version || after.Stores["history"].File != before.Stores["history"].File {
		t.Fatalf("store moved: %+v -> %+v", before.Stores["history"], after.Stores["history"])
	}
	if got, _ := os.ReadFile(d.Path(after.Stores["history"].File)); !bytes.Equal(got, storeBytes) {
		t.Fatal("the store's bytes changed")
	}
	if !seg2.Exists() {
		t.Fatal("the segment must survive a failed merge")
	}
}

// overwriteMembership replaces a run's current membership file with the given lines.
func overwriteMembership(t *testing.T, d *Dataset, typ string, run int, lines ...string) {
	t.Helper()
	m, _ := d.ReadManifest()
	path := d.Path(m.Membership[MembershipKey(typ, run)].File)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func memberLine(qid string) string {
	return `{"source_key":"s","remote_endpoint":"e","question_id":"` + qid + `"}`
}

// assertMergeFailsClosed checks a merge fails, with want when it is non-nil, and moves nothing.
func assertMergeFailsClosed(t *testing.T, d *Dataset, want error, setup func()) {
	t.Helper()
	setup()
	before, _ := d.ReadManifest()
	storeBytes, _ := os.ReadFile(d.Path(before.Stores["answers"].File))
	seg := writeFinishedSegment(t, d, "answers", 612, [][]byte{rec("s", "e", "qm", "", "new")})
	if _, err := d.MergeCompact("answers", 612, seg); err == nil || want != nil && !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	after, _ := d.ReadManifest()
	if after.Stores["answers"].Version != before.Stores["answers"].Version || len(after.Membership) != len(before.Membership) {
		t.Fatalf("manifest moved: %+v -> %+v", before.Stores["answers"], after.Stores["answers"])
	}
	if got, _ := os.ReadFile(d.Path(after.Stores["answers"].File)); !bytes.Equal(got, storeBytes) {
		t.Fatal("the store's bytes changed")
	}
	if !seg.Exists() {
		t.Fatal("the segment must survive a failed merge")
	}
}

func TestMembershipOutOfOrderFailsTheMergeClosed(t *testing.T) {
	d := newDataset(t)
	assertMergeFailsClosed(t, d, store.ErrMembershipOrder, func() {
		segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e", "qa", "", "a"), rec("s", "e", "qb", "", "b"), rec("s", "e", "qc", "", "c"), rec("s", "e", "qd", "", "d")})
		if _, err := d.MergeCompact("answers", 584, segA); err != nil {
			t.Fatal(err)
		}
		// The disorder sits before the last old key, so the walk itself reads it.
		overwriteMembership(t, d, "answers", 584, memberLine("qa"), memberLine("qc"), memberLine("qb"), memberLine("qd"))
	})
}

func TestMembershipDisorderPastTheLastOldKeyFailsTheMergeClosed(t *testing.T) {
	d := newDataset(t)
	assertMergeFailsClosed(t, d, store.ErrMembershipOrder, func() {
		segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e", "qa", "", "a")})
		if _, err := d.MergeCompact("answers", 584, segA); err != nil {
			t.Fatal(err)
		}
		overwriteMembership(t, d, "answers", 584, memberLine("qa"), memberLine("qz"), memberLine("qy"))
	})
}

func TestTruncatedMembershipLineFailsTheMergeClosed(t *testing.T) {
	d := newDataset(t)
	assertMergeFailsClosed(t, d, nil, func() {
		segA := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e", "qa", "", "a"), rec("s", "e", "qb", "", "b")})
		if _, err := d.MergeCompact("answers", 584, segA); err != nil {
			t.Fatal(err)
		}
		// Read as ("s", "e", ""), the cut first line would still be in order and leave qa held
		// by no run.
		overwriteMembership(t, d, "answers", 584, `{"source_key":"s","remote_endpoint":"e"`, memberLine("qb"))
	})
}

// appendPage stamps records in the segment kind's stamp column and appends them to seg
// as one fetched page.
func appendPage(t *testing.T, seg *store.Segment, recs [][]byte, at time.Time, key int) {
	t.Helper()
	lines, err := store.MustKind(seg.Type).StampLines(recs, at, key)
	if err == nil {
		err = seg.AppendLines(lines)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func logRec(id, data string) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "data": data})
	return b
}

func mergeLogs(t *testing.T, d *Dataset, class int, recs ...[]byte) store.MergeCounts {
	t.Helper()
	seg := writeFinishedSegment(t, d, store.TypeRecentLogs, class, recs)
	counts, err := d.MergeCompact(store.TypeRecentLogs, class, seg)
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

func logIDs(t *testing.T, d *Dataset) string {
	t.Helper()
	var ids []string
	for _, id := range storeIdentities(t, d, store.TypeRecentLogs) {
		ids = append(ids, id[0])
	}
	return strings.Join(ids, ",")
}

func classMembership(t *testing.T, d *Dataset, class int) string {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := m.Membership[MembershipKey(store.TypeRecentLogs, class)]
	if !ok {
		t.Fatalf("no membership for class %d", class)
	}
	ids, err := store.ReadMembershipFile(d.Path(ref.File), store.MustKind(store.TypeRecentLogs))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, id := range ids {
		out = append(out, id[0])
	}
	return strings.Join(out, ",")
}

func TestRecentLogsMergeAccumulates(t *testing.T) {
	d := newDataset(t)
	if c := mergeLogs(t, d, 590, logRec("e1", "a"), logRec("e2", "a")); c != (store.MergeCounts{Fetched: 2, New: 2}) {
		t.Fatalf("first pull counts = %+v", c)
	}
	if c := mergeLogs(t, d, 590, logRec("e2", "b"), logRec("e3", "b")); c != (store.MergeCounts{Fetched: 2, New: 1, Updated: 1}) {
		t.Fatalf("second pull counts = %+v", c)
	}
	if got := logIDs(t, d); got != "e1,e2,e3" {
		t.Fatalf("store after the second pull = %s", got)
	}
	m, _ := d.ReadManifest()
	if got := readData(t, d.Path(m.Stores[store.TypeRecentLogs].File), store.TypeRecentLogs, store.Identity{"e2"}); got != "b" {
		t.Errorf("e2 = %q, want the second pull's", got)
	}
	if got := classMembership(t, d, 590); got != "e1,e2,e3" {
		t.Errorf("membership after the second pull = %s", got)
	}
	if c := mergeLogs(t, d, 590, logRec("e4", "c")); c != (store.MergeCounts{Fetched: 1, New: 1}) {
		t.Fatalf("third pull counts = %+v", c)
	}
	if got := logIDs(t, d); got != "e1,e2,e3,e4" {
		t.Fatalf("store after the third pull = %s", got)
	}
}

func TestRecentLogsStampsTheClass(t *testing.T) {
	d := newDataset(t)
	mergeLogs(t, d, 590, logRec("e1", "a"))
	m, _ := d.ReadManifest()
	raw, err := os.ReadFile(d.Path(m.Stores[store.TypeRecentLogs].File))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
		t.Fatal(err)
	}
	if _, ok := row["_run_id"]; ok || row["_class_id"] != float64(590) {
		t.Errorf("stored row = %v, want _class_id 590 and no _run_id", row)
	}
}

func TestRecentLogsClassesKeepEachOthersRowsAcrossASweep(t *testing.T) {
	d := newDataset(t)
	mergeLogs(t, d, 590, logRec("a1", "x"), logRec("a2", "x"))
	writeFinishedSegment(t, d, store.TypeRecentLogs, 591, [][]byte{logRec("b1", "x")})
	mergeLogs(t, d, 590, logRec("a3", "x"))
	if got := logIDs(t, d); got != "a1,a2,a3,b1" {
		t.Fatalf("store after 590's pull swept 591 = %s", got)
	}
	if got := classMembership(t, d, 591); got != "b1" {
		t.Fatalf("591's membership after the sweep = %s", got)
	}
	mergeLogs(t, d, 591, logRec("b2", "x"))
	mergeLogs(t, d, 590, logRec("a4", "x"))
	if got := logIDs(t, d); got != "a1,a2,a3,a4,b1,b2" {
		t.Fatalf("store after alternating pulls = %s", got)
	}
	if got := classMembership(t, d, 591); got != "b1,b2" {
		t.Errorf("591's membership = %s", got)
	}
}

func TestRecentLogsMergeFailsClosedOnAMissingMembershipFile(t *testing.T) {
	d := newDataset(t)
	mergeLogs(t, d, 590, logRec("e1", "a"))
	m, _ := d.ReadManifest()
	if err := os.Remove(d.Path(m.Membership[MembershipKey(store.TypeRecentLogs, 590)].File)); err != nil {
		t.Fatal(err)
	}
	seg := writeFinishedSegment(t, d, store.TypeRecentLogs, 590, [][]byte{logRec("e2", "a")})
	if _, err := d.MergeCompact(store.TypeRecentLogs, 590, seg); err == nil {
		t.Fatal("the merge succeeded with the class's membership file missing")
	}
	if got := logIDs(t, d); got != "e1" {
		t.Errorf("store after the failed merge = %s", got)
	}
}

func TestRecentLogsKeepsAClassWhoseMembershipWasLost(t *testing.T) {
	d := newDataset(t)
	mergeLogs(t, d, 590, logRec("a1", "x"), logRec("a2", "x"))
	mergeLogs(t, d, 591, logRec("b1", "x"))
	m, _ := d.ReadManifest()
	if err := os.Remove(d.Path(m.Membership[MembershipKey(store.TypeRecentLogs, 590)].File)); err != nil {
		t.Fatal(err)
	}
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	if c := mergeLogs(t, d, 591, logRec("b2", "x")); c.Removed != 0 {
		t.Errorf("591's pull removed %d rows", c.Removed)
	}
	if got := logIDs(t, d); got != "a1,a2,b1,b2" {
		t.Errorf("store after 591's pull = %s, want 590's rows kept", got)
	}
}

func TestRecentLogsReindexKeepsTheStoreWhenTheNewestMembershipIsLost(t *testing.T) {
	d := newDataset(t)
	mergeLogs(t, d, 591, logRec("b1", "x"))
	mergeLogs(t, d, 590, logRec("a1", "x"), logRec("a2", "x"))
	m, _ := d.ReadManifest()
	if err := os.Remove(d.Path(m.Membership[MembershipKey(store.TypeRecentLogs, 590)].File)); err != nil {
		t.Fatal(err)
	}
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	if m, _ := d.ReadManifest(); m.Stores[store.TypeRecentLogs].Count != 3 {
		t.Fatalf("reindexed store = %+v, want the 3 rows on disk", m.Stores[store.TypeRecentLogs])
	}
	mergeLogs(t, d, 591, logRec("b2", "x"))
	if got := logIDs(t, d); got != "a1,a2,b1,b2" {
		t.Errorf("store after 591's pull = %s, want every row kept", got)
	}
}

func TestRecentLogsTieBreaksOnTheClassStamp(t *testing.T) {
	d := newDataset(t)
	at := time.Unix(1000, 0).UTC()
	for class, data := range map[int]string{590: "low", 591: "high"} {
		seg := store.OpenSegment(d.Dir, store.TypeRecentLogs, class)
		appendPage(t, seg, [][]byte{logRec("e1", data)}, at, class)
		if err := seg.WriteCursor(&store.Cursor{Pages: 1, Items: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.MergeCompact(store.TypeRecentLogs, 590, store.OpenSegment(d.Dir, store.TypeRecentLogs, 590)); err != nil {
		t.Fatal(err)
	}
	m, _ := d.ReadManifest()
	if got := readData(t, d.Path(m.Stores[store.TypeRecentLogs].File), store.TypeRecentLogs, store.Identity{"e1"}); got != "high" {
		t.Errorf("e1 = %q, want the higher class's row on a tied fetch time", got)
	}
}
