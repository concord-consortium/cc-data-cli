package duck

import (
	"context"
	"encoding/json"
	"maps"
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
// not (doc_id, idx, entry_id), dA's a-01 and a-02 tie on created and on idx, and a-00 has neither.
var clueFixtureEntries = []clueEntry{
	{doc: "dA", id: "a-03", idx: 0, created: clueT0, server: clueT0, action: "/content/tileMap/T1/content/setText", undoable: true, parseOK: true, parseable: true,
		records: []clueRecord{
			{"dA", "/content/tileMap/T1/content/setText", []cluePatch{set("/content/tileMap/T1/content/text", "hi"), {"add", "/content/tileMap/T1/content/x", 1, true}}},
			{"dA", "/content/tileMap/T1/content/setText", []cluePatch{{"remove", "/content/tileMap/T2", nil, false}}},
		}},
	{doc: "dA", id: "a-01", idx: 1, created: clueT1, server: clueT1, action: "/content/sharedModelMap/S1/sharedModel/setX", undoable: "yes", parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/content/sharedModelMap/S1/sharedModel/setX", []cluePatch{set("/content/sharedModelMap/S1/x", nil)}}}},
	{doc: "dA", id: "a-02", idx: 1, created: clueT1, server: clueT2, action: "/content/tileMap/T3/content/setProgram", undoable: nil, parseOK: true, parseable: true,
		records: []clueRecord{{"dA", "/content/tileMap/T3/content/setProgram", []cluePatch{set("/content/tileMap/T3/content/program", map[string]any{"n": 1}), set("/content/tileMap/T3/content/program/values/x", 5)}}}},
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
	{"dA", `{"tileMap":{"T1":{"content":{"type":"Dataflow"}},"T2":{"content":{"type":"Dataflow"}},"T3":{"content":{"type":"Text"}},"T4":{"content":{"type":5}},"T5":{}},"rowMap":{"r1":{},"r2":{}},"sharedModelMap":{"s1":{}},"annotations":{"a1":{},"a2":{},"a3":{}}}`,
		12, map[string]any{"uid": "101", "documentKey": "dA", "classHash": "hA"},
		[]string{"5", "[Dataflow, Text]", `{"Dataflow":2,"Text":1}`, "2", "2", "1", "3"}},
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
