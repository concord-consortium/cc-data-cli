# Implementation Plan: CLUE Document Views

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-111
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

The branch stacks on REPORT-110's. Every step below is written against the interfaces REPORT-110 posted (#910), its finished spec on `REPORT-110-clue-fetch`, assumed at its head (`specs/REPORT-110-fetch-clue-documents-history-and-content/`: R8, R10, R13 and its registry step), and the assumptions in the requirements' Technical Notes (A1 to A7). REPORT-110 declares every store type once as a `store.Kind` (`Name`, `Identity`, `Pinned`, ...) with `store.KindOf(name)`, and its `statements()` builds every store view in one loop over `store.Kinds()` with `storeView(k)`, falling back to `typedEmpty(k.Pinned)`. Two names come from REPORT-110's code:

- `cluePins(typ)`: `store.MustKind(typ).Pinned`, the same map REPORT-110's stand-ins are built from. `MustKind` panics on an unknown name, which is right since the three names are constants.
- `store.TypeClueDocuments`, `store.TypeClueHistory`, `store.TypeClueContent`: the three kinds' names, `clue_documents`, `clue_history` and `clue_content`.

**Hooking the registry loop.** The first step adds `derivedStoreViews` to `views.go`, beside `statements()`: a `map[string]func(viewSet) viewStmt` keyed by kind name, and the loop builds a kind's view from it when it has an entry and from `storeView(k)` otherwise; each view step adds its kind's entry. That keeps the store views in registry order (so REPORT-110's per-run views, appended after them, still follow), keeps `answers` and `history` on `storeView`, and makes the override visible in one place rather than as a skip inside the loop. REPORT-152 registers its `recent_logs` view through the same map (#917), so it stays keyed by kind name and holds nothing CLUE-specific.

The Go below was built as written (stage 7, with stand-ins for the two REPORT-110 names) and run through cc-data's own driver (DuckDB 1.5.4) over a real dataset folder: an empty dataset, a populated one, `Materialize` of all four views, a documents rewrite and a deleted documents file. It compiles, passes `go vet` and `gofmt`, and the outputs quoted below are from that run.

### Scaffolding and `clue_documents`

**Summary**: Adds the file every CLUE builder lives in, the helpers they share (the store source with its stand-in, the typed-scalar rule, the view maker that builds the fallback from the same SQL), and redefines `clue_documents`. Helpers only a later view uses (`clueUndoable`, `jsonPayload`, `clueChangeCount`) arrive with that view. A discrete commit because every later step uses the helpers, and `clue_documents` is the smallest view to prove them on.

**Files affected**:
- `internal/duck/clue_views.go`: new.
- `internal/duck/views.go`: `derivedStoreViews`, with the `clue_documents` entry, and the registry loop in `statements()` consulting it; nothing of REPORT-110's is deleted, since its plain view is `storeView(k)`, which the other kinds still use.
- `internal/duck/clue_views_test.go`: REPORT-110's file; the fixture builder and the `clue_documents` tests, and REPORT-110's CLUE tests compare each view with its store's columns plus the derived ones (`clueDerived`, which each view step extends).
- `internal/guidance/src/core.md`, `docs/researcher-guide.md`: the `clue_documents` entry and row describe `tools`, `n_tools`, `created_at` and `*_unparsed`.

**Estimated diff size**: ~300 lines

```go
package duck

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// clueSource is a CLUE store as a relation: its file, or a typed-empty stand-in over the
// store's recorded columns (the pins when there is no store) when standIn is set or the
// file is absent. A missing file is still declared, so materializing refuses the view;
// only the store's own view passes warn, so the file is reported once.
func (vs viewSet) clueSource(typ string, standIn, warn bool) (rel string, files []string) {
	cols := cluePins(typ)
	st, ok := vs.m.Stores[typ]
	if ok && len(st.Columns) > 0 {
		cols = st.Columns
	}
	if standIn || !ok || st.File == "" {
		return "(" + typedEmpty(cols) + ")", nil
	}
	if fileMissing(filepath.Join(vs.canonDir, st.File)) {
		if warn {
			vs.warnf("store file %s is missing on disk; views reading it see no %s rows (run cc-data dataset reindex)", st.File, typ)
		}
		return "(" + typedEmpty(cols) + ")", []string{st.File}
	}
	return fmt.Sprintf("read_json(%s, format='newline_delimited', columns=%s)", vs.file(st.File), columnsClause(cols)), []string{st.File}
}

// clueView registers a view whose fallback is its primary built over stand-ins.
func (vs viewSet) clueView(bare string, build func(standIn bool) (string, []string)) viewStmt {
	name := vs.prefix + sqlIdent(bare)
	primary, files := build(false)
	fallback, _ := build(true)
	return viewStmt{name: name, primary: "CREATE VIEW " + name + " AS " + primary, fallback: "CREATE VIEW " + name + " AS " + fallback, files: files}
}

// typedScalar turns a column the store keeps as raw JSON into its typed form. A value
// of any other JSON kind is NULL rather than converted, since TRY_CAST from JSON reads
// "12" and 12.5 as 12; the raw value then survives in <name>_unparsed.
type typedScalar struct {
	name  string
	kinds []string // json_type names admitted
	conv  func(col string) string
}

func (ts typedScalar) typed(col string) string {
	kinds := make([]string, len(ts.kinds))
	for i, k := range ts.kinds {
		kinds[i] = sqlStr(k)
	}
	return fmt.Sprintf("CASE WHEN json_type(%s) IN (%s) THEN %s END", col, strings.Join(kinds, ", "), ts.conv(col))
}

// replace is the typed column under the stored column's own name, for a REPLACE list.
func (ts typedScalar) replace(col string) string {
	return ts.typed(col) + " AS " + sqlIdent(ts.name)
}

// unparsed keeps a value the typed column could not hold, and only that value.
func (ts typedScalar) unparsed(col string) string {
	return unparsedColumn(col, ts.typed(col), ts.name)
}

func unparsedColumn(raw, typed, name string) string {
	return fmt.Sprintf("CASE WHEN %s IS NOT NULL AND %s IS NULL THEN %s END AS %s", raw, typed, raw, sqlIdent(name+"_unparsed"))
}

var (
	clueTools     = typedScalar{"tools", []string{"ARRAY"}, func(c string) string { return fmt.Sprintf("TRY_CAST(%s AS VARCHAR[])", c) }}
	clueCreatedAt = typedScalar{"created_at", []string{"BIGINT", "UBIGINT", "DOUBLE"}, epochExpr(1000)}
)

func cluePins(typ string) map[string]string {
	return store.MustKind(typ).Pinned
}

func (vs viewSet) clueDocumentsView() viewStmt {
	return vs.clueView(store.TypeClueDocuments, func(standIn bool) (string, []string) {
		src, files := vs.clueSource(store.TypeClueDocuments, standIn, true)
		return fmt.Sprintf("SELECT d.* REPLACE (%s, %s), len(%s) AS n_tools, %s, %s FROM %s d",
			clueTools.replace("d.tools"), clueCreatedAt.replace("d.created_at"), clueTools.typed("d.tools"),
			clueTools.unparsed("d.tools"), clueCreatedAt.unparsed("d.created_at"), src), files
	})
}
```

In `views.go`, beside `statements()`, so REPORT-152 adds its `recent_logs` entry where its plan looks for the map (its view step edits `views.go`):

```go
// derivedStoreViews replaces a kind's plain storeView with its derived view under the
// same name.
var derivedStoreViews = map[string]func(viewSet) viewStmt{
	store.TypeClueDocuments: viewSet.clueDocumentsView,
}
```

The expressions inside `REPLACE` and beside it name the qualified source column (`d.tools`), which DuckDB resolves to the input column, not the replaced output: in the prototype `undoable_unparsed` came out as the raw `"yes"` while `undoable` was NULL. `epochExpr` is the existing `logs` helper (`views.go:198-206`), so the epoch handling has one definition.

**Fixture builder** (`clue_views_test.go`): Go structs for a document, an entry (`records []record`, each with `tree`, `action`, `patches []patch`), and content, landed with REPORT-110's `land` helper, which merges them through the store itself, so each store's manifest `Columns` are the pins plus the column the fixture widens it with, as a fetch records them. The fixture is the R17 set, synthetic. The store orders lines by its length-prefixed identity key, so the entry ids are chosen to make that order differ from R13's: `a-9` and `a-02` tie on `created` and `idx`, and the shorter `a-9` is stored first while the key orders it last. The lone-surrogate entry's `entry_json` is raw text holding the six characters `\ud83d`, not marshaled from a struct: `encoding/json` cannot carry a lone surrogate (`Unmarshal` turns the escape into U+FFFD and `Marshal` writes U+FFFD for the bytes a Go string would hold instead, checked), and the entry would then parse in DuckDB as well. It also returns the Go-side expectations the tests compare with: the patch count summed over the structs, and the patch keys sorted by `(doc_id, idx, entry_id, rec_idx, patch_idx)` in Go, with a NULL `idx` after every non-NULL one in its document as DuckDB orders it.

**Tests**:
- `TestClueDocumentsTypesTools`: `tools` is `VARCHAR[]`, `n_tools` its length, both NULL for a NULL `tools`; a `tools` object gives NULL with `tools_unparsed` set.
- `TestClueDocumentsCreatedAt`: an epoch-ms number is the UTC `TIMESTAMP`; `"1700000000123"` (a string) is NULL with `created_at_unparsed` set; NULL stays NULL in both.
- `TestClueDocumentsFallbackDeclaresEveryColumn`: on an empty dataset, `DESCRIBE clue_documents` lists every pinned column plus the derived ones at the primary's types (compared with the types `DESCRIBE` gives on the populated fixture, so the two cannot drift).
- `TestClueViewsPassWidenedColumns`: with a column in each store's manifest `Columns` that the pins lack (the fixture builder adds one per store), the column is in `clue_documents`, `clue_history` and `clue_content`, and in each view's fallback with the primary's type. A malformed store file does not make a primary fail to install (see the requirements' Technical Notes), so the test describes each fallback's SQL directly and checks it reads no file. Reading the pins instead of `st.Columns` drops it (checked in the build). The content store's extra column is named `j`, which a CTE column added beside `*` would collide with: that form fails `clue_content`'s primary and fallback alike, so `Open` fails for the whole dataset (checked).

