package duck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// contractStoreColumns is the minimal typed schema an absent store falls back to.
var contractStoreColumns = map[string]string{
	"source_key":      "VARCHAR",
	"remote_endpoint": "VARCHAR",
	"question_id":     "VARCHAR",
	"history_id":      "VARCHAR",
	"_fetched_at":     "TIMESTAMP",
	"_run_id":         "BIGINT",
}

var membershipColumns = map[string]string{
	"source_key":      "VARCHAR",
	"remote_endpoint": "VARCHAR",
	"question_id":     "VARCHAR",
	"history_id":      "VARCHAR",
}

// viewStmt is one view with its primary SQL and a typed-empty fallback.
type viewStmt struct {
	name         string
	materialized string // optional read_parquet form, tried before primary
	primary      string
	fallback     string
	files        []string // files the primary reads (for the degradation warning)

	// Set alongside materialized: the file it reads, the manifest entry that
	// names it, and the signature of the definition the entry was checked
	// against, so a session can re-check the copy's freshness after it opens.
	parquet   string
	record    dataset.Materialized
	signature string
}

// viewSet builds the view statements for one dataset under a schema prefix.
type viewSet struct {
	prefix   string // "" for the default schema, or `"name".`
	canonDir string
	m        *dataset.Manifest
	warn     io.Writer
}

func (vs viewSet) warnf(format string, args ...any) {
	if vs.warn != nil {
		fmt.Fprintf(vs.warn, "warning: "+format+"\n", args...)
	}
}

func (vs viewSet) statements() []viewStmt {
	var stmts []viewStmt
	stmts = append(stmts, vs.reportsView())
	stmts = append(stmts, vs.reportPromptsView())
	stmts = append(stmts, vs.logsView())
	stmts = append(stmts, vs.storeView(store.TypeAnswers))
	stmts = append(stmts, vs.storeView(store.TypeHistory))
	stmts = append(stmts, vs.runMembershipView())
	stmts = append(stmts, vs.downloadsView())
	stmts = append(stmts, vs.attachmentFilesView())
	stmts = append(stmts, vs.attachmentStatesView())
	stmts = append(stmts, vs.attachmentContentView())
	stmts = append(stmts, vs.dimensionViewStmts()...)
	stmts = append(stmts, vs.perDownloadViews()...)
	return stmts
}

func (vs viewSet) file(rel string) string {
	return sqlStr(filepath.Join(vs.canonDir, rel))
}

// reportsView builds the reports UNION over allowlisted per-run CSVs. A run whose
// report_type is outside the allowlist is quarantined with a warning; a CSV whose
// file is missing degrades to a typed-empty stand-in from its recorded columns
// (contributing zero rows) rather than collapsing the whole union.
func (vs viewSet) reportsView() viewStmt {
	return vs.reportUnionView(`"reports"`, true, nil, func(dl dataset.Download) bool {
		if !dataset.IsAllowedReportType(dl.ReportType) {
			vs.warnf("run %d report_type %q is unknown to this cc-data version; excluded from the reports view (upgrade suggested)", dl.RunID, dl.ReportType)
			return false
		}
		return true
	})
}

// reportPromptsView exposes the two pseudo-header rows of answers-type CSVs.
func (vs viewSet) reportPromptsView() viewStmt {
	return vs.reportUnionView(`"report_prompts"`, false, nil, func(dl dataset.Download) bool {
		return dl.ReportType == dataset.ReportTypeAnswers
	})
}

// derivedColumn is an output column appended to every member of a report union,
// derived from the src column of the CSV. A member whose CSV lacks src contributes a
// typed NULL instead, so typ is declared once and cannot disagree with what the
// member emits.
type derivedColumn struct {
	name string
	typ  string
	src  string
	expr func(col string) string
}

// sql renders the column for one download, where col is the quoted source identifier.
func (d derivedColumn) sql(dl dataset.Download) string {
	if _, ok := dl.Columns[d.src]; !ok {
		return fmt.Sprintf("CAST(NULL AS %s)", d.typ)
	}
	return d.expr(sqlIdent(d.src))
}

// sourceSuffix renames a CSV column whose name a derived column already claims. The derived
// name is what the catalog entry documents, so it wins and the source is still reachable;
// leaving both would retype the column to VARCHAR across the whole union, or, with two or more
// members, be a binder error that the fallback hits too and costs the whole dataset.
const sourceSuffix = "_source"

// collisions returns the CSV columns a derived column would shadow, in derived-column order.
func collisions(dl dataset.Download, derived []derivedColumn) []string {
	var names []string
	for _, d := range derived {
		if _, taken := dl.Columns[d.name]; taken {
			names = append(names, d.name)
		}
	}
	return names
}

