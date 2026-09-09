# `logs` view with parsed `parameters` and `extras`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-112
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Add a `logs` view over the log-type report CSVs that exposes `parameters` and `extras` as queryable JSON alongside the original strings, and both of the log's UNIX-integer clocks as real UTC timestamps. The existing `reports` view is unchanged.

## Project Owner Overview

A Student Actions report carries two columns that hold the most useful part of a log event: `parameters`, the event's own payload, and `extras`, a snapshot of where the student's attention was when it fired. Scott found `extras` populated on 100% of 258,916 CLUE log rows, carrying `navTabsOpen`, `selectedNavTab`, `workspaceMode`, `problemPath`, `group` and `role`.

Today both arrive as opaque strings, so every log-based study either exports to another tool or hand-writes its own parse. A view that presents them as JSON, and presents both of the row's clocks as timestamps rather than UNIX integers, makes them answerable in SQL directly.

## Background

**Log CSVs are already identified two ways, and they do not agree.** `slugToType` maps `student-actions`, `student-actions-with-metadata` and `teacher-actions` to `ReportTypeLog` (`internal/dataset/reporttype.go:17-23`). Separately, `recoverReportType` identifies a log CSV by the presence of the `event` and `time` columns and returns `ReportTypeLog` with `recovered = false`, a confident answer (`internal/dataset/reindex.go:336`). A CSV recovered that way carries the log report type but **no slug**, because provenance is what was lost. So "log-type CSVs" and "slug-recognized log CSVs" are different sets, and the difference is exactly the reindexed-without-a-manifest case.

**Two existing view shapes, and this is the union one.** `reportUnionView` unions report CSVs by an `include` predicate over `dl.ReportType`, using each download's own recorded schema and a typed-empty stand-in for a missing file (`internal/duck/views.go:100-134`). `dimensionViewStmt` is REPORT-94's slug-recognized pattern, and it rests on a stated premise: "The schema is known rather than sniffed" (`views.go:474-476`). That premise does not hold for logs, because `student-actions-with-metadata` carries columns the other two do not, so the fixed-schema pattern would have to declare three schemas or the wrong one.

**A log row carries two clocks, not one instant at two resolutions.** The Kinesis ingester lambda is the authority (`cloud-formation/log-ingester.json:138-160`):

```js
data.time = 0;
if (rawData.hasOwnProperty('time')) {
  data.time = Math.round(rawData.time / 1000);   // the client's own clock, ms, rounded to seconds
}
if ((data.time === 0) || isNaN(data.time)) {
  data.time = Math.round(timestamp / 1000);      // falls back to the server clock
}
data.timestamp = timestamp;                      // server receipt time, ms, from the API gateway transform
```

So `time` is the student's device clock at second resolution, silently falling back to the server clock when the client sends nothing, a zero or a non-number, with no flag recording which happened. `timestamp` is the server's receipt time in milliseconds, never client-controlled and never absent. The Glue table declares both `bigint` (`log-ingester.json:593`, `:597`).

**Ordering on `time` alone discards most of the resolution the data has.** Measured on the `student-actions-test` staging dataset, run 187: ordering events within a session by `time` leaves **29.4%** of adjacent pairs tied, against 0.3% ordering by `timestamp`, with a median real gap of 1,267 ms. Observed client-to-server skew in that sample runs to 872 ms. Inter-event intervals are the signal a behavior study reads, so the view exposes both clocks rather than choosing for the researcher.

**Both directions of the 1000 factor fail silently.** Scott's build carries the comment "log `time` is UNIX seconds, not milliseconds; dividing by 1000 silently yields January 1970 rather than an error" (`sources/clue/recipes/log-events/build-parquet.sh:51-53`). Verified in the other direction on the embedded engine: `to_timestamp(TRY_CAST(1757462400000 AS DOUBLE))` returns the year **57661** with no error. With both clocks exposed, one expression must divide by 1000 and the other must not, so each mistake has a test.