---

### `clue_history`: normalized action and the document's columns

**Summary**: Redefines `clue_history` with R3's action normalization, R5's payload and R21's `undoable`, and joins R4's document columns. The normalization lives in one function `clue_patches` reuses.

**Files affected**:
- `internal/duck/clue_views.go`: `clueActionExprs`, `clueHistoryView`.
- `internal/duck/views.go`: `derivedStoreViews` gains the `clue_history` entry.
- `internal/duck/materialize.go`: `incompleteRuns` maps a store download to every view whose declared files hold that store's current file, not only the view named after the store, and its doc comment's store rule ("The store views map by download type") says so.
- `internal/duck/clue_views_test.go`: history tests.
- `internal/fetch/clue_test.go`: the tests that check `entry_json` is stored byte for byte read it as `coalesce(entry_json::VARCHAR, entry_json_unparsed)`, since an unparseable payload is NULL in `entry_json`.
- `internal/guidance/src/core.md`, `docs/researcher-guide.md`: the `clue_history` entry and row.

**Estimated diff size**: ~270 lines

```go
// clueDocColumns are the documents-store columns joined onto every clue_history and
// clue_content row, so a recipe written against one document-level file finds them
// on either view. Their types are the documents store's own.
var clueDocColumns = []string{"portal_class_id", "unit", "investigation", "problem", "offering_id"}

var clueUndoable = typedScalar{"undoable", []string{"BOOLEAN"}, func(c string) string { return fmt.Sprintf("CAST(%s AS BOOLEAN)", c) }}

// jsonPayload parses a stored payload string; the cast keeps the bytes when it parses.
func jsonPayload(col string) string { return fmt.Sprintf("TRY_CAST(%s AS JSON)", col) }

// clueActionExprs normalizes an MST action path as cc-data-studies' normalizeAction does:
// each substitution replaces the first match only, as a non-global JavaScript regex does.
func clueActionExprs(col string) (action, tileID, sharedModelID string) {
	col = fmt.Sprintf("nullif(%s, '')", col) // normalizeAction returns nulls for any falsy raw
	action = fmt.Sprintf("regexp_replace(regexp_replace(regexp_replace(%s, '/tileMap/[^/]+', '/tileMap/{tile}'), '/sharedModelMap/[^/]+', '/sharedModelMap/{sharedModel}'), '/rowMap/[^/]+', '/rowMap/{row}')", col)
	tileID = fmt.Sprintf("nullif(regexp_extract(%s, '/tileMap/([^/]+)', 1), '')", col)
	sharedModelID = fmt.Sprintf("nullif(regexp_extract(%s, '/sharedModelMap/([^/]+)', 1), '')", col)
	return action, tileID, sharedModelID
}

// clueDocJoin is the documents store narrowed to the joined columns, for a LEFT JOIN
// USING (doc_id). The store holds one row per doc_id, so the join keeps the row count.
func (vs viewSet) clueDocJoin(standIn bool, extra ...string) (rel string, files []string) {
	src, files := vs.clueSource(store.TypeClueDocuments, standIn, false)
	cols := append([]string{"doc_id"}, extra...)
	for _, c := range clueDocColumns {
		cols = append(cols, sqlIdent(c))
	}
	return fmt.Sprintf("(SELECT %s FROM %s)", strings.Join(cols, ", "), src), files
}

func docColumnRefs(alias string) string {
	refs := make([]string, len(clueDocColumns))
	for i, c := range clueDocColumns {
		refs[i] = alias + "." + sqlIdent(c)
	}
	return strings.Join(refs, ", ")
}

func (vs viewSet) clueHistoryView() viewStmt {
	return vs.clueView(store.TypeClueHistory, func(standIn bool) (string, []string) {
		h, files := vs.clueSource(store.TypeClueHistory, standIn, true)
		d, docFiles := vs.clueDocJoin(standIn, "uid AS doc_uid")
		action, tileID, sharedModelID := clueActionExprs("h.action_raw")
		entry := jsonPayload("h.entry_json")
		return fmt.Sprintf(`SELECT h.* REPLACE (%s AS entry_json, %s), %s, %s,
  %s AS action, %s AS tile_id, %s AS shared_model_id, d.doc_uid, %s
