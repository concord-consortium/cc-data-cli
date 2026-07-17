package dataset

import (
	"os"
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
