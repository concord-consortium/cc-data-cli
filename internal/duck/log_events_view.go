package duck

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// logEventColumns are the columns every student log source shares, in log_events' order: a
// Student Actions run's, which are the recent_logs store's source columns too. Their types are
// that store's pins.
var logEventColumns = []string{
	"id", "session", "application", "activity", "event", "event_value", "time",
	"parameters", "extras", "run_remote_endpoint", "timestamp", "user_id", "primary_user_id",
}

// logEventsView is one row per log event across every log run's CSV and the recent_logs store,
// matched on the id the log ingester gives an event and every copy of it carries. The row shows
// the newest fetch's copy.
func (vs viewSet) logEventsView() viewStmt {
	return vs.clueView("log_events", func(standIn bool) (string, []string) {
		var members, files []string
		for _, dl := range vs.m.Downloads {
			if dl.Type != "report" || dl.ReportType != dataset.ReportTypeLog || len(dl.Files) == 0 || dl.Columns == nil {
				continue
			}
			members = append(members, vs.logEventsCSVMember(dl, standIn))
			files = append(files, dl.Files...)
		}
		// logs and recent_logs already warn about their own missing files.
		src, _, storeFiles := vs.clueSource(store.TypeRecentLogs, standIn, false)
		members = append(members, logEventsMember([]string{"CAST(NULL AS BIGINT) AS run_id", "_class_id", "_fetched_at"},
			src, func(string) bool { return true }, false))
		files = append(files, storeFiles...)
		return logEventsSelect(strings.Join(members, "\nUNION ALL\n")), files
	})
}

// logEventsCSVMember reads one log run's CSV with every column as text, so a column's type
// never depends on what DetectCSV saw in that file, and converts the integer columns from it.
func (vs viewSet) logEventsCSVMember(dl dataset.Download, standIn bool) string {
	provenance := []string{
		fmt.Sprintf("CAST(%d AS BIGINT) AS run_id", dl.RunID),
		"CAST(NULL AS BIGINT) AS _class_id",
		sqlTimestamp(dl.FetchedAt) + " AS _fetched_at",
	}
	if standIn || fileMissing(filepath.Join(vs.canonDir, dl.Files[0])) {
		return logEventsMember(provenance, "", func(string) bool { return false }, false)
	}
	text := make(map[string]string, len(dl.Columns))
	for col := range dl.Columns {
		text[col] = dataset.TypeVARCHAR
	}
	has := func(col string) bool { _, ok := dl.Columns[col]; return ok }
	return logEventsMember(provenance, vs.csvRead(dl, text), has, true)
}

// logEventsMember selects the provenance columns and logEventColumns at the store's pinned
// types from src, or no rows when src is empty. A column the source lacks is a typed NULL;
// convert turns a text column into its pinned type, NULL where a value does not convert.
func logEventsMember(provenance []string, src string, has func(string) bool, convert bool) string {
	pins := store.MustKind(store.TypeRecentLogs).Pinned
	cols := append([]string{}, provenance...)
	for _, col := range logEventColumns {
		typ := pins[col]
		switch {
		case !has(col):
			cols = append(cols, fmt.Sprintf("CAST(NULL AS %s) AS %s", typ, sqlIdent(col)))
		case convert && typ != store.TypeVARCHAR:
			cols = append(cols, fmt.Sprintf("TRY_CAST(%s AS %s) AS %s", sqlIdent(col), typ, sqlIdent(col)))
		default:
			cols = append(cols, sqlIdent(col))
		}
	}
	if src == "" {
		return "SELECT " + strings.Join(cols, ", ") + " WHERE false"
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM " + src
}

// logEventsSelect keeps one row per id over the members' union. A NULL id would put every
// unmatched row in one partition, so those rows bypass the partition and carry only their own
// run. Only a CSV row can lack an id (the store refuses one), so such a row has a run and no
// class. Copies still tied after the order are one delivered record repeated within a CSV, which
// agree in every column carried here.
func logEventsSelect(union string) string {
	cols := make([]string, 0, len(logEventColumns)+len(logDerived)+3)
	for _, col := range logEventColumns {
		cols = append(cols, sqlIdent(col))
	}
	cols = append(cols,
		"CASE WHEN id IS NULL THEN [run_id] "+
			"ELSE coalesce(list_sort(list(DISTINCT run_id) FILTER (WHERE run_id IS NOT NULL) OVER (PARTITION BY id)), CAST([] AS BIGINT[])) END AS run_ids",
		"max(_class_id) OVER (PARTITION BY id) AS _class_id",
		"_fetched_at")
	for _, d := range logDerived {
		cols = append(cols, fmt.Sprintf("%s AS %s", d.expr(sqlIdent(d.src)), sqlIdent(d.name)))
	}
	return fmt.Sprintf("SELECT %s FROM (\n%s\n) QUALIFY id IS NULL OR ROW_NUMBER() OVER (PARTITION BY id ORDER BY _fetched_at DESC, run_id DESC NULLS LAST) = 1",
		strings.Join(cols, ", "), union)
}
