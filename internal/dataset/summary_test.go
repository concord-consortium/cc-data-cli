package dataset

import (
	"os"
	"strings"
	"testing"
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
	Create(root, Ref{Portal: "learn.concord.org", Name: "a"}, "first")
	Create(root, Ref{Portal: "localhost:8080", Name: "b"}, "second")

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
