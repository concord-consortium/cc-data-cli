package dataset

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
)

func TestShowJSONSchema(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	d.MergeCompact("answers", 584, seg)

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	if s.Ref != "learn.concord.org/ds" || s.Portal != "learn.concord.org" {
		t.Fatalf("show ref/portal wrong: %+v", s)
	}
	if s.Totals["answers"] != 1 {
		t.Fatalf("totals = %+v", s.Totals)
	}
	if s.Downloads == nil {
		t.Fatal("downloads should be a non-nil slice for stable JSON")
	}
	if s.Warnings == nil {
		t.Fatal("warnings should be present (possibly empty)")
	}
}

func TestShowWarnsOnOrphanFile(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	d.MergeCompact("answers", 584, seg)
	// Plant an orphan store version not in the manifest.
	os.WriteFile(d.Path("answers.v9.jsonl"), []byte("{}\n"), 0o600)

	s, _ := d.BuildShowJSON(false)
	found := false
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "ORPHAN_FILE:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an orphan-file warning: %v", s.Warnings)
	}
}

func TestPurgeThenShowZeroHoldings(t *testing.T) {
	d := newDataset(t)
	seg := writeFinishedSegment(t, d, "answers", 584, [][]byte{rec("s", "e1", "q1", "", "A")})
	d.MergeCompact("answers", 584, seg)
	if err := d.Purge(); err != nil {
		t.Fatal(err)
	}
	s, _ := d.BuildShowJSON(false)
	if s.Totals["answers"] != 0 || len(s.Downloads) != 0 {
		t.Fatalf("purge should leave zero holdings: %+v", s)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("purge should leave no warnings: %v", s.Warnings)
	}
}

func TestListJSON(t *testing.T) {
	clock = fixedClock
	defer func() { clock = defaultClock }()
	root := t.TempDir()
	Create(root, Ref{Portal: config.MustPortal("learn.concord.org"), Name: "a"}, "first")
	Create(root, Ref{Portal: config.MustPortal("localhost:8080"), Name: "b"}, "second")

	list, err := BuildListJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Datasets) != 2 {
		t.Fatalf("expected 2 datasets, got %d", len(list.Datasets))
	}
	// Portal folder encoding is decoded back to the real host in the ref.
	var foundLocalhost bool
	for _, ds := range list.Datasets {
		if ds.Portal == "localhost:8080" && ds.Ref == "localhost:8080/b" {
			foundLocalhost = true
		}
	}
	if !foundLocalhost {
		t.Fatalf("port-bearing portal should decode: %+v", list.Datasets)
	}
}

func warningsWithPrefix(warnings []string, prefix string) []string {
	var out []string
	for _, w := range warnings {
		if strings.HasPrefix(w, prefix) {
			out = append(out, w)
		}
	}
	return out
}

// A lost provenance record costs the run's filter and any dimension view it feeds as well as its
// exact report type, and this warning is the only thing that says so.
func TestShowWarnsWhatARecoveredDownloadLost(t *testing.T) {
	d := newDataset(t)
	rc := 1
	if err := d.UpsertDownload(Download{
		Type: "report", RunID: 584, ReportType: ReportTypeRecovered, Files: []string{"report_584.csv"},
		RowCount: &rc, Columns: map[string]string{"learner_id": TypeBIGINT}, Complete: true, Recovered: true,
	}); err != nil {
		t.Fatal(err)
	}

	s, _ := d.BuildShowJSON(false)
	got := warningsWithPrefix(s.Warnings, "RECOVERED_PROVENANCE:")
	if len(got) != 1 {
		t.Fatalf("expected one recovered-provenance warning, got %v", s.Warnings)
	}
	for _, want := range []string{"584", "re-fetch", "report_type", "filter", "dimension view"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the warning does not name %q: %q", want, got[0])
		}
	}
}

// Store downloads carry no provenance, so the recovered flag there is not a gap. Widening the
// condition while editing the string next to it is the mutation this catches.
func TestShowDoesNotWarnForARecoveredStoreDownload(t *testing.T) {
	d := newDataset(t)
	if err := d.UpsertDownload(Download{
		Type: "answers", RunID: 584, Complete: true, Recovered: true,
	}); err != nil {
		t.Fatal(err)
	}

	s, _ := d.BuildShowJSON(false)
	if got := warningsWithPrefix(s.Warnings, "RECOVERED_PROVENANCE:"); len(got) != 0 {
		t.Fatalf("a store download raised a provenance warning: %v", got)
	}
}

func TestMaterializedBytesIsReportedSeparately(t *testing.T) {
	d := newDataset(t)
	os.WriteFile(d.Path("answers.v1.jsonl"), []byte("{}\n"), 0o600)
	os.MkdirAll(d.Path(MaterializedDir), 0o700)
	body := bytes.Repeat([]byte("x"), 500)
	os.WriteFile(d.Path(MaterializedDir+"/answers.parquet"), body, 0o600)

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	if s.MaterializedBytes != int64(len(body)) {
		t.Fatalf("materialized_bytes = %d, want %d", s.MaterializedBytes, len(body))
	}
	if s.SizeBytes <= s.MaterializedBytes {
		t.Fatalf("size_bytes (%d) must still count everything, including the %d derived bytes",
			s.SizeBytes, s.MaterializedBytes)
	}

	list, err := BuildListJSON(filepath.Dir(filepath.Dir(filepath.Dir(d.Dir))))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Datasets) != 1 {
		t.Fatalf("want one dataset listed, got %d", len(list.Datasets))
	}
	if list.Datasets[0].MaterializedBytes != int64(len(body)) {
		t.Fatalf("list materialized_bytes = %d, want %d", list.Datasets[0].MaterializedBytes, len(body))
	}
}

func TestUnreferencedMaterializedWarning(t *testing.T) {
	d := newDataset(t)
	os.MkdirAll(d.Path(MaterializedDir), 0o700)
	os.WriteFile(d.Path(MaterializedDir+"/logs.parquet"), []byte("PAR1"), 0o600)
	os.WriteFile(d.Path(MaterializedDir+"/answers.parquet"), []byte("PAR1"), 0o600)

	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	m.Materialized["answers"] = Materialized{File: MaterializedDir + "/answers.parquet"}
	if err := writeManifestFile(d.Dir, m); err != nil {
		t.Fatal(err)
	}

	s, err := d.BuildShowJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	var named []string
	for _, w := range s.Warnings {
		if strings.HasPrefix(w, "UNREFERENCED_MATERIALIZED") {
			named = append(named, w)
		}
	}
	if len(named) != 1 || !strings.Contains(named[0], "logs.parquet") {
		t.Fatalf("want exactly the unrecorded logs.parquet named, got %v", named)
	}
	if !strings.Contains(named[0], "dataset materialize") {
		t.Fatalf("the warning must name the command that restores it: %s", named[0])
	}
	if !sort.StringsAreSorted(s.Warnings) {
		t.Fatalf("warnings must stay sorted: %v", s.Warnings)
	}
}