// sourceName is the free name a shadowed CSV column is re-emitted under. A CSV carrying both
// the derived name and the suffixed one would otherwise emit the target twice, which fails the
// primary and the fallback alike and leaves the dataset unopenable.
//
// The target is only checked against the CSV's own columns, not against names other renames
// took. Two renames cannot converge while no derived name is another with sourceSuffix repeated
// on the end, which is worth re-checking if a fifth derived column is ever added.
func sourceName(dl dataset.Download, col string) string {
	name := col + sourceSuffix
	for {
		if _, taken := dl.Columns[name]; !taken {
			return name
		}
		name += sourceSuffix
	}
}

// logsView unions the log-type report CSVs with parameters and extras parsed as JSON
// and both of the row's clocks rendered as timestamps. Admission is by report type
// rather than slug because reindex recovers the type from a CSV whose slug is gone,
// and a slug test would drop rows the reports view still shows.
func (vs viewSet) logsView() viewStmt {
	// keepData is inert here; it only gates the answers-type pseudo-header filter.
	return vs.reportUnionView(`"logs"`, true, logDerived, func(dl dataset.Download) bool {
		return dl.ReportType == dataset.ReportTypeLog
	})
}

// logDerived are the columns logsView appends to every member. time and timestamp are
// different clocks rather than one instant at two resolutions: the ingester sets time
// from the client's own clock, rounded to seconds, and timestamp to server receipt in
// milliseconds. Hence the divisor on one and not the other.
var logDerived = []derivedColumn{
	{"parameters_json", store.TypeJSON, "parameters", jsonExpr},
	{"extras_json", store.TypeJSON, "extras", jsonExpr},
	{"event_time", store.TypeTIMESTAMP, "time", epochExpr(1)},
	{"received_time", store.TypeTIMESTAMP, "timestamp", epochExpr(1000)},
}

// jsonExpr parses a column as JSON, yielding NULL rather than an error for a value
// that is not valid JSON.
func jsonExpr(col string) string {
	return fmt.Sprintf("TRY_CAST(%s AS %s)", col, store.TypeJSON)
}

// epochExpr converts a UNIX epoch column counting perSecond units per second to a UTC
// timestamp. Two guards, for two different failures. TRY_CAST tolerates DetectCSV typing the
// column per file, since to_timestamp on a VARCHAR is a binder error that would fail the whole
// view rather than null one column. TRY catches the conversion itself: nan, inf and any value
// outside the TIMESTAMP epoch range cast to DOUBLE happily and then throw, which surfaces at
// query time on a view that installed cleanly, so no fallback and no warning would fire.
// AT TIME ZONE 'UTC' turns to_timestamp's TIMESTAMPTZ into a TIMESTAMP, so the type does not
// depend on whether a CSV is present and compares directly to the store's _fetched_at.
func epochExpr(perSecond int) func(string) string {
	return func(col string) string {
		seconds := fmt.Sprintf("TRY_CAST(%s AS DOUBLE)", col)
		if perSecond != 1 {
			seconds = fmt.Sprintf("%s / %d", seconds, perSecond)
		}
		return fmt.Sprintf("TRY(to_timestamp(%s) AT TIME ZONE 'UTC')", seconds)
	}
}

// reportUnionView builds a union over the report CSVs the include predicate
// admits, using a typed-empty stand-in for any CSV whose file is missing so one
// broken artifact costs its rows, never the session or the union's schema.
func (vs viewSet) reportUnionView(bare string, keepData bool, derived []derivedColumn, include func(dataset.Download) bool) viewStmt {
	var members []string
	var emptyMembers []string
	var files []string
	for _, dl := range vs.m.Downloads {
		if dl.Type != "report" || len(dl.Files) == 0 || dl.Columns == nil {
			continue
		}
		if !include(dl) {
			continue
		}
		if fileMissing(filepath.Join(vs.canonDir, dl.Files[0])) {
			vs.warnf("report CSV %s is missing on disk; contributing zero rows to %s (run cc-data dataset reindex)", dl.Files[0], strings.Trim(bare, `"`))
			members = append(members, vs.csvEmptyMember(dl, derived))
		} else {
			members = append(members, vs.csvScan(dl, keepData, derived))
		}
		// The typed-empty schema for every admitted member (present or missing).
		emptyMembers = append(emptyMembers, vs.csvEmptyMember(dl, derived))
		files = append(files, dl.Files...)
	}
	name := vs.prefix + bare
	if len(members) == 0 {
		// A stand-in may only declare columns every populated schema also has, so this
		// carries the derived columns and no source ones.
		cols := []string{"CAST(NULL AS BIGINT) AS run_id"}
		for _, d := range derived {
			cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", d.typ, sqlIdent(d.name)))
		}
		stmt := fmt.Sprintf("CREATE VIEW %s AS SELECT %s WHERE false", name, strings.Join(cols, ", "))
		return viewStmt{name: name, primary: stmt, fallback: stmt}
	}
	primary := fmt.Sprintf("CREATE VIEW %s AS %s", name, strings.Join(members, "\nUNION ALL BY NAME\n"))
	// Per-member binding cannot be validated here (no live connection), so the
	// fallback unions every admitted member's typed-empty schema. If a single
	// present-but-corrupt CSV makes the primary CREATE VIEW fail, the view still
	// installs the full report+schema shape (zero rows) instead of collapsing to
	// a run_id-only stand-in that would discard every valid report's columns.
	fallback := fmt.Sprintf("CREATE VIEW %s AS %s", name, strings.Join(emptyMembers, "\nUNION ALL BY NAME\n"))
	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
}

