package duck

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// logEventsNames and logEventsTypes are log_events' columns in order and their types, whatever
// the dataset holds.
var (
	logEventsNames = []string{
		"id", "session", "application", "activity", "event", "event_value", "time",
		"parameters", "extras", "run_remote_endpoint", "timestamp", "user_id", "primary_user_id",
		"run_ids", "_class_id", "_fetched_at",
		"parameters_json", "extras_json", "event_time", "received_time",
	}
	logEventsTypes = map[string]string{
		"id": "VARCHAR", "session": "VARCHAR", "application": "VARCHAR", "activity": "VARCHAR",
		"event": "VARCHAR", "event_value": "VARCHAR", "time": "BIGINT", "parameters": "VARCHAR",
		"extras": "VARCHAR", "run_remote_endpoint": "VARCHAR", "timestamp": "BIGINT", "user_id": "BIGINT",
		"primary_user_id": "BIGINT", "run_ids": "BIGINT[]", "_class_id": "BIGINT", "_fetched_at": "TIMESTAMP",
		"parameters_json": "JSON", "extras_json": "JSON", "event_time": "TIMESTAMP", "received_time": "TIMESTAMP",
	}
)

const (
	logColumnsCSV      = "id,session,application,activity,event,event_value,time,parameters,extras,run_remote_endpoint,timestamp"
	studentActionsCSV  = logColumnsCSV + ",user_id,primary_user_id\n"
	withMetadataCSV    = logColumnsCSV + ",learner_id,class_id,runnable_url,student_id,class,school,user_id,primary_user_id,offering_id,permission_forms,username,student_name,teachers,last_run\n"
	teacherActionsCSV  = logColumnsCSV + ",username\n"
	logEventsEndpoint  = "https://learn.example/learners/1"
	withMetadataSlug   = "student-actions-with-metadata"
	teacherActionsSlug = "teacher-actions"
)

func logColumnsRow(id, eventValue, clientTime, endpoint string) string {
	return fmt.Sprintf("%s,s1,CLUE,act,e,%s,%s,{},{},%s,%d", id, eventValue, clientTime, endpoint, pinnedMillis)
}

func studentActionsRow(id, eventValue, clientTime string) string {
	return logColumnsRow(id, eventValue, clientTime, logEventsEndpoint) + ",7,\n"
}

func withMetadataRow(id string) string {
	return logColumnsRow(id, "v", fmt.Sprint(pinnedSeconds), logEventsEndpoint) +
		",1,590,https://act/1,81,Class,School,7,,70,,hash,81,Teacher,2026-09-01\n"
}

// logEventsFixture holds three overlapping student runs, a Teacher Actions run and a recent_logs
// pull. fetched dates each run from its day number.
// logEventsClass is the class whose recent-logs pull the fixture lands.
const logEventsClass = 590

func logEventsFixture(t *testing.T, name string, fetched func(day int) time.Time) *dataset.Dataset {
	t.Helper()
	d := newDS(t, name)
	sec := fmt.Sprint(pinnedSeconds)
	addLogCSV(t, d, logFixture{run: 1, fetchedAt: fetched(10), csv: studentActionsCSV +
		studentActionsRow("a", "007", sec) + studentActionsRow("b", "007", sec) + studentActionsRow("", "007", sec)})
	addLogCSV(t, d, logFixture{run: 2, fetchedAt: fetched(11), csv: studentActionsCSV +
		studentActionsRow("b", "v", sec) + studentActionsRow("c", "v", sec) + studentActionsRow("", "v", sec) +
		studentActionsRow("d", "v", "oops")})
	meta := withMetadataSlug
	addLogCSV(t, d, logFixture{run: 3, slug: &meta, fetchedAt: fetched(9), csv: withMetadataCSV +
		withMetadataRow("c") + withMetadataRow("007") + withMetadataRow("007")})
	teacher := teacherActionsSlug
	addLogCSV(t, d, logFixture{run: 4, slug: &teacher, fetchedAt: fetched(9), csv: teacherActionsCSV +
		logColumnsRow("t", "v", sec, "") + ",9@learn.concord.org\n"})
	land(t, d, store.TypeRecentLogs, logEventsClass, logEvent("b", "{}", pinnedMillis), logEvent("r", "{}", pinnedMillis))
	if err := d.UpsertDownload(dataset.Download{Type: store.TypeRecentLogs, ClassID: logEventsClass, Complete: true}); err != nil {
		t.Fatal(err)
	}
	return d
}