**`time`'s and `timestamp`'s detected types vary by file.** `DetectCSV` widens each column over every value in that one CSV (`internal/dataset/csvdetect.go:57-59`, `81-93`), so `time` lands as `BIGINT` when every value is integral, `DOUBLE` if any is fractional, and `VARCHAR` if any is non-numeric. Two runs in one dataset can disagree. Verified: `to_timestamp` on a `VARCHAR` is a hard binder error, not a null, so a bare `to_timestamp(time)` would fail the whole view's creation for one badly typed run; `to_timestamp(TRY_CAST(time AS DOUBLE))` returns the right instant for `BIGINT`, `DOUBLE` and `VARCHAR` alike and `NULL` for a non-numeric value. `UNION ALL BY NAME` across members that typed `time` differently binds without error. The same reasoning and the same `TRY_CAST` apply to `timestamp`; both were observed as `BIGINT` on all three staging log datasets (`student-actions-test`, `log-test`, `metadata-test`, 1,472 rows).

**The three admitted reports do not have the same shape, and `username` is where that bites.** `get_log_cols(remove_username: true)` strips the log's `username` for the student reports (`report_query.ex:105`), while `athena/teacher_actions_report.ex:34` calls `get_log_cols()` bare, so teacher rows keep it as `<userid>@<portal>`. Separately `get_learner_cols` carries `username` as a *learner* column (`report_query.ex:73-76`), which `student-actions-with-metadata` requests and `student-actions` does not. On top of that, `hash_username` replaces any column named `username` with a salted SHA1 when the run set `hide_names`, and `hide_learner_student_name` replaces `student_name` with `student_id` while keeping the column name (`report_query.ex:151-161`).

Verified against the three staging log datasets: `student-actions` yields 13 columns and no `username` at all; `student-actions-with-metadata` yields 25, including `username`, `student_name`, `class`, `school`, `teachers` and `permission_forms`. So `logs.username` has five possible meanings across a dataset: absent, a student's username, a student's hash, a teacher's username, a teacher's hash. Distinguishing them takes both `slug` and `hide_names`, which is why neither alone belongs inline. See the resolved question below.

This is not hypothetical. The `metadata-test` dataset holds run 186 (`student-actions`) and run 221 (`student-actions-with-metadata`) together today, and its `reports` view already returns 364 rows with `username` NULL beside 16 with `username` = `tstudentone` and `student_name` = `Test Student One`.

**The 128 KB premise is a Python limit, not a DuckDB one.** `athena_logs.py:35-36` raises `csv.field_size_limit` with the comment "real rows exceed csv's default 128 KB field limit". That is the Python `csv` module's cap. Verified on the embedded engine (DuckDB **v1.5.4**, via `duckdb-go/v2 v2.10504.0`): `read_csv`'s default is **2,000,000 bytes**, a limit on the whole line rather than one field. A 130 KB field loads unconfigured; a 3 MB line fails with an error naming `max_line_size`, which is also accepted as `maximum_line_size`.

**No real row exceeds that default, on the best evidence available.** Scott's `build-parquet.sh` calls `read_csv` with no line-size option (`build-parquet.sh:54-61`) and its own check compares CSV row count to Parquet row count (`:93-96`), over the full 258,916-row corpus. Had a line exceeded 2 MB, that build would have failed rather than under-counted.

**A note on verifying this story's SQL.** The local `duckdb` CLI is v1.3.0; the binary embeds v1.5.4. Anything checked against the CLI is checked against a different engine, and v1.3.0 is separately known to mis-decorrelate a subquery in the study suite. Verification belongs in Go against the embedded engine.

## Requirements

