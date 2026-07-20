package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// bulkServer serves paged answers/history with an optional total_endpoints and
// an optional one-shot EXPIRED_CURSOR on a given token.
type bulkServer struct {
	*httptest.Server
	pages          []string // JSON bodies keyed by page index
	tokens         []string // next_page_token per page ("" = last)
	totalEndpoints *int
	expireToken    string // return 410 when this token arrives, once
	expired        bool
}

func newBulkServer(t *testing.T, b *bulkServer) *bulkServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/reports/", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("page_token")
		if token != "" && token == b.expireToken && !b.expired {
			b.expired = true
			w.WriteHeader(http.StatusGone)
			fmt.Fprint(w, `{"error":"EXPIRED_CURSOR","message":"expired"}`)
			return
		}
		idx := b.pageForToken(token)
		next := b.tokens[idx]
		te := ""
		if b.totalEndpoints != nil {
			te = fmt.Sprintf(`,"total_endpoints":%d`, *b.totalEndpoints)
		}
		nextField := "null"
		if next != "" {
			nextField = fmt.Sprintf("%q", next)
		}
		fmt.Fprintf(w, `{"items":%s,"next_page_token":%s%s}`, b.pages[idx], nextField, te)
	})
	b.Server = httptest.NewServer(mux)
	return b
}

// pageForToken maps an incoming page_token to the page index; "" -> page 0, and
// tokenN -> page N.
func (b *bulkServer) pageForToken(token string) int {
	if token == "" {
		return 0
	}
	for i := 0; i < len(b.tokens); i++ {
		if i > 0 && b.tokens[i-1] == token {
			return i
		}
	}
	return 0
}

func answerItem(sk, re, qid string) string {
	return fmt.Sprintf(`{"source_key":%q,"remote_endpoint":%q,"question_id":%q,"report_state":"{}"}`, sk, re, qid)
}

func runPaged(t *testing.T, d *dataset.Dataset, srv *bulkServer, refresh bool) (map[string]any, error) {
	t.Helper()
	result, err := FetchPaged(context.Background(), PagedOptions{
		DS:       d,
		Client:   fastClient(srv.URL),
		RunID:    584,
		Type:     store.TypeAnswers,
		Refresh:  refresh,
		Progress: discard{},
	})
	if result == nil {
		return nil, err
	}
	return result.(map[string]any), err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func timeZero() time.Time { return time.Unix(1000, 0).UTC() }

func TestPagedMultiPageMergeAndCoverage(t *testing.T) {
	d := newTestDataset(t)
	te := 3
	srv := newBulkServer(t, &bulkServer{
		pages: []string{
			"[" + answerItem("s", "e1", "q1") + "," + answerItem("s", "e2", "q2") + "]",
			"[" + answerItem("s", "e3", "q3") + "]",
		},
		tokens:         []string{"tok1", ""},
		totalEndpoints: &te,
	})
	defer srv.Close()

	result, err := runPaged(t, d, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	if result["complete"] != true {
		t.Fatalf("should be complete: %+v", result)
	}
	counts := result["merge_counts"].(store.MergeCounts)
	if counts.New != 3 {
		t.Fatalf("expected 3 new, got %+v", counts)
	}
	cov := result["coverage"].(*dataset.Coverage)
	if cov.Queried == nil || *cov.Queried != 3 || cov.WithData != 3 || cov.Empty == nil || *cov.Empty != 0 {
		t.Fatalf("coverage = %+v", cov)
	}
	// Download entry is complete.
	m, _ := d.ReadManifest()
	if len(m.Downloads) != 1 || !m.Downloads[0].Complete {
		t.Fatalf("download entry: %+v", m.Downloads)
	}
}

func TestPagedNoTotalEndpoints(t *testing.T) {
	d := newTestDataset(t)
	srv := newBulkServer(t, &bulkServer{
		pages:  []string{"[" + answerItem("s", "e1", "q1") + "]"},
		tokens: []string{""},
	})
	defer srv.Close()
	result, err := runPaged(t, d, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	cov := result["coverage"].(*dataset.Coverage)
	if cov.Queried != nil || cov.Empty != nil || cov.WithData != 1 {
		t.Fatalf("coverage without total_endpoints should mark queried/empty unknown: %+v", cov)
	}
}

func TestPagedExpiredCursorRestart(t *testing.T) {
	d := newTestDataset(t)
	te := 2
	srv := newBulkServer(t, &bulkServer{
		pages: []string{
			"[" + answerItem("s", "e1", "q1") + "]",
			"[" + answerItem("s", "e2", "q2") + "]",
		},
		tokens:         []string{"tok1", ""},
		totalEndpoints: &te,
		expireToken:    "tok1", // first attempt to page 2 expires
	})
	defer srv.Close()
	result, err := runPaged(t, d, srv, false)
	if err != nil {
		t.Fatalf("expired cursor should restart and complete: %v", err)
	}
	if result["complete"] != true {
		t.Fatal("should complete after restart")
	}
	// After restart from null the full set is re-fetched and merged.
	if result["merge_counts"].(store.MergeCounts).New != 2 {
		t.Fatalf("restart should end with 2 identities: %+v", result["merge_counts"])
	}
}

func TestPagedResumeFromCursor(t *testing.T) {
	d := newTestDataset(t)
	// Pre-seed a segment + cursor as if page 1 was fetched and the process died.
	seg := store.OpenSegment(d.Dir, store.TypeAnswers, 584)
	seg.AppendPage([][]byte{[]byte(answerItem("s", "e1", "q1"))}, timeZero(), 584)
	tok := "tok1"
	seg.WriteCursor(&store.Cursor{NextPageToken: &tok, Pages: 1, Items: 1})

	srv := newBulkServer(t, &bulkServer{
		pages:  []string{"[" + answerItem("s", "e1", "q1") + "]", "[" + answerItem("s", "e2", "q2") + "]"},
		tokens: []string{"tok1", ""},
	})
	defer srv.Close()

	result, err := runPaged(t, d, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	// Resume fetches page 2 and merges e1 + e2.
	if result["merge_counts"].(store.MergeCounts).New != 2 {
		t.Fatalf("resume should merge 2 identities: %+v", result["merge_counts"])
	}
}

func TestPagedStaleSegmentDiscarded(t *testing.T) {
	d := newTestDataset(t)
	// A segment with no cursor is stale.
	seg := store.OpenSegment(d.Dir, store.TypeAnswers, 584)
	seg.AppendPage([][]byte{[]byte(answerItem("s", "stale", "q"))}, timeZero(), 584)

	srv := newBulkServer(t, &bulkServer{
		pages:  []string{"[" + answerItem("s", "e1", "q1") + "]"},
		tokens: []string{""},
	})
	defer srv.Close()
	result, err := runPaged(t, d, srv, false)
	if err != nil {
		t.Fatal(err)
	}
	// The stale record must not survive; only the fresh fetch's identity lands.
	if result["merge_counts"].(store.MergeCounts).New != 1 {
		t.Fatalf("stale segment should be discarded, got %+v", result["merge_counts"])
	}
}