// csvEmptyMember is a zero-row scan carrying the CSV's recorded schema plus
// run_id and the derived columns, so a missing CSV keeps its columns in the union
// and the fallback declares the same shape the primary does.
func (vs viewSet) csvEmptyMember(dl dataset.Download, derived []derivedColumn) string {
	cols := make([]string, 0, len(dl.Columns)+len(derived)+1)
	cols = append(cols, fmt.Sprintf("CAST(%d AS BIGINT) AS run_id", dl.RunID))
	order := dl.ColumnOrder
	if len(order) == 0 {
		for k := range dl.Columns {
			order = append(order, k)
		}
		sort.Strings(order)
	}
	shadowed := map[string]bool{}
	for _, name := range collisions(dl, derived) {
		shadowed[name] = true
	}
	for _, k := range order {
		t, ok := dl.Columns[k]
		if !ok {
			continue
		}
		name := k
		if shadowed[k] {
			name = sourceName(dl, k)
		}
		cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", t, sqlIdent(name)))
	}
	for _, d := range derived {
		cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", d.typ, sqlIdent(d.name)))
	}
	return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

// csvScan builds one per-run CSV SELECT. When keepData is true the pseudo-header
// rows are filtered out (answers-type only); when false only they are kept.
func (vs viewSet) csvScan(dl dataset.Download, keepData bool, derived []derivedColumn) string {
	dialect := dataset.DefaultCSVDialect()
	if dl.CSVDialect != nil {
		dialect = *dl.CSVDialect
	}
	star := "*"
	var extra strings.Builder
	if shadowed := collisions(dl, derived); len(shadowed) > 0 {
		quoted := make([]string, len(shadowed))
		for i, name := range shadowed {
			quoted[i] = sqlIdent(name)
			fmt.Fprintf(&extra, ", %s AS %s", sqlIdent(name), sqlIdent(sourceName(dl, name)))
		}
		star = "* EXCLUDE (" + strings.Join(quoted, ", ") + ")"
	}
	for _, d := range derived {
		fmt.Fprintf(&extra, ", %s AS %s", d.sql(dl), sqlIdent(d.name))
	}
	scan := fmt.Sprintf("SELECT %d AS run_id, %s%s FROM read_csv(%s, auto_detect=false, header=true, delim=%s, quote=%s, escape=%s, columns=%s)",
		dl.RunID, star, extra.String(), vs.file(dl.Files[0]), sqlStr(dialect.Delim), sqlStr(dialect.Quote), sqlStr(dialect.Escape), orderedColumnsClause(dl.Columns, dl.ColumnOrder))
	if dl.ReportType == dataset.ReportTypeAnswers {
		if keepData {
			return scan + " WHERE student_id::VARCHAR NOT IN ('Prompt', 'Correct answer')"
		}
		return scan + " WHERE student_id::VARCHAR IN ('Prompt', 'Correct answer')"
	}
	return scan
}

// storeView builds the answers or history view over the current store.
func (vs viewSet) storeView(typ string) viewStmt {
	name := vs.prefix + sqlIdent(typ)
	st, ok := vs.m.Stores[typ]
	cols := contractStoreColumns
	if ok && len(st.Columns) > 0 {
		cols = st.Columns
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS %s", name, typedEmpty(cols))
	if !ok || st.File == "" {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM read_json(%s, format='newline_delimited', columns=%s)",
		name, vs.file(st.File), columnsClause(st.Columns))
	return viewStmt{name: name, primary: primary, fallback: fallback, files: []string{st.File}}
}

// runMembershipView unifies every current membership version with run_id + type.
func (vs viewSet) runMembershipView() viewStmt {
	var members []string
	var files []string
	for key, ref := range vs.m.Membership {
		typ, run, ok := parseMemberKey(key)
		if !ok {
			continue
		}
		members = append(members, fmt.Sprintf(
			"SELECT %d AS run_id, %s AS type, source_key, remote_endpoint, question_id, history_id FROM read_json(%s, format='newline_delimited', columns=%s)",
			run, sqlStr(typ), vs.file(ref.File), columnsClause(membershipColumns)))
		files = append(files, ref.File)
	}
	name := vs.prefix + `"run_membership"`
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS type, CAST(NULL AS VARCHAR) AS source_key, CAST(NULL AS VARCHAR) AS remote_endpoint, CAST(NULL AS VARCHAR) AS question_id, CAST(NULL AS VARCHAR) AS history_id WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	sort.Strings(members)
	primary := fmt.Sprintf("CREATE VIEW %s AS %s", name, strings.Join(members, "\nUNION ALL BY NAME\n"))
	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
}

// downloadsView is a VALUES dimension table from the manifest.
//
// hide_names separates the two meanings of a name column: where a run hid names, student_name
// holds the student id and username a hash, under those same names, so rows fetched under
// different roles are union-compatible and indistinguishable in the reports view. It is NULL
// wherever no filter is on disk to derive it from.
func (vs viewSet) downloadsView() viewStmt {
	name := vs.prefix + `"downloads"`
	header := "(run_id, type, slug, report_type, hide_names, complete)"
	var rows []string
	for _, dl := range vs.m.Downloads {
		rows = append(rows, fmt.Sprintf("(%d, %s, %s, %s, %s, %t)",
			dl.RunID, sqlStr(dl.Type), sqlStr(dl.Slug), sqlStr(dl.ReportType), hideNamesLiteral(dl), dl.Complete))
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS type, CAST(NULL AS VARCHAR) AS slug, CAST(NULL AS VARCHAR) AS report_type, CAST(NULL AS BOOLEAN) AS %s, CAST(NULL AS BOOLEAN) AS complete WHERE false", name, sqlIdent(dimensionHideName))
	if len(rows) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM (VALUES %s) AS t%s", name, strings.Join(rows, ", "), header)
	return viewStmt{name: name, primary: primary, fallback: fallback}
}

// attachmentFilesView is a VALUES table from the manifest attachment index.
func (vs viewSet) attachmentFilesView() viewStmt {
	name := vs.prefix + `"attachment_files"`
	header := "(id12, name, source, public_path, content_type, size, file)"
	var rows []string
	for _, af := range vs.m.Attachments {
		rows = append(rows, fmt.Sprintf("(%s, %s, %s, %s, %s, %d, %s)",
			sqlStr(af.ID12), sqlStr(af.Name), sqlStr(af.Source), sqlStr(af.PublicPath), sqlStr(af.ContentType), af.Size, vs.file(af.File)))
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS VARCHAR) AS id12, CAST(NULL AS VARCHAR) AS name, CAST(NULL AS VARCHAR) AS source, CAST(NULL AS VARCHAR) AS public_path, CAST(NULL AS VARCHAR) AS content_type, CAST(NULL AS BIGINT) AS size, CAST(NULL AS VARCHAR) AS file WHERE false", name)
	if len(rows) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM (VALUES %s) AS t%s", name, strings.Join(rows, ", "), header)
	return viewStmt{name: name, primary: primary, fallback: fallback}
}

// attachmentStatesView exposes offloaded state files as raw text plus TRY_CAST JSON.
func (vs viewSet) attachmentStatesView() viewStmt {
	name := vs.prefix + `"attachment_states"`
	var members, files []string
	for _, af := range vs.m.Attachments {
		if !af.State {
			continue
		}
		members = append(members, fmt.Sprintf(
			"SELECT %s AS id12, %s AS name, filename, content FROM read_text(%s)",
			sqlStr(af.ID12), sqlStr(af.Name), vs.file(af.File)))
		files = append(files, af.File)
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS VARCHAR) AS filename, CAST(NULL AS VARCHAR) AS id12, CAST(NULL AS VARCHAR) AS name, CAST(NULL AS VARCHAR) AS content, CAST(NULL AS JSON) AS state WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	inner := strings.Join(members, "\nUNION ALL BY NAME\n")
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT filename, id12, name, content, TRY_CAST(content AS JSON) AS state FROM (%s)", name, inner)
	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
}

// attachmentContentView exposes the text content of every downloaded attachment
// (regardless of the current-answer State flag), so historical/offloaded states
// - e.g. every saved CODAP/SageModeler snapshot across a session's history, not
// just the one the current answer points at - are queryable and diffable.
// attachment_states stays as the narrow current-answer view for back-compat.
func (vs viewSet) attachmentContentView() viewStmt {
	name := vs.prefix + `"attachment_content"`
	var members, files []string
	for _, af := range vs.m.Attachments {
		// read_text requires valid UTF-8, so binary attachments (audio, images)
		// cannot be exposed as text; they remain downloadable via attachment_files.
		// Offloaded states are JSON/text, which is the point of this view.
		if !isTextContentType(af.ContentType) {
			continue
		}
		members = append(members, fmt.Sprintf(
			"SELECT %s AS id12, %s AS name, %s AS source, %s AS public_path, filename, content FROM read_text(%s)",
			sqlStr(af.ID12), sqlStr(af.Name), sqlStr(af.Source), sqlStr(af.PublicPath), vs.file(af.File)))
		files = append(files, af.File)
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS VARCHAR) AS id12, CAST(NULL AS VARCHAR) AS name, CAST(NULL AS VARCHAR) AS source, CAST(NULL AS VARCHAR) AS public_path, CAST(NULL AS VARCHAR) AS filename, CAST(NULL AS VARCHAR) AS content, CAST(NULL AS JSON) AS state WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	inner := strings.Join(members, "\nUNION ALL BY NAME\n")
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT id12, name, source, public_path, filename, content, TRY_CAST(content AS JSON) AS state FROM (%s)", name, inner)
	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
}

// perDownloadViews builds report_<run>, answers_<run>, history_<run>, and
// report_<run>_job_<id> views.
func (vs viewSet) perDownloadViews() []viewStmt {
	var stmts []viewStmt
	// Report CSV per-run views from the download entries.
	for _, dl := range vs.m.Downloads {
		switch dl.Type {
		case "report":
			if len(dl.Files) > 0 && dl.Columns != nil {
				vn := vs.prefix + sqlIdent(fmt.Sprintf("report_%d", dl.RunID))
				stmts = append(stmts, viewStmt{name: vn, primary: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvScan(dl, true, nil)), fallback: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvEmptyMember(dl, nil)), files: dl.Files})
			}
		case "report_job":
			if len(dl.Files) > 0 && dl.Columns != nil && dl.JobID != nil {
				vn := vs.prefix + sqlIdent(fmt.Sprintf("report_%d_job_%d", dl.RunID, *dl.JobID))
				stmts = append(stmts, viewStmt{name: vn, primary: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvScan(dl, true, nil)), fallback: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvEmptyMember(dl, nil)), files: dl.Files})
			}
		}
	}
	// Store per-run views from the membership map (type-scoped joins).
	keys := make([]string, 0, len(vs.m.Membership))
	for key := range vs.m.Membership {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if typ, run, ok := parseMemberKey(key); ok {
			stmts = append(stmts, vs.perStoreView(typ, run))
		}
	}
	return stmts
}