func septemberNoon(day int) time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) }

// land stamps a store's rows at time.Unix(<class id>, 0), so runs fetched at time.Unix(1, 0) are older.
func beforeTheStore(int) time.Time { return time.Unix(1, 0) }

func fetchedAllAt(at time.Time) func(int) time.Time { return func(int) time.Time { return at } }

func openLogEvents(t *testing.T) *Engine {
	t.Helper()
	return openEngine(t, []DatasetSpec{{DS: logEventsFixture(t, "events", septemberNoon)}}, nil)
}

func describeNames(t *testing.T, e *Engine, rel string) []string {
	t.Helper()
	return queryStrings(t, e, "SELECT column_name FROM (DESCRIBE SELECT * FROM "+rel+")")
}

func TestLogEventsHasOneRowPerEvent(t *testing.T) {
	e := openLogEvents(t)
	if n := queryInt(t, e, "SELECT count(*) FROM log_events"); n != 9 {
		t.Errorf("log_events has %d rows, want 9", n)
	}
	sources := `SELECT count(DISTINCT id) + count(*) FILTER (WHERE id IS NULL) FROM (
		SELECT id::VARCHAR AS id FROM logs UNION ALL SELECT id FROM recent_logs)`
	if want, got := queryInt(t, e, sources), queryInt(t, e, "SELECT count(*) FROM log_events"); got != want {
		t.Errorf("log_events has %d rows, want the sources' %d distinct ids plus their id-less rows", got, want)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM log_events WHERE id IS NOT NULL"); n != queryInt(t, e, "SELECT count(DISTINCT id) FROM log_events") {
		t.Error("an id appears more than once in log_events")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM logs"); n != 11 {
		t.Errorf("logs has %d rows, want one per run per event (11)", n)
	}
	got := queryStrings(t, e, "SELECT id::VARCHAR || ':' || count(*) FROM logs WHERE id IS NOT NULL GROUP BY id HAVING count(*) > 1 ORDER BY 1")
	if want := []string{"007:2", "b:2", "c:2"}; !slices.Equal(got, want) {
		t.Errorf("ids logs holds more than once: %v, want %v", got, want)
	}
}

func TestLogEventsKeepsRowsWithNoID(t *testing.T) {
	e := openLogEvents(t)
	got := queryStrings(t, e, "SELECT run_ids::VARCHAR FROM log_events WHERE id IS NULL ORDER BY 1")
	if want := []string{"[1]", "[2]"}; !slices.Equal(got, want) {
		t.Errorf("rows with no id have run_ids %v, want %v", got, want)
	}
}

func TestLogEventsListsWhereAnEventIs(t *testing.T) {
	e := openLogEvents(t)
	for id, want := range map[string]string{
		"b":   fmt.Sprint("[1, 2]|", logEventsClass),
		"c":   "[2, 3]|<NULL>",
		"r":   fmt.Sprint("[]|", logEventsClass),
		"007": "[3]|<NULL>",
	} {
		got := queryString(t, e, "SELECT coalesce(run_ids::VARCHAR, '<NULL>') || '|' || coalesce(_class_id::VARCHAR, '<NULL>') FROM log_events WHERE id = '"+id+"'")
		if got != want {
			t.Errorf("%s: run_ids|_class_id = %s, want %s", id, got, want)
		}
	}
}