FROM %s h LEFT JOIN %s d USING (doc_id)`,
			entry, clueUndoable.replace("h.undoable"), unparsedColumn("h.entry_json", entry, "entry_json"), clueUndoable.unparsed("h.undoable"),
			action, tileID, sharedModelID, docColumnRefs("d"), h, d), append(files, docFiles...)
	})
}
```

`files` carries both store files, so a documents fetch makes `clue_history`'s Parquet stale (R15). `incompleteRuns` maps a store download by `view == dl.Type` once REPORT-110 makes `isStoreType` registry-derived, so an incomplete `clue_documents` download would never caveat `clue_history` or `clue_content`, nor an incomplete `clue_history` one `clue_patches`; the store branch also admits a view whose `files` hold `m.Stores[dl.Type].File`:

```go
if view == dl.Type || view == "run_membership" || slices.Contains(files, m.Stores[dl.Type].File) {
```

A download whose store has no manifest entry reads `File` as `""`, which no view's `files` holds, so it matches nothing. `materialize.go` imports `slices`. The view keeps the store's name, so `Materialize`'s row-count check (`materialize.go:195-208`) refuses a copy whose join changed the count: R4 is guarded by shipped code as well as the tests. REPORT-110's per-run `clue_history_<run>` views read this view by name and inherit every derived column.

