package duck

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

var clueKinds = []string{store.TypeClueDocuments, store.TypeClueHistory, store.TypeClueContent}

// clueColumns is each CLUE view's expected schema: its own route's fields and the stamps.
var clueColumns = map[string]map[string]string{
	store.TypeClueDocuments: {
		"doc_id": "VARCHAR", "uid": "VARCHAR", "context_id": "VARCHAR", "portal_class_id": "BIGINT",
		"owner_kind": "VARCHAR", "type": "VARCHAR", "visibility": "VARCHAR", "title": "VARCHAR",
		"unit": "VARCHAR", "investigation": "VARCHAR", "problem": "VARCHAR", "group_id": "VARCHAR",
		"network": "VARCHAR", "offering_id": "BIGINT", "tools": "JSON", "created_at": "JSON",
		"metadata_sources": "JSON", "firestore_metadata": "JSON", "rtdb_metadata": "JSON",
		"_fetched_at": "TIMESTAMP", "_run_id": "BIGINT",
	},
	store.TypeClueHistory: {
		"doc_id": "VARCHAR", "entry_id": "VARCHAR", "idx": "BIGINT", "prev_entry_id": "VARCHAR",
		"server_created": "TIMESTAMP", "created": "TIMESTAMP", "model": "VARCHAR", "action_raw": "VARCHAR",
		"undoable": "JSON", "state": "VARCHAR", "n_records": "BIGINT", "is_revert": "BOOLEAN",
		"entry_uid": "VARCHAR", "parse_ok": "BOOLEAN", "entry_json": "VARCHAR",
		"_fetched_at": "TIMESTAMP", "_run_id": "BIGINT",
	},
	store.TypeClueContent: {
		"doc_id": "VARCHAR", "uid": "VARCHAR", "context_id": "VARCHAR", "found": "BOOLEAN",
		"type": "VARCHAR", "version": "VARCHAR", "change_count": "JSON", "last_history_entry_id": "VARCHAR",
		"self": "JSON", "content_json": "VARCHAR",
		"_fetched_at": "TIMESTAMP", "_run_id": "BIGINT",
	},
}

func land(t *testing.T, d *dataset.Dataset, typ string, run int, recs ...map[string]any) {
	t.Helper()
	var raw [][]byte
	for _, r := range recs {
		b, _ := json.Marshal(r)
		raw = append(raw, b)
	}
	landRaw(t, d, typ, run, raw)
}

func TestClueViewsOnADatasetWithNoClueData(t *testing.T) {
	fresh := openEngine(t, []DatasetSpec{{DS: newDS(t, "clue")}}, nil)
	if n := queryInt(t, fresh, "SELECT count(doc_id) + count(entry_id) FROM run_membership"); n != 0 {
		t.Fatalf("an empty run_membership returned %d CLUE ids", n)
	}
	fresh.Close()

	d := newDS(t, "clue")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, typ := range clueKinds {
		if got, want := columnTypes(t, e, typ), clueColumns[typ]; !maps.Equal(got, want) {
			t.Fatalf("%s columns %v, want exactly its own route's %v", typ, got, want)
		}
		if n := queryInt(t, e, "SELECT count(*) FROM "+typ); n != 0 {
			t.Fatalf("%s has %d rows", typ, n)
		}
	}
	if n := queryInt(t, e, "SELECT count(doc_id) FROM run_membership"); n != 0 {
		t.Fatalf("answers membership rows carry %d doc_ids", n)
	}
}

