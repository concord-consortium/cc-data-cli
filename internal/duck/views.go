package duck

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	name     string
	primary  string
	fallback string
	files    []string // files the primary reads (for the degradation warning)
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
	stmts = append(stmts, vs.storeView(store.TypeAnswers))
	stmts = append(stmts, vs.storeView(store.TypeHistory))
	stmts = append(stmts, vs.runMembershipView())
	stmts = append(stmts, vs.downloadsView())
	stmts = append(stmts, vs.attachmentFilesView())
	stmts = append(stmts, vs.attachmentStatesView())
	stmts = append(stmts, vs.attachmentContentView())
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
	return vs.reportUnionView(`"reports"`, true, func(dl dataset.Download) bool {
		if !dataset.IsAllowedReportType(dl.ReportType) {
			vs.warnf("run %d report_type %q is unknown to this cc-data version; excluded from the reports view (upgrade suggested)", dl.RunID, dl.ReportType)
			return false
		}
		return true
	})
}

// reportPromptsView exposes the two pseudo-header rows of answers-type CSVs.
func (vs viewSet) reportPromptsView() viewStmt {
	return vs.reportUnionView(`"report_prompts"`, false, func(dl dataset.Download) bool {
		return dl.ReportType == dataset.ReportTypeAnswers
	})
}

// reportUnionView builds a union over the report CSVs the include predicate
// admits, using a typed-empty stand-in for any CSV whose file is missing so one
// broken artifact costs its rows, never the session or the union's schema.
func (vs viewSet) reportUnionView(bare string, keepData bool, include func(dataset.Download) bool) viewStmt {
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
			members = append(members, vs.csvEmptyMember(dl))
		} else {
			members = append(members, vs.csvScan(dl, keepData))
		}
		// The typed-empty schema for every admitted member (present or missing).
		emptyMembers = append(emptyMembers, vs.csvEmptyMember(dl))
		files = append(files, dl.Files...)
	}
	name := vs.prefix + bare
	runOnly := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: runOnly, fallback: runOnly}
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
// run_id, so a missing CSV keeps its columns in the union.
func (vs viewSet) csvEmptyMember(dl dataset.Download) string {
	cols := make([]string, 0, len(dl.Columns)+1)
	cols = append(cols, fmt.Sprintf("CAST(%d AS BIGINT) AS run_id", dl.RunID))
	order := dl.ColumnOrder
	if len(order) == 0 {
		for k := range dl.Columns {
			order = append(order, k)
		}
		sort.Strings(order)
	}
	for _, k := range order {
		if t, ok := dl.Columns[k]; ok {
			cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", t, sqlIdent(k)))
		}
	}
	return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

// csvScan builds one per-run CSV SELECT. When keepData is true the pseudo-header
// rows are filtered out (answers-type only); when false only they are kept.
func (vs viewSet) csvScan(dl dataset.Download, keepData bool) string {
	dialect := dataset.DefaultCSVDialect()
	if dl.CSVDialect != nil {
		dialect = *dl.CSVDialect
	}
	scan := fmt.Sprintf("SELECT %d AS run_id, * FROM read_csv(%s, auto_detect=false, header=true, delim=%s, quote=%s, escape=%s, columns=%s)",
		dl.RunID, vs.file(dl.Files[0]), sqlStr(dialect.Delim), sqlStr(dialect.Quote), sqlStr(dialect.Escape), orderedColumnsClause(dl.Columns, dl.ColumnOrder))
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
func (vs viewSet) downloadsView() viewStmt {
	name := vs.prefix + `"downloads"`
	header := "(run_id, type, slug, report_type, complete)"
	var rows []string
	for _, dl := range vs.m.Downloads {
		rows = append(rows, fmt.Sprintf("(%d, %s, %s, %s, %t)", dl.RunID, sqlStr(dl.Type), sqlStr(dl.Slug), sqlStr(dl.ReportType), dl.Complete))
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS type, CAST(NULL AS VARCHAR) AS slug, CAST(NULL AS VARCHAR) AS report_type, CAST(NULL AS BOOLEAN) AS complete WHERE false", name)
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
	var members []string
	for _, af := range vs.m.Attachments {
		if !af.State {
			continue
		}
		members = append(members, fmt.Sprintf(
			"SELECT %s AS id12, %s AS name, filename, content FROM read_text(%s)",
			sqlStr(af.ID12), sqlStr(af.Name), vs.file(af.File)))
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS VARCHAR) AS filename, CAST(NULL AS VARCHAR) AS id12, CAST(NULL AS VARCHAR) AS name, CAST(NULL AS VARCHAR) AS content, CAST(NULL AS JSON) AS state WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	inner := strings.Join(members, "\nUNION ALL BY NAME\n")
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT filename, id12, name, content, TRY_CAST(content AS JSON) AS state FROM (%s)", name, inner)
	return viewStmt{name: name, primary: primary, fallback: fallback}
}

// attachmentContentView exposes the text content of every downloaded attachment
// (regardless of the current-answer State flag), so historical/offloaded states
// - e.g. every saved CODAP/SageModeler snapshot across a session's history, not
// just the one the current answer points at - are queryable and diffable.
// attachment_states stays as the narrow current-answer view for back-compat.
func (vs viewSet) attachmentContentView() viewStmt {
	name := vs.prefix + `"attachment_content"`
	var members []string
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
	}
	fallback := fmt.Sprintf("CREATE VIEW %s AS SELECT CAST(NULL AS VARCHAR) AS id12, CAST(NULL AS VARCHAR) AS name, CAST(NULL AS VARCHAR) AS source, CAST(NULL AS VARCHAR) AS public_path, CAST(NULL AS VARCHAR) AS filename, CAST(NULL AS VARCHAR) AS content, CAST(NULL AS JSON) AS state WHERE false", name)
	if len(members) == 0 {
		return viewStmt{name: name, primary: fallback, fallback: fallback}
	}
	inner := strings.Join(members, "\nUNION ALL BY NAME\n")
	primary := fmt.Sprintf("CREATE VIEW %s AS SELECT id12, name, source, public_path, filename, content, TRY_CAST(content AS JSON) AS state FROM (%s)", name, inner)
	return viewStmt{name: name, primary: primary, fallback: fallback}
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
				stmts = append(stmts, viewStmt{name: vn, primary: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvScan(dl, true)), fallback: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvEmptyMember(dl)), files: dl.Files})
			}
		case "report_job":
			if len(dl.Files) > 0 && dl.Columns != nil && dl.JobID != nil {
				vn := vs.prefix + sqlIdent(fmt.Sprintf("report_%d_job_%d", dl.RunID, *dl.JobID))
				stmts = append(stmts, viewStmt{name: vn, primary: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvScan(dl, true)), fallback: fmt.Sprintf("CREATE VIEW %s AS %s", vn, vs.csvEmptyMember(dl)), files: dl.Files})
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
