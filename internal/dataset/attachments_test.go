package dataset

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"audio123.mp3", "audio123.mp3"},
		{"../etc/passwd", "passwd"},
		{"a/b/c.png", "c.png"},
		{"a:b", "a_b"},
		{`x?*|.txt`, "x___.txt"},
		{"trailing.  ", "trailing"},
		{"CON", "_CON"},
		{"com1", "_com1"},
		{"", "_"},
	}
	for _, c := range cases {
		got := SanitizeFilename(c.in)
		if got != c.want {
			t.Fatalf("SanitizeFilename(%q) = %q, want %q", c.in, got, c.want)
		}
		// The result must be a single path component staying inside attachments/.
		rel := filepath.Join(AttachmentsDir, "id12_"+got)
		if strings.Contains(got, "/") || strings.Contains(got, "\\") || !strings.HasPrefix(rel, AttachmentsDir) {
			t.Fatalf("sanitized name escapes: %q", got)
		}
	}
}

func TestID12Deterministic(t *testing.T) {
	a := FileID12("src", "path/x.mp3")
	b := FileID12("src", "path/x.mp3")
	if a != b || len(a) != 12 {
		t.Fatalf("id12 = %q,%q", a, b)
	}
	if FileID12("src", "y") == a {
		t.Fatal("different publicPath should give different id12")
	}
}

func TestScanHistoryUsesHistoryID(t *testing.T) {
	d := newDataset(t)
	// A history record whose embedded id is the answer id, but doc_id must be history_id.
	rec := []byte(`{"source_key":"s","remote_endpoint":"e","question_id":"q","history_id":"HID","id":"ANSWERID","attachments":{"audioFile":{"publicPath":"p/a.mp3","contentType":"audio/mp3"}}}`)
	refs := d.ScanRecordAttachments("history", rec)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].DocID != "HID" {
		t.Fatalf("history doc_id should be history_id, got %q", refs[0].DocID)
	}
	if refs[0].PublicPath != "p/a.mp3" || refs[0].Name != "audioFile" {
		t.Fatalf("ref = %+v", refs[0])
	}
}

func TestScanAnswersUsesID(t *testing.T) {
	d := newDataset(t)
	rec := []byte(`{"source_key":"s","remote_endpoint":"e","question_id":"q","id":"DOC","attachments":{"f":{"publicPath":"p"}}}`)
	refs := d.ScanRecordAttachments("answers", rec)
	if len(refs) != 1 || refs[0].DocID != "DOC" {
		t.Fatalf("answers doc_id should be id: %+v", refs)
	}
}

func TestScanStateMarkerSetsState(t *testing.T) {
	d := newDataset(t)
	// report_state is already double-decoded: an object referencing an attachment
	// by name via an __attachment__ marker.
	rec := []byte(`{"source_key":"s","remote_endpoint":"e","question_id":"q","id":"D","attachments":{"state.json":{"publicPath":"p"},"other":{"publicPath":"o"}},"report_state":{"nested":{"__attachment__":"state.json"}}}`)
	refs := d.ScanRecordAttachments("answers", rec)
	byName := map[string]AttachRef{}
	for _, r := range refs {
		byName[r.Name] = r
	}
	if !byName["state.json"].State {
		t.Fatal("state.json should be marked State via __attachment__")
	}
	if byName["other"].State {
		t.Fatal("other should not be marked State")
	}
}

func TestScanToleratesFolderField(t *testing.T) {
	d := newDataset(t)
	rec := []byte(`{"source_key":"s","remote_endpoint":"e","question_id":"q","id":"D","attachments":{"f":{"publicPath":"p","contentType":"x","folder":{"id":"1","ownerId":"2"}}}}`)
	refs := d.ScanRecordAttachments("answers", rec)
	if len(refs) != 1 || refs[0].PublicPath != "p" {
		t.Fatalf("should tolerate folder field: %+v", refs)
	}
}