func TestLogEventsShowsTheNewestCopy(t *testing.T) {
	const query = "SELECT strftime(_fetched_at, '%Y-%m-%d %H:%M:%S') FROM log_events WHERE id = 'b'"
	if got := queryString(t, openLogEvents(t), query); got != "2026-09-11 12:00:00" {
		t.Errorf("b shows the copy fetched %s, want run 2's", got)
	}
	early := openEngine(t, []DatasetSpec{{DS: logEventsFixture(t, "early", beforeTheStore)}}, nil)
	if got := queryString(t, early, query); got != time.Unix(logEventsClass, 0).UTC().Format(time.DateTime) {
		t.Errorf("b shows the copy fetched %s, want the store's", got)
	}
}

// b's event_value is 007 in run 1, v in run 2 and NULL in the store, so it names the copy shown
// when fetch times tie.
func TestLogEventsBreaksTiesOnRunID(t *testing.T) {
	const query = "SELECT coalesce(event_value, '<NULL>') FROM log_events WHERE id = 'b'"
	for name, at := range map[string]time.Time{
		"runs tied after the store": septemberNoon(10),
		"runs tied with the store":  time.Unix(logEventsClass, 0),
	} {
		e := openEngine(t, []DatasetSpec{{DS: logEventsFixture(t, "tied", fetchedAllAt(at))}}, nil)
		if got := queryString(t, e, query); got != "v" {
			t.Errorf("%s: b shows event_value %s, want run 2's v", name, got)
		}
	}
}

func TestLogEventsTypes(t *testing.T) {
	runsOnly := newDS(t, "runs")
	addLogCSV(t, runsOnly, logFixture{run: 1, csv: studentActionsCSV + studentActionsRow("a", "007", fmt.Sprint(pinnedSeconds))})
	for name, d := range map[string]*dataset.Dataset{
		"the fixture":      logEventsFixture(t, "events", septemberNoon),
		"an empty dataset": newDS(t, "empty"),
		"runs only":        runsOnly,
		"recent_logs only": recentLogsFixture(t),
	} {
		e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
		if got := describeNames(t, e, "log_events"); !slices.Equal(got, logEventsNames) {
			t.Errorf("%s: columns %v, want %v", name, got, logEventsNames)
		}
		if got := columnTypes(t, e, "log_events"); !maps.Equal(got, logEventsTypes) {
			t.Errorf("%s: types %v, want %v", name, got, logEventsTypes)
		}
	}

	e := openLogEvents(t)
	if got := queryString(t, e, "SELECT event_value FROM log_events WHERE id = 'a'"); got != "007" {
		t.Errorf("a's event_value = %q, want the CSV's text 007", got)
	}
	if got := queryString(t, e, "SELECT coalesce(time::VARCHAR, '<NULL>') FROM log_events WHERE id = 'd'"); got != "<NULL>" {
		t.Errorf("d's time = %s, want NULL for oops", got)
	}
	if n := queryInt(t, e, "SELECT sum(time) FROM log_events"); n == 0 {
		t.Error("sum(time) is 0")
	}
}

func TestLogEventsTeacherRows(t *testing.T) {
	e := openLogEvents(t)
	if got := queryString(t, e, "SELECT coalesce(user_id::VARCHAR, '<NULL>') || '|' || coalesce(run_remote_endpoint, '<NULL>') FROM log_events WHERE id = 't'"); got != "<NULL>|<NULL>" {
		t.Errorf("the teacher event's user_id|run_remote_endpoint = %s, want both NULL", got)
	}
	if slices.Contains(describeNames(t, e, "log_events"), "username") {
		t.Error("log_events has a username column")
	}
	if n := queryInt(t, e, "SELECT count(*) FILTER (WHERE user_id IS NOT NULL) FROM log_events"); n != 8 {
		t.Errorf("%d rows have a user_id, want the 8 student events", n)
	}
}

func TestLogEventsWithAMissingCSV(t *testing.T) {
	d := logEventsFixture(t, "events", septemberNoon)
	if err := os.Remove(d.Path("report_2.csv")); err != nil {
		t.Fatal(err)
	}
	e, warnings := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM log_events"); n != 7 {
		t.Errorf("log_events has %d rows, want 7: run 2's b and c survive from other sources", n)
	}
	if strings.Contains(warnings, "log_events") {
		t.Errorf("a warning names log_events:\n%s", warnings)
	}
}