// perStoreView joins the store to a run's membership, type-scoped.
func (vs viewSet) perStoreView(typ string, run int) viewStmt {
	vn := vs.prefix + sqlIdent(fmt.Sprintf("%s_%d", typ, run))
	storeName := vs.prefix + sqlIdent(typ)
	memName := vs.prefix + `"run_membership"`
	using := "source_key, remote_endpoint, question_id"
	if typ == store.TypeHistory {
		using += ", history_id"
	}
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT s.* FROM %s s JOIN %s m USING (%s) WHERE m.run_id = %d AND m.type = %s",
		vn, storeName, memName, using, run, sqlStr(typ))
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM %s WHERE false", vn, storeName)
	return viewStmt{name: vn, primary: primary, fallback: fallback}
}

// columnsClause renders a DuckDB columns={...} map with sorted keys. For
// read_json this is fine (matched by name); read_csv needs file order, so CSV
// callers use orderedColumnsClause.
func columnsClause(cols map[string]string) string {
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return renderColumns(keys, cols)
}

// orderedColumnsClause renders columns in the given order (read_csv matches the
// columns map by position, not by header name). Any columns not in order are
// appended sorted for stability.
func orderedColumnsClause(cols map[string]string, order []string) string {
	seen := map[string]bool{}
	keys := make([]string, 0, len(cols))
	for _, k := range order {
		if _, ok := cols[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range cols {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)
	return renderColumns(keys, cols)
}

func renderColumns(keys []string, cols map[string]string) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", sqlStr(k), sqlStr(cols[k])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// typedEmpty builds a zero-row SELECT with the given column schema.
func typedEmpty(cols map[string]string) string {
	keys := make([]string, 0, len(cols))
	for k := range cols {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("CAST(NULL AS %s) AS %s", cols[k], sqlIdent(k)))
	}
	return "SELECT " + strings.Join(parts, ", ") + " WHERE false"
}

func parseMemberKey(key string) (typ string, run int, ok bool) {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return "", 0, false
	}
	var r int
	if _, err := fmt.Sscanf(key[i+1:], "%d", &r); err != nil {
		return "", 0, false
	}
	return key[:i], r, true
}

