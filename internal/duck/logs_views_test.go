package duck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
)

// The instant every timestamp assertion pins, in both of the log's units.
const (
	pinnedSeconds = 1757462400
	pinnedMillis  = 1757462400872
	pinnedUTC     = "2025-09-10T00:00:00"
)

// The columns logs promises regardless of what any one CSV recorded.
var derivedNames = []string{"parameters_json", "extras_json", "event_time", "received_time"}

type logFixture struct {
	run    int
	slug   *string
	csv    string
	absent bool // record the download but leave no file on disk
}

func addLogCSV(t *testing.T, d *dataset.Dataset, f logFixture) {
	t.Helper()
	name := fmt.Sprintf("report_%d.csv", f.run)
	path := d.Path(name)
	if err := os.WriteFile(path, []byte(f.csv), 0o600); err != nil {
		t.Fatal(err)
	}
	rowCount, cols, order, dialect, err := dataset.DetectCSV(path, dataset.ReportTypeLog)
	if err != nil {
		t.Fatal(err)
	}
	if f.absent {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	slug := "student-actions"
	if f.slug != nil {
		slug = *f.slug
	}
	if err := d.UpsertDownload(dataset.Download{
		Type: "report", RunID: f.run, ReportType: dataset.ReportTypeLog, Slug: slug,
		Filters: json.RawMessage(`{}`), Files: []string{name}, RowCount: &rowCount,
		Columns: cols, ColumnOrder: order, CSVDialect: &dialect, Complete: true,
		FetchedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
}

// clueCSV is a row shaped like a real CLUE log event, plus whatever extra rows a test adds.
func clueCSV(rows ...string) string {
	out := "event,time,timestamp,parameters,extras\n"
	out += fmt.Sprintf("clickTile,%d,%d,\"{\"\"tileId\"\":\"\"t7\"\"}\",\"{\"\"selectedNavTab\"\":\"\"problems\"\",\"\"navTabsOpen\"\":true}\"\n",
		pinnedSeconds, pinnedMillis)
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}

func logsStmt(t *testing.T, d *dataset.Dataset) viewStmt {
	t.Helper()
	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	return viewSet{canonDir: d.Path(""), m: m}.logsView()
}

func TestLogsParsesClueParametersAndExtras(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	e, _ := openWithWarnings(t, d)

	if got := queryString(t, e, `SELECT extras_json->>'selectedNavTab' FROM logs`); got != "problems" {
		t.Errorf("selectedNavTab = %q, want %q", got, "problems")
	}
	if got := queryString(t, e, `SELECT extras_json->>'navTabsOpen' FROM logs`); got != "true" {
		t.Errorf("navTabsOpen = %q, want %q", got, "true")
	}
	if got := queryString(t, e, `SELECT parameters_json->>'tileId' FROM logs`); got != "t7" {
		t.Errorf("tileId = %q, want %q", got, "t7")
	}
	// The parse is additive: the source strings stay queryable under their own names.
	if got := queryString(t, e, `SELECT parameters FROM logs`); got != `{"tileId":"t7"}` {
		t.Errorf("parameters = %q, want the original string", got)
	}
}

func TestLogsMalformedExtrasIsNullNotAnError(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV(
		fmt.Sprintf(`bad,%d,%d,"{""tileId"":""t8""}",not json`, pinnedSeconds, pinnedMillis))})
	e, _ := openWithWarnings(t, d)

	if got := queryString(t, e, `SELECT coalesce(extras_json->>'selectedNavTab', '<NULL>') FROM logs WHERE event = 'bad'`); got != "<NULL>" {
		t.Errorf("unparsable extras yielded %q, want a null", got)
	}
	// The same row's other JSON column still parses, so the failure is per column.
	if got := queryString(t, e, `SELECT parameters_json->>'tileId' FROM logs WHERE event = 'bad'`); got != "t8" {
		t.Errorf("tileId on the malformed-extras row = %q, want %q", got, "t8")
	}
}

// The milliseconds reading of the pinned value is the year 57661, silently, so the two
// timestamps are pinned in opposite directions: event_time must not divide, received_time must.
func TestLogsTimestampsPinBothEpochReadings(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	e, _ := openWithWarnings(t, d)

	if got := queryString(t, e, `SELECT strftime(event_time, '%Y-%m-%dT%H:%M:%S') FROM logs`); got != pinnedUTC {
		t.Errorf("event_time = %q, want %q", got, pinnedUTC)
	}
	want := pinnedUTC + ".872"
	if got := queryString(t, e, `SELECT strftime(received_time, '%Y-%m-%dT%H:%M:%S.%g') FROM logs`); got != want {
		t.Errorf("received_time = %q, want %q", got, want)
	}
}

// A TIMESTAMPTZ would render identically on a UTC machine, so the type is asserted through
// DESCRIBE and the value through a naive-timestamp comparison, never through ::VARCHAR.
func TestLogsTimestampsAreUTCValuedTimestamps(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	e, _ := openWithWarnings(t, d)

	for _, col := range []string{"event_time", "received_time"} {
		got := queryString(t, e, fmt.Sprintf(`SELECT column_type FROM (DESCRIBE logs) WHERE column_name = '%s'`, col))
		if got != "TIMESTAMP" {
			t.Errorf("%s is %s, want TIMESTAMP", col, got)
		}
	}
	if got := queryString(t, e, `SELECT (event_time = TIMESTAMP '`+pinnedUTC+`')::VARCHAR FROM logs`); got != "true" {
		t.Errorf("event_time did not equal the UTC instant as a naive timestamp (got %q)", got)
	}
	// The stores declare their own time column the same way, which is what makes the two
	// directly comparable; a TIMESTAMPTZ here would be an offset apart from _fetched_at.
	stored := queryString(t, e, `SELECT column_type FROM (DESCRIBE answers) WHERE column_name = '_fetched_at'`)
	derived := queryString(t, e, `SELECT column_type FROM (DESCRIBE logs) WHERE column_name = 'event_time'`)
	if stored != derived {
		t.Errorf("logs.event_time is %s but answers._fetched_at is %s", derived, stored)
	}
}

// DetectCSV types each column per file, so one dataset can hold a BIGINT time and a VARCHAR one.
// to_timestamp on a VARCHAR is a binder error, which would cost the whole view rather than a column.
func TestLogsHandlesVarcharTimeColumn(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	addLogCSV(t, d, logFixture{run: 2, csv: "event,time,timestamp,parameters,extras\n" +
		fmt.Sprintf("numeric,%d,%d,{},{}\n", pinnedSeconds, pinnedMillis) +
		fmt.Sprintf("notanumber,notatime,%d,{},{}\n", pinnedMillis)})

	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	types := map[int]string{}
	for _, dl := range m.Downloads {
		types[dl.RunID] = dl.Columns["time"]
	}
	if types[1] != dataset.TypeBIGINT || types[2] != dataset.TypeVARCHAR {
		t.Fatalf("fixture did not produce the two detected types (run 1 %q, run 2 %q), so the test proves nothing", types[1], types[2])
	}

	e, _ := openWithWarnings(t, d)
	if got := queryString(t, e, `SELECT strftime(event_time, '%Y-%m-%dT%H:%M:%S') FROM logs WHERE event = 'numeric'`); got != pinnedUTC {
		t.Errorf("event_time from a VARCHAR-typed column = %q, want %q", got, pinnedUTC)
	}
	if got := queryString(t, e, `SELECT coalesce(strftime(event_time, '%Y-%m-%dT%H:%M:%S'), '<NULL>') FROM logs WHERE event = 'notanumber'`); got != "<NULL>" {
		t.Errorf("a non-numeric time yielded %q, want a null", got)
	}
	// The run that could not type its time still contributes its other columns.
	if n := queryInt(t, e, `SELECT count(*) FROM logs WHERE run_id = 2`); n != 2 {
		t.Errorf("run 2 contributed %d rows, want 2", n)
	}
}

// recoverReportType admits a CSV to the log type on event and time alone, so a member can
// lack parameters entirely and referencing it would fail the view for every other run.
func TestLogsMemberLackingParametersStillContributes(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	addLogCSV(t, d, logFixture{run: 2, csv: "event,time,extras\n" +
		fmt.Sprintf("noparams,%d,\"{\"\"selectedNavTab\"\":\"\"x\"\"}\"\n", pinnedSeconds)})
	e, _ := openWithWarnings(t, d)

	if got := queryString(t, e, `SELECT coalesce(parameters_json->>'tileId', '<NULL>') FROM logs WHERE event = 'noparams'`); got != "<NULL>" {
		t.Errorf("parameters_json on a member with no parameters column = %q, want a null", got)
	}
	if got := queryString(t, e, `SELECT extras_json->>'selectedNavTab' FROM logs WHERE event = 'noparams'`); got != "x" {
		t.Errorf("the member's own columns did not survive: selectedNavTab = %q", got)
	}
	// timestamp is absent from that CSV too, so the other typed-NULL branch is covered.
	if got := queryString(t, e, `SELECT coalesce(strftime(received_time, '%Y-%m-%d'), '<NULL>') FROM logs WHERE event = 'noparams'`); got != "<NULL>" {
		t.Errorf("received_time with no timestamp column = %q, want a null", got)
	}
}

// reindex recovers the log type from a CSV whose provenance is gone, so admission is by
// report type. A slug test here would drop rows the reports view still shows.
func TestLogsAdmitsASluglessRecoveredCSV(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, slug: new(string), csv: clueCSV()})

	m, err := d.ReadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Downloads[0].Slug; got != "" {
		t.Fatalf("fixture recorded slug %q, so it does not exercise the recovered case", got)
	}

	e, _ := openWithWarnings(t, d)
	if got := queryString(t, e, `SELECT extras_json->>'selectedNavTab' FROM logs`); got != "problems" {
		t.Errorf("a slugless log CSV contributed %q, want its parsed row", got)
	}
	if n := queryInt(t, e, `SELECT count(*) FROM logs`); n != 1 {
		t.Errorf("a slugless log CSV contributed %d rows, want 1", n)
	}
}

