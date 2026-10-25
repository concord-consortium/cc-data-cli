# cc-data-cli: one row per log event across overlapping log runs and recent-log pulls

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-192
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

A new view, `log_events`, gives each log event in a dataset once, however many log runs and recent-log pulls hold it, by matching rows on the log's server-assigned `id`. `logs` and `recent_logs` stay as they are, and the guidance names `log_events` as the view to count events from.

## Project Owner Overview

A class's logs reach a dataset from more than one place, and those places overlap by design: a package that re-runs pulls a fresh whole-history Student Actions run each day, and the recent-logs pulls that 0.3.0 adds (REPORT-152) re-read the last ten minutes on purpose and cover hours a nightly run also covers. Today nothing in cc-data removes the copies, so every package that counts events has to work out for itself which rows are the same event, and one that forgets double counts. REPORT-189's CLUE package already carries its own anti-join for this.

This story puts that work in cc-data once. Every log row carries an `id` the log ingester assigns, the same in every copy of the event, so copies can be matched exactly. Duplicates that got two different ids (a client re-sending an event) cannot be matched this way and stay out of scope.

## Background

**Jira** is the scope: a view with one row per log `id` across every log run in the dataset and the rows REPORT-152's `recent_logs` stores; `logs` unchanged; the column set and tiebreak decided here; guidance that names the view and lists `id`. Background research: `log-cursor-and-dedup.md` (global oob, `streams/researcher-dashboard/`), whose "Dedup options" recommends this view.

**This branch** (`REPORT-192-one-row-per-log-event`) stacks on `REPORT-115-clue-guidance`, the last branch of the cc-data-cli 0.3.0 stack (REPORT-110 `REPORT-110-clue-fetch`, REPORT-111 `REPORT-111-clue-views`, REPORT-152 `REPORT-152-recent-logs`, RD-15 `RD-15-raise-display-limit`, REPORT-115). Everything below is as of that branch.

**Where log rows live today.**

- **`logs`** (`internal/duck/views.go`, `logsView` over `reportUnionView`) is a `UNION ALL BY NAME` over every download with `Type == "report"` and `ReportType == "log"`, each read with the column types `DetectCSV` recorded for that file, plus `run_id` and four derived columns (`logDerived`: `parameters_json`, `extras_json`, `event_time`, `received_time`). Two overlapping runs give an event twice. REPORT-146's R4 makes `logs WHERE run_id = <run>` a run's log freshness (`core.md`), so `logs` keeping one row per run per event is a contract.
- **`recent_logs`** (REPORT-152) is one merged store keyed by `id` (`store.recentLogsKind`: `Strict`, `ClassKeyed`, `Accumulate`), so it holds one row per `id` whatever the number of pulls. Its 13 pinned source columns are `id`, `session`, `application`, `activity`, `event`, `event_value`, `parameters`, `extras`, `run_remote_endpoint` (VARCHAR) and `time`, `timestamp`, `user_id`, `primary_user_id` (BIGINT), plus `_class_id` and `_fetched_at` stamps. `recentLogsView` adds `logDerived`. The guidance today says an event a log run also holds is in both views, "so count one view or the other" (`core.md`, the researcher guide, the `get_recent_logs` tool description); `cc-data get recent-logs`' help says only that earlier days land in `logs`.
- **REPORT-189** (cc-data-studies `REPORT-189-clue-dataflow-package`, `stages.py` `EVENTS`) works around the overlap itself: a log run's `logs` rows plus the class's `recent_logs` rows whose `id` the run does not hold. It also reads the with-metadata run's roster columns (`offering_id`, `class`, `school`).

**The three log reports** (report-service `master`, `server/lib/report_server/reports/report_query.ex` `get_log_cols`, and `athena/*_report.ex`):

| Report (slug) | Columns |
|---|---|
| Student Actions (`student-actions`) | the 11 log columns `id, session, application, activity, event, event_value, time, parameters, extras, run_remote_endpoint, timestamp` (`username` removed), then `user_id, primary_user_id` |
| Student Actions with Metadata (`student-actions-with-metadata`) | the same 11, then 14 learner columns: `learner_id, class_id, runnable_url, student_id, class, school, user_id, primary_user_id, offering_id, permission_forms, username, student_name, teachers, last_run`. `username` is the learner's, a salted hash when the run hid names, when `student_name` holds the student id |
| Teacher Actions (`teacher-actions`) | the 11 plus the log's own `username` (the teacher's `<user id>@<portal>`), no `user_id` or `primary_user_id` |