// sqlStr single-quotes a string literal, doubling embedded quotes.
// isTextContentType reports whether an attachment's content is safe to expose as
// text via read_text (which requires valid UTF-8). Offloaded states are JSON;
// binary types (audio, images) are excluded. An unknown/blank type is excluded
// to stay safe against binary payloads.
func isTextContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "csv") ||
		strings.Contains(ct, "svg")
}

func sqlStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sqlIdent double-quotes an identifier, doubling embedded quotes.
func sqlIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// StaticViewNames returns the names of the views every dataset gets, derived by building
// the statement set over an empty manifest so the per-run and per-job views (which come
// from manifest entries) are excluded. Deriving it keeps the drift guard's inventory side
// from becoming a second hand-maintained list.
func StaticViewNames() []string {
	vs := viewSet{m: &dataset.Manifest{}}
	var names []string
	for _, st := range vs.statements() {
		names = append(names, strings.Trim(st.name, `"`))
	}
	return names
}

// applyMaterialized points a view at its Parquet when the manifest records one
// that is fresh. A stale entry is silent by contract; a missing or unreadable
// one is not, and the registration loop is what tells those apart.
func (vs viewSet) applyMaterialized(stmts []viewStmt) []viewStmt {
	if len(vs.m.Materialized) == 0 {
		return stmts
	}
	eligible := map[string]bool{}
	for _, n := range materializableFrom(stmts, vs.prefix) {
		eligible[n] = true
	}
	for i, st := range stmts {
		bare := bareViewName(st.name, vs.prefix)
		if !eligible[bare] {
			continue
		}
		mat, ok := vs.m.Materialized[bare]
		signature := viewSignature(st, vs.canonDir, vs.prefix)
		if !ok || !mat.Fresh(vs.canonDir, st.files, signature) {
			continue
		}
		stmts[i].materialized = fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM read_parquet(%s)",
			st.name, vs.file(mat.File))
		stmts[i].parquet = filepath.Join(vs.canonDir, mat.File)
		stmts[i].record = mat
		stmts[i].signature = signature
	}
	return stmts
}