func TestLogsMissingCSVKeepsTheDerivedColumns(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV(), absent: true})
	e, warn := openWithWarnings(t, d)

	if !strings.Contains(warn, "missing on disk") {
		t.Errorf("no degradation warning for the missing CSV: %q", warn)
	}
	for _, col := range derivedNames {
		if n := queryInt(t, e, fmt.Sprintf(`SELECT count(*) FROM (DESCRIBE logs) WHERE column_name = '%s'`, col)); n != 1 {
			t.Errorf("logs lost %s when its only CSV was missing", col)
		}
	}
	if n := queryInt(t, e, `SELECT count(*) FROM logs`); n != 0 {
		t.Errorf("missing CSV contributed %d rows, want 0", n)
	}
}

// An unskipped duplicate fails differently by member count, so one case without the other
// tests half the guard: a lone member has no UNION and CREATE VIEW renames the second column
// to event_time_1, while two members are a binder error that fails the fallback too. Both are
// asserted on the name prefix, since the exact name stays unique either way.
func TestLogsDoesNotDuplicateAColidingSourceColumn(t *testing.T) {
	collides := "event,time,event_time\n" + fmt.Sprintf("z,%d,2025-01-01\n", pinnedSeconds)

	t.Run("one member binds and yields one column", func(t *testing.T) {
		d := newDS(t, "ds")
		addLogCSV(t, d, logFixture{run: 1, csv: collides})
		e, _ := openWithWarnings(t, d)
		if n := queryInt(t, e, `SELECT count(*) FROM (DESCRIBE logs) WHERE column_name LIKE 'event_time%'`); n != 1 {
			t.Errorf("logs declares %d event_time columns, want 1; a second would be renamed event_time_1", n)
		}
		if got := queryString(t, e, `SELECT column_type FROM (DESCRIBE logs) WHERE column_name = 'event_time'`); got != dataset.TypeVARCHAR {
			t.Errorf("event_time is %s, want the CSV's own %s: the derived column displaced the source one", got, dataset.TypeVARCHAR)
		}
	})

	t.Run("two members still install the view", func(t *testing.T) {
		d := newDS(t, "ds")
		addLogCSV(t, d, logFixture{run: 1, csv: collides})
		addLogCSV(t, d, logFixture{run: 2, csv: clueCSV()})
		e, _ := openWithWarnings(t, d)
		if n := queryInt(t, e, `SELECT count(*) FROM logs`); n != 2 {
			t.Errorf("logs returned %d rows, want 2; a duplicate name would have failed both statements", n)
		}
		if n := queryInt(t, e, `SELECT count(*) FROM (DESCRIBE logs) WHERE column_name LIKE 'event_time%'`); n != 1 {
			t.Errorf("logs declares %d event_time columns, want 1", n)
		}
	})

	// csvEmptyMember builds the fallback, so the skip has to run there too or the fallback
	// carries the duplicate and Open refuses the whole dataset rather than one view.
	t.Run("the stand-in skips it too", func(t *testing.T) {
		d := newDS(t, "ds")
		addLogCSV(t, d, logFixture{run: 1, csv: collides, absent: true})
		e, _ := openWithWarnings(t, d)
		if n := queryInt(t, e, `SELECT count(*) FROM (DESCRIBE logs) WHERE column_name LIKE 'event_time%'`); n != 1 {
			t.Errorf("the stand-in declares %d event_time columns, want 1", n)
		}
		if got := strings.Count(logsStmt(t, d).fallback, "AS \"event_time\""); got != 1 {
			t.Errorf("the fallback declares event_time %d times, want 1", got)
		}
	})
}