The recent-logs route answers the Student Actions columns, so the 13 Student Actions columns are exactly `recent_logs`' source columns.

**Checked on real data (2026-10-25, read-only DuckDB over the local datasets' CSVs).**

- `learn.concord.org/dataflow-cohort`: 7 Student Actions with Metadata runs, 263,570 rows, every row with an `id`, no `id` repeated within a run or across runs.
- `learn.concord.org/isaias-logs-2026-10-16` run 2412 and `isaias-logs-2026-10-16-backfilled` run 2415, two overlapping Student Actions runs: 26,801 rows, 14,806 ids, all 11,995 of run 2412's ids also in run 2415, and for every shared id the 12 other shared columns are identical in both runs. A Teacher Actions run (2413) has the 12 columns in the table above.
- Staging `student-actions-test` runs 186 and 187: 728 rows, 364 ids.

**Cost, measured (throwaway, DuckDB CLI 1.5.5 under cc-data's sandbox settings: `memory_limit` 2 GB, 4 threads).** Two Student Actions CSVs of 3M rows each overlapping by 2M (2.75 GB of CSV) plus a 200,000-row recent store, deduplicated with `QUALIFY ROW_NUMBER() OVER (PARTITION BY id ORDER BY <fetch time> DESC, run_id DESC) = 1`: 4M rows out, `count(*) = count(DISTINCT id)`, 3.5 s for a query reading every column, and a per-event list of the runs holding it (`list(DISTINCT run_id) OVER (PARTITION BY id)`) added no measurable time. Closing the order with every source column as well took 6.5 s.

## Requirements

### The view

- **R1. `log_events`** is a view in every dataset, beside `logs`, built from every download `logs` admits (`Type` `report`, `ReportType` `log`: Student Actions, Student Actions with Metadata and Teacher Actions runs) and the `recent_logs` store.
- **R2. One row per `id`.** Rows from any of those sources with the same non-NULL `id` are one event and give one row. A row whose `id` is NULL (or a CSV with no `id` column) cannot be matched, so each such row is its own event: never merged with another, never dropped. On a dataset whose rows all carry an `id`, `SELECT count(*) FROM log_events` equals the number of distinct `id`s over `logs` and `recent_logs` together, compared as text (a CSV of all-digit ids scans `id` as BIGINT in `logs`).
- **R3. Columns**, the same on every dataset whatever it holds, in this order:
  - the 13 Student Actions columns at `recent_logs`' pinned types: `id`, `session`, `application`, `activity`, `event`, `event_value`, `time`, `parameters`, `extras`, `run_remote_endpoint`, `timestamp`, `user_id`, `primary_user_id`. A source without one of them (Teacher Actions' `user_id`) gives NULL;
  - `run_ids` `BIGINT[]`: every log run in the dataset holding the event, ascending, each once; `[]` when only `recent_logs` holds it;
  - `_class_id` `BIGINT`: the class of the `recent_logs` row holding the event, NULL when `recent_logs` does not hold it;
  - `_fetched_at` `TIMESTAMP` (UTC): when the copy the row shows was fetched (R4);
  - `logs`' four derived columns, `parameters_json`, `extras_json`, `event_time`, `received_time`, computed as `logs` computes them.

  No other column: not `run_id` (an event has several), not `username` (it means up to five things across the three reports, `core.md`'s `logs` entry), and not the with-metadata learner columns (a recent or Student Actions copy has none, so they would depend on which copy won). Roster columns come from joining `student_id_mapping` or `student_metadata` on `run_remote_endpoint`.
- **R4. Which copy the row shows.** The newest fetch wins: a log run's copy is dated by its download's `fetched_at`, a `recent_logs` copy by its row's `_fetched_at`; equal times go to the higher run id, then to a log run over `recent_logs`. The two are compared as stored: a download's `fetched_at` to the microsecond, a `recent_logs` row's to the second (`store.stampLines` writes RFC 3339 seconds). Copies still tied are copies within one CSV of one delivered record, and either gives the same row (R10's tests pin a tie of identical rows, not a choice between different ones).
- **R5. Types.** Each column has R3's type however `DetectCSV` typed it in any one CSV. The VARCHAR columns carry the CSV's text unchanged (an `id` or `event_value` of digits stays text). `time`, `timestamp`, `user_id` and `primary_user_id` convert from the CSV's text, a value that does not convert being NULL rather than an error, so one bad cell costs that cell, never the query.
- **R6. `logs` and `recent_logs` are unchanged**: same rows, columns and types, `logs` still one row per run per event.
- **R7. Degradation**, as the views it reads behave: a log CSV missing on disk contributes no rows, with `logs`' warning; a missing `recent_logs` store file likewise, with its warning; a dataset with neither has an empty `log_events` with R3's columns and types; and a CSV or store that fails to bind leaves `log_events` installed with R3's schema rather than failing the session.
- **R8. Materialization.** `log_events` materializes like the other file-reading views, and its Parquet stops being used when a member CSV, the store file or the view's definition changes (a new or re-fetched log run).
- **R9. Multi-dataset sessions** get one `log_events` per dataset, under its prefix, as every view does. Events are not matched across datasets.

### Tests

- **R10. Tests.** A dataset holding two overlapping Student Actions runs, a Student Actions with Metadata run overlapping them, a Teacher Actions run, and `recent_logs` rows overlapping the runs and one only in `recent_logs`, gives each `id` once in `log_events`, with `count(*)` equal to `count(DISTINCT id)` over `logs` and `recent_logs`, and `logs` still one row per run per event. Also: R3's columns and types on that dataset, on an empty one and on one with only runs or only `recent_logs`; `run_ids` and `_class_id` for an event in two runs and `recent_logs`, in two runs only, and only in `recent_logs`; R4's winner where a later run and a later recent pull each win; a NULL-`id` row kept beside another; a VARCHAR column whose CSV `DetectCSV` typed BIGINT (all digits) kept as text; a non-numeric `time` NULL without failing the query; R7's missing CSV; R7's fallback carrying R3's columns and types, and a missing store file costing only its rows and warning once; and R8's Parquet returning the same rows as the raw view.
### Guidance

- **R11. `core.md`** (and so the skill and the `guide` tool, which carry it):
  - a `log_events` entry in `## Views`: one row per event across log runs and recent pulls, matched by `id`; **count events from it**, never from `logs` or the union of `logs` and `recent_logs`; take a run's freshness from `logs`; the columns it carries and the ones it leaves out (R3), with the joins for roster columns (`student_metadata` for `username`, names, class and school; `student_id_mapping` for ids and `offering_id`); `run_ids` and `_class_id` to scope it (`list_contains(run_ids, <run>) OR _class_id = <class>`); that two copies with different `id`s (a client re-sending an event) are not matched; and that a Teacher Actions run's events are in it with `user_id` and `run_remote_endpoint` NULL and without the teacher's `username`, so counting student events adds `WHERE user_id IS NOT NULL` (every student row has a `user_id`, since the log reports join learners) and teacher analysis reads `logs`;
  - the `logs` and `recent_logs` entries' "count one view or the other" replaced by a pointer to `log_events`;
  - the CLUE routing bullet's "`logs` and `recent_logs` add what the student had on screen between edits" names `log_events` instead;
  - the log-run column list in "Runs and their data" gains `id` (the server-assigned event id, the same in every copy of an event);
  - that section's first bullet ("duplicate-free by construction") says overlapping log runs each keep their copy of an event in `logs`, and `log_events` gives it once.
- **R12. Researcher guide and README** (`docs/researcher-guide.md`, `README.md`): a `log_events` row in the views table, the "A dataset never holds duplicate data" bullet saying overlapping log runs each keep their copy and `log_events` has each event once, the `recent_logs` row's "count one or the other" and the Recent logs section's "Days before the window" bullet pointing to `log_events` instead, the Materializing section's "It is bigger than the one view you care about" bullet naming `log_events` as one more copy of the log rows, and the "Comparing over time means separate datasets" bullet resting on re-pulling a run overwriting its records rather than on a dataset never keeping duplicates. `README.md`'s query bullet lists `log_events`, its "A dataset never contains duplicate data" bullet says log runs stay per-run CSVs and `log_events` has each event once, and its "Writes are atomic" bullet says queries see complete data, never a page twice, rather than "duplicate-free data".
- **R13. Tool text**: the `get_recent_logs` MCP tool description and `cc-data get recent-logs`' long help say to count from `log_events` rather than one view or the other.

## Technical Notes

- **Files**: `internal/duck/log_events_view.go` (new: the builder), `internal/duck/views.go` (registered after `logsView` in `statements()`, and the shared `csvRead`), its tests in `log_events_view_test.go` beside `logs_views_test.go` and `recent_logs_view_test.go`, `internal/guidance/src/core.md`, `docs/researcher-guide.md`, `README.md`, `internal/mcpserver/tools.go` (and the description pin in `server_test.go`), `cmd/get_recent_logs.go`.
- **Guards that pick the view up by themselves**: `StaticViewNames()` builds the statement set over an empty manifest, so `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` fail until R11's and R12's entries exist; `materializableViews` admits any static view that declares files (R8). The MCP instructions index (`guidance.Instructions()`) names no view, so it does not change.
- **Reading the CSVs.** `logs` reads each CSV at its detected types, which can differ per file (an all-empty column is VARCHAR, a numeric `event_value` BIGINT or DOUBLE). R5 needs `log_events` to read the shared columns itself rather than from `logs`. A view that only reads other views also declares no files and would never materialize (REPORT-111 met the same with `clue_patches`, which reads its store directly for that reason).
- **The `id` is the same in every copy.** It is set once by the log ingester's transform (`cloud-formation` `log-ingester.json`, `nanoid(21)`), before S3, so the raw table, the nightly copy and `logs_by_hour` carry it unchanged (REPORT-107 reconciled staging days id for id). Firehose re-delivering an already-transformed buffer repeats its ids, which is the one way one CSV can hold an `id` twice (R4's last tie).
- **Checked through cc-data's engine (2026-10-25, throwaway).** A `log_events` builder in `internal/duck`, registered after `logsView`, over Student Actions, Teacher Actions and with-metadata CSVs written with `DetectCSV` and a `recent_logs` store landed with the test helpers: each CSV read with every recorded column as VARCHAR (`orderedColumnsClause` over `ColumnOrder`) and the shared columns cast as R5 says; `recent_logs` through `clueSource`; `UNION ALL` of members already in R3's order; `QUALIFY id IS NULL OR ROW_NUMBER() OVER (PARTITION BY id ORDER BY _fetched_at DESC, run_id DESC NULLS LAST) = 1`. It gave R3's types on a populated and an empty dataset, 8 rows for 6 ids and 2 NULL-id rows (10 `logs` rows), `007` as text, a `time` of `oops` as NULL, an `id` twice in one CSV once, `run_ids` `[10, 11]` and `_class_id` 590 for an event in two runs and `recent_logs`, 6 rows with one CSV deleted, and the same 8 rows from its Parquet, which `Materialize` wrote and the next session read. `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` then failed naming `log_events`, as Technical Notes says.
- **`run_ids` needs a coalesce.** `list(DISTINCT run_id) FILTER (WHERE run_id IS NOT NULL) OVER (PARTITION BY id)` is NULL, not `[]`, for an event only `recent_logs` holds (measured), so R3's `[]` is `coalesce(..., [])`. A NULL-`id` row's `run_ids` is its own run alone, not the runs of every other NULL-`id` row its partition would collect.
- **Cost.** Per Background, about a second per 1.8M input rows at the sandbox's 4 threads; a package's per-class dataset is far below that. Materializing (R8) pays it once.

