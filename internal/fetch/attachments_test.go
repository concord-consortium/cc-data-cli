package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

type presignServer struct {
	*httptest.Server
	presignCalls  int32
	lastDispos    string
	notFoundDocID string
}

func newPresignServer(t *testing.T, ps *presignServer) *presignServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/s3") {
			w.Write([]byte("filebytes"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/attachments") {
			atomic.AddInt32(&ps.presignCalls, 1)
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Attachments []struct {
					Collection string `json:"collection"`
					Source     string `json:"source"`
					DocID      string `json:"doc_id"`
					Name       string `json:"name"`
				} `json:"attachments"`
				Disposition string `json:"disposition"`
			}
			json.Unmarshal(body, &req)
			ps.lastDispos = req.Disposition
			var results []string
			for _, a := range req.Attachments {
				if a.DocID == ps.notFoundDocID {
					results = append(results, fmt.Sprintf(`{"doc_id":%q,"name":%q,"error":"not_found"}`, a.DocID, a.Name))
					continue
				}
				results = append(results, fmt.Sprintf(`{"doc_id":%q,"name":%q,"url":%q}`, a.DocID, a.Name, ps.URL+"/api/v1/reports/584/s3"))
			}
			fmt.Fprintf(w, `{"results":[%s],"expires_in_seconds":600}`, strings.Join(results, ","))
			return
		}
		http.NotFound(w, r)
	})
	ps.Server = httptest.NewServer(mux)
	return ps
}

// seedAnswers builds a dataset with an answers store for a run whose records
// carry attachments.
func seedAnswers(t *testing.T, run int, recs ...[]byte) *dataset.Dataset {
	t.Helper()
	d := newTestDataset(t)
	seg := store.OpenSegment(d.Dir, store.TypeAnswers, run)
	if err := seg.AppendPage(recs, timeZero(), run); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: len(recs)})
	if _, err := d.MergeCompact(store.TypeAnswers, run, seg); err != nil {
		t.Fatal(err)
	}
	return d
}

func answerWithAttachment(re, qid, id, name, publicPath string) []byte {
	return []byte(fmt.Sprintf(`{"source_key":"s","remote_endpoint":%q,"question_id":%q,"id":%q,"attachments":{%q:{"publicPath":%q}}}`, re, qid, id, name, publicPath))
}

func runAttachments(t *testing.T, d *dataset.Dataset, ps *presignServer, opts AttachmentOptions) (any, *output.CLIError) {
	t.Helper()
	opts.DS = d
	opts.Client = fastClient(ps.URL)
	opts.RunID = 584
	opts.Progress = discard{}
	result, err := FetchAttachments(context.Background(), opts)
	if err != nil {
		return result, err.(*output.CLIError)
	}
	return result, nil
}

