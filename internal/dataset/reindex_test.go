package dataset

import (
	"os"
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