## Self-Review

Roles: package author (REPORT-189, Wildfire), senior Go engineer on `internal/duck`, QA engineer, education researcher, performance engineer. Each finding was checked against the code or the local datasets before it was written; all were fixed in place.

- **RESOLVED: Teacher events lose who did them.** A Teacher Actions CSV (run 2413) has `username` and no `user_id` or `run_remote_endpoint` (all 2,103 rows empty), so in `log_events` its events carry no actor. R11 now says so and sends teacher analysis to `logs`.
- **RESOLVED: Another Parquet copy.** The researcher guide's "It is bigger than the one view you care about" bullet explains why `materialized/` outgrows one view; a materialized `log_events` is a third copy of the log rows. Added to R12.
- **RESOLVED: "Duplicate-free by construction".** `core.md`'s first bullet, which the MCP index repeats in its first sentence, is true of re-fetching a run but reads as true of overlapping log runs, which it is not. R11 qualifies it.
- **RESOLVED: Comparing ids.** `logs` scans an all-digit `id` column as BIGINT (`DetectCSV`), so R2's count is stated over ids compared as text.
- **RESOLVED: Fetch times at two precisions.** `recent_logs`' `_fetched_at` is whole seconds; a download's is microseconds. Only copies fetched within the same second can be ordered by it differently from the wall clock, and they carry the same R3 columns (R4's decision). Stated in R4.

Second pass. Roles: education researcher, docs reviewer. Checked against the code and copies of real runs 2412, 2413, 2415 and 253.

- **RESOLVED: Technical Notes named the wrong files.** It put the builder in `views.go` and left out `README.md`, while the plan adds `log_events_view.go` and edits `README.md` (R12). Fixed.

- **RESOLVED: Teacher events are counted as events (Doug: A).** R1 admits Teacher Actions runs, and over runs 2412, 2415, 2413 and 253, 2,103 of `log_events`' 77,829 rows are run 2413's teacher events. Every student row has a `user_id` (the log reports `INNER JOIN` learners, `report_query.ex`; all 75,726 student rows above do) and no teacher row does, so R11's entry gives `WHERE user_id IS NOT NULL` for counting student events. Rejected: leaving Teacher Actions out, which needs a column-sniffing admission rule since `logsView` admits by report type, and a source-report column, which widens R3 for one report's rows. Narrower views can follow later (Out of Scope).

Fourth pass (findings in implementation.md's Self-Review): R11's roster join named `student_id_mapping` alone, which has no names, class or school; R12 left out the researcher guide's "A dataset never holds duplicate data" bullet; R3 miscounted `username`'s meanings.

Fifth pass (findings in implementation.md's Self-Review): R12 left out the researcher guide's "Comparing over time" bullet, which still said a dataset never keeps duplicates.

## Out of Scope

- Duplicates under two different `id`s (a client re-posting an event, the ingester's transform retried): nothing short of comparing content can match them (Jira).
- Changing `logs` or `recent_logs`, or deduplicating log runs at fetch time into a store.
- Fetching recent logs (REPORT-152) and the route (REPORT-150).
- Matching events across datasets in a multi-dataset session.
- REPORT-189 switching its `EVENTS` anti-join to `log_events`: it works as written, and it needs roster columns `log_events` leaves out. Its own `log_events` fragment is a CTE, which shadows the view, so it keeps reading its own rows. Any switch is that package's follow-up.
- `student_log_events` and `teacher_log_events`. A student view is `log_events`' builder with `user_id IS NOT NULL` added (a view over `log_events` would declare no files and never materialize). A teacher view needs its own builder keeping `username`, the only column naming the teacher, which `log_events` leaves out.
- REPORT-185 (removing a superseded run): once it lands, a leftover run no longer double-counts in `log_events` anyway.

## Open Questions

### RESOLVED: Judgment call: a fixed column set, not a union of every source's columns
**Context**: Jira leaves the column set to the spec, and the three reports differ.
**Options considered**:
- A) The 13 Student Actions columns at fixed types, plus provenance and the derived columns.
- B) Every column of every source (`UNION ALL BY NAME`), the winning copy's values.

**Decision**: A. Under B a row's learner columns and `username` would depend on which copy won (a recent pull usually wins, and carries none), the schema would change with what the dataset holds, and `username` would mix four meanings. The 13 are what every student source shares and what `recent_logs` pins, and roster columns are one join away.

### RESOLVED: Judgment call: `run_ids` and `_class_id` rather than the winner's `run_id`
**Context**: A package scopes events to its run and class (REPORT-189 reads `logs WHERE run_id = <run>` and `recent_logs WHERE _class_id = <class>`).
**Options considered**:
- A) Every run holding the event (`run_ids`) and the recent copy's class.
- B) The winning copy's `run_id`, as the dimension views keep.

