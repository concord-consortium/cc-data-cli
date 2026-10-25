# Implementation Plan: cc-data-cli: one row per log event across overlapping log runs and recent-log pulls

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-192
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

Two commits on `REPORT-192-one-row-per-log-event`, on `REPORT-115-clue-guidance`. Each passes `go test ./...`, `go vet ./...` and `gofmt -l .` on its own.

**Running the tests.** Every test and cc-data command runs under a temporary HOME (REPORT-197), review subagents included. The asdf `go` shim resolves through HOME, so call the toolchain directly and keep the real module and build caches, read before HOME changes (a `$($G env ...)` inside the same prefix runs under the temporary HOME):

```bash
G=$HOME/.asdf/installs/golang/1.25.12/go/bin/go
MOD=$(GOTOOLCHAIN=local $G env GOMODCACHE) CACHE=$(GOTOOLCHAIN=local $G env GOCACHE)
HOME=$(mktemp -d) GOMODCACHE=$MOD GOCACHE=$CACHE GOTOOLCHAIN=local $G test -count=1 ./...
```

## Implementation Plan

### Add the log_events view

**Summary**: The view (R1 to R9), its tests (R10), and the two catalog entries the drift guards require as soon as the view is registered (part of R11 and R12). The rest of the guidance is the next step.

**Files affected**:
- `internal/duck/log_events_view.go` (new): the builder.
- `internal/duck/views.go`: register it after `logsView` in `statements()`; extract `csvRead` from `csvScan` and `dimensionScan`.
- `internal/duck/log_events_view_test.go` (new): the tests.
- `internal/duck/logs_views_test.go`: `logFixture` gains `fetchedAt`.
- `internal/guidance/src/core.md`: the `log_events` entry in `## Views`.
- `docs/researcher-guide.md`: the `log_events` row in the views table.

**Estimated diff size**: ~370 lines (about 120 code, 220 tests, 30 prose).

**`views.go`.** In `statements()`:

```go
	stmts = append(stmts, vs.logsView())
	stmts = append(stmts, vs.logEventsView())
```

`csvScan` and `dimensionScan` build the same `read_csv(...)` call from a download's file, dialect and columns, and `log_events` is a third caller with different column types, so the call moves into one helper and all three use it:

```go
// csvRead reads a download's CSV with the given column types, in the order the CSV records.
func (vs viewSet) csvRead(dl dataset.Download, cols map[string]string) string {
	dialect := dataset.DefaultCSVDialect()
	if dl.CSVDialect != nil {
		dialect = *dl.CSVDialect
	}
	return fmt.Sprintf("read_csv(%s, auto_detect=false, header=true, delim=%s, quote=%s, escape=%s, columns=%s)",
		vs.file(dl.Files[0]), sqlStr(dialect.Delim), sqlStr(dialect.Quote), sqlStr(dialect.Escape), orderedColumnsClause(cols, dl.ColumnOrder))
}
```

`csvScan` becomes `fmt.Sprintf("SELECT %d AS run_id, %s%s FROM %s", dl.RunID, star, extra.String(), vs.csvRead(dl, dl.Columns))` and `dimensionScan` `fmt.Sprintf("SELECT %s, * FROM %s", strings.Join(vs.dimensionInjected(dl, d), ", "), vs.csvRead(dl, dl.Columns))`, each dropping its own dialect lines. The statements they produce are byte-identical, so no materialized signature changes (`viewSignature` hashes the statement).

**`log_events_view.go`** (new, in full):

```go
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
		src, storeFiles := vs.clueSource(store.TypeRecentLogs, standIn, false)
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
```

Notes on the shape, each checked by the stage 4 throwaway (requirements Technical Notes):

