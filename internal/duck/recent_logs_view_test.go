package duck

import (
	"maps"
	"slices"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// recentLogsTypes is the recent_logs store's pinned types plus logs' four derived columns.
var recentLogsTypes = map[string]string{
	"id": "VARCHAR", "session": "VARCHAR", "application": "VARCHAR", "activity": "VARCHAR",
	"event": "VARCHAR", "event_value": "VARCHAR", "parameters": "VARCHAR", "extras": "VARCHAR",
	"run_remote_endpoint": "VARCHAR", "time": "BIGINT", "timestamp": "BIGINT", "user_id": "BIGINT",
	"primary_user_id": "BIGINT", "_class_id": "BIGINT", "_fetched_at": "TIMESTAMP",
	"parameters_json": "JSON", "extras_json": "JSON", "event_time": "TIMESTAMP", "received_time": "TIMESTAMP",
}

func logEvent(id, parameters string, timestamp int64) map[string]any {
	return map[string]any{
		"id": id, "session": "s1", "application": "CLUE", "activity": "a", "event": "e",
		"event_value": nil, "time": timestamp / 1000, "parameters": parameters, "extras": nil,
		"run_remote_endpoint": "https://learn.example/learners/1", "timestamp": timestamp,
		"user_id": 7, "primary_user_id": nil,
	}
}

func recentLogsFixture(t *testing.T) *dataset.Dataset {
	t.Helper()
	d := newDS(t, "recent")
	land(t, d, store.TypeRecentLogs, 590,
		logEvent("e1", `{"tileType":"Dataflow"}`, 1792540800123),
		logEvent("e2", "not json", 1792540801000))
	if err := d.UpsertDownload(dataset.Download{Type: store.TypeRecentLogs, ClassID: 590, Complete: true}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRecentLogsView(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: recentLogsFixture(t)}}, nil)
	if got := queryString(t, e, "SELECT parameters_json->>'tileType' FROM recent_logs WHERE id = 'e1'"); got != "Dataflow" {
		t.Errorf("tileType = %q", got)
	}
	if got := queryString(t, e, "SELECT coalesce(parameters_json::VARCHAR, '<null>') || '|' || parameters FROM recent_logs WHERE id = 'e2'"); got != "<null>|not json" {
		t.Errorf("an unparseable parameters gave %q", got)
	}
	if got := queryString(t, e, "SELECT strftime(received_time, '%Y-%m-%d %H:%M:%S.%g') FROM recent_logs WHERE id = 'e1'"); got != "2026-10-21 00:00:00.123" {
		t.Errorf("received_time = %q", got)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM recent_logs WHERE _class_id = 590"); n != 2 {
		t.Errorf("rows stamped with the class: %d", n)
	}
	if got := columnTypes(t, e, "recent_logs"); !maps.Equal(got, recentLogsTypes) {
		t.Errorf("recent_logs types = %v, want %v", got, recentLogsTypes)
	}
}

func TestRecentLogsViewOnAnEmptyDataset(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: newDS(t, "empty")}}, nil)
	if n := queryInt(t, e, "SELECT count(*) FROM recent_logs"); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	if got := columnTypes(t, e, "recent_logs"); !maps.Equal(got, recentLogsTypes) {
		t.Errorf("recent_logs types = %v, want %v", got, recentLogsTypes)
	}
}

func TestRecentLogsKeepsPinnedTypesForAnAllNullColumn(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: recentLogsFixture(t)}}, nil)
	if n := queryInt(t, e, "SELECT count(primary_user_id) FROM recent_logs"); n != 0 {
		t.Fatalf("the fixture has %d primary_user_id values, want none", n)
	}
	if got := columnTypes(t, e, "recent_logs")["primary_user_id"]; got != "BIGINT" {
		t.Errorf("primary_user_id = %s", got)
	}
}

func TestRecentLogsStaysApartFromRunsAndLogs(t *testing.T) {
	d := recentLogsFixture(t)
	addReportCSV(t, d, 584, "log", "id,event,time,timestamp\nx1,e,1,1000\n")
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if n := queryInt(t, e, "SELECT count(*) FROM logs"); n != 1 {
		t.Errorf("logs = %d rows, want the log run's 1", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_membership"); n != 0 {
		t.Errorf("run_membership has %d rows for a recent-logs pull", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'run_membership' AND column_name = 'id'"); n != 0 {
		t.Error("run_membership has an id column")
	}
	if n := queryInt(t, e, "SELECT count(*) FROM information_schema.tables WHERE table_name LIKE 'recent_logs_%'"); n != 0 {
		t.Errorf("%d per-class recent_logs views", n)
	}
}

func TestDownloadsNamesTheClass(t *testing.T) {
	runsOnly := newDS(t, "runs")
	addReportCSV(t, runsOnly, 584, "log", "id,event,time,timestamp\nx1,e,1,1000\n")
	e := openEngine(t, []DatasetSpec{{DS: runsOnly}}, nil)
	if got := queryString(t, e, "SELECT typeof(class_id) FROM downloads"); got != "BIGINT" {
		t.Errorf("class_id with only run downloads is %s", got)
	}
	if got := queryString(t, e, "SELECT coalesce(class_id::VARCHAR, '<null>') FROM downloads"); got != "<null>" {
		t.Errorf("a run download's class_id = %s", got)
	}

	e = openEngine(t, []DatasetSpec{{DS: recentLogsFixture(t)}}, nil)
	if got := queryString(t, e, "SELECT coalesce(run_id::VARCHAR, '<null>') || '|' || class_id FROM downloads WHERE type = 'recent_logs'"); got != "<null>|590" {
		t.Errorf("the recent_logs download = %s", got)
	}
	if got := queryString(t, e, "SELECT coalesce(hide_names::VARCHAR, '<null>') FROM downloads WHERE type = 'recent_logs'"); got != "<null>" {
		t.Errorf("the recent_logs download's hide_names = %s", got)
	}
	if got := queryString(t, e, "SELECT typeof(run_id) FROM downloads"); got != "BIGINT" {
		t.Errorf("run_id with only recent_logs downloads is %s", got)
	}

	fresh := openEngine(t, []DatasetSpec{{DS: newDS(t, "fresh")}}, nil)
	if got := columnTypes(t, fresh, "downloads")["class_id"]; got != "BIGINT" {
		t.Errorf("an empty downloads' class_id is %q", got)
	}
}

func TestRecentLogsMaterializes(t *testing.T) {
	d := recentLogsFixture(t)
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)
	raw := viewRows(t, e, "recent_logs")
	e.Close()
	if len(raw) != 2 {
		t.Fatalf("raw rows: %v", raw)
	}
	res, _ := materialize(t, d, MaterializeOptions{})
	if !slices.Contains(res.Written(), "recent_logs") {
		t.Fatalf("recent_logs was not written: %+v", res.Views)
	}
	e = openEngine(t, []DatasetSpec{{DS: d}}, nil)
	if !slices.Contains(pinnedViews(e), "recent_logs") {
		t.Fatalf("recent_logs is not read from its Parquet, pinned %v", pinnedViews(e))
	}
	if got := viewRows(t, e, "recent_logs"); !slices.Equal(got, raw) {
		t.Errorf("from Parquet %v, want %v", got, raw)
	}
}