// The fallback only runs when the primary CREATE VIEW fails at install time, so it is
// asserted on the generated statement rather than by corrupting a CSV.
func TestLogsFallbackDeclaresTheDerivedColumns(t *testing.T) {
	d := newDS(t, "ds")
	addLogCSV(t, d, logFixture{run: 1, csv: clueCSV()})
	fallback := logsStmt(t, d).fallback

	for _, col := range derivedNames {
		if !strings.Contains(fallback, `AS "`+col+`"`) {
			t.Errorf("the logs fallback does not declare %s:\n%s", col, fallback)
		}
	}
}

func TestLogsOnADatasetWithNoLogRuns(t *testing.T) {
	d := newDS(t, "ds")
	e, _ := openWithWarnings(t, d)

	for _, col := range []string{"run_id", "parameters_json", "extras_json", "event_time", "received_time"} {
		if n := queryInt(t, e, fmt.Sprintf(`SELECT count(*) FROM (DESCRIBE logs) WHERE column_name = '%s'`, col)); n != 1 {
			t.Errorf("logs does not declare %s before any log run has been downloaded", col)
		}
	}
	// The documented query has to bind, not error, on an empty dataset.
	if got := queryStrings(t, e, `SELECT extras_json->>'selectedNavTab' FROM logs`); len(got) != 0 {
		t.Errorf("empty logs returned %d rows, want 0", len(got))
	}
	// It declares no source columns: those depend on what was downloaded.
	if n := queryInt(t, e, `SELECT count(*) FROM (DESCRIBE logs)`); n != 5 {
		t.Errorf("empty logs declares %d columns, want run_id plus the four derived", n)
	}
}

