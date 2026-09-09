# Implementation Plan: `logs` view with parsed `parameters` and `extras`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-112
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

One repository, one PR. The view and its catalog entry land in the same commit because the drift guard makes any other order a red build: `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` compare `duck.StaticViewNames()` against `core.md` and `docs/researcher-guide.md` in **both** directions (`internal/guidance/guard_test.go:38-52`, `:113-129`), so registering `logs` without documenting it fails, and documenting it without registering it fails too. REPORT-94's atomic rule is enforced here rather than remembered.

## Implementation Plan

### Generalize the report union to carry derived columns

**Summary**: `logs` is `reportUnionView` with four extra output columns. Rather than copy the union, the existing builder gains a per-member derived-column hook, so the missing-file stand-in, the corrupt-CSV fallback and the quarantine warning keep one implementation.

**Files affected**:
- `internal/duck/views.go`: `reportUnionView`, `csvScan`, `csvEmptyMember` take a derived-column descriptor.

**Estimated diff size**: ~90 lines

`reportUnionView` gains a parameter describing the columns to append; `reports` and `report_prompts` pass none and are byte-identical to today. The descriptor is a slice of `{name, typ, expr func(dl dataset.Download) string}` so each member's expression can depend on that download's recorded schema, which is what the absent-source-column case needs.

Both `csvScan` and `csvEmptyMember` append from the same descriptor, so the populated member and the stand-in cannot drift: the stand-in emits `CAST(NULL AS <typ>) AS <name>` for every derived column, which is what keeps `logs` carrying `parameters_json` even when its only CSV is missing.

The duplicate-name skip below has to run in **both** of them, from the same predicate. Applying it in `csvScan` alone is the obvious half-implementation and it reintroduces exactly the failure the skip exists to prevent: `csvEmptyMember` is what builds the `fallback` statement, so a stand-in that still appends a colliding name makes the fallback fail alongside the primary, and `Open` returns an error for the whole dataset rather than degrading one view.

The zero-member short-circuit (`views.go:122-124`) emits `run_id` plus each derived column as a typed NULL rather than `run_id` alone. With an empty descriptor that is character-for-character today's statement, so `reports` and `report_prompts` are unaffected.

`perDownloadViews` is the third caller of `csvScan`/`csvEmptyMember` (`views.go:318`, `:323`). It passes no derived columns, so `report_<run>` views keep exactly today's shape even for a log run. The run-scoped form of the parsed view is `logs` with `WHERE run_id = <id>`, which is the pairing the `reports` catalog entry already documents; giving `report_<run>` type-dependent columns would be a wider change than this story.

Verified by building it: with the descriptor threaded through all three callers, the generated statement for every pre-existing view is byte-identical, and the full suite passes except the two drift-guard failures the documentation step exists to clear.

### Add the `logs` view

**Summary**: the log-type union with `parameters_json`, `extras_json`, `event_time` and `received_time`.

**Files affected**:
- `internal/duck/views.go`: `logsView`, registered in `statements()`.

**Estimated diff size**: ~70 lines

```go
// logsView unions the log-type report CSVs with parameters and extras parsed as JSON and
// time rendered as a timestamp. Admission is by report type, not slug: all three log slugs
// map to ReportTypeLog, and reindex recovers that type confidently from a CSV whose slug is
// gone (dataset/reindex.go), so a slug test would silently drop rows `reports` still shows.
func (vs viewSet) logsView() viewStmt {
	return vs.reportUnionView(`"logs"`, true, logDerived, func(dl dataset.Download) bool {
		return dl.ReportType == dataset.ReportTypeLog
	})
}
```

The four derived columns, each guarded on the member's own recorded schema:

```go
var logDerived = []derivedColumn{
	{"parameters_json", store.TypeJSON, jsonOf("parameters")},
	{"extras_json", store.TypeJSON, jsonOf("extras")},
	// The two columns are different clocks, not one instant at two resolutions: `time` is the
	// client's, rounded to seconds by the ingester, and `timestamp` is server receipt in
	// milliseconds (cloud-formation/log-ingester.json). Hence the factor of 1000 on one and
	// not the other.
	//
	// TRY_CAST to DOUBLE first: DetectCSV types each column per file, so either can arrive
	// BIGINT, DOUBLE or VARCHAR, and to_timestamp(VARCHAR) is a binder error that would fail
	// the whole view rather than null one column. AT TIME ZONE 'UTC' turns to_timestamp's
	// TIMESTAMPTZ into a UTC-valued TIMESTAMP, so the column's type does not depend on
	// whether a CSV is present and it compares directly to the store's _fetched_at.
	{"event_time", store.TypeTIMESTAMP, epochOf("time", 1)},
	{"received_time", store.TypeTIMESTAMP, epochOf("timestamp", 1000)},
}
```

`epochOf(src, perSecond)` returns `CAST(NULL AS TIMESTAMP)` when `dl.Columns[src]` is absent, and otherwise `to_timestamp(TRY_CAST(<src> AS DOUBLE)) AT TIME ZONE 'UTC'`, dividing by `perSecond` first when it is not 1. Taking the divisor as a parameter rather than writing the two expressions out is what keeps the seconds and milliseconds readings from being independently editable.

`jsonOf(src)` returns `CAST(NULL AS JSON)` when `dl.Columns[src]` is absent and `TRY_CAST(<src> AS JSON)` otherwise. The absent case is reachable: `recoverReportType` admits a CSV to the log type on `event` and `time` alone (`internal/dataset/reindex.go:336`), so a CSV with no `parameters` column can be a member, and referencing it would fail the view's creation for every other run in the dataset.

A member also skips a derived column whose name its own schema already contains. The consequence of omitting that skip depends on how many log CSVs the dataset holds, and only one of the two outcomes is survivable. With a single member there is no `UNION` keyword, DuckDB accepts two output columns of the same name, and a later `SELECT event_time` silently resolves to one of them. With two or more, `UNION ALL BY NAME` rejects the duplicate outright, and since the `fallback` statement unions the same members it carries the same duplicate and fails identically, so `Open` returns an error and the **whole dataset** fails to register rather than one view (`internal/duck/engine.go:88-91`).

None of the four names is in the server's log column list today (`report_query.ex:67`), so this is a guard against a later server change. Note that nothing in cc-data can detect that change: it holds no copy of the log column list and derives none, so a test asserting the four names against a transcript of that list would fail only when someone edited the transcript. The skip is therefore the entire defense, and what it is tested against is its own behavior at both member counts, not a list.

Note also what the skip buys and what it costs: the view installs, but a member that keeps its own `event_time` while its siblings emit the derived one puts a `VARCHAR` and a `TIMESTAMP` under one name, and `UNION ALL BY NAME` resolves that to `VARCHAR` for the whole view without complaint. That is accepted rather than solved, on the grounds that a retyped column beats a dataset that will not open.

Registration goes after `reportPromptsView` in `statements()` (`views.go:56-70`), which places `logs` next to the other report-derived views and inside what `StaticViewNames()` enumerates.

### Document the view in both guarded surfaces

**Summary**: the catalog entry and the researcher-guide row, in this commit, because the guard fails without them.

**Files affected**:
- `internal/guidance/src/core.md`: a `logs` entry in the `## Views` section.
- `docs/researcher-guide.md`: a `logs` row in the `## 5. How datasets are organized` view table.

**Estimated diff size**: ~40 lines

The entry has to earn its length the way the `reports` entry does, by naming what a caller would otherwise get wrong. Four things qualify:

- `parameters_json`/`extras_json` are null when the source string is not valid JSON, so `->>` on them is safe, but a null means unparsable rather than absent.
- `event_time` and `received_time` are **different clocks**, not one instant at two resolutions. `event_time` comes from `time`, the client's device clock rounded to seconds by the ingester, which falls back to the server clock when the client sends nothing usable. `received_time` comes from `timestamp`, server receipt in milliseconds. A reader who assumes they are the same field at two resolutions will read device-clock skew as latency, so the entry has to say this outright.
- Both are timezone-naive `TIMESTAMP` holding **UTC**. That is a convention rather than a type, so it has to be written down, and it is what makes them comparable to `_fetched_at`.
- The original `parameters`, `extras`, `time` and `timestamp` columns are retained, so nothing is lost by the parse.
- `logs` unions all three log reports, whose shapes differ. `username` can be absent, a student's, a student's salted hash, a teacher's, or a teacher's hash, and `student_name` holds `student_id` when the run hid names. Interpreting any name-bearing column takes a join to `downloads` on `run_id` selecting **both** `slug` and `hide_names`; the entry gives that join, because neither attribute alone is enough and the view deliberately carries neither inline.

The entry must not name a `cc-data` command: `TestCoreNamesNoCommand` (`guard_test.go:105-110`) fails on any occurrence of `` `cc-data `` in the core, because the core renders into the MCP instructions where there is no shell.

### Tests

**Summary**: each named test fails under a specific mutation of the code above.

**Files affected**:
- `internal/duck/views_test.go`: extend.

**Estimated diff size**: ~180 lines

Written against real CSV files through the real view builder, since the whole subject is generated SQL:

- **A CLUE-shaped row is queryable through the parse**: `extras_json->>'selectedNavTab'` returns `problems` and `parameters_json->>'tileId'` returns the tile id. Fails if the derived columns are not appended.
- **A malformed `extras` yields null, not an error**: a row whose `extras` is `not json` returns a null `extras_json` while the same row's `parameters_json` still parses. Fails if `TRY_CAST` is written as `CAST`.
- **`event_time` is seconds, pinned to an instant**: `time = 1757462400` yields `2025-09-10T00:00:00Z`. Fails if anyone divides or multiplies by 1000, which is otherwise silent: the milliseconds reading of that value is the year 57661, not an error.
- **`received_time` is milliseconds, pinned to the same instant**: `timestamp = 1757462400872` yields `2025-09-10T00:00:00.872Z`, which pins the divisor and the surviving sub-second precision in one assertion. Fails if the `/1000` is dropped, whose silent reading is again the year 57661.
- **Both are UTC-valued `TIMESTAMP`, not `TIMESTAMPTZ`**: `DESCRIBE logs` reports `TIMESTAMP` for both columns, and `event_time = TIMESTAMP '2025-09-10 00:00:00'` is true for the pinned row. Fails if `AT TIME ZONE 'UTC'` is dropped, which is otherwise invisible on a UTC machine and would only show up on a developer's. Assert through `DESCRIBE` or a scanned `time.Time`, never through `event_time::VARCHAR`, which renders in the session timezone and would make the test itself machine-dependent.
- **A run whose `time` detected as `VARCHAR` still works**: two runs in one dataset, one with integral `time` and one with a non-numeric value, so `DetectCSV` types them `BIGINT` and `VARCHAR`. The view creates, the first run's `event_time` is correct and the second's is null. Fails if the `TRY_CAST(time AS DOUBLE)` is dropped, because `to_timestamp(VARCHAR)` is a binder error that takes the whole view down.
- **A member lacking `parameters` contributes**: a CSV with `event`, `time` and `extras` and no `parameters`. The view creates and that run's `parameters_json` is null. Fails if `jsonOf` does not check `dl.Columns`.
- **A missing CSV keeps the derived columns**: the only log download's file absent on disk, so the stand-in is the sole member. `logs` still has `parameters_json`, `extras_json`, `event_time` and `received_time`, and zero rows. Fails if `csvEmptyMember` does not append the descriptor.
- **A source column named `event_time` is not duplicated, at both member counts**: with one log CSV carrying its own `event_time`, `logs` has exactly one column of that name, asserted by counting columns rather than by reading a value, since a single member emits no `UNION` and the duplicate binds silently. With a second log CSV in the dataset the union is real, so the same fixture asserts the view installs at all; without the skip both the primary and the fallback hit `Binder Error: UNION (ALL) BY NAME operation doesn't support duplicate names` and `Open` fails for the whole dataset. One case without the other tests half the guard.
- **The stand-in skips a colliding name too**: the same single-CSV fixture with its file absent on disk, so `csvEmptyMember` is the sole member, plus the generated `fallback` string asserted to declare one `event_time`. Fails if the skip predicate is wired into `csvScan` only, which is the half-implementation that would take the whole dataset down at two members.
- **The corrupt-CSV fallback carries the derived columns**: the `logs` statement's `fallback` string declares `parameters_json`, `extras_json`, `event_time` and `received_time`. Asserted on the generated statement rather than by corrupting a CSV, since the fallback is only reached when the primary `CREATE VIEW` fails at install time and `views_test.go` is `package duck`, so `viewStmt.fallback` is in scope. Fails if `csvEmptyMember` appends the descriptor but the fallback is assembled from something else.
- **An empty dataset still binds the documented query**: with no log downloads, `logs` declares `run_id`, `parameters_json`, `extras_json`, `event_time` and `received_time`, and `SELECT extras_json->>'selectedNavTab' FROM logs` returns no rows rather than erroring. Fails if the zero-member short-circuit is left emitting `run_id` alone.
- **`reports` is unchanged**: the generated `CREATE VIEW` statement for `reports` is byte-identical to what the same manifest produced before the derived-column parameter existed. This is the assertion that fails if the generalization leaks into the callers that pass no derived columns.