- A `logs` view exists over the log-type report CSVs, carrying every column each CSV recorded, plus `run_id`, as `reports` does.
- `parameters_json` and `extras_json` are `TRY_CAST(... AS JSON)`, null on unparsable input and never an error.
- The original `parameters`, `extras`, `time` and `timestamp` columns are retained alongside the derived ones, unchanged and under their own names.
- `event_time` is a timestamp derived from `time`, and `received_time` a timestamp derived from `timestamp`. Each is correct whether its source was detected as `BIGINT`, `DOUBLE` or `VARCHAR`, and null rather than an error for a value that is not a number.
- `event_time` treats `time` as UNIX **seconds** and `received_time` treats `timestamp` as UNIX **milliseconds**. A test pins one known epoch value per column to its expected instant, so neither reading can be introduced silently.
- Both are declared and emitted as `TIMESTAMP` holding UTC, so the column's type does not depend on whether a CSV is present on disk and the two are directly comparable to each other and to the store's `_fetched_at`. See the resolved question below.
- The view admits a log CSV recovered by `reindex` without a slug, on the same terms as one with a slug. See the resolved question below.
- A missing CSV contributes zero rows and keeps its columns, and a corrupt one costs its rows rather than the view, matching `reportUnionView`'s existing fallback behavior.
- On a dataset with no log runs at all, `logs` still declares `run_id`, `parameters_json`, `extras_json`, `event_time` and `received_time`, so a documented query binds before any log data has been downloaded. It does **not** declare the source columns, which depend on what was downloaded. See the resolved question below.
- The `reports` view behaves exactly as before, asserted rather than assumed.
- A catalog entry lands in the same change as the view, per REPORT-94's atomic rule, in REPORT-104's entry structure under `internal/guidance/src/`, plus the researcher-guide view-table row the same guard checks.
- The entry names all three log reports as members, states what `username` can mean across them, and gives the join to `downloads` on `run_id` selecting both `slug` and `hide_names` as the way to interpret any name-bearing column. See the resolved question below.
- Tests: a CLUE row's `extras_json->>'navTabsOpen'` is queryable; a malformed `extras` yields null rather than an error; a run whose `time` column detected as `VARCHAR` still produces a correct `event_time`; each of the two epoch readings is pinned so neither the missing nor the spurious factor of 1000 can be introduced silently.

## Technical Notes

**Shape.** `logs` follows `reportUnionView`, not `dimensionViewStmt`: per-download recorded schemas unioned `BY NAME`, a typed-empty stand-in per admitted member, and the same two-statement primary/fallback pair. The derived columns are appended to each member's `SELECT` rather than wrapped around the union, so a member whose CSV lacks any of `parameters`, `extras`, `time` or `timestamp` still contributes.

**The derived expressions.**

```sql
TRY_CAST(parameters AS JSON) AS parameters_json,
TRY_CAST(extras AS JSON)     AS extras_json,
to_timestamp(TRY_CAST(time AS DOUBLE))             AT TIME ZONE 'UTC' AS event_time,
to_timestamp(TRY_CAST(timestamp AS DOUBLE) / 1000) AT TIME ZONE 'UTC' AS received_time
```

`AT TIME ZONE 'UTC'` is not decoration. See the resolved question on the timestamp type below.

Verified on the embedded engine: `TRY_CAST('not json at all' AS JSON)` is null with no error, `TRY_CAST('{"unclosed":' AS JSON)` likewise, and `TRY_CAST('{"selectedNavTab":"problems"}' AS JSON)->>'selectedNavTab'` returns `problems`.

**The Go driver returns a JSON column as `map[string]interface{}`, not a string.** Scanning a bare `TRY_CAST(... AS JSON)` into a `*string` fails with "unsupported Scan". Tests should assert through `->>`, which returns `VARCHAR`, rather than scanning the JSON column directly.

**No line-size option is set.** The default 2,000,000 bytes exceeds anything the real corpus contains, and a value invented here would be a second cap that could disagree with the one Scott's pipeline has been running under. See the resolved question below.

