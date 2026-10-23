package duck

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

var clueKinds = []string{store.TypeClueDocuments, store.TypeClueHistory, store.TypeClueContent}

// clueColumns is each CLUE store's schema: its own route's fields and the stamps.
var clueColumns = map[string]map[string]string{
	store.TypeClueDocuments: {
		"doc_id": "VARCHAR", "uid": "VARCHAR", "context_id": "VARCHAR", "portal_class_id": "BIGINT",
		"owner_type": "VARCHAR", "type": "VARCHAR", "visibility": "VARCHAR", "title": "VARCHAR",
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

// clueDerived is what each CLUE view changes of its store's schema: columns it retypes and
// columns it adds.
var clueDerived = map[string]map[string]string{
	store.TypeClueDocuments: {
		"tools": "VARCHAR[]", "n_tools": "BIGINT", "tools_unparsed": "JSON",
		"created_at": "TIMESTAMP", "created_at_unparsed": "JSON",
	},
	store.TypeClueHistory: {
		"entry_json": "JSON", "entry_json_unparsed": "VARCHAR", "undoable": "BOOLEAN", "undoable_unparsed": "JSON",
		"action": "VARCHAR", "tile_id": "VARCHAR", "shared_model_id": "VARCHAR", "doc_uid": "VARCHAR",
		"portal_class_id": "BIGINT", "unit": "VARCHAR", "investigation": "VARCHAR", "problem": "VARCHAR", "offering_id": "BIGINT",
	},
	store.TypeClueContent: {
		"content_json": "JSON", "content_json_unparsed": "VARCHAR", "change_count": "BIGINT", "change_count_unparsed": "JSON",
		"parse_ok": "BOOLEAN", "n_tiles": "BIGINT", "tile_types": "VARCHAR[]", "tile_type_counts": "JSON", "n_dataflow_tiles": "BIGINT",
		"n_rows": "BIGINT", "n_shared_models": "BIGINT", "n_annotations": "BIGINT",
		"self_uid": "VARCHAR", "self_doc_key": "VARCHAR", "self_class_hash": "VARCHAR",
		"portal_class_id": "BIGINT", "unit": "VARCHAR", "investigation": "VARCHAR", "problem": "VARCHAR", "offering_id": "BIGINT",
	},
}

// clueViewColumns is each CLUE view's expected schema over a store with only the pinned columns.
func clueViewColumns(typ string) map[string]string {
	cols := maps.Clone(clueColumns[typ])
	maps.Copy(cols, clueDerived[typ])
	return cols
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
		if got, want := columnTypes(t, e, typ), clueViewColumns(typ); !maps.Equal(got, want) {
			t.Fatalf("%s columns %v, want its store's columns plus its derived ones %v", typ, got, want)
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
		map[string]any{"doc_id": "d1", "uid": "1234", "context_id": "c", "owner_type": "user", "created_at": 1700000000001, "offering_id": nil},
		map[string]any{"doc_id": "d2", "uid": "group_701_a", "context_id": "c", "owner_type": "group", "created_at": 1700000000000, "offering_id": nil})
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

	stores := mustManifest(t, d).Stores
	if docs := stores[store.TypeClueDocuments].Columns; docs["created_at"] != "JSON" || docs["offering_id"] != "BIGINT" {
		t.Fatalf("created_at %s, offering_id %s: pins must hold over what the values would sniff as", docs["created_at"], docs["offering_id"])
	}
	if got := stores[store.TypeClueContent].Columns["change_count"]; got != "JSON" {
		t.Fatalf("change_count %s: an integer must not sniff past its JSON pin", got)
	}
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
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
		if got := columnTypes(t, e, typ); !maps.Equal(got, clueViewColumns(typ)) {
			t.Fatalf("%s columns after reindex %v, want %v", typ, got, clueViewColumns(typ))
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

// cluePatch is one JSON Patch operation. hasValue false leaves value out, as a remove has it.
type cluePatch struct {
	op, path string
	value    any
	hasValue bool
}

func (p cluePatch) MarshalJSON() ([]byte, error) {
	m := map[string]any{"op": p.op, "path": p.path}
	if p.hasValue {
		m["value"] = p.value
	}
	return json.Marshal(m)
}

type clueRecord struct {
	Tree    string      `json:"tree"`
	Action  string      `json:"action"`
	Patches []cluePatch `json:"patches"`
}

// clueEntry is one history row. raw, when set, is the stored entry_json verbatim, for the
// payloads a struct cannot produce; parseable says whether DuckDB parses it.
type clueEntry struct {
	doc, id         string
	idx             any
	created, server any
	action          any
	undoable        any
	isRevert        bool
	parseOK         bool
	records         []clueRecord
	raw             string
	parseable       bool
}

func (en clueEntry) entryJSON() string {
	if en.raw != "" {
		return en.raw
	}
	b, _ := json.Marshal(map[string]any{"records": en.records})
	return string(b)
}

// clueDoc is one documents-store row; every field it sets is its own, so a wrong join shows.
type clueDoc struct {
	id, uid                      string
	class, offering              int
	unit, investigation, problem string
	tools, createdAt             any
}

var clueFixtureDocs = []clueDoc{
	{"dA", "101", 11, 21, "uA", "iA", "pA", []string{"Text", "Dataflow"}, 1700000000123},
	{"dB", "102", 12, 22, "uB", "iB", "pB", nil, "1700000000123"},
	{"dC", "103", 13, 23, "uC", "iC", "pC", map[string]any{"a": 1}, 1700000000123.5},
	{"dD", "104", 14, 24, "uD", "iD", "pD", []string{}, map[string]any{"ms": 1}},
}

const (
	clueT0 = "2025-09-01T12:00:00.000Z"
	clueT1 = "2025-09-01T12:00:05.000Z"
	clueT2 = "2025-09-01T12:00:09.000Z"
)

func set(path string, v any) cluePatch { return cluePatch{"replace", path, v, true} }

// clueFixtureEntries is the synthetic history: entry ids are chosen so the store's identity-key order is
// not (doc_id, idx, entry_id), dA's a-9 and a-02 tie on created and on idx,
// a-9's shorter id stores it first while the key orders it last, and a-00 has neither.
var clueFixtureEntries = []clueEntry{
	{doc: "dA", id: "a-03", idx: 0, created: clueT0, server: clueT0, action: "/content/tileMap/T1/content/setText", undoable: true, parseOK: true, parseable: true,
		records: []clueRecord{
			{"dA", "/content/tileMap/T1/content/setText", []cluePatch{set("/content/tileMap/T1/content/text", "hi"), {"add", "/content/tileMap/T1/content/x", 1, true}}},
			{"dA", "/content/tileMap/T1/content/setText", []cluePatch{{"remove", "/content/tileMap/T2", nil, false}}},
		}},
	{doc: "dA", id: "a-9", idx: 1, created: clueT1, server: clueT1, action: "/content/sharedModelMap/S1/sharedModel/setX", undoable: "yes", parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/content/sharedModelMap/S1/sharedModel/setX", []cluePatch{set("/content/sharedModelMap/S1/x", nil)}}}},
	{doc: "dA", id: "a-02", idx: 1, created: clueT1, server: clueT2, action: "/content/tileMap/T3/content/setProgram", undoable: nil, parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/content/tileMap/T3/content/setProgram", []cluePatch{set("/content/tileMap/T3/content/program", map[string]any{"n": 1}), set("/content/tileMap/T3/content/program/values/x", 5)}}}},
	{doc: "dA", id: "a-04", idx: 2, created: clueT2, server: clueT2, action: "/content/tileMap/T7/content/setTitle", undoable: true, parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/content/tileMap/T7/content/setTitle", []cluePatch{set("/content/title", "t")}}}},
	{doc: "dA", id: "a-00", idx: nil, created: nil, server: clueT2, action: "/addTile", undoable: false, isRevert: true, parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/addTile", []cluePatch{{"add", "/content/tileMap/T4", map[string]any{"id": "T4"}, true}}}}},
	{doc: "dB", id: "b-1", idx: 0, created: clueT0, server: clueT0, action: "/content/rowMap/R1/setValue", undoable: 1, parseOK: false, raw: `{"records":[{"patches":[{"op":"add",`},
	{doc: "dB", id: "b-2", idx: 1, created: clueT1, server: clueT1, action: "", parseOK: true, parseable: true, raw: `{"records":[]}`},
	{doc: "dB", id: "b-3", idx: 2, created: clueT1, server: clueT1, action: nil, parseOK: true, parseable: true, raw: `{"records":[{"tree":"dB","action":"x"}]}`},
	{doc: "dB", id: "b-4", idx: 3, created: clueT2, server: clueT2, action: "/content/tileMap/A/content/tileMap/B/x", parseOK: true, parseable: true, raw: `{"records":{"patches":[{"op":"add","path":"/p","value":1}]}}`},
	{doc: "dB", id: "b-5", idx: 4, created: clueT2, server: clueT2, action: "/content/tileMap/T9/content/setText", parseOK: true, raw: `{"records":[{"patches":[{"op":"add","path":"/t","value":"\ud83d"}]}]}`},
	{doc: "dX", id: "x-1", idx: 0, created: clueT0, server: clueT0, action: "/content/rowMap/R9/setValue", undoable: true, parseOK: true, parseable: true,
		records: []clueRecord{{"dX", "/content/rowMap/R9/setValue", []cluePatch{{"add", "/content/rowMap/R9", 1, true}}}}},
}

// clueContentRow is one content-store row and the tile summary its content should give.
type clueContentRow struct {
	doc         string
	content     any
	changeCount any
	self        any
	summary     []string // n_tiles, tile_types, tile_type_counts, n_dataflow_tiles, n_rows, n_shared_models, n_annotations
}

var clueFixtureContent = []clueContentRow{
	{"dA", `{"tileMap":{"T1":{"content":{"type":"Text"}},"T2":{"content":{"type":"Dataflow"}},"T3":{"content":{"type":"Dataflow"}},"T4":{"content":{"type":5}},"T5":{},"T6":{"content":{"type":"Geometry"}}},"rowMap":{"r1":{},"r2":{}},"sharedModelMap":{"s1":{}},"annotations":{"a1":{},"a2":{},"a3":{}}}`,
		12, map[string]any{"uid": "101", "documentKey": "dA", "classHash": "hA"},
		[]string{"6", "[Dataflow, Geometry, Text]", `{"Dataflow":2,"Geometry":1,"Text":1}`, "2", "2", "1", "3"}},
	{"dB", `{}`, "12", nil, []string{"0", "[]", "{}", "0", "0", "0", "0"}},
	{"dC", `{"tileMap":`, 12.5, map[string]any{"uid": "103"}, nil},
	{"dD", nil, nil, nil, nil},
	{"dX", `{"tileMap":{"Z":{"content":{}}}}`, map[string]any{"n": 1}, nil, []string{"1", "[]", "{}", "0", "0", "0", "0"}},
}

// clueWidened is the column the fixture adds to each store beyond the pins, as a field the
// export adds later would be.
var clueWidened = map[string]string{
	store.TypeClueDocuments: "w_doc",
	store.TypeClueHistory:   "w_hist",
	store.TypeClueContent:   "j",
}

// clueFixture lands the three CLUE stores through the store's own merge, so each store's manifest
// Columns are the pins plus the widened column.
func clueFixture(t *testing.T) *dataset.Dataset {
	t.Helper()
	d := newDS(t, "clue")
	var docs []map[string]any
	for _, doc := range clueFixtureDocs {
		docs = append(docs, map[string]any{
			"doc_id": doc.id, "uid": doc.uid, "context_id": "c", "portal_class_id": doc.class, "owner_type": "user",
			"unit": doc.unit, "investigation": doc.investigation, "problem": doc.problem, "offering_id": doc.offering,
			"tools": doc.tools, "created_at": doc.createdAt, clueWidened[store.TypeClueDocuments]: "w-" + doc.id,
		})
	}
	land(t, d, store.TypeClueDocuments, 700, docs...)
	var entries []map[string]any
	for _, en := range clueFixtureEntries {
		entries = append(entries, map[string]any{
			"doc_id": en.doc, "entry_id": en.id, "idx": en.idx, "created": en.created, "server_created": en.server,
			"action_raw": en.action, "undoable": en.undoable, "is_revert": en.isRevert, "parse_ok": en.parseOK,
			"entry_json": en.entryJSON(), clueWidened[store.TypeClueHistory]: "w-" + en.id,
		})
	}
	land(t, d, store.TypeClueHistory, 700, entries...)
	var content []map[string]any
	for _, c := range clueFixtureContent {
		content = append(content, map[string]any{
			"doc_id": c.doc, "found": c.content != nil, "content_json": c.content, "change_count": c.changeCount,
			"self": c.self, clueWidened[store.TypeClueContent]: "w-" + c.doc,
		})
	}
	land(t, d, store.TypeClueContent, 700, content...)
	return d
}

// queryRow runs a query that must return exactly one row and gives each column as a string.
func queryRow(t *testing.T, e *Engine, sql string) []string {
	t.Helper()
	rows, err := e.Query(context.Background(), sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	n := 0
	for rows.Next() {
		n++
		cells := make([]*string, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = out[:0]
		for _, c := range cells {
			if c == nil {
				out = append(out, "<NULL>")
			} else {
				out = append(out, *c)
			}
		}
	}
	if n != 1 {
		t.Fatalf("query %q returned %d rows, want 1", sql, n)
	}
	return out
}

func TestClueDocumentsTypesTools(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for doc, want := range map[string][]string{
		"dA": {"[Text, Dataflow]", "2", "<NULL>"},
		"dB": {"<NULL>", "<NULL>", "<NULL>"},
		"dC": {"<NULL>", "<NULL>", `{"a":1}`},
		"dD": {"[]", "0", "<NULL>"},
	} {
		got := queryRow(t, e, "SELECT tools::VARCHAR, n_tools::VARCHAR, tools_unparsed::VARCHAR FROM clue_documents WHERE doc_id = '"+doc+"'")
		if !slices.Equal(got, want) {
			t.Errorf("%s tools, n_tools, tools_unparsed = %v, want %v", doc, got, want)
		}
	}
}

func TestClueDocumentsCreatedAt(t *testing.T) {
	d := clueFixture(t)
	land(t, d, store.TypeClueDocuments, 701, map[string]any{"doc_id": "dN", "uid": "105", "created_at": nil})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for doc, want := range map[string][]string{
		"dA": {"2023-11-14 22:13:20.123", "<NULL>"},
		"dB": {"<NULL>", `"1700000000123"`},
		"dC": {"2023-11-14 22:13:20.1235", "<NULL>"},
		"dD": {"<NULL>", `{"ms":1}`},
		"dN": {"<NULL>", "<NULL>"},
	} {
		got := queryRow(t, e, "SELECT created_at::VARCHAR, created_at_unparsed::VARCHAR FROM clue_documents WHERE doc_id = '"+doc+"'")
		if !slices.Equal(got, want) {
			t.Errorf("%s created_at, created_at_unparsed = %v, want %v", doc, got, want)
		}
	}
}

func TestClueDocumentsFallbackDeclaresEveryColumn(t *testing.T) {
	assertClueFallback(t, store.TypeClueDocuments)
}

// assertClueFallback checks a CLUE view on a dataset with no CLUE stores declares exactly the
// columns, at the types, the view has over the populated fixture, less the widened one.
func assertClueFallback(t *testing.T, view string) {
	t.Helper()
	populated := columnTypes(t, openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil), view)
	for typ, col := range clueWidened {
		if typ == view {
			delete(populated, col)
		}
	}
	empty := newDS(t, "empty")
	buildStore(t, empty, 584, [][]byte{answerRec("s", "e1", "q1", "hi")})
	got := columnTypes(t, openEngine(t, []DatasetSpec{{DS: empty}}, nil), view)
	if len(got) == 0 || !maps.Equal(got, populated) {
		t.Fatalf("%s on a dataset without its stores declares %v, want the populated view's %v", view, got, populated)
	}
}

// clueFallbackColumns describes a view's registered fallback over a dataset. The fallback runs
// only when the primary fails to install, which a JSONL file with explicit columns does not do,
// so its SQL is described directly.
func clueFallbackColumns(t *testing.T, e *Engine, d *dataset.Dataset, view string) map[string]string {
	t.Helper()
	vs := viewSet{canonDir: d.Path(""), m: mustManifest(t, d)}
	for _, st := range vs.statements() {
		if st.name == sqlIdent(view) {
			if strings.Contains(st.fallback, "read_json(") {
				t.Fatalf("%s's fallback reads a file:\n%s", view, st.fallback)
			}
			return columnTypes(t, e, "("+strings.TrimPrefix(st.fallback, "CREATE VIEW "+st.name+" AS ")+")")
		}
	}
	t.Fatalf("no view %s", view)
	return nil
}

func TestClueViewsPassWidenedColumns(t *testing.T) {
	d := clueFixture(t)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, typ := range clueKinds {
		cols := columnTypes(t, e, typ)
		if cols[clueWidened[typ]] != "VARCHAR" {
			t.Fatalf("%s columns %v lack the widened %s", typ, cols, clueWidened[typ])
		}
		if n := queryInt(t, e, "SELECT count(*) FROM "+typ+" WHERE "+sqlIdent(clueWidened[typ])+" LIKE 'w-%'"); n == 0 {
			t.Fatalf("%s carries no values in its widened column", typ)
		}
		if got := clueFallbackColumns(t, e, d, typ); !maps.Equal(got, cols) {
			t.Fatalf("%s fallback columns %v, want the primary's %v", typ, got, cols)
		}
	}
}

func TestClueHistoryNormalizesActions(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for entry, want := range map[string][]string{
		"a-03": {"/content/tileMap/T1/content/setText", "/content/tileMap/{tile}/content/setText", "T1", "<NULL>"},
		"a-9":  {"/content/sharedModelMap/S1/sharedModel/setX", "/content/sharedModelMap/{sharedModel}/sharedModel/setX", "<NULL>", "S1"},
		"b-1":  {"/content/rowMap/R1/setValue", "/content/rowMap/{row}/setValue", "<NULL>", "<NULL>"},
		"b-4":  {"/content/tileMap/A/content/tileMap/B/x", "/content/tileMap/{tile}/content/tileMap/B/x", "A", "<NULL>"},
		"a-00": {"/addTile", "/addTile", "<NULL>", "<NULL>"},
		"b-2":  {"", "<NULL>", "<NULL>", "<NULL>"},
		"b-3":  {"<NULL>", "<NULL>", "<NULL>", "<NULL>"},
	} {
		got := queryRow(t, e, "SELECT action_raw, action, tile_id, shared_model_id FROM clue_history WHERE entry_id = '"+entry+"'")
		if !slices.Equal(got, want) {
			t.Errorf("%s action_raw, action, tile_id, shared_model_id = %v, want %v", entry, got, want)
		}
	}
}

// clueDocColumnsQuery selects a row's joined document columns, in clueDocColumns order after
// doc_uid when the view has it.
func clueDocColumnsQuery(withUID bool) string {
	cols := "portal_class_id::VARCHAR, unit, investigation, problem, offering_id::VARCHAR"
	if withUID {
		cols = "doc_uid, " + cols
	}
	return cols
}

func clueDocWant(doc string, withUID bool) []string {
	var want []string
	for _, d := range clueFixtureDocs {
		if d.id == doc {
			want = []string{fmt.Sprint(d.class), d.unit, d.investigation, d.problem, fmt.Sprint(d.offering)}
			if withUID {
				want = append([]string{d.uid}, want...)
			}
			return want
		}
	}
	n := len(clueDocColumns)
	if withUID {
		n++
	}
	return slices.Repeat([]string{"<NULL>"}, n)
}

func TestClueHistoryCarriesItsDocumentsColumns(t *testing.T) {
	d := clueFixture(t)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, en := range clueFixtureEntries {
		got := queryRow(t, e, "SELECT "+clueDocColumnsQuery(true)+" FROM clue_history WHERE doc_id = '"+en.doc+"' AND entry_id = '"+en.id+"'")
		if want := clueDocWant(en.doc, true); !slices.Equal(got, want) {
			t.Errorf("%s/%s document columns %v, want %v", en.doc, en.id, got, want)
		}
	}
	want := mustManifest(t, d).Stores[store.TypeClueHistory].Count
	if n := queryInt(t, e, "SELECT count(*) FROM clue_history"); n != want || n != len(clueFixtureEntries) {
		t.Fatalf("clue_history has %d rows, want the store's %d", n, want)
	}
}

func TestClueHistoryWithoutADocumentsStore(t *testing.T) {
	check := func(t *testing.T, d *dataset.Dataset, wantWarning string) {
		t.Helper()
		e, warn := openWithWarnings(t, d)
		if n := queryInt(t, e, "SELECT count(*) FROM clue_history"); n != len(clueFixtureEntries) {
			t.Fatalf("clue_history has %d rows without its documents, want %d", n, len(clueFixtureEntries))
		}
		if n := queryInt(t, e, "SELECT count(*) FROM clue_history WHERE coalesce(doc_uid, unit, investigation, problem, portal_class_id::VARCHAR, offering_id::VARCHAR) IS NOT NULL"); n != 0 {
			t.Fatalf("%d clue_history rows carry document columns with no documents store", n)
		}
		if n := queryInt(t, e, "SELECT count(*) FROM clue_content"); n != len(clueFixtureContent) {
			t.Fatalf("clue_content has %d rows without its documents, want %d", n, len(clueFixtureContent))
		}
		if n := queryInt(t, e, "SELECT count(*) FROM clue_content WHERE coalesce(unit, investigation, problem, portal_class_id::VARCHAR, offering_id::VARCHAR) IS NOT NULL"); n != 0 {
			t.Fatalf("%d clue_content rows carry document columns with no documents store", n)
		}
		if got := strings.Count(warn, "clue_documents.v"); wantWarning != "" && got != 1 || wantWarning == "" && got != 0 {
			t.Fatalf("warnings name the documents file %d times: %q", got, warn)
		}
		if !strings.Contains(warn, wantWarning) {
			t.Fatalf("warnings %q do not name %q", warn, wantWarning)
		}
	}
	t.Run("absent from the manifest", func(t *testing.T) {
		d := clueFixture(t)
		if err := d.UpdateManifest(func(m *dataset.Manifest) error {
			delete(m.Stores, store.TypeClueDocuments)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		check(t, d, "")
	})
	t.Run("file missing on disk", func(t *testing.T) {
		d := clueFixture(t)
		file := mustManifest(t, d).Stores[store.TypeClueDocuments].File
		if err := os.Remove(d.Path(file)); err != nil {
			t.Fatal(err)
		}
		check(t, d, file)

		res, _ := materialize(t, d, MaterializeOptions{})
		for _, view := range []string{store.TypeClueDocuments, store.TypeClueHistory, store.TypeClueContent} {
			if r, ok := res.Refused()[view]; !ok || !strings.Contains(r, file) {
				t.Fatalf("%s outcome %q, want refused naming %s", view, outcomeReason(res, view), file)
			}
		}
		res, _ = materialize(t, d, MaterializeOptions{AllowPartial: true})
		for _, view := range []string{store.TypeClueHistory, store.TypeClueContent} {
			if r := outcomeReason(res, view); !slices.Contains(res.Written(), view) || !strings.Contains(r, "built from 1 of 2 declared inputs") || !strings.Contains(r, file) {
				t.Fatalf("%s with partial copies allowed: written %v, reason %q", view, res.Written(), r)
			}
		}
	})
}

func TestClueHistoryFallbackDeclaresEveryColumn(t *testing.T) {
	assertClueFallback(t, store.TypeClueHistory)
}

func TestClueHistoryPayloads(t *testing.T) {
	d := clueFixture(t)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, en := range clueFixtureEntries {
		got := queryRow(t, e, "SELECT entry_json::VARCHAR, entry_json_unparsed, parse_ok::VARCHAR FROM clue_history WHERE entry_id = '"+en.id+"'")
		want := []string{en.entryJSON(), "<NULL>", fmt.Sprint(en.parseOK)}
		if !en.parseable {
			want = []string{"<NULL>", en.entryJSON(), fmt.Sprint(en.parseOK)}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s entry_json, entry_json_unparsed, parse_ok = %v, want %v", en.id, got, want)
		}
	}
	if n := queryInt(t, e, "SELECT count(entry_json ->> '$.records') FROM clue_history"); n == 0 {
		t.Fatal("->> over entry_json read nothing")
	}
	for entry, want := range map[string][]string{
		"a-03": {"true", "<NULL>"},
		"a-00": {"false", "<NULL>"},
		"a-9":  {"<NULL>", `"yes"`},
		"b-1":  {"<NULL>", "1"},
		"a-02": {"<NULL>", "<NULL>"},
	} {
		if got := queryRow(t, e, "SELECT undoable::VARCHAR, undoable_unparsed::VARCHAR FROM clue_history WHERE entry_id = '"+entry+"'"); !slices.Equal(got, want) {
			t.Errorf("%s undoable, undoable_unparsed = %v, want %v", entry, got, want)
		}
	}
}

func TestClueViewsCaveatIncompleteStores(t *testing.T) {
	d := clueFixture(t)
	if err := d.UpdateManifest(func(m *dataset.Manifest) error {
		m.Downloads = append(m.Downloads,
			dataset.Download{Type: store.TypeClueDocuments, RunID: 801, Complete: false},
			dataset.Download{Type: store.TypeClueHistory, RunID: 802, Complete: false})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res, _ := materialize(t, d, MaterializeOptions{})
	for view, want := range map[string][]int{
		store.TypeClueDocuments: {801},
		store.TypeClueHistory:   {801, 802},
		store.TypeClueContent:   {801},
		"clue_patches":          {802},
	} {
		r := outcomeReason(res, view)
		for _, run := range []int{801, 802} {
			if named := strings.Contains(r, fmt.Sprint(run)); named != slices.Contains(want, run) {
				t.Errorf("%s outcome %q: names run %d %v, want %v", view, r, run, named, !named)
			}
		}
	}
}

func TestClueContentTileSummaries(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for _, c := range clueFixtureContent {
		want := c.summary
		if want == nil {
			want = slices.Repeat([]string{"<NULL>"}, 7)
		}
		got := queryRow(t, e, "SELECT n_tiles::VARCHAR, tile_types::VARCHAR, tile_type_counts::VARCHAR, n_dataflow_tiles::VARCHAR, n_rows::VARCHAR, n_shared_models::VARCHAR, n_annotations::VARCHAR FROM clue_content WHERE doc_id = '"+c.doc+"'")
		if !slices.Equal(got, want) {
			t.Errorf("%s tile summary %v, want %v", c.doc, got, want)
		}
	}
}

func TestClueContentCarriesItsDocumentsColumns(t *testing.T) {
	d := clueFixture(t)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, c := range clueFixtureContent {
		got := queryRow(t, e, "SELECT "+clueDocColumnsQuery(false)+" FROM clue_content WHERE doc_id = '"+c.doc+"'")
		if want := clueDocWant(c.doc, false); !slices.Equal(got, want) {
			t.Errorf("%s document columns %v, want %v", c.doc, got, want)
		}
	}
	want := mustManifest(t, d).Stores[store.TypeClueContent].Count
	if n := queryInt(t, e, "SELECT count(*) FROM clue_content"); n != want || n != len(clueFixtureContent) {
		t.Fatalf("clue_content has %d rows, want the store's %d", n, want)
	}
}

func TestClueContentSelfColumns(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for doc, want := range map[string][]string{
		"dA": {"101", "dA", "hA"},
		"dC": {"103", "<NULL>", "<NULL>"},
		"dB": {"<NULL>", "<NULL>", "<NULL>"},
	} {
		if got := queryRow(t, e, "SELECT self_uid, self_doc_key, self_class_hash FROM clue_content WHERE doc_id = '"+doc+"'"); !slices.Equal(got, want) {
			t.Errorf("%s self_uid, self_doc_key, self_class_hash = %v, want %v", doc, got, want)
		}
	}
}

func TestClueContentChangeCount(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for doc, want := range map[string][]string{
		"dA": {"12", "<NULL>"},
		"dB": {"<NULL>", `"12"`},
		"dC": {"<NULL>", "12.5"},
		"dX": {"<NULL>", `{"n":1}`},
		"dD": {"<NULL>", "<NULL>"},
	} {
		if got := queryRow(t, e, "SELECT change_count::VARCHAR, change_count_unparsed::VARCHAR FROM clue_content WHERE doc_id = '"+doc+"'"); !slices.Equal(got, want) {
			t.Errorf("%s change_count, change_count_unparsed = %v, want %v", doc, got, want)
		}
	}
}

func TestClueContentPayloads(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for _, c := range clueFixtureContent {
		want := []string{"<NULL>", "<NULL>", "<NULL>"}
		if stored, ok := c.content.(string); ok && c.summary != nil {
			want = []string{stored, "<NULL>", "true"}
		} else if ok {
			want = []string{"<NULL>", stored, "false"}
		}
		if got := queryRow(t, e, "SELECT content_json::VARCHAR, content_json_unparsed, parse_ok::VARCHAR FROM clue_content WHERE doc_id = '"+c.doc+"'"); !slices.Equal(got, want) {
			t.Errorf("%s content_json, content_json_unparsed, parse_ok = %v, want %v", c.doc, got, want)
		}
	}
}

func TestClueContentFallbackDeclaresEveryColumn(t *testing.T) {
	assertClueFallback(t, store.TypeClueContent)
}

// cluePatchRow is one patch the fixture's struct-built entries hold, with its position.
type cluePatchRow struct {
	entry          clueEntry
	recIdx, patIdx int
	patch          cluePatch
}

func (p cluePatchRow) key() string {
	return fmt.Sprintf("%s/%s/%d/%d", p.entry.doc, p.entry.id, p.recIdx, p.patIdx)
}

// clueFixturePatches lists every patch of the fixture's parseable entries, computed in Go,
// sorted by cluePatchKey with a NULL idx after every non-NULL one in its document, as DuckDB's
// default NULLS LAST orders it.
func clueFixturePatches() []cluePatchRow {
	var out []cluePatchRow
	for _, en := range clueFixtureEntries {
		if !en.parseable {
			continue
		}
		for ri, rec := range en.records {
			for pi, p := range rec.Patches {
				out = append(out, cluePatchRow{en, ri, pi, p})
			}
		}
	}
	idx := func(en clueEntry) (int, bool) {
		i, ok := en.idx.(int)
		return i, ok
	}
	slices.SortFunc(out, func(a, b cluePatchRow) int {
		if c := strings.Compare(a.entry.doc, b.entry.doc); c != 0 {
			return c
		}
		ai, aok := idx(a.entry)
		bi, bok := idx(b.entry)
		if aok != bok {
			if aok {
				return -1
			}
			return 1
		}
		if c := ai - bi; c != 0 {
			return c
		}
		if c := strings.Compare(a.entry.id, b.entry.id); c != 0 {
			return c
		}
		if c := a.recIdx - b.recIdx; c != 0 {
			return c
		}
		return a.patIdx - b.patIdx
	})
	return out
}

const cluePatchKeySQL = "doc_id || '/' || entry_id || '/' || rec_idx || '/' || patch_idx"

func TestCluePatchesCountsEveryPatch(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	want := clueFixturePatches()
	if len(want) == 0 {
		t.Fatal("the fixture holds no patches")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_patches"); n != len(want) {
		t.Fatalf("clue_patches has %d rows, want the fixture's %d patches", n, len(want))
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_patches WHERE doc_id = 'dB'"); n != 0 {
		t.Fatalf("dB's entries hold no explodable patches but gave %d rows", n)
	}
}

func TestCluePatchesKeyIsUnique(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	if n := queryInt(t, e, "SELECT count(*) - count(DISTINCT (doc_id, entry_id, rec_idx, patch_idx)) FROM clue_patches"); n != 0 {
		t.Fatalf("%d duplicate patch keys", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM (SELECT 1 FROM clue_patches GROUP BY "+cluePatchKey+" HAVING count(*) > 1)"); n != 0 {
		t.Fatalf("%d patch keys repeat under cluePatchKey", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM clue_patches p ANTI JOIN clue_history h USING (doc_id, entry_id)"); n != 0 {
		t.Fatalf("%d patches join no clue_history row", n)
	}
}

func TestCluePatchesOrderedByTheKey(t *testing.T) {
	d := clueFixture(t)
	want := clueFixturePatches()
	var keys, lags []string
	prev := map[string]string{}
	for _, p := range want {
		keys = append(keys, p.key())
		lag, ok := prev[p.entry.doc]
		if !ok {
			lag = "<NULL>"
		}
		lags = append(lags, p.key()+" "+lag)
		prev[p.entry.doc] = p.patch.path
	}
	for i := range 2 {
		e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
		if got := queryStrings(t, e, "SELECT "+cluePatchKeySQL+" FROM clue_patches ORDER BY "+cluePatchKey); !slices.Equal(got, keys) {
			t.Fatalf("open %d: patches ordered by the key %v, want %v", i, got, keys)
		}
		got := queryStrings(t, e, "SELECT "+cluePatchKeySQL+" || ' ' || coalesce(lag(path) OVER (PARTITION BY doc_id ORDER BY "+cluePatchKey+"), '<NULL>') FROM clue_patches ORDER BY "+cluePatchKey)
		if !slices.Equal(got, lags) {
			t.Fatalf("open %d: lag(path) over the key %v, want %v", i, got, lags)
		}
		e.Close()
	}
}

var tileInPath = regexp.MustCompile(`/tileMap/([^/]+)`)

func TestCluePatchesColumns(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil)
	for _, p := range clueFixturePatches() {
		tile := "<NULL>"
		if m := tileInPath.FindStringSubmatch(p.patch.path); m != nil {
			tile = m[1]
		} else if m := tileInPath.FindStringSubmatch(fmt.Sprint(p.entry.action)); m != nil {
			tile = m[1]
		}
		value := "set"
		switch {
		case !p.patch.hasValue:
			value = "absent"
		case p.patch.value == nil:
			value = "null"
		}
		got := queryRow(t, e, fmt.Sprintf(`SELECT coalesce(tile_id, '<NULL>'), op, path,
  CASE WHEN value IS NULL THEN 'absent' WHEN json_type(value) = 'NULL' THEN 'null' ELSE 'set' END,
  is_revert::VARCHAR
FROM clue_patches WHERE %s = '%s'`, cluePatchKeySQL, p.key()))
		if want := []string{tile, p.patch.op, p.patch.path, value, fmt.Sprint(p.entry.isRevert)}; !slices.Equal(got, want) {
			t.Errorf("%s tile_id, op, path, value, is_revert = %v, want %v", p.key(), got, want)
		}
	}
	if n := queryInt(t, e, `SELECT count(*) FROM clue_patches p JOIN clue_history h USING (doc_id, entry_id)
WHERE p.action IS DISTINCT FROM h.action OR p.created IS DISTINCT FROM h.created
  OR p.server_created IS DISTINCT FROM h.server_created OR p.idx IS DISTINCT FROM h.idx`); n != 0 {
		t.Fatalf("%d patches disagree with their entry's action, idx or clocks", n)
	}
}

var cluePatchesColumns = map[string]string{
	"doc_id": "VARCHAR", "entry_id": "VARCHAR", "idx": "BIGINT", "created": "TIMESTAMP", "server_created": "TIMESTAMP",
	"action": "VARCHAR", "tile_id": "VARCHAR", "is_revert": "BOOLEAN", "rec_idx": "BIGINT", "patch_idx": "BIGINT",
	"op": "VARCHAR", "path": "VARCHAR", "value": "JSON",
}

func TestCluePatchesFallbackDeclaresEveryColumn(t *testing.T) {
	if got := columnTypes(t, openEngine(t, []DatasetSpec{{DS: clueFixture(t)}}, nil), "clue_patches"); !maps.Equal(got, cluePatchesColumns) {
		t.Fatalf("clue_patches columns %v, want %v", got, cluePatchesColumns)
	}
	assertClueFallback(t, "clue_patches")
}

func pinnedViews(e *Engine) []string {
	var out []string
	for _, p := range e.pinned {
		out = append(out, strings.Trim(p.stmt.name, `"`))
	}
	return out
}

func TestClueViewsMaterialize(t *testing.T) {
	d := clueFixture(t)
	views := append(slices.Clone(clueKinds), "clue_patches")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	raw := map[string][]string{}
	for _, v := range views {
		raw[v] = viewRows(t, e, v)
		if len(raw[v]) == 0 {
			t.Fatalf("%s has no rows to compare", v)
		}
	}
	e.Close()

	res, _ := materialize(t, d, MaterializeOptions{})
	for _, v := range views {
		if !slices.Contains(res.Written(), v) {
			t.Fatalf("%s was not written: %+v", v, res.Views)
		}
	}
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	for _, v := range views {
		if !slices.Contains(pinnedViews(e), v) {
			t.Fatalf("%s is not read from its Parquet, pinned %v", v, pinnedViews(e))
		}
		if got := viewRows(t, e, v); !slices.Equal(got, raw[v]) {
			t.Fatalf("%s from its Parquet %v, want the raw view's %v", v, got, raw[v])
		}
	}
	e.Close()

	land(t, d, store.TypeClueDocuments, 702, map[string]any{"doc_id": "dE", "uid": "105"})
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	pinned := pinnedViews(e)
	for _, v := range []string{store.TypeClueDocuments, store.TypeClueHistory, store.TypeClueContent} {
		if slices.Contains(pinned, v) {
			t.Fatalf("%s still reads its Parquet after a documents fetch", v)
		}
	}
	if !slices.Contains(pinned, "clue_patches") {
		t.Fatalf("clue_patches went stale on a documents fetch, pinned %v", pinned)
	}
}