- The members are a positional `UNION ALL`, since every member emits the same columns in the same order; `BY NAME` would buy nothing.
- The derived columns are `logDerived`'s expressions over R3's columns rather than over the CSV's own: `event_time` comes from the converted `time`, so a fractional client time in a CSV is rounded first (the ingester writes whole seconds, so no real row has one).
- `_fetched_at` in the outer select is the winning copy's, because `QUALIFY` keeps the row whose window order put it first.
- `max(_class_id) OVER (PARTITION BY id)` is the one `recent_logs` row's class, since the store holds an `id` once. The store's identity is `Strict`, so it never holds a NULL `id`: the NULL partition is CSV rows only, whose `_class_id` is NULL, and a NULL-`id` row's `run_ids` is `[run_id]` with no NULL to filter.
- `clueSource(..., warn=false)`: `recentLogsView` already warns about a missing store file, and `logsView` about a missing CSV, so each problem is reported once per session (R7).
- The view reads files and is in `StaticViewNames()`, so `materializableViews` admits it and `viewSignature` covers its SQL, which embeds each run's id and fetch time (R8).

**Tests** (`log_events_view_test.go`; `logFixture` gains `fetchedAt time.Time`, zero meaning today's fixed 2026-09-10 12:00 UTC, so existing callers are unchanged). One fixture, `logEventsFixture`, holds:

- run 1, Student Actions, fetched 2026-09-10: events `a`, `b`, and a row with an empty `id`, every row's `event_value` `007` (so `DetectCSV` types that column BIGINT);
- run 2, Student Actions, fetched 2026-09-11: `b`, `c`, another row with an empty `id`, and `d` with `time` `oops`;
- run 3, Student Actions with Metadata, fetched 2026-09-09: `c`, and `007` twice (one record repeated) with its learner columns filled;
- run 4, Teacher Actions, fetched 2026-09-09: `t` with `username` and no `user_id` column;
- `recent_logs` class 590 (`land`, stamped at `time.Unix(590, 0)`): `b` and `r`.

Tests, each naming the requirement and the change that would make it fail:

- `TestLogEventsHasOneRowPerEvent` (R2, R10): `count(*)` is 9 (`a`, `b`, `c`, `d`, `007`, `t`, `r` and the two empty-id rows), equal to `count(DISTINCT id::VARCHAR)` over `logs` and `recent_logs` plus the empty-id rows; `logs` still has 11 rows, `b` and `c` twice each and `007` twice. Fails if the `QUALIFY` goes or partitions on something else.
- `TestLogEventsKeepsRowsWithNoID` (R2): both empty-id rows present, with `run_ids` `[1]` and `[2]`. Fails if the `id IS NULL OR` goes (they collapse to one) or if the `CASE` goes (both get `[1, 2]`).
- `TestLogEventsListsWhereAnEventIs` (R3): `run_ids` `[1, 2]` and `_class_id` 590 for `b`; `[2, 3]` and NULL for `c`; `[]` (not NULL) and 590 for `r`; `[3]` for `007` (held twice in one run, listed once). Fails without the `coalesce`, the `DISTINCT` or the `max(...) OVER`.
- `TestLogEventsShowsTheNewestCopy` (R4): `b`'s `_fetched_at` is run 2's; with a second fixture whose runs are fetched at `time.Unix(1, 0)`, before the store's 1970 stamp, `b`'s is the store's. Fails if the order is reversed or drops `_fetched_at`.
- `TestLogEventsBreaksTiesOnRunID` (R4): with every run fetched at one time, after the store's stamp and then at it, `b`'s `event_value` is run 2's `v` (run 1 has `007`, the store NULL). Fails if the order drops `run_id`, sorts it ascending, or puts the store's NULL `run_id` first.
- `TestLogEventsTypes` (R3, R5): the 20 columns in R3's order (`slices.Equal` on `DESCRIBE`'s `column_name`s, since `columnTypes` is a map and cannot see order) and their types (`maps.Equal` against a literal map) on the fixture, on an empty dataset, on runs only and on `recent_logs` only; `a`'s `event_value` reads back as `'007'`; `d`'s `time` is NULL and `SELECT sum(time) FROM log_events` runs. Fails if a CSV is read at its detected types (`a`'s `event_value` becomes `'7'`), if the `TRY_CAST` becomes a `CAST` (`oops` fails the query), or if two columns swap places. (Run 3's `007` `id` cannot pin the first: its column also holds `c`, so `DetectCSV` types it VARCHAR.)
- `TestLogEventsTeacherRows` (R3, R11): `t` is present with `user_id` and `run_remote_endpoint` NULL, `log_events` has no `username` column, and `count(*) FILTER (WHERE user_id IS NOT NULL)` is the fixture's student events (8 of 9), so the guidance's filter is pinned to drop exactly the teacher row.
- R6 needs no new test: `logs_views_test.go` and `recent_logs_view_test.go` pin both views' rows and types and stay unchanged, apart from `logFixture`'s new field.
- `TestLogEventsWithAMissingCSV` (R7): with run 2's CSV deleted, the session opens, `log_events` has run 2's events only where another source holds them (`b` from run 1 and the store, `c` from run 3: 7 rows), and no warning names `log_events`. (`reports`, `logs` and the per-run `report_2` view already each warn about the file, so a count of `report_2.csv` mentions pins nothing of this view's.) Fails if `logEventsCSVMember` drops its `fileMissing` check: the primary then fails to bind, the view degrades with a warning naming it, and it has no rows.
- `TestLogEventsFallbackDeclaresEveryColumn` (R7): the fallback statement, installed under another name in a session over an empty dataset, returns no rows and R3's 20 columns and types. Fails if the fallback is built over the real files (the session's sandbox refuses another dataset's CSV, so installing it errors) or loses a column.
- `TestLogEventsWithAMissingStoreFile` (R7): with the `recent_logs` store file deleted, the session opens, `log_events` holds the CSV events, and the warnings name the store file exactly once. Fails if `log_events` warns as well as `recent_logs` (`clueSource`'s `warn` set to true).
- `TestLogEventsMaterializes` (R8): `Materialize` writes it, the next session pins it, and `viewRows` from the Parquet equals the raw view's; after a new log run is added, the Parquet is no longer pinned.
- `TestLogEventsPerDataset` (R9): two datasets in one session each get their own `log_events` with only their own events.

**Prose.** The guards fail as soon as the view is registered (`TestGuidanceDocumentsEveryStaticView`, `TestResearcherGuideDocumentsEveryStaticView`, checked), so this step adds the entries they parse:

- `core.md`, after the `recent_logs` entry:

  ```
  - `log_events`: one row per log event across every log run and `recent_logs`,
    matched on `id`, which the log ingester gives an event and every copy of it
    carries. **Count events from `log_events`**, never from `logs`, where two
    overlapping runs each keep their copy, nor from `logs` and `recent_logs`
    together; a run's log freshness still comes from `logs`. It has the 13
    `student-actions` columns at `recent_logs`' types and the four parsed columns,
    plus `run_ids` (every log run holding the event, `[]` when only `recent_logs`
    does), `_class_id` (the class of its `recent_logs` copy, else NULL) and
    `_fetched_at` (when the copy shown was fetched; the newest wins). Scope it to a
    run and a class with `list_contains(run_ids, <run>) OR _class_id = <class>`. It
    leaves out `username` and the `student-actions-with-metadata` learner columns,
    which a recent copy lacks: join `student_metadata` (names, `class`, `school`,
    teachers) or `student_id_mapping` (ids, `offering_id`) on
    `run_remote_endpoint` for them. A `teacher-actions` run's events are here with `user_id` and
    `run_remote_endpoint` NULL and no `username`: every student row has a
    `user_id`, so add `WHERE user_id IS NOT NULL` to count student events, and
    read teacher activity from `logs`. A row with no `id` is kept as its own event, and two copies with
    different ids (a client sending an event twice) are not matched.
  ```
- `researcher-guide.md`, a row after `recent_logs`:

  ```
  | `log_events` | Each log event once, however many log runs and recent-log pulls hold it, matched on the event's `id`. Count events here rather than in `logs` or `recent_logs`. It has a Student Actions run's columns and the four parsed columns, plus `run_ids` (the log runs holding the event) and `_class_id` (the class whose recent-logs pull holds it). |
  ```

---

### Point the rest of the guidance at log_events

**Summary**: Every other place that tells a reader how to count across `logs` and `recent_logs`, and the log column list's missing `id` (R11 to R13). Prose and one test pin; no behavior change.

**Files affected**:
- `internal/guidance/src/core.md`: the first "Runs and their data" bullet, the log column list, the `logs` and `recent_logs` entries, the CLUE routing bullet.
- `docs/researcher-guide.md`: the "A dataset never holds duplicate data" bullet, the `recent_logs` table row, the Recent logs "Days before the window" bullet, the Materializing "It is bigger" bullet, the "Comparing over time means separate datasets" bullet.
- `internal/mcpserver/tools.go` and `internal/mcpserver/server_test.go`: the `get_recent_logs` description and its pinned fact.
- `cmd/get_recent_logs.go`: `getRecentLogsLong`'s last sentence.
- `README.md`: the query bullet's view list and the "A dataset never contains duplicate data" bullet.

**Estimated diff size**: ~50 lines.

Edits (before, then after):

- `core.md`, first bullet: "Datasets are duplicate-free by construction: re-fetching a run replaces its records. Create a dataset per point-in-time pull to compare over time." gains, after its first sentence: "Two log runs whose windows overlap each keep their copy of an event in `logs`; `log_events` has it once."
- `core.md`, log column list: "(columns include `session`, `application`, ..." becomes "(columns include `id`, the event's id, the same in every copy of it, then `session`, `application`, ...".
- `core.md`, `logs` entry: after "unioned with `run_id`, plus four parsed columns.", add "Two overlapping runs each hold their copy of an event here; count events from `log_events`."
- `core.md`, CLUE routing (the `clue_patches` bullet): "`logs` and `recent_logs` add what the student had on screen between edits (the `logs` entry describes CLUE's `extras`)." becomes "`log_events` adds what the student had on screen between edits, each event once across log runs and recent pulls (the `logs` entry describes CLUE's `extras`)."
- `core.md`, `recent_logs` entry: "**An event a log run also holds is in both `logs` and `recent_logs`**: count one view or the other for the hours they share, not their union." becomes "An event a log run also holds is in both `logs` and `recent_logs`; `log_events` has it once, so count from there."
- `researcher-guide.md`, "A dataset never holds duplicate data" bullet, which says two overlapping runs are "matched by identity" and pulled "without creating duplicates": after its last sentence, add "Log runs are the exception: each run's CSV is kept whole, so two overlapping log runs each hold their copy of an event in `logs`, and `log_events` has it once."
- `researcher-guide.md`, `recent_logs` row: "Kept apart from `logs`: an event a Student Actions run also holds is in both, so count one or the other." becomes "Kept apart from `logs`: an event a Student Actions run also holds is in both, and `log_events` has it once."
- `researcher-guide.md`, "Days before the window" bullet: "so for the hours both cover, count one view or the other rather than adding them." becomes "so for the hours both cover an event is in each view; `log_events` has it once, so count from it."
- `researcher-guide.md`, "It is bigger" bullet: "`reports` unions the same log CSVs `logs` reads, so both carry those rows" becomes "`reports` unions the same log CSVs `logs` reads, and `log_events` has their events again, so each carries those rows". The measured file sizes after it stay as they are, since they describe `reports` and `logs` only.
- `researcher-guide.md`, "Comparing over time means separate datasets" bullet: "Because a dataset never keeps duplicates (re-pulling a run overwrites its records), to compare" becomes "Because re-pulling a run overwrites its records, to compare".
- `tools.go`, `get_recent_logs`: "An event a log run also holds appears in both logs and recent_logs, so count one view or the other, not their union; order rows by received_time." becomes "An event a log run also holds appears in both logs and recent_logs; count events from log_events, which has each once; order rows by received_time." `server_test.go`'s pinned fact `"appears in both logs and recent_logs"` stays, and `"count events from log_events"` joins it.
- `get_recent_logs.go`: "Days before the window come from a Student Actions run, whose events land in logs, not recent_logs." gains "Count events across both from log_events, which has each once."

- `README.md`, the query bullet: after "`recent_logs` (a class's log events from recent-logs pulls, kept apart from the log reports)," add "`log_events` (each log event once, across the log reports and recent-logs pulls),".
- `README.md`, "A dataset never contains duplicate data" bullet: after "Overlapping runs, regenerated reports, and repeated pulls simply update records in place," add "except that report CSVs stay one file per run, so two overlapping log runs each hold their copy of an event and `log_events` is where each event appears once;". (The sentence goes on "and every `get` reports its counts".)

After the edits, `grep -rn "one view or the\|view or the other\|one or the other" internal docs cmd README.md` returns nothing. (`core.md` wraps "count one / view or the other" across lines, which only the middle pattern sees.)

## Self-Review

Roles: commit reviewer (does each step build and pass alone), test writer (can each named test be written against `internal/duck`'s helpers), operator (upgrade and downgrade), senior Go engineer reading `internal/duck` cold. The planned `log_events_view.go` and the `csvRead` refactor were built for real on this branch (throwaway, deleted): `go vet` and `gofmt -l` were clean, and `go test ./...` passed in every package except `internal/guidance`, whose two catalog guards failed naming `log_events`, which is why the first step carries the two entries. The planned fixture, built with `DetectCSV` and `land`, gave every number the tests assert: 9 `log_events` rows, 11 `logs` rows, 7 distinct ids, `run_ids` `[1, 2]`/590 for `b`, `[2, 3]` for `c`, `[]`/590 for `r`, `[3]` for `007`, `[1]` and `[2]` for the two empty ids, `d`'s `time` NULL with `sum(time)` running, and `b` taking the store's `_fetched_at` when the runs were fetched at `time.Unix(1, 0)`.

- **RESOLVED: README describes the old contract.** Its query bullet lists the views without `log_events`, and its "A dataset never contains duplicate data" bullet says overlapping runs "update records in place", which is not true of log CSVs. Both added to the second step (and to requirements R12).
- **RESOLVED: `cluePins` named for CLUE.** It is `store.MustKind(typ).Pinned`; the builder calls that directly rather than a helper whose name says CLUE.
- **RESOLVED: the first tests list miscounted.** It described run 3's rows wrongly and gave `logs` 12 rows; the built fixture has 11, and the list now matches it. R6's test was a capture of current output written as literals, which the existing `logs` and `recent_logs` tests already are; it now says so instead.
- **RESOLVED: R7's store and bind cases had no test.** Only the missing CSV did, while every sibling view has a fallback test (`TestLogsFallbackDeclaresTheDerivedColumns`, `TestClue*FallbackDeclaresEveryColumn`, `TestDimensionViewFallbackKeepsTheColumnShape`). Two tests added. Built as throwaway against the planned code, both passed, and each failed under the mutation it names. Making a missing CSV column's NULL untyped broke neither: the `recent_logs` member is always present and typed, even as a stand-in, so it fixes every column's type in the union, and the cast stays as a guard nothing can observe.
- **Upgrade and downgrade need nothing.** The view changes no file on disk. An older cc-data opening a dataset materialized by this one ignores the `log_events` entry in the manifest's `Materialized` map, since `applyMaterialized` looks up only the views it builds, and the `csvRead` refactor leaves every other view's statement, so its signature, unchanged.

Second pass. Roles: senior Go engineer reading `internal/duck` cold, test writer, docs reviewer, package author (REPORT-189), education researcher (whose finding is in requirements). The planned code and the step 1 prose were applied as throwaway (deleted): `go vet` and `gofmt -l` clean, `go test ./...` green, and over copies of real runs 2412, 2415, 2413 and 253 `log_events` gave 77,829 rows, one per distinct `id` of 89,824 `logs` rows, 11,995 with `run_ids` `[2412, 2415]`.

- **RESOLVED: `logEventsView` repeated `clueView`.** Its last four lines were `clueView`'s body; it now calls `vs.clueView("log_events", ...)`, as `recentLogsView` does. Built, same rows and warnings.
- **RESOLVED: two unreachable guards.** `recentLogsKind` is `Strict`, so the store refuses a NULL `id` and the NULL partition holds CSV rows only: `list_filter([run_id], ...)` and the `_class_id` `CASE` matched plain `[run_id]` and `max(...) OVER` row for row (throwaway). Both simplified.
- **RESOLVED: R3's column order was untested.** `columnTypes` returns a map, so `maps.Equal` passes with columns swapped. `TestLogEventsTypes` now also compares `DESCRIBE`'s names in order.
- **RESOLVED: "the warnings name `report_2.csv` once" was false.** `reports`, `logs` and the per-run `report_2` view each warn (measured: three). The test now asserts no warning names `log_events`; dropping the `fileMissing` check produced exactly that warning and zero rows (throwaway).
- **RESOLVED: the CLUE routing bullet still sent readers to `logs` and `recent_logs`.** `core.md`'s `clue_patches` bullet (REPORT-115) says they "add what the student had on screen between edits", the union R11 forbids counting. Added to the second step and R11.
- **RESOLVED: the closing grep could not see `core.md`.** Its "count one / view or the other" wraps, so no pattern matched it (checked on the current tree); the pattern gains `view or the other` and the path list `README.md`.

Third pass. Roles: implementer building each step from the spec alone, test writer (mutation per named test), docs reviewer, package author (REPORT-189). The planned code was applied as throwaway (deleted) with the fixture built as listed: 9 `log_events` rows, 11 `logs` rows, every `run_ids`/`_class_id` value above, 8 rows with a `user_id`, and only `log_events`' two catalog guards failing in `go test ./...`.

- **RESOLVED: `TestLogEventsTypes` could not fail under its named mutation.** Run 3's `id` column holds `c` as well as `007`, so `DetectCSV` types it VARCHAR, and the `oops` cell is caught by the `TRY_CAST` either way; reading every CSV at its detected types passed every assertion (throwaway). Run 1's `event_value` is now all `007`: the mutation reads `'7'`. The `oops` check is pinned to `TRY_CAST` becoming `CAST`, which fails the query (throwaway).
- **RESOLVED: the missing-CSV test left out `c`.** With run 2's CSV gone, `c` survives from run 3: 7 rows (throwaway). Stated.
- **RESOLVED: the test command read the caches under the temporary HOME.** Bash expands `$($G env GOMODCACHE)` in a prefix after the earlier `HOME=` applies, and without `GOTOOLCHAIN=local` it also fetched go1.27.2 (`go.mod`'s toolchain line) into the fresh cache (checked). The caches are now read first.
- **RESOLVED: requirements' Background and R10 disagreed with the plan.** The `get recent-logs` help never said "count one view or the other"; R10 asked for an event "in one run and `recent_logs`" and a Teacher Actions run overlapping the others, neither in the fixture (and none of Teacher Actions run 2413's ids is in runs 2412 or 2415, checked). Both reworded to match. REPORT-189's own `log_events` CTE shadows the new view (checked in DuckDB), noted in its Out of Scope bullet.

Fourth pass. Roles: implementer building each step from the spec alone, test writer (mutation per named test), docs reviewer checking every shipped sentence against the code, package author (REPORT-189). The planned code, `csvRead` refactor, `logFixture` field and all eleven tests were built (throwaway, deleted): every asserted number held, each test failed under each mutation it names (15 run), `go vet`/`gofmt -l` clean, and only the two catalog guards failed in `go test ./...`. Both steps' prose applied: `go test ./internal/guidance/ ./internal/mcpserver/ ./cmd/` green, closing grep empty. The third pass's claims rechecked: the corrected command reads the real caches, 0 of run 2413's 2,103 ids are in runs 2412 or 2415, and a CTE shadows a same-named view.

- **RESOLVED: the `log_events` entry sent readers to the wrong view for names.** It said to join `student_id_mapping` for `username` and the learner columns, but that view has ids and `offering_id` only; `username`, `student_name`, `class`, `school` and the teachers are `student_metadata`'s (`dimensionViews`, and `core.md`'s own `student_metadata` entry). The entry and R11 now name both. Correctness defect.
- **RESOLVED: the researcher guide still promised overlapping runs make no duplicates.** Its "A dataset never holds duplicate data" bullet, the twin of the README bullet R12 already fixes, says two overlapping runs are matched by identity. Added to step 2 and R12 (applied: guards green, grep empty). Correctness defect.
- **RESOLVED: two wording slips.** R3 said `username` means four things where `core.md` says up to five; the fallback test's failure is the sandbox refusing the other dataset's CSV, not rows returned (measured).

Fifth pass. Roles: researcher reading every shipped sentence, docs reviewer grepping the repo for the old contract, implementer building from the spec alone, test writer. The planned code, `csvRead` refactor and `logFixture` field were built with the fixture (throwaway, deleted): 9 `log_events` rows, 11 `logs` rows, 7 distinct ids, every `run_ids`/`_class_id` value above, 8 rows with a `user_id`, R3's 20 columns in order on a populated and an empty dataset, 7 rows and no `log_events` warning with run 2's CSV gone, one store-file warning with the store gone. Both steps' prose applied: `go test ./...`, `go vet ./...` clean, closing grep empty; the spec's test command run verbatim reads the real caches. Over `dataflow-cohort`'s CSVs, all 263,570 log rows' `run_remote_endpoint` match `student_metadata` and `student_id_mapping` rows, so the entry's roster join holds; the recent-logs route inner-joins learners (report-service `REPORT-150-report-server-the-class-scoped`, `recent_logs.ex`), so its rows always carry a `user_id`.

- **RESOLVED: the researcher guide still said a dataset never keeps duplicates.** Section 8's "Comparing over time means separate datasets" bullet opens "Because a dataset never keeps duplicates", which overlapping log runs contradict once step 2 calls them the exception in section 3. Added to step 2 and R12 (applied: guards green). Correctness defect.
- **RESOLVED: "an event in both is in each view".** The planned "Days before the window" text dropped "for the hours both cover", leaving "both" naming the two views. Reworded.

## Open Questions

### RESOLVED: Judgment call: one builder reading the CSVs, not a view over `logs` and `recent_logs`
**Context**: `SELECT ... FROM logs UNION ALL SELECT ... FROM recent_logs` would be a few lines.
**Options considered**:
- A) Read each CSV and the store directly, as written.
- B) Select from the two views.

**Decision**: A. B inherits each CSV's detected types (R5 fails on `007` and `oops`) and declares no files, so it would never materialize (R8), the reason REPORT-111's `clue_patches` reads its store directly.

### RESOLVED: Judgment call: extract `csvRead`
**Context**: A third copy of the `read_csv(...)` format string, or a shared helper.
**Options considered**:
- A) One helper used by `csvScan`, `dimensionScan` and `log_events`.
- B) A third copy.

**Decision**: A. The three must agree on dialect handling. Built for real (throwaway), the refactor left the primary and fallback statements of all 21 other views on a dataset holding a log, an answers and a Student ID Mapping CSV byte-identical, so no materialized Parquet goes stale on upgrade.