**The zero-member stand-in declares the derived columns and nothing else.** `reportUnionView` short-circuits when no download matches (`views.go:122-124`), so that path never reaches `csvEmptyMember`. It emits `run_id` plus each derived column as a typed NULL. The rule it follows is that a stand-in may only declare columns that are a **subset of every populated schema**, so the view gains columns when data lands and never loses one. The four derived columns qualify because cc-data emits them for every member regardless of what the CSV held; the source columns do not, and `username` is the proof: `get_athena_query/3` passes `remove_username: true` (`server/lib/report_server/reports/report_query.ex:105`) while `athena/teacher_actions_report.ex:34` does not, so `teacher-actions` carries `username` and the two student-actions reports do not.

This belongs in `reportUnionView` rather than in `logs`, so `reports` and `report_prompts` pass no derived columns and keep today's exact `run_id`-only statement, and the CLUE views REPORT-111 adds inherit the behavior without a further decision.

**The typed-empty stand-in has to declare the derived columns too**, or a dataset whose only log CSV is missing would install a `logs` view without `parameters_json`, and a query written against a populated dataset would fail against a degraded one. `JSON` and `TIMESTAMP` are the declared types there, spelled as `store.TypeJSON` and `store.TypeTIMESTAMP` (`internal/store/columns.go:9-17`) rather than as bare strings; the `dataset` package deliberately has no constant for either, since neither can come out of CSV detection.

## Out of Scope

- Changing the `reports` view, its membership, or its column set.
- Parsing `parameters`/`extras` for any other report type. Only log-type CSVs have them.
- A typed or per-key projection of `extras`. The keys Scott found are CLUE's, and a view that named them would be a CLUE-specific view rather than a log view.
- Materialization, which is REPORT-113.
- Retiring the recipes that do this by hand, which is REPORT-119.
- An inline provenance column on `logs`. Considered and deferred rather than missed; see the resolved question below.

## Open Questions

### RESOLVED: does `logs` admit a log CSV that has no slug?

**Context**: the Jira says both "selects from the log-type CSVs only" and "slug-recognized via provenance". Those pick different sets: `recoverReportType` types a reindexed CSV as `log` confidently but cannot restore its slug.

**Decision**: admit on `dl.ReportType == ReportTypeLog`, not on slug. Three reasons. All three log slugs map to that one type, so the type is the natural key and slug matching would mean listing three. `recoverReportType` returns `recovered = false` for a log CSV, meaning the codebase already treats that identification as confident, and excluding those rows would drop from `logs` data the user can still see in `reports`, with nothing to explain the difference. And the dimension-view pattern that slug-recognition belongs to rests on a fixed known schema, which logs does not have. Recorded because "slug-recognized" appears in the Jira and a reader would otherwise take it as settled.

### RESOLVED: does this story set a CSV line-size option at all?

**Context**: the Jira's scope asks for "CSV reader configuration for large fields, with a fixture row over 128 KB" and its acceptance criterion is "a row with a `parameters` value over 128 KB loads and is queryable". Both rest on the 128 KB number, which is Python's limit. DuckDB's default is 2,000,000 bytes, so that criterion passes today against unmodified code.

**Decision**: set no option, and keep a test at the real boundary instead. A criterion that passes before the change is worth nothing, which is the "test that cannot fail" rule. Raising the cap anyway would be insurance against a row nobody has ever observed, priced in a second limit that disagrees with the one Scott's pipeline has been running under unconfigured across the whole corpus. What replaces it is a test that pins the actual boundary: a fixture row over 128 KB loads and is queryable **without** any option set, which is the fact worth protecting, since it is what makes the option unnecessary. If a real row ever exceeds 2 MB, DuckDB fails loudly with an error naming `max_line_size` and the fix is one parameter, so the cost of being wrong here is a clear error rather than silent truncation.

## Self-Review

### Senior Engineer

#### RESOLVED: a log CSV is admitted on two columns, so the derived columns cannot assume the source ones exist