// materializableViews returns the views of this dataset that can be
// materialized: those that scan files, minus the per-run views and the two
// attachment views. The first two halves are code-derived, so a view added
// later joins or stays out of the set by its own construction rather than by a
// list someone updates.
//
// It takes the manifest because "declares files" is a property of a populated
// dataset: over an empty manifest every builder returns its stand-in and no view
// declares anything.
func materializableViews(m *dataset.Manifest) []string {
	vs := viewSet{m: m}
	return materializableFrom(vs.statements(), vs.prefix)
}

// neverMaterialized names the views kept out of the set by hand. The attachment
// views read_text every attachment, so materializing them would copy the
// attachment corpus a second time for the two views with the largest output.
var neverMaterialized = map[string]bool{"attachment_states": true, "attachment_content": true}

// materializableFrom takes statements the caller has already built, so Open does
// not build the full view set twice: applyMaterialized has the slice in hand and
// passes it straight through. Those statements carry the schema prefix of
// whichever dataset they were built for, so the caller passes it too and the set
// stays keyed on bare names.
func materializableFrom(stmts []viewStmt, prefix string) []string {
	static := map[string]bool{}
	for _, n := range StaticViewNames() {
		static[n] = true
	}
	var out []string
	for _, st := range stmts {
		bare := bareViewName(st.name, prefix)
		if len(st.files) > 0 && static[bare] && !neverMaterialized[bare] {
			out = append(out, bare)
		}
	}
	return out
}