A note for whoever writes them: the Go driver returns a JSON column as `map[string]interface{}`, so scanning a bare `parameters_json` into a `*string` fails with "unsupported Scan". Assert through `->>`, which returns VARCHAR.

A second note: `TRY_CAST(... AS JSON)` needs no extension load. `attachmentContentView` already ships the same expression (`views.go:304`), so the JSON type is known to work in the embedded engine as configured.

## Requirements Coverage

| Requirement | Step |
|---|---|
| `logs` exists over log-type CSVs with every recorded column plus `run_id` | add the `logs` view |
| `parameters_json`/`extras_json` are `TRY_CAST`, null on unparsable | add the `logs` view; tests |
| Original `parameters`/`extras` retained | add the `logs` view (`*` keeps them) |
| `event_time`/`received_time` correct for BIGINT, DOUBLE and VARCHAR sources | add the `logs` view; tests |
| `event_time` is seconds and `received_time` milliseconds, each pinned by a test | tests |
| Both derived timestamps are UTC-valued `TIMESTAMP`, stable across present/missing/absent CSVs | add the `logs` view; generalize the report union; tests |
| A slugless recovered log CSV is admitted | add the `logs` view (type-based predicate) |
| Missing CSV contributes zero rows and keeps its columns | generalize the report union; tests |
| A dataset with no log runs still declares the derived columns | generalize the report union; tests |
| A derived name colliding with a source column costs neither the view nor the dataset | add the `logs` view; tests |
| `reports` behaves exactly as before | generalize the report union; tests |
| Catalog entry plus researcher-guide row, same change | document the view in both guarded surfaces |
| Tests for `navTabsOpen`, malformed extras, VARCHAR `time` | tests |

### Gaps found, requirement with no step

**RESOLVED: the zero-member case bypassed the stand-in entirely.** The requirements said the stand-in declares the derived columns, and the plan delivered that through `csvEmptyMember`, but `reportUnionView` short-circuits before reaching it when no download matches, so a dataset with no log runs got a `run_id`-only `logs` and the documented query failed to bind. Resolved by moving the zero-member statement onto the derived-column descriptor, with the declared set limited to what every populated schema also contains. Full reasoning in the requirements' resolved question.

