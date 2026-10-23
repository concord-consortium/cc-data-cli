package duck

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// clueSource is a CLUE store as a relation: its file when the store has one on disk,
// otherwise a typed-empty stand-in. Its columns are the store's recorded ones, as
// storeView reads them, so a field the export adds later passes through; with no store,
// the kind's pinned columns. standIn forces the stand-in, which is how a view's fallback
// is the view's own SQL with no files in it, so the two cannot declare different
// columns. Only the store's own view passes warn, so a missing file is reported once
// rather than by every view that joins it. A missing file is still declared, as
// storeView and reportUnionView declare theirs, so materializing refuses the view
// unless partial copies are allowed, rather than writing one short of that store.
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