func TestAttachmentsDownload(t *testing.T) {
	d := seedAnswers(t, 584,
		answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"),
		answerWithAttachment("e2", "q2", "d2", "b.png", "p/b.png"),
	)
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()

	result, cliErr := runAttachments(t, d, ps, AttachmentOptions{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	m := result.(map[string]any)
	files := m["files"].([]string)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %v", files)
	}
	for _, f := range files {
		data, err := os.ReadFile(d.Path(f))
		if err != nil || string(data) != "filebytes" {
			t.Fatalf("attachment %s not downloaded: %v", f, err)
		}
	}
	// Manifest attachment index rebuilt.
	man, _ := d.ReadManifest()
	if len(man.Attachments) != 2 {
		t.Fatalf("attachment index should have 2 entries, got %d", len(man.Attachments))
	}
}

func TestAttachmentsNoSourcesError(t *testing.T) {
	d := newTestDataset(t) // no answers/history
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	_, cliErr := runAttachments(t, d, ps, AttachmentOptions{})
	if cliErr == nil || cliErr.ExitCode != output.ExitUsage {
		t.Fatalf("no sources should be a usage error, got %+v", cliErr)
	}
}

func TestAttachmentsSelectorName(t *testing.T) {
	d := seedAnswers(t, 584,
		answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"),
		answerWithAttachment("e2", "q2", "d2", "b.png", "p/b.png"),
	)
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	result, cliErr := runAttachments(t, d, ps, AttachmentOptions{Name: "a.mp3"})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if files := result.(map[string]any)["files"].([]string); len(files) != 1 {
		t.Fatalf("--name should select 1 file, got %v", files)
	}
}

func TestAttachmentsPartialFailure(t *testing.T) {
	d := seedAnswers(t, 584,
		answerWithAttachment("e1", "q1", "good", "a.mp3", "p/a.mp3"),
		answerWithAttachment("e2", "q2", "missing", "b.png", "p/b.png"),
	)
	ps := newPresignServer(t, &presignServer{notFoundDocID: "missing"})
	defer ps.Close()
	result, cliErr := runAttachments(t, d, ps, AttachmentOptions{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	cov := result.(map[string]any)["coverage"].(map[string]any)
	missing := cov["missing"].([]dataset.MissingItem)
	if len(missing) != 1 || missing[0].Error != "not_found" {
		t.Fatalf("expected 1 not_found missing item, got %+v", missing)
	}
}

func TestAttachmentsResumeAndRefresh(t *testing.T) {
	d := seedAnswers(t, 584, answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"))
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()

	runAttachments(t, d, ps, AttachmentOptions{})
	first := atomic.LoadInt32(&ps.presignCalls)

	// Resume: the file exists, so no new presign call.
	runAttachments(t, d, ps, AttachmentOptions{})
	if atomic.LoadInt32(&ps.presignCalls) != first {
		t.Fatal("resume should not presign an already-downloaded file")
	}

	// --refresh re-presigns.
	runAttachments(t, d, ps, AttachmentOptions{Refresh: true})
	if atomic.LoadInt32(&ps.presignCalls) <= first {
		t.Fatal("--refresh should presign again")
	}
}

func TestAttachmentsURLMode(t *testing.T) {
	d := seedAnswers(t, 584, answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"))
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()

	var out bytes.Buffer
	restore := output.SetStreams(&out, io.Discard)
	defer restore()

	before, _ := os.ReadFile(d.Path("manifest.json"))
	result, cliErr := runAttachments(t, d, ps, AttachmentOptions{URL: true})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if result != nil {
		t.Fatal("--url should not emit a result line")
	}
	// Single ref: bare URL on stdout.
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "http") {
		t.Fatalf("--url single should print a bare URL, got %q", out.String())
	}
	// Nothing written to the dataset.
	if _, err := os.Stat(d.Path("attachments/" + dataset.FileID12("report-service-pro", "p/a.mp3") + "_a.mp3")); !os.IsNotExist(err) {
		t.Fatal("--url must not download files")
	}
	after, _ := os.ReadFile(d.Path("manifest.json"))
	if string(before) != string(after) {
		t.Fatal("--url must not change the manifest")
	}
}

func TestAttachmentsInlineDisposition(t *testing.T) {
	d := seedAnswers(t, 584, answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"))
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	runAttachments(t, d, ps, AttachmentOptions{Inline: true})
	if ps.lastDispos != "inline" {
		t.Fatalf("--inline should send disposition inline, got %q", ps.lastDispos)
	}
}

func TestAttachmentsGCDeletesUnreferenced(t *testing.T) {
	d := seedAnswers(t, 584, answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"))
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	// Plant an orphan file no record references.
	os.MkdirAll(d.Path("attachments"), 0o700)
	orphan := d.Path("attachments/deadbeef00_orphan.bin")
	os.WriteFile(orphan, []byte("x"), 0o600)

	runAttachments(t, d, ps, AttachmentOptions{})
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("GC should delete the unreferenced orphan file")
	}
}

func TestAttachmentsSameRunExclusion(t *testing.T) {
	d := seedAnswers(t, 584, answerWithAttachment("e1", "q1", "d1", "a.mp3", "p/a.mp3"))
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	held := storeDownloadLock(t, d, "seg_attachments_584.lock")
	defer held.Unlock()
	_, cliErr := runAttachments(t, d, ps, AttachmentOptions{})
	if cliErr == nil || !strings.Contains(cliErr.Message, "download busy") {
		t.Fatalf("second attachments fetch should be busy, got %+v", cliErr)
	}
}

func TestAttachmentsChunking(t *testing.T) {
	var recs [][]byte
	for i := 0; i < 250; i++ {
		recs = append(recs, answerWithAttachment(fmt.Sprintf("e%d", i), "q", fmt.Sprintf("d%d", i), fmt.Sprintf("f%d.bin", i), fmt.Sprintf("p/%d", i)))
	}
	d := seedAnswers(t, 584, recs...)
	ps := newPresignServer(t, &presignServer{})
	defer ps.Close()
	result, cliErr := runAttachments(t, d, ps, AttachmentOptions{})
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	if files := result.(map[string]any)["files"].([]string); len(files) != 250 {
		t.Fatalf("expected 250 files, got %d", len(files))
	}
	// 250 refs / 100 per chunk = 3 presign calls.
	if atomic.LoadInt32(&ps.presignCalls) != 3 {
		t.Fatalf("expected 3 presign chunks, got %d", ps.presignCalls)
	}
}