**Tests**:
- `TestClueHistoryNormalizesActions`: a table of `action_raw` to `(action, tile_id, shared_model_id)`: a `tileMap` path, a `sharedModelMap` path, a `rowMap` path, `/content/tileMap/A/content/tileMap/B/x` (only `A` captured and only the first segment rewritten), `/addTile` (all three unchanged or NULL), `''` and NULL (all three NULL).
- `TestClueHistoryCarriesItsDocumentsColumns`: every row's `doc_uid` and R10 columns equal its own fixture document's (each fixture document's values are distinct), the documents-absent entry has them NULL, and `count(*)` equals the history store's count.
- `TestClueHistoryWithoutADocumentsStore`: with no documents store in the manifest, and again with its file deleted, `clue_history` keeps every row and the joined columns are NULL, and the open's warnings name the missing file exactly once. With the file deleted, `Materialize` refuses `clue_documents`, `clue_history` and `clue_content` with a reason naming it, and with `AllowPartial` writes `clue_history` and `clue_content` with the "built from 1 of 2 declared inputs" caveat (both fail if `clueSource` drops a missing file from `files`).
- `TestClueHistoryFallbackDeclaresEveryColumn`: as for documents, on a dataset with no CLUE stores.
- `TestClueHistoryPayloads`: a parseable `entry_json` is `JSON` and byte-identical to the stored string (`entry_json::VARCHAR`), `->>` on it never raises over the whole fixture, the unparseable one is NULL with `entry_json_unparsed` holding the stored bytes; `undoable` follows R21 for `true`, `"yes"` and NULL; the lone-surrogate entry keeps `parse_ok` true with NULL `entry_json` and its bytes in `entry_json_unparsed`.
- `TestClueViewsCaveatIncompleteStores`: with an incomplete `clue_documents` download and an incomplete `clue_history` one, `Materialize`'s outcome for `clue_history` names both runs, `clue_content` the documents run and `clue_patches` the history run. Each view step adds its view's row to this test and to `TestClueHistoryWithoutADocumentsStore`.

---

### `clue_content`: tile summaries and the document's columns

**Summary**: Redefines `clue_content` with R7's payload and `parse_ok`, R8's tile summaries and `self_*` columns, R21's `change_count`, and the R10 join.

**Files affected**:
- `internal/duck/clue_views.go`: `clueContentView`.
- `internal/duck/views.go`: `derivedStoreViews` gains the `clue_content` entry.
- `internal/duck/clue_views_test.go`: content tests.
- `internal/fetch/clue_test.go`: the byte-for-byte content check reads `coalesce(content_json::VARCHAR, content_json_unparsed)`.
- `internal/guidance/src/core.md`, `docs/researcher-guide.md`: the `clue_content` entry and row.

**Estimated diff size**: ~260 lines

```go
// clueMapCount is the key count of one of content's top-level maps: 0 when the content
// parsed and the map is absent, as cc-data-studies' summarise has it, and NULL with no content.
func clueMapCount(path string) string {
	return fmt.Sprintf("CASE WHEN cj.j IS NOT NULL THEN coalesce(len(json_keys(cj.j, %s)), 0) END", sqlStr(path))
}

var clueChangeCount = typedScalar{"change_count", []string{"BIGINT", "UBIGINT"}, func(c string) string { return fmt.Sprintf("TRY_CAST(%s AS BIGINT)", c) }}

func (vs viewSet) clueContentView() viewStmt {
	return vs.clueView(store.TypeClueContent, func(standIn bool) (string, []string) {
		c, files := vs.clueSource(store.TypeClueContent, standIn, true)
		d, docFiles := vs.clueDocJoin(standIn)
		// Lateral relations, referenced qualified, so no store column can shadow them; untyped
		// or non-string tile types are skipped, though n_tiles still counts their keys.
		return fmt.Sprintf(`SELECT c.* REPLACE (cj.j AS content_json, %s),
  CASE WHEN c.content_json IS NOT NULL AND cj.j IS NULL THEN c.content_json END AS content_json_unparsed,
  %s,
  CASE WHEN c.content_json IS NOT NULL THEN cj.j IS NOT NULL END AS parse_ok,
  %s AS n_tiles,
  CASE WHEN cj.j IS NOT NULL THEN list_sort(list_distinct(ct.types)) END AS tile_types,
  CASE WHEN cj.j IS NOT NULL THEN coalesce(to_json((SELECT histogram(x) FROM unnest(ct.types) u(x))), '{}'::JSON) END AS tile_type_counts,
  CASE WHEN cj.j IS NOT NULL THEN len(list_filter(ct.types, lambda x: x = 'Dataflow')) END AS n_dataflow_tiles,
  %s AS n_rows, %s AS n_shared_models, %s AS n_annotations,
  c.self ->> 'uid' AS self_uid, c.self ->> 'documentKey' AS self_doc_key, c.self ->> 'classHash' AS self_class_hash,
  %s
FROM %s c
CROSS JOIN LATERAL (SELECT TRY_CAST(c.content_json AS JSON) AS j) cj
CROSS JOIN LATERAL (SELECT list_transform(list_filter(json_extract(cj.j, '$.tileMap.*.content.type'), lambda x: json_type(x) = 'VARCHAR'), lambda x: x ->> '$') AS types) ct
LEFT JOIN %s d USING (doc_id)`,
			clueChangeCount.replace("c.change_count"), clueChangeCount.unparsed("c.change_count"),
			clueMapCount("$.tileMap"), clueMapCount("$.rowMap"), clueMapCount("$.sharedModelMap"), clueMapCount("$.annotations"),
			docColumnRefs("d"), c, d), append(files, docFiles...)
	})
}
```

In the prototype this gave, for content with two `Dataflow` tiles, one `Text`, one numeric `type` and one tile with no content: `parse_ok` true, `n_tiles` 5, `tile_types [Dataflow Text]`, `tile_type_counts {"Dataflow":2,"Text":1}`, `n_dataflow_tiles` 2; for `{}`: true, 0, `[]`, `{}`, 0; for a missing document and for unparseable content, NULLs throughout except `parse_ok` false on the unparseable one. The builder as written here (with the join, every derived column and the R21 scalars) ran the same way in stage 7's build of this plan; the documents side contributes only `doc_id` and the R10 columns, none of which the content store has (A3). The parsed content and the type list are lateral relations referenced as `cj.j` and `ct.types` rather than columns added beside `*`, so a field the export adds later under any name passes through instead of shadowing them (see the third pass's content finding).

