package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBuildIndexPropagatesReadError verifies that a non-EOF read error during the
// segment scan is returned rather than swallowed into a partial index (finding
// #43). A partial index feeds a truncated identity set to `covered`, and
// StreamMerge then drops any old-store record whose key is absent from it, which
// silently deletes durable records and violates the never-lose contract.
//
// The read error is injected by making the segment path a directory: os.Open
// succeeds, but the first Read returns EISDIR, which is not io.EOF. (The happy
// path is covered by the merge and reindex tests, which index real segments.)
func TestBuildIndexPropagatesReadError(t *testing.T) {
	dir := t.TempDir()
	seg := OpenSegment(dir, "answers", 584)
	if err := os.MkdirAll(filepath.Dir(seg.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(seg.Path(), 0o755); err != nil {
		t.Fatal(err)
	}

	idx, err := seg.BuildIndex()
	if err == nil {
		t.Fatalf("BuildIndex must return the read error, not a partial index + nil (got %d keys)", idx.Len())
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("a real read error must not be reported as EOF: %v", err)
	}
}

// TestBuildIndexHappyPathStillWorks is a control so the fault test above cannot
// pass merely because BuildIndex always errors: a well-formed segment indexes
// both records.
func TestBuildIndexHappyPathStillWorks(t *testing.T) {
	dir := t.TempDir()
	seg := OpenSegment(dir, "answers", 584)
	recs := [][]byte{
		[]byte(`{"source_key":"s","remote_endpoint":"e1","question_id":"q1","data":"A"}`),
		[]byte(`{"source_key":"s","remote_endpoint":"e2","question_id":"q2","data":"A"}`),
	}
	if err := seg.AppendPage(recs, time.Unix(584, 0).UTC(), 584); err != nil {
		t.Fatal(err)
	}
	idx, err := seg.BuildIndex()
	if err != nil {
		t.Fatal(err)
	}
	// BuildIndex keeps the segment file open for offset reads; close it so the
	// temp dir cleanup succeeds (Windows cannot remove a file with an open handle).
	defer idx.Close()
	if idx.Len() != 2 {
		t.Fatalf("expected 2 indexed identities, got %d", idx.Len())
	}
}

func TestBuildIndexRefusesATornLine(t *testing.T) {
	dir := t.TempDir()
	seg := OpenSegment(dir, TypeAnswers, 1)
	if err := os.MkdirAll(filepath.Dir(seg.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	whole := `{"question_id":"q","remote_endpoint":"r","source_key":"s","answer":"a"}`
	torn := `{"question_id":"q","remote_endpoint":"r","source_key":"s","ans` + whole + "\n"
	if err := os.WriteFile(seg.Path(), []byte(torn), 0o600); err != nil {
		t.Fatal(err)
	}
	if idx, err := seg.BuildIndex(); err == nil {
		idx.Close()
		t.Fatal("BuildIndex accepted a torn segment line")
	}
}