// The fallback installs only when the primary fails to bind, so it is installed directly, in a
// session over another dataset whose sandbox would refuse the fixture's files.
func TestLogEventsFallbackDeclaresEveryColumn(t *testing.T) {
	d := logEventsFixture(t, "events", septemberNoon)
	var fallback string
	for _, st := range (viewSet{canonDir: d.Path(""), m: mustManifest(t, d)}).statements() {
		if st.name == sqlIdent("log_events") {
			fallback = strings.TrimPrefix(st.fallback, "CREATE VIEW "+st.name+" AS ")
		}
	}
	if fallback == "" {
		t.Fatal("no log_events statement")
	}
	e := openEngine(t, []DatasetSpec{{DS: newDS(t, "empty")}}, nil)
	if err := e.exec(context.Background(), "CREATE VIEW log_events_fallback AS "+fallback); err != nil {
		t.Fatalf("installing the fallback: %v", err)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM log_events_fallback"); n != 0 {
		t.Errorf("the fallback has %d rows", n)
	}
	if got := describeNames(t, e, "log_events_fallback"); !slices.Equal(got, logEventsNames) {
		t.Errorf("fallback columns %v, want %v", got, logEventsNames)
	}
	if got := columnTypes(t, e, "log_events_fallback"); !maps.Equal(got, logEventsTypes) {
		t.Errorf("fallback types %v, want %v", got, logEventsTypes)
	}
}

func TestLogEventsWithAMissingStoreFile(t *testing.T) {
	d := logEventsFixture(t, "events", septemberNoon)
	file := mustManifest(t, d).Stores[store.TypeRecentLogs].File
	if err := os.Remove(d.Path(file)); err != nil {
		t.Fatal(err)
	}
	e, warnings := openWithWarnings(t, d)
	if n := queryInt(t, e, "SELECT count(*) FROM log_events"); n != 8 {
		t.Errorf("log_events has %d rows, want the CSVs' 8 events", n)
	}
	if n := strings.Count(warnings, file); n != 1 {
		t.Errorf("the warnings name %s %d times, want once:\n%s", file, n, warnings)
	}
}

func TestLogEventsMaterializes(t *testing.T) {
	d := logEventsFixture(t, "events", septemberNoon)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	raw := viewRows(t, e, "log_events")
	e.Close()
	if len(raw) != 9 {
		t.Fatalf("raw rows: %v", raw)
	}
	res, _ := materialize(t, d, MaterializeOptions{})
	if !slices.Contains(res.Written(), "log_events") {
		t.Fatalf("log_events was not written: %+v", res.Views)
	}
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if !slices.Contains(pinnedViews(e), "log_events") {
		t.Fatalf("log_events is not read from its Parquet, pinned %v", pinnedViews(e))
	}
	if got := viewRows(t, e, "log_events"); !slices.Equal(got, raw) {
		t.Errorf("from Parquet %v, want %v", got, raw)
	}
	e.Close()

	addLogCSV(t, d, logFixture{run: 5, csv: studentActionsCSV + studentActionsRow("z", "v", fmt.Sprint(pinnedSeconds))})
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if slices.Contains(pinnedViews(e), "log_events") {
		t.Error("log_events is still read from its Parquet after a new log run")
	}
}

func TestLogEventsPerDataset(t *testing.T) {
	other := newDS(t, "other")
	addLogCSV(t, other, logFixture{run: 9, csv: studentActionsCSV + studentActionsRow("b", "v", fmt.Sprint(pinnedSeconds))})
	e := openEngine(t, []DatasetSpec{{DS: logEventsFixture(t, "events", septemberNoon)}, {DS: other}}, nil)
	if n := queryInt(t, e, `SELECT count(*) FROM "events".log_events`); n != 9 {
		t.Errorf("events.log_events has %d rows, want 9", n)
	}
	if got := queryStrings(t, e, `SELECT id || ':' || run_ids::VARCHAR FROM "other".log_events`); !slices.Equal(got, []string{"b:[9]"}) {
		t.Errorf("other.log_events = %v, want only its own b", got)
	}
}