**Tests**:
- `TestClueContentTileSummaries`: per fixture document, hand-written expectations for every R8 column, including untyped tiles, a non-string type, absent maps, `{}`, NULL and unparseable content.
- `TestClueContentCarriesItsDocumentsColumns`: as for history, and the row count equals the content store's.
- `TestClueContentSelfColumns`: `self_uid`, `self_doc_key`, `self_class_hash` from a `self` object, NULL for a NULL `self`.
- `TestClueContentChangeCount`: R21 for `12`, `"12"`, `12.5` and NULL.
- `TestClueContentPayloads`: a parseable `content_json` is `JSON` and byte-identical to the stored string, the unparseable one is NULL with `content_json_unparsed` holding the stored bytes, and `parse_ok` is true, false and NULL for parseable, unparseable and NULL content (R7).
- `TestClueContentFallbackDeclaresEveryColumn`: as for documents.

---

### `clue_patches`

**Summary**: Adds the new view: one row per patch with R13's unique key, read from the history store with `clue_history`'s normalization, materializable. Registering it adds a static view, so its catalog entry and guide row land in this commit or the drift guard goes red.

**Files affected**:
- `internal/duck/clue_views.go`: `cluePatchesView`.
- `internal/duck/views.go`: `statements()` appends `vs.cluePatchesView()` after the registry loop and before the per-run views.
- `internal/duck/clue_views_test.go`: patch tests, and the materialization test over all four views.
- `internal/guidance/src/core.md`, `docs/researcher-guide.md`: the `clue_patches` entry and row.
- `README.md`: the view list names `clue_patches`.

**Estimated diff size**: ~295 lines

```go
// cluePatchKey is clue_patches' unique key; rec_idx and patch_idx break every tie. The view
// has no ORDER BY (a full-corpus sort exceeds the sandbox's caps), so ordered output and
// windows must use this key.
const cluePatchKey = "doc_id, idx, entry_id, rec_idx, patch_idx"

// cluePatchesView reads the history store itself rather than the clue_history view: a
// view reading another by name declares no files and is never materialized, and its
// Parquet's signature would not cover the other view's SQL.
func (vs viewSet) cluePatchesView() viewStmt {
	return vs.clueView("clue_patches", func(standIn bool) (string, []string) {
		h, files := vs.clueSource(store.TypeClueHistory, standIn, false)
		action, tileID, _ := clueActionExprs("action_raw")
		// Two unnests in one SELECT zip by position, so each range() index is its element's.
		return fmt.Sprintf(`WITH e AS (SELECT doc_id, entry_id, idx, created, server_created, is_revert, %s AS action, %s AS entry_tile_id, %s AS entry FROM %s),
rec AS (SELECT * EXCLUDE (entry), unnest(json_extract(entry, '$.records[*]')) AS rec,
  unnest(range(CAST(json_array_length(entry, '$.records') AS BIGINT))) AS rec_idx FROM e),
pat AS (SELECT * EXCLUDE (rec), unnest(json_extract(rec, '$.patches[*]')) AS patch,
  unnest(range(CAST(json_array_length(rec, '$.patches') AS BIGINT))) AS patch_idx FROM rec)
