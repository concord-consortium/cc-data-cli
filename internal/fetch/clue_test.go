package fetch

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

func fixture(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "clue", name))
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(b, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func pageBody(t *testing.T, items []map[string]any) string {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// clueServer serves one route's pages, page i answered for token "t<i>".
func clueServer(t *testing.T, total int, pages ...[]map[string]any) *bulkServer {
	t.Helper()
	b := &bulkServer{totalEndpoints: &total}
	for i, p := range pages {
		b.pages = append(b.pages, pageBody(t, p))
		next := ""
		if i+1 < len(pages) {
			next = "t" + string(rune('1'+i))
		}
		b.tokens = append(b.tokens, next)
	}
	srv := newBulkServer(t, b)
	t.Cleanup(srv.Close)
	return srv
}

func pullClue(t *testing.T, d *dataset.Dataset, srv *bulkServer, typ string, run int) (map[string]any, error) {
	t.Helper()
	res, err := FetchPaged(context.Background(), PagedOptions{DS: d, Client: fastClient(srv.URL), RunID: run, Type: typ, Progress: discard{}})
	if res == nil {
		return nil, err
	}
	return res.(map[string]any), err
}

func mustPull(t *testing.T, d *dataset.Dataset, srv *bulkServer, typ string, run int) map[string]any {
	t.Helper()
	res, err := pullClue(t, d, srv, typ, run)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func historyPages(t *testing.T) [][]map[string]any {
	var pages [][]map[string]any
	for i := 1; i <= 4; i++ {
		pages = append(pages, fixture(t, "history_page"+string(rune('0'+i))+".json"))
	}
	return pages
}

func queryStrings(t *testing.T, d *dataset.Dataset, query string) []string {
	t.Helper()
	e, err := duck.Open(context.Background(), []duck.DatasetSpec{{DS: d}}, nil, discard{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	rows, err := e.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+"="+b)
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestClueRoutesAreRequestedAtTheirOwnPaths(t *testing.T) {
	for typ, route := range map[string]string{store.TypeClueDocuments: "documents", store.TypeClueHistory: "history", store.TypeClueContent: "content"} {
		d := newTestDataset(t)
		srv := clueServer(t, 3, fixture(t, "documents.json"))
		if typ == store.TypeClueContent {
			srv = clueServer(t, 3, fixture(t, "content.json"))
		}
		if typ == store.TypeClueHistory {
			srv = clueServer(t, 3, historyPages(t)...)
		}
		mustPull(t, d, srv, typ, 584)
		if len(srv.paths) == 0 {
			t.Fatalf("%s made no request", typ)
		}
		for _, p := range srv.paths {
			if p != "/api/v1/reports/584/clue/"+route {
				t.Fatalf("%s requested %s", typ, p)
			}
		}
	}
}

func TestClueStringsAreStoredByteForByte(t *testing.T) {
	d := newTestDataset(t)
	pages := historyPages(t)
	big := strings.Repeat(`{"op":"replace","path":"/<&>","value":"héllo"}`, 70_000)
	pages[3] = append(pages[3], map[string]any{"doc_id": "-Nd2", "entry_id": "e08", "idx": 1, "parse_ok": true, "entry_json": big})
	mustPull(t, d, clueServer(t, 3, pages...), store.TypeClueHistory, 584)
	mustPull(t, d, clueServer(t, 3, fixture(t, "content.json")), store.TypeClueContent, 584)

	want := map[string]string{}
	for _, p := range pages {
		for _, e := range p {
			want[e["entry_id"].(string)] = md5hex(e["entry_json"].(string))
		}
	}
	got := queryStrings(t, d, "SELECT entry_id, md5(entry_json) FROM clue_history")
	if len(got) != len(want) {
		t.Fatalf("stored %d entries, want %d", len(got), len(want))
	}
	for _, g := range got {
		id, sum, _ := strings.Cut(g, "=")
		if want[id] != sum {
			t.Fatalf("entry %s entry_json changed in the store", id)
		}
	}
	content := fixture(t, "content.json")[0]["content_json"].(string)
	if got := queryStrings(t, d, "SELECT doc_id, md5(content_json) FROM clue_content WHERE content_json IS NOT NULL"); len(got) != 1 || got[0] != "-Nd1="+md5hex(content) {
		t.Fatalf("content_json = %v", got)
	}
}

func storedIdentities(t *testing.T, d *dataset.Dataset) []string {
	t.Helper()
	out := queryStrings(t, d, "SELECT doc_id || '/' || entry_id, md5(entry_json) FROM clue_history")
	sort.Strings(out)
	return out
}

func TestClueHistoryResumedMidDocumentMatchesAnUninterruptedPull(t *testing.T) {
	pages := historyPages(t)

	whole := newTestDataset(t)
	mustPull(t, whole, clueServer(t, 3, pages...), store.TypeClueHistory, 584)

	d := newTestDataset(t)
	broken := clueServer(t, 3, pages...)
	broken.failFrom = 2
	if _, err := pullClue(t, d, broken, store.TypeClueHistory, 584); err == nil {
		t.Fatal("a pull whose third page fails must not complete")
	}
	if cur, ok, _ := store.OpenSegment(d.Dir, store.TypeClueHistory, 584).ReadCursor(); !ok || cur.Pages != 2 {
		t.Fatalf("cursor after the failure = %+v, want two pages persisted", cur)
	}
	// At-least-once paging: the resumed page re-delivers page 2's last entry.
	resumed := append([][]map[string]any{}, pages...)
	resumed[2] = append([]map[string]any{pages[1][1]}, pages[2]...)
	res := mustPull(t, d, clueServer(t, 3, resumed...), store.TypeClueHistory, 584)
	if got, want := storedIdentities(t, d), storedIdentities(t, whole); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resumed store %v, want %v", got, want)
	}
	if mc := res["merge_counts"].(store.MergeCounts); mc.New != 8 || mc.Fetched != 9 {
		t.Fatalf("merge counts %+v, want 8 new from 9 fetched", mc)
	}
}

func TestClueHistoryRestartsOnAnExpiredCursor(t *testing.T) {
	d := newTestDataset(t)
	srv := clueServer(t, 3, historyPages(t)...)
	srv.expireToken = "t2"
	res := mustPull(t, d, srv, store.TypeClueHistory, 584)
	if got := storedIdentities(t, d); len(got) != 8 {
		t.Fatalf("stored %d entries after the restart, want 8", len(got))
	}
	if mc := res["merge_counts"].(store.MergeCounts); mc.Fetched != 8 || mc.New != 8 {
		t.Fatalf("merge counts %+v: the restart must discard the pages fetched before it", mc)
	}
	if len(srv.paths) != 7 {
		t.Fatalf("%d requests, want two pages, the expired one, then all four again", len(srv.paths))
	}
}

func TestClueRePullUpdatesInPlaceAndRemovesWhatLeftScope(t *testing.T) {
	d := newTestDataset(t)
	docs := fixture(t, "documents.json")
	mustPull(t, d, clueServer(t, 3, docs), store.TypeClueDocuments, 584)
	changed := []map[string]any{docs[0], docs[1]}
	changed[0]["title"] = "renamed"
	res := mustPull(t, d, clueServer(t, 3, changed), store.TypeClueDocuments, 584)
	if mc := res["merge_counts"].(store.MergeCounts); mc.Updated != 2 || mc.Removed != 2 || mc.New != 0 {
		t.Fatalf("merge counts %+v, want 2 updated and 2 removed", mc)
	}
	if got := queryStrings(t, d, "SELECT doc_id, title FROM clue_documents ORDER BY doc_id"); strings.Join(got, ",") != "-Nd1=renamed,-Nd2=Dataflow <1> & notes" {
		t.Fatalf("documents %v", got)
	}
}

func TestClueDocumentSharedByTwoRuns(t *testing.T) {
	d := newTestDataset(t)
	docs := fixture(t, "documents.json")
	mustPull(t, d, clueServer(t, 3, docs[:2]), store.TypeClueDocuments, 584)
	mustPull(t, d, clueServer(t, 3, []map[string]any{docs[0], docs[2]}), store.TypeClueDocuments, 612)
	mustPull(t, d, clueServer(t, 3, docs[2:3]), store.TypeClueDocuments, 612)
	if got := queryStrings(t, d, "SELECT doc_id, uid FROM clue_documents ORDER BY doc_id"); len(got) != 3 || !strings.HasPrefix(got[0], "-Nd1=") {
		t.Fatalf("clue_documents %v, want -Nd1 kept because run 584 still holds it", got)
	}
	if got := queryStrings(t, d, "SELECT doc_id, uid FROM clue_documents_612"); len(got) != 1 || !strings.HasPrefix(got[0], "-Nd3=") {
		t.Fatalf("clue_documents_612 %v, want only -Nd3", got)
	}
}

func TestClueGroupDocumentWithNoOfferingIsKept(t *testing.T) {
	d := newTestDataset(t)
	mustPull(t, d, clueServer(t, 3, fixture(t, "documents.json")), store.TypeClueDocuments, 584)
	got := queryStrings(t, d, "SELECT doc_id, owner_type || '/' || coalesce(offering_id::VARCHAR, 'NULL') FROM clue_documents ORDER BY doc_id")
	want := "-Nd1=user/701,-Nd2=group/NULL,-Nd3=class/NULL,-Nd4=user/701"
	if strings.Join(got, ",") != want {
		t.Fatalf("clue_documents %v, want %s", got, want)
	}
	if got := queryStrings(t, d, "SELECT typeof(offering_id), typeof(owner_type) FROM clue_documents LIMIT 1"); len(got) != 1 || got[0] != "BIGINT=VARCHAR" {
		t.Fatalf("column types %v, want BIGINT and VARCHAR", got)
	}
}

func TestClueItemWithoutItsIdentityIsRefused(t *testing.T) {
	for name, bad := range map[string]map[string]any{
		"missing entry_id":  {"doc_id": "-Nd1", "entry_json": "{}"},
		"null doc_id":       {"doc_id": nil, "entry_id": "e1", "entry_json": "{}"},
		"numeric doc_id":    {"doc_id": 7, "entry_id": "e1", "entry_json": "{}"},
		"repeated entry_id": nil,
	} {
		d := newTestDataset(t)
		first := fixture(t, "history_page1.json")
		second := append(fixture(t, "history_page2.json"), bad)
		srv := clueServer(t, 3, first, second)
		if bad == nil {
			body := pageBody(t, fixture(t, "history_page2.json"))
			srv.pages[1] = strings.TrimSuffix(body, "]") + `,{"doc_id":"-Nd1","entry_id":"e9","entry_id":null,"entry_json":"{}"}]`
		}
		_, err := pullClue(t, d, srv, store.TypeClueHistory, 584)
		var cerr *output.CLIError
		if !errors.As(err, &cerr) || cerr.Code != "MALFORMED_ITEM" || cerr.ExitCode != output.ExitContract {
			t.Fatalf("%s: err = %v, want MALFORMED_ITEM", name, err)
		}
		if !strings.Contains(cerr.Message, "item 3 of page 2") {
			t.Fatalf("%s: message %q does not locate the item", name, cerr.Message)
		}
		if name == "missing entry_id" && cerr.Message != "run 584 clue_history: item 3 of page 2: entry_id is missing" {
			t.Fatalf("message %q", cerr.Message)
		}
		seg := store.OpenSegment(d.Dir, store.TypeClueHistory, 584)
		if cur, ok, _ := seg.ReadCursor(); !ok || cur.Pages != 1 {
			t.Fatalf("%s: cursor %+v, want it left on page 1", name, cur)
		}
		if b, _ := os.ReadFile(seg.Path()); strings.Count(string(b), "\n") != len(first) {
			t.Fatalf("%s: segment holds more than page 1", name)
		}
	}
}

func coverageOf(t *testing.T, res map[string]any) *dataset.Coverage {
	t.Helper()
	cov, _ := res["coverage"].(*dataset.Coverage)
	return cov
}

func TestClueCoverageCountsOwners(t *testing.T) {
	d := newTestDataset(t)
	cov := coverageOf(t, mustPull(t, d, clueServer(t, 5, fixture(t, "documents.json")), store.TypeClueDocuments, 584))
	if cov == nil || *cov.Queried != 5 || cov.WithData != 3 || *cov.Empty != 2 {
		t.Fatalf("documents coverage %+v, want 3 owners of 4 documents, of 5", cov)
	}
	cov = coverageOf(t, mustPull(t, d, clueServer(t, 5, fixture(t, "content.json")), store.TypeClueContent, 584))
	if cov == nil || cov.WithData != 3 {
		t.Fatalf("content coverage %+v, want 3 owners", cov)
	}
	res := mustPull(t, d, clueServer(t, 5, historyPages(t)...), store.TypeClueHistory, 584)
	if cov := coverageOf(t, res); cov == nil || cov.WithData != 2 || *cov.Empty != 3 {
		t.Fatalf("history coverage %+v, want the 2 owners of -Nd1, -Nd2 and -Nd4", cov)
	}
	if _, ok := res["coverage_note"]; ok {
		t.Fatal("history with its documents fetched needs no note")
	}
}

func TestClueHistoryCoverageNeedsTheDocumentsFirst(t *testing.T) {
	d := newTestDataset(t)
	res := mustPull(t, d, clueServer(t, 5, historyPages(t)...), store.TypeClueHistory, 584)
	if cov := coverageOf(t, res); cov != nil {
		t.Fatalf("history coverage %+v with no documents fetched, want none", cov)
	}
	if note, _ := res["coverage_note"].(string); !strings.Contains(note, "fetch clue-documents for run 584 before clue-history") {
		t.Fatalf("coverage_note = %q", note)
	}
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dl := range m.Downloads {
		if dl.Type == store.TypeClueHistory {
			found = true
			if dl.Coverage != nil {
				t.Fatalf("manifest coverage %+v, want none", dl.Coverage)
			}
		}
	}
	if !found {
		t.Fatal("no clue_history download in the manifest")
	}

	d2 := newTestDataset(t)
	mustPull(t, d2, clueServer(t, 5, fixture(t, "documents.json")[:1]), store.TypeClueDocuments, 584)
	res = mustPull(t, d2, clueServer(t, 5, historyPages(t)...), store.TypeClueHistory, 584)
	if cov := coverageOf(t, res); cov != nil || res["coverage_note"] == nil {
		t.Fatalf("a history document missing from clue_documents must leave coverage out: %+v", cov)
	}
}

func TestClueHistoryWithNoEntriesCountsNoOwners(t *testing.T) {
	d := newTestDataset(t)
	res := mustPull(t, d, clueServer(t, 5, []map[string]any{}), store.TypeClueHistory, 584)
	if cov := coverageOf(t, res); cov == nil || cov.WithData != 0 || *cov.Empty != 5 {
		t.Fatalf("coverage %+v, want 0 of 5 owners", cov)
	}
	if _, ok := res["coverage_note"]; ok {
		t.Fatal("a run with no history needs no documents fetched first")
	}
}

func TestClueCoverageWalksUnderTheDatasetLock(t *testing.T) {
	d := newTestDataset(t)
	ran := false
	coverageWalkHook = func() {
		ran = true
		done := make(chan bool)
		go func() {
			got, _ := d.Lock().TryLock()
			if got {
				d.Lock().Unlock()
			}
			done <- got
		}()
		if <-done {
			t.Error("the dataset lock was free during the coverage walk")
		}
	}
	t.Cleanup(func() { coverageWalkHook = func() {} })
	mustPull(t, d, clueServer(t, 3, fixture(t, "documents.json")), store.TypeClueDocuments, 584)
	if !ran {
		t.Fatal("the coverage walk never ran")
	}
}
