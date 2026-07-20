package duck

import (
	"context"
	"os"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

func addAttachment(t *testing.T, d *dataset.Dataset, af dataset.AttachmentFile, content []byte) {
	t.Helper()
	if err := os.MkdirAll(d.Path(dataset.AttachmentsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.Path(af.File), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateManifest(func(m *dataset.Manifest) error {
		m.Attachments = append(m.Attachments, af)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func queryStr(t *testing.T, e *Engine, sql string) string {
	t.Helper()
	rows, err := e.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var s string
	if rows.Next() {
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestAttachmentViews covers content_type on attachment_files (#3), and the new
// attachment_content view exposing every offloaded JSON state regardless of the
// current-answer State flag (#2) while attachment_states stays narrow. A binary
// (audio) attachment must not break attachment_content: read_text needs UTF-8, so
// binary files are excluded from content but remain in attachment_files.
func TestAttachmentViews(t *testing.T) {
	d := newDS(t, "att")
	// Current-answer state, a historical state (different session), and audio.
	addAttachment(t, d, dataset.AttachmentFile{
		ID12: "aaa", Name: "file.json", Source: "activity-player.concord.org",
		PublicPath: "ia/f1/s1/file.json", ContentType: "application/json", Size: 7,
		File: "attachments/aaa_file.json", State: true,
	}, []byte(`{"v":1}`))
	addAttachment(t, d, dataset.AttachmentFile{
		ID12: "bbb", Name: "file.json", Source: "activity-player.concord.org",
		PublicPath: "ia/f1/s2/file.json", ContentType: "application/json", Size: 7,
		File: "attachments/bbb_file.json", State: false,
	}, []byte(`{"v":2}`))
	addAttachment(t, d, dataset.AttachmentFile{
		ID12: "ccc", Name: "a.mp3", Source: "activity-player.concord.org",
		PublicPath: "ia/f1/s1/a.mp3", ContentType: "audio/mpeg", Size: 3,
		File: "attachments/ccc_a.mp3", State: false,
	}, []byte{0xff, 0xfb, 0x90}) // invalid UTF-8

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	// #3: content_type is exposed on attachment_files, for all three files.
	if got := queryStr(t, e, "SELECT content_type FROM attachment_files WHERE id12='ccc'"); got != "audio/mpeg" {
		t.Fatalf("attachment_files.content_type = %q, want audio/mpeg", got)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM attachment_files"); n != 3 {
		t.Fatalf("attachment_files should list all 3 files, got %d", n)
	}

	// attachment_states stays narrow: only the current-answer (State=true) state.
	if n := queryInt(t, e, "SELECT count(*) FROM attachment_states"); n != 1 {
		t.Fatalf("attachment_states should have 1 current-answer row, got %d", n)
	}

	// #2: attachment_content exposes BOTH json snapshots (current + historical),
	// and does not choke on the binary audio (excluded, not an error).
	if n := queryInt(t, e, "SELECT count(*) FROM attachment_content"); n != 2 {
		t.Fatalf("attachment_content should expose both json snapshots, got %d", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM attachment_content WHERE state IS NOT NULL"); n != 2 {
		t.Fatalf("both json snapshots should TRY_CAST to JSON, got %d", n)
	}
	// The historical snapshot (session s2) is visible in content but not in states.
	if got := queryStr(t, e, "SELECT public_path FROM attachment_content WHERE id12='bbb'"); got != "ia/f1/s2/file.json" {
		t.Fatalf("historical snapshot public_path = %q", got)
	}
}