// viewSignature identifies the definition a Parquet was built from. Two things
// are neutralized first, because neither changes what the view returns: the
// dataset directory, since the statement embeds absolute paths and a
// materialized view has to survive a rename, and the schema prefix, since the
// same dataset registers unprefixed alone and prefixed in a multi-dataset
// session.
func viewSignature(st viewStmt, canonDir, prefix string) string {
	sql := st.primary
	if prefix != "" {
		sql = strings.ReplaceAll(sql, prefix, "")
	}
	if canonDir != "" {
		sql = strings.ReplaceAll(sql, canonDir, "<dataset>")
	}
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

// bareViewName is a statement's view name with its schema prefix and quoting
// removed. That is the form StaticViewNames produces and the form the manifest's
// Materialized map is keyed on, so every lookup against either goes through it.
func bareViewName(name, prefix string) string {
	return strings.Trim(strings.TrimPrefix(name, prefix), `"`)
}

// IdentityColumnNames returns the columns that identify a record across the stores, derived
// from the membership key so the drift guard's inventory side stays code-derived. Deliberately
// not contractStoreColumns, which also carries the internal _fetched_at/_run_id bookkeeping.
func IdentityColumnNames() []string {
	names := make([]string, 0, len(membershipColumns))
	for name := range membershipColumns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// dimensionView describes one slug-recognized join dimension: the report that feeds it, and the
// fixed schema its stand-in declares before any run of that report has been downloaded. The
// schema is known rather than sniffed, which is the premise these dedicated views rest on.
type dimensionView struct {
	name    string
	slug    string
	columns []dimensionColumn
	// hideNames adds the run's hide_names filter value as a column. Without it a dataset
	// holding runs fetched under different roles puts real names and numeric student ids in
	// one column with nothing to tell them apart.
	hideNames bool
}

type dimensionColumn struct{ name, typ string }

// run_remote_endpoint is last in both schemas because the dedupe wrapper rewrites it and so
// re-appends it, and a stand-in whose columns sat in a different order to the populated view
// would be a second shape for a caller to learn.
var dimensionViews = []dimensionView{
	{
		name: "student_id_mapping",
		slug: "student-id-mapping",
		columns: []dimensionColumn{
			{"learner_id", dataset.TypeBIGINT},
			{"user_id", dataset.TypeBIGINT},
			{"primary_user_id", dataset.TypeBIGINT},
			{"student_id", dataset.TypeVARCHAR},
			{"class_id", dataset.TypeBIGINT},
			{"offering_id", dataset.TypeBIGINT},
			{"runnable_url", dataset.TypeVARCHAR},
			{"run_remote_endpoint", dataset.TypeVARCHAR},
		},
	},
	{
		name:      "student_metadata",
		slug:      "student-metadata",
		hideNames: true,
		columns: []dimensionColumn{
			{"learner_id", dataset.TypeBIGINT},
			{"user_id", dataset.TypeBIGINT},
			{"primary_user_id", dataset.TypeBIGINT},
			{"student_id", dataset.TypeVARCHAR},
			{"class_id", dataset.TypeBIGINT},
			{"school_id", dataset.TypeBIGINT},
			{"student_name", dataset.TypeVARCHAR},
			{"username", dataset.TypeVARCHAR},
			{"class", dataset.TypeVARCHAR},
			{"school", dataset.TypeVARCHAR},
			{"teacher_user_ids", dataset.TypeVARCHAR},
			{"teacher_names", dataset.TypeVARCHAR},
			{"teacher_emails", dataset.TypeVARCHAR},
			{"teacher_districts", dataset.TypeVARCHAR},
			{"teacher_states", dataset.TypeVARCHAR},
			{"permission_forms", dataset.TypeVARCHAR},
			{"last_run", dataset.TypeVARCHAR},
			{"run_remote_endpoint", dataset.TypeVARCHAR},
		},
	},
}

const (
	dimensionKey      = "learner_id"
	dimensionRunID    = "run_id"
	dimensionEndpoint = "run_remote_endpoint"
	dimensionRecency  = "fetched_at"
	dimensionHideName = "hide_names"
)

// dimensionViewStmts builds the deduplicated join dimensions over the downloads that fed them,
// recognized by report slug through the manifest's provenance rather than by column sniffing.
func (vs viewSet) dimensionViewStmts() []viewStmt {
	stmts := make([]viewStmt, 0, len(dimensionViews))
	for _, d := range dimensionViews {
		stmts = append(stmts, vs.dimensionViewStmt(d))
	}
	return stmts
}

func (vs viewSet) dimensionViewStmt(d dimensionView) viewStmt {
	name := vs.prefix + sqlIdent(d.name)

	var admitted []dataset.Download
	var members, emptyMembers, files []string
	for _, dl := range vs.m.Downloads {
		if dl.Type != "report" || dl.Slug != d.slug || len(dl.Files) == 0 || dl.Columns == nil {
			continue
		}
		if fileMissing(filepath.Join(vs.canonDir, dl.Files[0])) {
			vs.warnf("report CSV %s is missing on disk; contributing zero rows to %s (run cc-data dataset reindex)", dl.Files[0], d.name)
			members = append(members, vs.dimensionEmptyMember(dl, d))
		} else {
			members = append(members, vs.dimensionScan(dl, d))
		}
		emptyMembers = append(emptyMembers, vs.dimensionEmptyMember(dl, d))
		admitted = append(admitted, dl)
		files = append(files, dl.Files...)
	}

	// A union with no members has nothing to deduplicate, and the wrapper's EXCLUDE cannot bind
	// against a stand-in, so it is not applied. That case is every dataset until the first such
	// run lands, and it is the shape StaticViewNames builds.
	if len(members) == 0 {
		standIn := fmt.Sprintf("CREATE VIEW %s AS %s", name, d.standIn())
		return viewStmt{name: name, primary: standIn, fallback: standIn}
	}

	order := dimensionOrderColumns(admitted)
	primary := fmt.Sprintf("CREATE VIEW %s AS %s", name, d.dedupe(strings.Join(members, "\nUNION ALL BY NAME\n"), order))
	fallback := fmt.Sprintf("CREATE VIEW %s AS %s", name, d.dedupe(strings.Join(emptyMembers, "\nUNION ALL BY NAME\n"), order))
	return viewStmt{name: name, primary: primary, fallback: fallback, files: files}
}

// dedupe keeps one row per learner, latest fetch winning, and withholds the join key of a learner
// with no secure key. Ordering by fetch time rather than run id is the point: a higher run id is a
// later-created run, where this store's model everywhere else is that the latest fetch wins.
//
// The order is closed with the remaining columns because the portal query groups by the
// report-learner row rather than by the learner id, so one run may legitimately emit a learner
// twice, and on fetch time and run id alone those two rows tie completely.
func (d dimensionView) dedupe(inner string, order []string) string {
	by := []string{sqlIdent(dimensionRecency) + " DESC", sqlIdent(dimensionRunID) + " DESC"}
	for _, col := range order {
		by = append(by, sqlIdent(col)+" ASC")
	}
	// A real endpoint always ends with a non-empty secure key, so the bare trailing-slash form
	// identifies the learners who have none. They share that one string, so joining on it
	// attributes one student's answers to every other such learner; NULL never joins, and the
	// learners stay in the dimension with every other column intact.
	endpoint := fmt.Sprintf("CASE WHEN %s LIKE %s THEN NULL ELSE %s END AS %s",
		sqlIdent(dimensionEndpoint), sqlStr("%/"), sqlIdent(dimensionEndpoint), sqlIdent(dimensionEndpoint))
	return fmt.Sprintf("SELECT * EXCLUDE (%s, %s), %s FROM (\n%s\n) QUALIFY ROW_NUMBER() OVER (PARTITION BY %s ORDER BY %s) = 1",
		sqlIdent(dimensionRecency), sqlIdent(dimensionEndpoint), endpoint, inner, sqlIdent(dimensionKey), strings.Join(by, ", "))
}

// standIn is the zero-member view: the full typed column list rather than a run_id-only shape, so
// the documented joins can be run on a dataset before anything has been downloaded.
func (d dimensionView) standIn() string {
	cols := []string{fmt.Sprintf("CAST(NULL AS BIGINT) AS %s", sqlIdent(dimensionRunID))}
	if d.hideNames {
		cols = append(cols, fmt.Sprintf("CAST(NULL AS BOOLEAN) AS %s", sqlIdent(dimensionHideName)))
	}
	for _, c := range d.columns {
		cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", c.typ, sqlIdent(c.name)))
	}
	return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
}

// dimensionScan reads one run's CSV, injecting the run id and the fetch time the dedupe orders by.
func (vs viewSet) dimensionScan(dl dataset.Download, d dimensionView) string {
	dialect := dataset.DefaultCSVDialect()
	if dl.CSVDialect != nil {
		dialect = *dl.CSVDialect
	}
	return fmt.Sprintf("SELECT %s, * FROM read_csv(%s, auto_detect=false, header=true, delim=%s, quote=%s, escape=%s, columns=%s)",
		strings.Join(vs.dimensionInjected(dl, d), ", "), vs.file(dl.Files[0]),
		sqlStr(dialect.Delim), sqlStr(dialect.Quote), sqlStr(dialect.Escape),
		orderedColumnsClause(dl.Columns, dl.ColumnOrder))
}

// dimensionEmptyMember is a zero-row member for a CSV that is missing or unreadable. It carries
// the fixed schema as well as the recorded columns, so the wrapper's EXCLUDE and PARTITION BY bind
// even for a download whose recorded columns are incomplete.
func (vs viewSet) dimensionEmptyMember(dl dataset.Download, d dimensionView) string {
	cols := vs.dimensionInjected(dl, d)
	seen := map[string]bool{}
	for _, c := range d.columns {
		typ := c.typ
		if recorded, ok := dl.Columns[c.name]; ok {
			typ = recorded
		}
		cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", typ, sqlIdent(c.name)))
		seen[c.name] = true
	}
	extra := make([]string, 0, len(dl.Columns))
	for name := range dl.Columns {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", dl.Columns[name], sqlIdent(name)))
	}
	return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
}

// dimensionInjected is the run id, the fetch time the dedupe orders by, and for the metadata view
// the run's hide_names, which every member carries whether it reads a CSV or stands in for one.
func (vs viewSet) dimensionInjected(dl dataset.Download, d dimensionView) []string {
	cols := []string{
		fmt.Sprintf("CAST(%d AS BIGINT) AS %s", dl.RunID, sqlIdent(dimensionRunID)),
		fmt.Sprintf("%s AS %s", sqlTimestamp(dl.FetchedAt), sqlIdent(dimensionRecency)),
	}
	if d.hideNames {
		cols = append(cols, fmt.Sprintf("%s AS %s", hideNamesLiteral(dl), sqlIdent(dimensionHideName)))
	}
	return cols
}

// dimensionOrderColumns is every column the union carries besides the partition key and the
// injected bookkeeping, which is what closes the dedupe's ordering. Derived from the recorded
// columns rather than from the fixed schema, so every name is bindable in the union.
func dimensionOrderColumns(admitted []dataset.Download) []string {
	seen := map[string]bool{dimensionKey: true, dimensionRunID: true, dimensionRecency: true, dimensionHideName: true}
	var cols []string
	for _, dl := range admitted {
		for name := range dl.Columns {
			if !seen[name] {
				cols = append(cols, name)
				seen[name] = true
			}
		}
	}
	sort.Strings(cols)
	return cols
}

// hideNamesLiteral reads the run's hide_names from the filter the download recorded. It is NULL
// wherever no filter is on disk: a download made by a version that stored none, and any one a
// manifest-less reindex recovered.
func hideNamesLiteral(dl dataset.Download) string {
	var filter struct {
		HideNames *bool `json:"hide_names"`
	}
	if len(dl.Filters) > 0 && json.Unmarshal(dl.Filters, &filter) == nil && filter.HideNames != nil {
		return fmt.Sprintf("CAST(%t AS BOOLEAN)", *filter.HideNames)
	}
	return "CAST(NULL AS BOOLEAN)"
}

// sqlTimestamp renders a time as a DuckDB TIMESTAMP literal in UTC.
func sqlTimestamp(t time.Time) string {
	return fmt.Sprintf("CAST(%s AS TIMESTAMP)", sqlStr(t.UTC().Format("2006-01-02 15:04:05.999999")))
}
