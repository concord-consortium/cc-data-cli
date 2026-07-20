package dataset

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func TestReindexReproducesHoldings(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{
		rec("s", "e1", "q1", "", "A"),
		rec("s", "e2", "q2", "", "A"),
	})
	if _, err := d.MergeCompact("answers", 584, seg); err != nil {
		t.Fatal(err)
	}

	// Delete the manifest and reindex from the filesystem.
	os.Remove(d.Path("manifest.json"))
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.Stores["answers"].Count != 2 || m.Stores["answers"].Version != 1 {
		t.Fatalf("reindex store = %+v", m.Stores["answers"])
	}
	if _, ok := m.Membership[MembershipKey("answers", 584)]; !ok {
		t.Fatal("reindex should adopt membership")
	}
	if len(m.Downloads) != 1 || !m.Downloads[0].Recovered {
		t.Fatalf("reindex download should be recovered: %+v", m.Downloads)
	}
	if m.Stores["answers"].Columns["data"] != store.TypeVARCHAR {
		t.Fatalf("reindex should re-derive columns: %+v", m.Stores["answers"].Columns)
	}
}

// TestReindexPreservesManifestProvenance covers Option C: when reindex has the
// existing manifest in hand, it must carry forward label provenance (report_type,
// slug) and not flag the entry recovered. The CSV shape here is ambiguous
// (student_id, no pseudo-header rows) so shape-based recovery alone would return
// the distinguished "recovered" type with recovered=true.
func TestReindexPreservesManifestProvenance(t *testing.T) {
	d := newDataset(t)
	os.WriteFile(d.Path("report_219.csv"), []byte("student_id,logins\n1,5\n2,7\n"), 0o600)

	// A manifest download carrying authoritative provenance, as a real fetch writes.
	if err := d.UpdateManifest(func(m *Manifest) error {
		m.Downloads = append(m.Downloads, Download{
			Type:       "report",
			RunID:      219,
			Slug:       "usage-report",
			ReportType: ReportTypeUsage,
			Files:      []string{"report_219.csv"},
			Complete:   true,
			Recovered:  false,
			FetchedAt:  fixedClock(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	var got *Download
	for i := range m.Downloads {
		if m.Downloads[i].Type == "report" && m.Downloads[i].RunID == 219 {
			got = &m.Downloads[i]
		}
	}
	if got == nil {
		t.Fatal("report download missing after reindex")
	}
	if got.Recovered {
		t.Fatal("reindex must not flag recovered when the manifest carried authoritative provenance")
	}
	if got.ReportType != ReportTypeUsage {
		t.Fatalf("report_type not preserved: got %q, want %q", got.ReportType, ReportTypeUsage)
	}
	if got.Slug != "usage-report" {
		t.Fatalf("slug not preserved: got %q", got.Slug)
	}
	// And no spurious RECOVERED_PROVENANCE warning.
	for _, w := range driftWarnings(d, m) {
		if strings.HasPrefix(w, "RECOVERED_PROVENANCE") {
			t.Fatalf("unexpected warning: %s", w)
		}
	}
}

func TestReindexReportTypeRecovery(t *testing.T) {
	d := newDataset(t)
	// log CSV: no student_id column.
	os.WriteFile(d.Path("report_100.csv"), []byte("event,ts\nlogin,1\n"), 0o600)
	// answers CSV: student_id + pseudo-header rows.
	os.WriteFile(d.Path("report_200.csv"), []byte("student_id,x_answer\nPrompt,p\nCorrect answer,c\n1,a\n"), 0o600)
	// usage CSV: student_id, no pseudo-header rows -> recovered.
	os.WriteFile(d.Path("report_300.csv"), []byte("student_id,logins\n1,5\n2,7\n"), 0o600)

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, _ := d.ReadManifest()
	byRun := map[int]Download{}
	for _, dl := range m.Downloads {
		byRun[dl.RunID] = dl
	}
	if byRun[100].ReportType != ReportTypeLog || byRun[100].Recovered {
		t.Fatalf("log recovery wrong: %+v", byRun[100])
	}
	if byRun[200].ReportType != ReportTypeAnswers || byRun[200].Recovered {
		t.Fatalf("answers recovery wrong: %+v", byRun[200])
	}
	if byRun[300].ReportType != ReportTypeRecovered || !byRun[300].Recovered {
		t.Fatalf("usage should recover as 'recovered': %+v", byRun[300])
	}
	// answers CSV row_count excludes the 2 pseudo-header rows.
	if byRun[200].RowCount == nil || *byRun[200].RowCount != 1 {
		t.Fatalf("answers row_count should be 1: %v", byRun[200].RowCount)
	}
	if byRun[300].RowCount == nil || *byRun[300].RowCount != 2 {
		t.Fatalf("usage row_count should be 2: %v", byRun[300].RowCount)
	}
}

// attachAnswerRec builds an answer record that references one attachment, so
// RebuildAttachmentIndex keeps the on-disk file (a file no record references is
// GC'd). source_key is "s", matching AttachmentFileName("s", ...).
func attachAnswerRec(i int, name, publicPath string) []byte {
	return []byte(fmt.Sprintf(
		`{"source_key":"s","remote_endpoint":"e%d","question_id":"q%d","id":"a%d","attachments":{%q:{"publicPath":%q,"contentType":"application/json"}}}`,
		i, i, i, name, publicPath))
}

func getDownload(m *Manifest, typ string, run int) *Download {
	for i := range m.Downloads {
		if m.Downloads[i].Type == typ && m.Downloads[i].RunID == run {
			return &m.Downloads[i]
		}
	}
	return nil
}

// seedAttachmentRun seeds an answers store referencing the given attachments,
// writes those files to disk, and records an attachments download with the given
// coverage. Returns the on-disk relative file paths in order.
func seedAttachmentRun(t *testing.T, d *Dataset, run int, names, publicPaths []string, cov *Coverage) []string {
	t.Helper()
	if err := os.MkdirAll(d.Path(AttachmentsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	var recs [][]byte
	var files []string
	for i := range names {
		recs = append(recs, attachAnswerRec(run*10+i, names[i], publicPaths[i]))
		rel := AttachmentFileName("s", publicPaths[i], names[i])
		if err := os.WriteFile(d.Path(rel), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, rel)
	}
	seg := writeFinishedSegment(t, d, "answers", run, recs)
	if _, err := d.MergeCompact("answers", run, seg); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateManifest(func(m *Manifest) error {
		m.Downloads = append(m.Downloads, Download{
			Type: "attachments", RunID: run, Scanned: []string{"answers", "history"},
			Files: files, Coverage: cov, Complete: true, FetchedAt: fixedClock(),
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

// TestReindexPreservesAttachmentsDownload: reindex must carry the attachments
// download (which it has no filesystem path to rebuild) forward with its scanned
// and coverage provenance intact and without flagging it recovered.
func TestReindexPreservesAttachmentsDownload(t *testing.T) {
	d := newDataset(t)
	cov := &Coverage{WithData: 1, Missing: []MissingItem{{DocID: "a2", Name: "gone.mp3", Error: "not_found"}}}
	files := seedAttachmentRun(t, d, 584, []string{"file.json"}, []string{"p/a.json"}, cov)

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	got := getDownload(m, "attachments", 584)
	if got == nil {
		t.Fatalf("attachments download dropped by reindex: %+v", m.Downloads)
	}
	if got.Recovered {
		t.Fatal("carried attachments entry must not be flagged recovered")
	}
	if !reflect.DeepEqual(got.Scanned, []string{"answers", "history"}) {
		t.Fatalf("scanned not preserved: %v", got.Scanned)
	}
	if !reflect.DeepEqual(got.Coverage, cov) {
		t.Fatalf("coverage not preserved: %+v", got.Coverage)
	}
	if !reflect.DeepEqual(got.Files, files) {
		t.Fatalf("files = %v, want %v", got.Files, files)
	}
}

// TestReindexReconcilesAttachmentsFiles: a file removed from disk before reindex
// must drop out of the carried entry's Files (reindex is authoritative about the
// filesystem), while coverage is preserved.
func TestReindexReconcilesAttachmentsFiles(t *testing.T) {
	d := newDataset(t)
	files := seedAttachmentRun(t, d, 584,
		[]string{"keep.json", "gone.json"},
		[]string{"p/keep.json", "p/gone.json"},
		&Coverage{WithData: 2})

	if err := os.Remove(d.Path(files[1])); err != nil { // remove gone.json before reindex
		t.Fatal(err)
	}
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, _ := d.ReadManifest()
	got := getDownload(m, "attachments", 584)
	if got == nil {
		t.Fatal("attachments download dropped")
	}
	if !reflect.DeepEqual(got.Files, []string{files[0]}) {
		t.Fatalf("files not reconciled to on-disk reality: got %v, want [%s]", got.Files, files[0])
	}
	if got.Coverage.WithData != 2 {
		t.Fatalf("coverage (historical fetch record) must be preserved, got %+v", got.Coverage)
	}
}

// TestReindexAttachmentsMultipleRunsDeterministic: each run's attachments download
// survives, and Downloads is byte-stable across repeated reindexes.
func TestReindexAttachmentsMultipleRunsDeterministic(t *testing.T) {
	clock = fixedClock
	defer func() { clock = defaultClock }()

	d := newDataset(t)
	seedAttachmentRun(t, d, 584, []string{"f.json"}, []string{"p/584.json"}, &Coverage{WithData: 1})
	seedAttachmentRun(t, d, 612, []string{"f.json"}, []string{"p/612.json"}, &Coverage{WithData: 1})

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m1, _ := d.ReadManifest()
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m2, _ := d.ReadManifest()

	n := 0
	for _, dl := range m1.Downloads {
		if dl.Type == "attachments" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("both runs' attachments downloads should survive, got %d", n)
	}
	if !reflect.DeepEqual(m1.Downloads, m2.Downloads) {
		t.Fatalf("Downloads not deterministic across reindexes:\n%+v\n%+v", m1.Downloads, m2.Downloads)
	}
}

// TestReindexNoAttachmentsWithoutPrior documents the current disaster-recovery
// behavior: with no readable prior manifest, reindex has nothing to carry, so the
// attachments download stays absent (the files/index still rebuild from disk).
func TestReindexNoAttachmentsWithoutPrior(t *testing.T) {
	d := newDataset(t)
	seedAttachmentRun(t, d, 584, []string{"file.json"}, []string{"p/a.json"}, &Coverage{WithData: 1})

	os.Remove(d.Path("manifest.json"))
	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	m, _ := d.ReadManifest()
	if got := getDownload(m, "attachments", 584); got != nil {
		t.Fatalf("without a prior manifest there is nothing to carry; got %+v", got)
	}
	// The file index still rebuilds from disk.
	if len(m.Attachments) != 1 {
		t.Fatalf("attachment index should rebuild from disk, got %d", len(m.Attachments))
	}
}

func TestReindexBusyWhenActivityHeld(t *testing.T) {
	d := newDataset(t)
	ok, err := d.Activity().TryRLock()
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer d.Activity().RUnlock()
	if err := d.Reindex(); err != ErrBusy {
		t.Fatalf("reindex under fetch should be busy, got %v", err)
	}
}
