package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/creds"
	"github.com/zalando/go-keyring"
)

func TestTheTwoHistoryCommandsNameEachOther(t *testing.T) {
	clue := newGetClueHistoryCmd().Long
	if !strings.Contains(clue, "CLUE document history") || !strings.Contains(clue, "cc-data get history") {
		t.Fatalf("clue-history help does not set itself apart from get history:\n%s", clue)
	}
	if lara := newGetHistoryCmd().Long; !strings.Contains(lara, "cc-data get clue-history") {
		t.Fatalf("history help does not name get clue-history:\n%s", lara)
	}
	if docs := newGetClueDocumentsCmd().Long; !strings.Contains(docs, "before clue-history") {
		t.Fatalf("clue-documents help does not say it goes first:\n%s", docs)
	}
}

// clueRoutes maps each clue verb to its route and store type, with one page valid for it.
var clueRoutes = []struct {
	verb, route, typ, items string
}{
	{"clue-documents", "documents", "clue_documents", `[{"doc_id":"-Nd1","uid":"1234","context_id":"c"},{"doc_id":"-Nd2","uid":"1234","context_id":"c"}]`},
	{"clue-history", "history", "clue_history", `[{"doc_id":"-Nd1","entry_id":"e1","idx":0,"entry_json":"{}"},{"doc_id":"-Nd1","entry_id":"e2","idx":1,"entry_json":"{}"}]`},
	{"clue-content", "content", "clue_content", `[{"doc_id":"-Nd1","uid":"1234","context_id":"c","found":true},{"doc_id":"-Nd2","uid":"1234","context_id":"c","found":false}]`},
}

func TestGetClueCommandsEmitTheirResultLines(t *testing.T) {
	for _, rt := range clueRoutes {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if runtime.GOOS == "windows" {
			t.Setenv("USERPROFILE", home)
		}
		t.Setenv("CC_DATA_ROOT", t.TempDir())
		keyring.MockInit()
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			fmt.Fprintf(w, `{"items":%s,"next_page_token":null,"total_endpoints":4}`, rt.items)
		}))
		if err := (creds.Store{}).Save(config.MustPortal("learn.concord.org"), "test-token", srv.URL); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := runArgs(t, "dataset", "create", "learn.concord.org/ds"); code != 0 {
			t.Fatalf("dataset create exit %d: %s", code, stderr)
		}
		out, stderr, code := runArgs(t, "get", rt.verb, "584", "--dataset", "learn.concord.org/ds")
		srv.Close()
		if code != 0 {
			t.Fatalf("%s exit %d: %s", rt.verb, code, stderr)
		}
		var res struct {
			Type        string `json:"type"`
			RunID       int    `json:"run_id"`
			Complete    bool   `json:"complete"`
			MergeCounts struct {
				Fetched, New int
			} `json:"merge_counts"`
			Coverage     json.RawMessage `json:"coverage"`
			CoverageNote string          `json:"coverage_note"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
			t.Fatalf("%s result line %q: %v", rt.verb, out, err)
		}
		if res.Type != rt.typ || res.RunID != 584 || !res.Complete || res.MergeCounts.Fetched != 2 || res.MergeCounts.New != 2 {
			t.Fatalf("%s result %+v", rt.verb, res)
		}
		if len(paths) != 1 || paths[0] != "/api/v1/reports/584/clue/"+rt.route {
			t.Fatalf("%s requested %v", rt.verb, paths)
		}
		if rt.typ == "clue_history" {
			if string(res.Coverage) != "null" || !strings.Contains(res.CoverageNote, "fetch clue-documents for run 584") {
				t.Fatalf("history coverage %s, note %q", res.Coverage, res.CoverageNote)
			}
		} else if !strings.Contains(string(res.Coverage), `"with_data":1`) {
			t.Fatalf("%s coverage %s, want one owner", rt.verb, res.Coverage)
		}
	}
}