`recoverReportType` types a CSV as `log` on the presence of `event` and `time` alone (`internal/dataset/reindex.go:336`). Every log CSV the server produces carries `parameters` and `extras`, since `get_log_cols` includes both unconditionally and filters only `username` (`server/lib/report_server/reports/report_query.ex:62-70`). But the admission test is the recovery one, not the server one, so a CSV with `event` and `time` and no `parameters` is admitted to `logs` and its member `SELECT` would reference a column that is not there, failing the view's creation for every other run in the dataset.

So each member emits the derived columns explicitly, substituting a typed NULL where its own recorded schema lacks the source column: `CAST(NULL AS JSON) AS parameters_json`. `UNION ALL BY NAME` does fill an absent column with NULL rather than erroring, verified, so this is not about the union binding; it is about the member's own `SELECT` not referencing a column its CSV does not have. It also keeps `logs` carrying all four derived columns regardless of which runs are in the dataset, so a query written against one dataset does not fail against another.

#### RESOLVED: duplicate output column names are silently allowed, so the derived names need a guard

Verified on the embedded engine, and the answer differs by member count, which matters because only one of the two outcomes is survivable.

A bare `SELECT 1 AS event_time, 2 AS event_time` binds without error and yields one row with two columns of that name, so a dataset holding exactly one log CSV, where `strings.Join` emits no `UNION` keyword at all, would silently resolve `SELECT event_time` to one of them with nothing to say which. But with two or more members the union is real, and `UNION ALL BY NAME` rejects the duplicate outright: `Binder Error: UNION (ALL) BY NAME operation doesn't support duplicate names in the SELECT list`. That failure is not confined to the view. The `fallback` statement is a `UNION ALL BY NAME` over the same members, so it carries the same duplicate and fails the same way, and `Open` returns `registering view %s: ... (fallback also failed: ...)` (`internal/duck/engine.go:88-91`), which takes down the whole dataset rather than one view.

So the guard is what keeps a single server-side column addition from making every affected dataset unopenable, and the tests have to cover both member counts: the one-member case fails by counting columns, since the duplicate binds, and the multi-member case fails by the view not installing.

None of the four names appears in the authoritative log column list today (`report_query.ex:67`), so this is a guard against a later server change rather than a present defect. The requirement is that a member does not append a derived column whose name its own recorded schema already contains, in both the populated member and the typed-empty stand-in, and that the guard is exercised at one member and at two, since only the two-member case reaches the union.

Nothing here can detect the server change itself. cc-data holds no copy of the log column list and derives none: the dimension views hand-maintain their own fixed schemas for the portal reports, and nothing under `internal/` enumerates the log ones. A test asserting the four names against a transcript of `report_query.ex:67` would only fail when a person edited the transcript, so it would not fail on a server addition and is not worth writing. The runtime skip is the whole defense, which is why it is tested by behavior rather than by a list.

What the skip cannot prevent is worth stating as accepted rather than solved: a member that keeps its own `event_time` while its siblings emit the derived one puts a `VARCHAR` and a `TIMESTAMP` under one name, and `UNION ALL BY NAME` resolves that to `VARCHAR` for the whole view without complaint. That is the price of the view installing at all, and it is the better half of the trade against a dataset that will not open.

### Education Researcher

#### RESOLVED: the CSV carries both `time` and `timestamp`, and the view now parses both

The log column list is `id, session, username, application, activity, event, event_value, time, parameters, extras, run_remote_endpoint, timestamp` (`report_query.ex:67`). An earlier draft derived `event_time` from `time` alone, following Scott's build (`build-parquet.sh:54-61`), on the grounds that what `timestamp` holds was uncharacterized. It is characterized now, by the ingester lambda quoted in the Background: the two are **different clocks**, not one instant at two resolutions. `time` is the client's device clock rounded to seconds, with a silent fallback to the server clock; `timestamp` is server receipt time in milliseconds.