SELECT doc_id, entry_id, idx, created, server_created, action,
  coalesce(nullif(regexp_extract(patch ->> 'path', '/tileMap/([^/]+)', 1), ''), entry_tile_id) AS tile_id,
  is_revert, rec_idx, patch_idx, patch ->> 'op' AS op, patch ->> 'path' AS path, json_extract(patch, '$.value') AS value
FROM pat`, action, tileID, jsonPayload("entry_json"), h), files
	})
}
```

The view is not named after a store, so `Materialize` applies no row-count check to it; R18's Go-computed count is what checks it. It is in `StaticViewNames()` from the empty-manifest build, which is what puts it in the drift guard and the materializable set.

**Tests**:
- `TestCluePatchesCountsEveryPatch`: `count(*)` equals the patch count the fixture builder summed in Go over the parseable entries (the builder marks the lone-surrogate entry unparseable by hand, since Go's `json.Valid` accepts it as `JSON.parse` does and DuckDB does not); the unparseable entry, the empty `records`, the record without `patches` and a `records` object contribute nothing and raise nothing.
- `TestCluePatchesKeyIsUnique`: `count(*) = count(DISTINCT (doc_id, entry_id, rec_idx, patch_idx))`, no group of `cluePatchKey` itself repeats, and an anti-join to `clue_history` on `(doc_id, entry_id)` is empty.
- `TestCluePatchesOrderedByTheKey`: `SELECT doc_id, entry_id, rec_idx, patch_idx FROM clue_patches ORDER BY ` + `cluePatchKey` returns the Go-sorted key list on two `Open`s of the same dataset, and `lag(path) OVER (PARTITION BY doc_id ORDER BY ` + `cluePatchKey` + `)` equals the previous path computed in Go, over the fixture's entries tied on `created` and on `idx` and its entry with a NULL `idx`, read once as the view gives it and once with each entry's positions reversed first (a key that dropped `rec_idx` or `patch_idx` leaves ties there and fails).
- `TestCluePatchesColumns`: per fixture patch, `tile_id` from the path when it has one (an `/addTile` entry whose patch path names the new tile) and from the entry otherwise (an entry whose action names a tile and whose patch path names none), `op` and `path`, `value IS NULL` for the `remove` and `json_type(value) = 'NULL'` for the set-to-null patch, and the entry's `action`, `created`, `server_created` and `is_revert` on each.
- `TestCluePatchesFallbackDeclaresEveryColumn`: R11's columns at their types, written out by hand, on the fixture; then as for documents.
- `TestClueViewsMaterialize`: `Materialize` writes all four; reopening pins all four to their Parquet (`e.pinned`), and each returns the raw view's rows (`viewRows`). A fetch-like rewrite of the documents file then makes `clue_history` and `clue_content` stale and leaves `clue_patches` fresh.

---

### Guidance: clocks, ordering and the runtime trap

**Summary**: The caveats R20 lists, in the shared core and the researcher guide. A separate commit because it is prose over views that already exist and are already cataloged, so the guard is green on both sides of it.

**Files affected**:
- `internal/guidance/src/core.md`: beside the four entries in `## Views`, the clock rule, the ordering rule for entries and for patch windows, the runtime-output trap, `action` versus patches, `replace` carrying only the new value, the `value` NULL test, `*_unparsed`, and materializing a large CLUE dataset (one line in `## Materializing a dataset` pointing at `clue_patches`).
- `docs/researcher-guide.md`: the same points in the guide's plainer register, under the view table.

**Estimated diff size**: ~90 lines

No command spellings in `core.md` (`TestCoreNamesNoCommand`); the materialize pointer names the view and the operation, not `cc-data dataset materialize`.

## Open Questions

### RESOLVED: Judgment call: each view's fallback is its own SQL over stand-ins
**Context**: Every existing builder writes its fallback separately (`typedEmpty`, `csvEmptyMember`), which is a second column list per view.
**Options considered**:
- A) A hand-written typed-empty fallback per view.
- B) The primary's builder called with every source replaced by its typed-empty stand-in.

**Decision**: B. The derived columns are many and computed, so a second list would be the place they drift; built from the same SQL, the fallback cannot declare a different column or type. The prototype's empty dataset gave every derived column at the primary's type.

### RESOLVED: Judgment call: the CLUE builders in their own file
**Context**: `views.go` is 969 lines and REPORT-110 adds to it.
**Options considered**:
- A) Add the builders to `views.go`.
- B) `internal/duck/clue_views.go`, with `statements()` in `views.go` calling it.

**Decision**: B. The four builders and their helpers share nothing with the report unions or dimensions, and REPORT-152 edits `views.go` next; a separate file keeps the stacked branches' conflicts to the `statements()` lines and the `derivedStoreViews` map beside them, which both stories add entries to.

### RESOLVED: Judgment call: four view commits and a prose commit
**Context**: R19 makes registering a view and its guidance one change.
**Options considered**:
- A) One commit for all four views and their guidance.
- B) One commit per view, each carrying its own catalog entry and guide row, then the caveats.

**Decision**: B. Each view is reviewable alone and each commit keeps the guard green: the three redefinitions only rewrite entries REPORT-110 created, and `clue_patches` adds its entry with its registration.

## Self-Review