func TestClueStoresViewsAndMembership(t *testing.T) {
	d := newDS(t, "clue")
	buildStore(t, d, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	land(t, d, store.TypeClueDocuments, 700,
		map[string]any{"doc_id": "d1", "uid": "1234", "context_id": "c", "owner_kind": "student", "created_at": 1700000000001, "offering_id": nil},
		map[string]any{"doc_id": "d2", "uid": "group_701_a", "context_id": "c", "owner_kind": "group", "created_at": 1700000000000, "offering_id": nil})
	land(t, d, store.TypeClueHistory, 700,
		map[string]any{"doc_id": "d1", "entry_id": "e1", "idx": 0, "entry_json": `{"a":1}`},
		map[string]any{"doc_id": "d1", "entry_id": "e2", "idx": 1, "entry_json": `{"a":2}`})
	land(t, d, store.TypeClueHistory, 701,
		map[string]any{"doc_id": "d2", "entry_id": "e9", "idx": 0, "entry_json": `{}`})
	land(t, d, store.TypeClueContent, 700,
		map[string]any{"doc_id": "d1", "uid": "1234", "context_id": "c", "found": true, "content_json": `{"tiles":[]}`, "change_count": 3})
	land(t, d, store.TypeHistory, 584,
		map[string]any{"source_key": "s", "remote_endpoint": "e1", "question_id": "q1", "history_id": "h1"},
		map[string]any{"source_key": "s", "remote_endpoint": "e1", "question_id": "q1", "history_id": "h2"})

	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	types := columnTypes(t, e, store.TypeClueDocuments)
	if types["created_at"] != "JSON" || types["offering_id"] != "BIGINT" {
		t.Fatalf("created_at %s, offering_id %s: pins must hold over what the values would sniff as", types["created_at"], types["offering_id"])
	}
	if got := columnTypes(t, e, store.TypeClueContent)["change_count"]; got != "JSON" {
		t.Fatalf("change_count %s: an integer must not sniff past its JSON pin", got)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_history_700"); n != 2 {
		t.Fatalf("clue_history_700 has %d rows, want run 700's 2 entries", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_history_701"); n != 1 {
		t.Fatalf("clue_history_701 has %d rows, want only run 701's entry", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_history_701 WHERE entry_id = 'e9'"); n != 1 {
		t.Fatal("clue_history_701 is missing its entry")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM answers_584"); n != 1 {
		t.Fatalf("answers_584 has %d rows", n)
	}
	if got, want := columnTypes(t, e, "answers_584"), columnTypes(t, e, "answers"); !maps.Equal(got, want) {
		t.Fatalf("answers_584 columns %v, want answers' %v", got, want)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_membership WHERE type = 'answers' AND doc_id IS NULL AND entry_id IS NULL AND question_id = 'q1'"); n != 1 {
		t.Fatal("an answers membership row must carry its own columns and NULL CLUE ones")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_membership WHERE type = 'history' AND doc_id IS NULL AND entry_id IS NULL AND source_key IS NOT NULL"); n != 2 {
		t.Fatalf("history membership rows %d, want 2 with NULL CLUE columns", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM history_584"); n != 2 {
		t.Fatalf("history_584 has %d rows, want both", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_membership WHERE type = 'clue_history' AND source_key IS NULL AND remote_endpoint IS NULL AND question_id IS NULL AND history_id IS NULL AND doc_id IS NOT NULL AND entry_id IS NOT NULL"); n != 3 {
		t.Fatalf("clue_history membership rows %d, want 3 with NULL LARA columns", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_content c JOIN run_membership m USING (doc_id) WHERE m.run_id = 700 AND m.type = 'clue_content'"); n != 1 {
		t.Fatal("clue_content does not join its membership on doc_id")
	}
	e.Close()

	if err := d.Reindex(); err != nil {
		t.Fatal(err)
	}
	show, err := ShowJSON(d, false)
	if err != nil {
		t.Fatal(err)
	}
	for typ, want := range map[string]int{store.TypeClueDocuments: 2, store.TypeClueHistory: 3, store.TypeClueContent: 1, store.TypeAnswers: 1, store.TypeHistory: 2} {
		if show.Totals[typ] != want {
			t.Fatalf("after reindex %s total %d, want %d", typ, show.Totals[typ], want)
		}
	}
	m, _ := d.ReadManifest()
	if len(m.Membership) != 6 {
		t.Fatalf("reindexed membership %v", m.Membership)
	}

	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	before := viewRows(t, e, store.TypeClueHistory)
	if len(before) != 3 {
		t.Fatalf("clue_history has %d rows before materializing, want 3", len(before))
	}
	beforeTypes := columnTypes(t, e, store.TypeClueHistory)
	for _, typ := range clueKinds {
		if got := columnTypes(t, e, typ); !maps.Equal(got, clueColumns[typ]) {
			t.Fatalf("%s columns after reindex %v, want %v", typ, got, clueColumns[typ])
		}
	}
	e.Close()
	materialize(t, d, MaterializeOptions{})
	m, _ = d.ReadManifest()
	mat, ok := m.Materialized[store.TypeClueHistory]
	if !ok {
		t.Fatalf("clue_history was not materialized: %v", m.Materialized)
	}
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	parquet := "(SELECT * FROM read_parquet('" + d.Path(mat.File) + "'))"
	if got := viewRows(t, e, parquet); !slices.Equal(got, before) {
		t.Fatalf("Parquet rows %v, want the view's %v", got, before)
	}
	if got := columnTypes(t, e, parquet); !maps.Equal(got, beforeTypes) {
		t.Fatalf("Parquet columns %v, want %v", got, beforeTypes)
	}
}