**RESOLVED: the corrupt-CSV fallback had no test.** The requirement says a corrupt CSV costs its rows rather than the view, which is the `fallback` statement built from `emptyMembers`. The plan routed the derived columns through `csvEmptyMember`, so the behavior was covered, but the only test touching that function exercised it as a union member for a *missing* file, never as the fallback. Those are different statements and one can be right while the other is wrong. A test now asserts the `logs` fallback string declares all four derived columns.

### Gaps found, step no requirement asked for

**Orphan 1: the duplicate-name skip.** No requirement asks for it; it came out of the stage 3 review. The check that produced it found only half the behavior: DuckDB does accept two output columns of the same name silently, but only where there is no `UNION`, and under `UNION ALL BY NAME` the same duplicate is a binder error that fails the fallback too and so costs the whole dataset. It is a few lines in two functions and three tests, and the alternative is a failure mode that is either silent or total depending on how many CSVs happen to be in the dataset.

**Orphan 2: generalizing `reportUnionView` rather than writing a second union.** No requirement asks for either shape. It is the smaller change and it keeps the missing-file, corrupt-CSV and quarantine behaviors in one place, but it does edit a function two shipped views depend on, which is why the byte-identical `reports` assertion is in the test list.

## Open Questions

None.

## Self-Review

### Whoever has to review the resulting commits

#### RESOLVED: two of the steps are one commit, and the plan presented them as though they were not

Every other spec in this repo treats a step as a commit that compiles and passes on its own. That does not hold here, and it was checked rather than reasoned about: with `logs` registered in `statements()` and no catalog entry, `go build` succeeds and `go test ./internal/guidance/` fails with `views registered but not documented: [logs]` and `views missing from the researcher guide's table: [logs]`. So "add the `logs` view" and "document the view in both guarded surfaces" cannot be separate commits without a red one between them.

That is the guard working as designed, and the fix is to say so rather than to weaken it. The two steps are one commit. They stay separate steps because they are separate concerns to review, the SQL and the prose, but the plan now states the commit boundary explicitly instead of leaving the reader to infer one per step and produce a broken bisect.

The first step, generalizing `reportUnionView`, does stand alone: with no caller passing a derived-column descriptor, every generated statement is unchanged and the suite is green.

### Whoever has to run the tests

#### RESOLVED: the byte-identical `reports` assertion is writable, which was worth confirming before promising it

The test list promises that `reports`'s generated `CREATE VIEW` is byte-identical after the generalization. That promise is only worth making if a test can actually reach the statement text. It can: `internal/duck/views_test.go` is `package duck` rather than `duck_test` and already calls `populated.statements()` (`views_test.go:39`), so `viewStmt`'s unexported `primary` and `fallback` fields are in scope and comparable as strings.

The assertion is therefore a literal string comparison against a golden value, not a structural approximation of one. The golden has to be captured by running `statements()` on the **pre-change** code and pasting the result in. A golden captured by running the new code records whatever the new code does, which is a test that cannot fail, and it would pass just as happily if the generalization had changed `reports` in some way nobody noticed.

### Senior Engineer

#### RESOLVED: `keepData` is inert for this view and would otherwise read as meaningful

`reportUnionView`'s `keepData` argument only ever does anything inside `csvScan`, and only under `if dl.ReportType == dataset.ReportTypeAnswers` (`internal/duck/views.go:163-178`). `logs` admits members on `dl.ReportType == dataset.ReportTypeLog`, so that branch is unreachable for every member and the argument's value cannot change the generated SQL.

It is passed as `true` to match `reportsView`. Recorded because the argument is not obviously inert at the call site, and a later reader comparing `logs` to `report_prompts`, which passes `false` for a reason, would otherwise look for a reason here too.