Roles: Senior Engineer, Commit Reviewer, Test Writer, Operator. Every finding was checked by building this plan's Go as written, with stand-ins for `cluePins` and the three type names, and running it through the engine, `Materialize` and the mutations named below; each had one defensible correction, made in place. The build itself confirmed what the plan claims: every view's fallback on an empty dataset carries every column at the primary's type, R21's scalars and `_unparsed` columns behave as R21 says, a history row with no document keeps its row, all four views materialize, and after a documents rewrite only `clue_patches` stays pinned to its Parquet.

### Operator

#### RESOLVED: A missing documents file was reported three times
`clueSource` warned from every view that read the documents store, so one deleted file printed the same line for `clue_documents`, `clue_history` and `clue_content`. It now takes a `warn` flag that only the store's own view sets, and the history test checks the warning appears once.

### Senior Engineer

#### RESOLVED: A typed column's name was written twice
Each typed scalar carried its name in `typedScalar.name` and again as a string at each `unparsedColumn` call and each `AS` alias, so a rename could leave the two apart. `typedScalar` now renders its own `replace` and `unparsed` columns from its name. The same pass removed a dead `if !standIn` inside `clueSource` (that branch had already returned) and the misaligned `var` block `gofmt` rewrote.

### Commit Reviewer

#### RESOLVED: Helpers declared a step before their first use
`clueUndoable`, `clueChangeCount` and `jsonPayload` sat in the scaffolding step, which uses none of them. CI's only static check is `go vet` (`.github/workflows/ci.yml:37`), which does not report an unused package-level variable, so nothing failed, but the scaffolding commit would carry code its own diff never calls. Each now arrives with the view that first uses it; each step's code compiled alone in the build.

### Test Writer

No finding survived; the materialize test's staleness claim held as written. (The order test it checked was replaced when the view's `ORDER BY` was dropped; see the requirements' sort finding.)

Second pass. Roles: Senior Engineer, Operator, a downstream consumer (REPORT-152), Commit Reviewer. Each finding was checked by building this plan's Go again as throwaway code (the same stand-ins, plus `isStoreType` widened to the CLUE kinds as REPORT-110's registry makes it) and running it through `Open` and `Materialize`; each had one defensible correction, made in place. The requirements' second-pass finding on `clue_patches`' `ORDER BY` was decided as A (Doug, 2026-10-22): the view's SQL has no `ORDER BY`, and the order test orders by `cluePatchKey`.

### Senior Engineer

#### RESOLVED: The redefined views dropped every column the store widened beyond the pins
`clueSource` built its `read_json` columns from `cluePins(typ)`, while REPORT-110's stores record pins plus every field observed (its R10: "a field the export adds later is not pinned and widens as today"; `NewColumnDetector` seeds the pins and widens the rest, `columns.go:41-62`), and `storeView` reads `st.Columns`. In the build, a field present in each store's manifest `Columns` but not in the pins was missing from all three views, against R1, R2 and R6's "every store column passed through". `clueSource` now reads `st.Columns` when the store has them and the pins otherwise, as `storeView` does; with that, the field passed through each view and each fallback's `DESCRIBE` still equaled its primary's. A widened field whose name a derived column uses would not fail the view: DuckDB renames the later duplicate to `<name>_1` (checked), which A3 rules out for today's fields.

### Operator

#### RESOLVED: An incomplete download of one store never caveated the views that read it from another
`incompleteRuns` (`materialize.go:534-556`) maps a store download to the view named after its type and to `run_membership`. With the CLUE kinds counted as stores, `Materialize` reported an incomplete `clue_history` run on `clue_history` only and wrote `clue_patches` with no caveat, and an incomplete `clue_documents` run never reached `clue_history` or `clue_content`, though both join that store. The store branch now also admits a view whose declared files hold the store's current file (the `clue_history` step); rerun, the four outcomes named the right runs, and the duck suite stayed green.

### Downstream consumer (REPORT-152)

#### RESOLVED: `derivedStoreViews` lived in the file REPORT-152 does not edit
The plan declared the map in `clue_views.go`, while REPORT-152's view step adds `"recent_logs"` to it under `internal/duck/views.go` and calls it the map "which `statements()`' loop over the kinds consults". The map now sits in `views.go` beside that loop, each view step adds its entry there, and REPORT-152's plan holds as written. The build compiled with the map in its own file.

### Commit Reviewer

#### RESOLVED: A resolved entry misstated what CI runs
The first pass's helpers finding said CI "runs only `go vet`"; `.github/workflows/ci.yml:34-39` runs `go build`, `go vet` and `go test` on four platforms. What the finding needed is that `go vet` is the only static check and does not report an unused package-level variable, which is what it now says.

### Third pass

Roles: SQL Robustness Reviewer (DuckDB), Fixture Engineer. Each finding was checked by building this plan's Go as throwaway code (the same stand-ins, the three derived builders and `cluePatchesView` wired into `statements()`, `isStoreType` widened, the `incompleteRuns` change applied) and running it through `Open` and `Materialize` with cc-data's driver; each had one defensible correction, made in place, and the rebuilt plan passed `go vet`, `gofmt` and the duck suite. The requirements' third pass records the upstream findings (a NULL `idx` in the fixture and the Go sort) and the offering question, since decided.