That makes exposing only one a research decision rather than a formatting one, and the measurement settles it: ordering within a session by `time` leaves 29.4% of adjacent event pairs tied against 0.3% on `timestamp`. A study reading inter-event intervals would be working from the coarser of two available clocks, and mixing two clocks across rows besides, since `time` falls back.

**Decision**: derive both. `event_time` stays on `time`, which keeps Scott's meaning and, more importantly, agrees with the filter that selected the rows: the report server prunes a run with `log.time >= <unix seconds>` (`report_query.ex:181-200`), so an `event_time` built on `timestamp` could sit outside the date range the researcher asked for. `received_time` carries `timestamp`, at the resolution the data actually has. The catalog entry has to say which clock each one is, because the names alone do not, and a researcher who assumes they are the same field at two resolutions will read device-clock skew as latency.

Both source columns are retained unchanged as `BIGINT`, not as strings: `DetectCSV` typed both `BIGINT` on all three staging log datasets. The earlier draft's claim that `timestamp` is "retained unchanged as a string column" came from Scott's build declaring `timestamp: 'VARCHAR'` in its own `columns=` map, which is his forced declaration rather than what cc-data detects.


### RESOLVED: does `logs` carry a provenance column so a caller can tell the three reports apart?

**Context**: `logs` unions `student-actions`, `student-actions-with-metadata` and `teacher-actions`, whose column sets and `username` semantics differ as the Background records. A caller writing `SELECT username, count(*) FROM logs GROUP BY 1` gets a silent mixture and no reason to suspect one. `dimensionView.hideNames` is a precedent for fixing this inside the view: it adds the run's `hide_names` inline even though `downloads` already carries it, because "a dataset holding runs fetched under different roles puts real names and numeric student ids in one column with nothing to tell them apart" (`views.go:222-225`).

**Options considered**: add `slug` as a derived column; add a narrower flag such as `is_teacher_action`; or add nothing and document the join to `downloads`.

**Decision**: add nothing, and make the catalog entry carry the explanation.

A `slug` column was the initial recommendation and it is a half-fix, which is the worst of the three. `slug` separates three of the five states of `username`; the other two are the `hide_names` split, so `GROUP BY slug, username`, which is exactly the query an inline `slug` invites, still silently merges plain and hashed usernames from two runs of the same slug. It looks like it worked. Disambiguating the column takes `slug` **and** `hide_names`, and both already sit in `downloads` on the same `run_id` key that every `logs` row carries.

`is_teacher_action` was rejected for not separating `student-actions` from `student-actions-with-metadata`, which is the split that exists in a real dataset today, and for inventing a vocabulary where `slug` already is one.

The general reason is worth more than this story: copying one provenance attribute inline puts a value in a second place that must agree with `downloads` forever, and doing it one attribute at a time and one view at a time is how `logs` ends up with `slug`, the dimension views with `hide_names`, and `reports` with neither. `hideNames` is a convenience denormalization rather than a necessity, since those views keep `run_id` too, so extending it is not the same as following a design. Whether union fact views should carry provenance at all is one decision across `reports`, `logs` and the views REPORT-111 adds, and REPORT-113 is its forcing point because materialization freezes the column set. So the check belongs to whoever specs REPORT-113: decide it there for every union view at once, before a materialized column set makes the answer expensive to change.

What this story owes instead is an entry that names all three reports as members, enumerates what `username` can mean, and gives the `downloads` join selecting both `slug` and `hide_names`.

### RESOLVED: is a derived timestamp `TIMESTAMP` or `TIMESTAMP WITH TIME ZONE`?

**Context**: an earlier draft wrote the expression as a bare `to_timestamp(TRY_CAST(time AS DOUBLE))` while declaring the stand-in type `TIMESTAMP`. Those disagree. `to_timestamp(DOUBLE)` returns `TIMESTAMP WITH TIME ZONE`, verified by `DESCRIBE` on the two statements this story builds: the primary view's `event_time` came back `TIMESTAMP WITH TIME ZONE` and both the fallback's and the zero-member stand-in's came back `TIMESTAMP`.