**Decision**: A. Under B, `WHERE run_id = 584` would silently miss every event of run 584 whose newest copy is in another run or in `recent_logs`. The list costs nothing measurable (Background).

### RESOLVED: Judgment call: a row with no `id` is kept as its own event
**Context**: `PARTITION BY id` would put every NULL-`id` row in one partition and keep one of them.
**Options considered**:
- A) Keep each NULL-`id` row.
- B) Drop them.
- C) Let them collapse.

**Decision**: A. No real row lacks an `id` (Background), but a CSV from a report without the column would otherwise lose all its rows (B) or all but one (C). Keeping them overcounts only what cannot be matched.

### RESOLVED: Low confidence: R4's last tie is left open
**Context**: Closing the order with every source column makes the row chosen deterministic even for two different rows with one `id` in one CSV, and roughly doubles the view's cost (Background: 6.5 s against 3.5 s).
**Options considered**:
- A) Leave it open, as R4 says.
- B) Close it with every source column.
- C) Close it with a cheap column (`timestamp`, then `session`), which settles most but not all differing pairs.

**Decision**: A. One CSV can repeat an `id` two ways, and neither varies R3's columns. Firehose re-delivering a transformed buffer repeats the record byte for byte. And a log report joins each log row to `"report-service"."learners"` on `run_remote_endpoint` (`report_query.ex` `get_athena_query`), so a learner row the portal query emits twice (the case `views.go`'s `dimensionView.dedupe` comment describes) emits the log row twice; the copies differ at most in the learner columns, while `user_id` and `primary_user_id` are the one learner's. R3 carries none of the columns that could differ, so B's cost buys nothing a reader could see.