### SQL Robustness Reviewer

#### RESOLVED: A content field named `j` or `tile_type_list` made the whole dataset unopenable
`clueContentView` added the parsed content and the type list as CTE columns beside `SELECT *` over the store, while `clueSource` passes every column the store records and REPORT-110's R10 widens a store with any field the export adds. In the build, a content store with an extra `j` column failed `clue_content`'s primary with a binder error (`list_distinct(VARCHAR)`, the store's columns having shadowed the CTE's), and the fallback, the same SQL over the stand-in, failed the same way, so `register` returned an error and `Open` failed: every query on the dataset, `answers` included. That is unlike the second pass's widened-column note, where a derived output name collides and DuckDB renames the duplicate. The builder now computes both in `CROSS JOIN LATERAL` relations referenced as `cj.j` and `ct.types`. Rerun with store columns named `j`, `types`, `cj` and `tile_type_list`, they passed through, every R8 column matched the prototype's values, `DESCRIBE` was identical to the CTE form's for primary and fallback (names, types and order), and all four views materialized and pinned. On 30,000 synthetic documents (91 MB, DuckDB 1.5.5 CLI, `threads = 4`) the lateral form took 0.34 to 0.41 s against the CTE form's 0.40 to 0.45 s, at about 1.7 times the CPU. `clue_history` and `clue_patches` select named columns into their subqueries and CTEs, and passed the same check with history columns named `entry`, `rec`, `patch` and `action`. The widened-columns test now names the content column `j`.

### Fixture Engineer

#### RESOLVED: The lone-surrogate entry cannot be built by marshaling a struct
The fixture builder marshals Go structs with `encoding/json`, and R17's lone-surrogate entry needs its `entry_json` to hold the escape `\ud83d`. Go cannot carry it: `json.Unmarshal` of `{"text":"\ud83d"}` yields U+FFFD, and `json.Marshal` of the bytes a Go string would hold for U+D83D writes U+FFFD (checked, Go 1.27), which DuckDB parses, so the entry would explode into patches and the R18 test written against it would fail or be bent to expect the wrong rows. The fixture builder now writes that entry's `entry_json` as raw text.

### Fourth pass

Roles: Dataset Lifecycle Reviewer (materialize, refusal and caveat contracts as `main` documents them), Documentation Drift Reviewer (prose the plan's changes invalidate), and a whole-document consistency read of both files. Each finding was checked against `main` or by building this plan's Go as throwaway code (stand-ins for `store.KindOf` and the three type names, the three derived builders and `cluePatchesView` wired into `statements()`, `isStoreType` widened, the `incompleteRuns` change applied) and running it through `Open` and `Materialize` with cc-data's driver; each had one defensible correction, made in place. The duck suite stayed green with the correction applied. The requirements' fourth pass records the consistency findings in that file.

### Dataset Lifecycle Reviewer

#### RESOLVED: A missing store file dropped out of the views' declared inputs, so materializing wrote short copies without a word
`clueSource` returned no files when a store's file was missing on disk, while `storeView` declares its file regardless and `reportUnionView` declares a missing CSV's file beside its typed-empty member. `Materialize` refuses a view whose declared input is missing unless `--allow-partial`, and then caveats it, which `core.md` ("a view can also be refused when a file it reads is missing from disk") and the researcher guide ("materializing refuses that view rather than writing a short Parquet") both promise. In the build, with the documents file deleted, `answers` beside it was refused while `clue_history` and `clue_content` were written with no caveat, their joined columns NULL in a Parquet that then read as fresh, and `clue_documents` got no outcome at all; with the history file deleted, `clue_patches` got no outcome and `clue_history` was refused only by the row-count check, with no remedy named. `clueSource` now returns the file in its missing branch, keeping the typed-empty relation, so queries still keep their rows (R14). Rerun: the three documents-reading views were refused with the missing-input reason naming `clue_documents.v1.jsonl` and both remedies; with `AllowPartial`, `clue_history` and `clue_content` were written with "built from 1 of 2 declared inputs (missing: clue_documents.v1.jsonl)" and `clue_patches` over a missing history file with "built from 0 of 1"; the open's single warning was unchanged. R15 now says a missing file is still declared, and `TestClueHistoryWithoutADocumentsStore` checks the refusal and the caveat.

### Documentation Drift Reviewer

#### RESOLVED: The `incompleteRuns` change left its doc comment describing the old rule
`incompleteRuns`' header (`materialize.go:521-533` on `main`) says "The store views map by download type, since a store is one merged file across every run", which the `clue_history` step's file-based admission makes incomplete. The step's file list now updates that sentence in the same commit.

#### RESOLVED: The file-placement judgment call still described the map's old home
"Judgment call: the CLUE builders in their own file" said a separate file keeps the stacked branches' conflicts to the `statements()` lines, written while `derivedStoreViews` lived in `clue_views.go`. The second pass moved the map to `views.go`, where REPORT-152's view step adds `recent_logs` to it (its implementation spec on `REPORT-152-recent-logs`), so the shared lines are the loop and the map. The decision text now names both.
