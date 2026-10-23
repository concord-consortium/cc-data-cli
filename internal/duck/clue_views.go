package duck

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// clueDocColumns are the documents-store columns joined onto every clue_history and
// clue_content row, so a recipe written against one document-level file finds them
// on either view. Their types are the documents store's own.
var clueDocColumns = []string{"portal_class_id", "unit", "investigation", "problem", "offering_id"}

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
	clueUndoable  = typedScalar{"undoable", []string{"BOOLEAN"}, func(c string) string { return fmt.Sprintf("CAST(%s AS BOOLEAN)", c) }}
)

// jsonPayload parses a stored payload string; the cast keeps the bytes when it parses.
func jsonPayload(col string) string { return fmt.Sprintf("TRY_CAST(%s AS JSON)", col) }

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