// DuckDB's read_csv caps the whole line at 2,000,000 bytes, not a field at Python's 128 KB,
// which is why no line-size option is set. This is the boundary that makes it unnecessary.
func TestLogsOversizedParametersFieldLoadsUnconfigured(t *testing.T) {
	d := newDS(t, "ds")
	big := strings.Repeat("x", 130*1024)
	addLogCSV(t, d, logFixture{run: 1, csv: "event,time,timestamp,parameters,extras\n" +
		fmt.Sprintf("big,%d,%d,\"{\"\"blob\"\":\"\"%s\"\"}\",\"{\"\"selectedNavTab\"\":\"\"problems\"\"}\"\n",
			pinnedSeconds, pinnedMillis, big)})
	e, _ := openWithWarnings(t, d)

	if n := queryInt(t, e, `SELECT length(parameters_json->>'blob') FROM logs`); n != len(big) {
		t.Errorf("oversized parameters field came back %d bytes, want %d", n, len(big))
	}
	if got := queryString(t, e, `SELECT extras_json->>'selectedNavTab' FROM logs`); got != "problems" {
		t.Errorf("the oversized row's other columns did not survive: %q", got)
	}
}

// The statement reportsView produces for a caller that passes no derived columns.
// Regenerating this from the current code would make the test unable to fail, so it is
// updated only when reports is deliberately changed.
const reportsGolden = `CREATE VIEW "reports" AS SELECT 7 AS run_id, * FROM read_csv('DIR/a.csv', auto_detect=false, header=true, delim=',', quote='"', escape='"', columns={'student_id': 'VARCHAR', 'q1': 'VARCHAR'}) WHERE student_id::VARCHAR NOT IN ('Prompt', 'Correct answer')
UNION ALL BY NAME
SELECT CAST(9 AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS "event", CAST(NULL AS BIGINT) AS "time" WHERE false`

const reportsFallbackGolden = `CREATE VIEW "reports" AS SELECT CAST(7 AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS "student_id", CAST(NULL AS VARCHAR) AS "q1" WHERE false
UNION ALL BY NAME
SELECT CAST(9 AS BIGINT) AS run_id, CAST(NULL AS VARCHAR) AS "event", CAST(NULL AS BIGINT) AS "time" WHERE false`

func TestReportsViewSQLIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("student_id,q1\n1,x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vs := viewSet{canonDir: dir, m: &dataset.Manifest{Downloads: []dataset.Download{
		{Type: "report", RunID: 7, ReportType: dataset.ReportTypeAnswers, Files: []string{"a.csv"},
			Columns: map[string]string{"student_id": dataset.TypeVARCHAR, "q1": dataset.TypeVARCHAR}, ColumnOrder: []string{"student_id", "q1"}},
		{Type: "report", RunID: 9, ReportType: dataset.ReportTypeLog, Files: []string{"gone.csv"},
			Columns: map[string]string{"event": dataset.TypeVARCHAR, "time": dataset.TypeBIGINT}, ColumnOrder: []string{"event", "time"}},
	}}}
	st := vs.reportsView()
	if got := strings.ReplaceAll(st.primary, dir, "DIR"); got != reportsGolden {
		t.Errorf("reports primary changed:\ngot  %s\nwant %s", got, reportsGolden)
	}
	if got := strings.ReplaceAll(st.fallback, dir, "DIR"); got != reportsFallbackGolden {
		t.Errorf("reports fallback changed:\ngot  %s\nwant %s", got, reportsFallbackGolden)
	}
}