That is the schema instability the stand-in rule exists to prevent, and it fails silently rather than loudly. `to_timestamp(1757462400::DOUBLE) = TIMESTAMP '2025-09-10 00:00:00'` is **false** while the same comparison against a `TIMESTAMPTZ` literal is true, so `WHERE event_time >= TIMESTAMP '2025-09-01'` returns different rows before and after a CSV goes missing. It also crosses views: the store stamps `_fetched_at` as `fetchedAt.UTC().Format(time.RFC3339)` (`internal/store/segment.go:69`) and declares it `TIMESTAMP` (`internal/store/columns.go:26`). Loaded through the exact `read_json` shape `storeView` uses and compared to a `to_timestamp` value for the same instant, the two compare **not equal** and subtract to **-4 hours**, the test machine's UTC offset. And because a `TIMESTAMPTZ` buckets in the session timezone, which DuckDB takes from the machine, `date_trunc('day', event_time)` puts the pinned epoch value in September 9 rather than September 10 on a US-Eastern machine, so "events per day" would differ by who ran it.

**Options considered**: append `AT TIME ZONE 'UTC'` and declare `TIMESTAMP`; keep the bare `to_timestamp` and declare `TIMESTAMPTZ` in both stand-ins; or keep it and `SET TimeZone='UTC'` on every connection in `engine.Open`.

**Decision**: `AT TIME ZONE 'UTC'`, declared `TIMESTAMP`. Verified that it yields a plain `TIMESTAMP` whose fields are UTC, stays NULL on a non-numeric input, unions with the `CAST(NULL AS TIMESTAMP)` stand-in as `TIMESTAMP`, compares equal to `_fetched_at` for the same instant, preserves the milliseconds `received_time` carries, and makes `date_trunc` machine-independent. It is also what this repo already does for its own time column: convert to UTC in Go, declare `TIMESTAMP`.

The `TIMESTAMPTZ` option is the better modeling choice in the abstract, and it was rejected because it buys a true instant type at the price of leaving both the cross-view comparison and the day-bucketing wrong-by-default and documented as caveats. The `SET TimeZone` option was rejected on a verified fact: it fixes comparison and bucketing but **not** the type instability, since the primary still binds `TIMESTAMPTZ` and the fallback `TIMESTAMP`, and it is a session-wide side effect on every other view for one column's benefit.

Because the column is now timezone-naive, that it holds UTC is a convention rather than a type, so the catalog entry has to say so.

### RESOLVED: what does `logs` look like on a dataset with no log runs?

**Context**: `reportUnionView` returns a `run_id`-only stand-in when nothing matches (`views.go:122-124`), so the query this story's catalog entry documents would fail with `Binder Error: Referenced column "extras_json" not found in FROM clause!` until the first log run lands. REPORT-94 met the same choice and went the other way: `standIn()`'s comment says it declares "the full typed column list rather than a run_id-only shape, so the documented joins can be run on a dataset before anything has been downloaded" (`views.go:609-610`).

**Options considered**: leave it `run_id`-only, matching `reports`; declare `run_id` plus the four derived columns; or declare `run_id` plus the twelve base log columns plus the four derived, mirroring `standIn()`.

**Decision**: `run_id` plus the four derived columns, implemented in `reportUnionView`.

The twelve-column option was rejected on a fact that kills it: `username` is not common to the log reports, so a stand-in declaring it would make `logs` *lose* a column once a student-actions run landed, which is the schema instability that option existed to prevent. That yields the rule stated in the Technical Notes, that a stand-in may only declare a subset of every populated schema. The four derived columns are the largest set satisfying it.

A query naming a source column still fails on an empty dataset, and that is correct rather than a shortfall: source columns depend on what was downloaded, which is the contract the `reports` entry already states when it calls `res_<N>_*` positional per run. The line falls exactly where cc-data's guarantee ends and the data's contribution begins.