# cc-data-cli: one row per log event across overlapping log runs and recent-log pulls

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-192

**Status**: **Closed**

## Overview

A new view, `log_events`, gives each log event in a dataset once, however many log runs and recent-log pulls hold it, by matching rows on the log's server-assigned `id`. `logs` and `recent_logs` stay as they are, and the guidance names `log_events` as the view to count events from.

A class's logs reach a dataset from more than one place, and those places overlap by design: a package that re-runs pulls a fresh whole-history Student Actions run each day, and recent-logs pulls (REPORT-152) re-read the last ten minutes on purpose and cover hours a nightly run also covers. Before this, every package that counted events had to work out for itself which rows were the same event (REPORT-189's CLUE package carries its own anti-join for it). Every log row carries an `id` the log ingester assigns, the same in every copy of the event, so copies can be matched exactly. Duplicates that got two different ids (a client re-sending an event) cannot be matched this way and stay out of scope.

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

  No other column: not `run_id` (an event has several), not `username` (it means up to five things across the three reports), and not the with-metadata learner columns (a recent or Student Actions copy has none, so they would depend on which copy won). Roster columns come from joining `student_id_mapping` or `student_metadata` on `run_remote_endpoint`.
- **R4. Which copy the row shows.** The newest fetch wins: a log run's copy is dated by its download's `fetched_at`, a `recent_logs` copy by its row's `_fetched_at`; equal times go to the higher run id, then to a log run over `recent_logs`. The two are compared as stored: a download's `fetched_at` to the microsecond, a `recent_logs` row's to the second. Copies still tied are copies within one CSV of one delivered record, and either gives the same row.
- **R5. Types.** Each column has R3's type however `DetectCSV` typed it in any one CSV. The VARCHAR columns carry the CSV's text unchanged (an `id` or `event_value` of digits stays text). `time`, `timestamp`, `user_id` and `primary_user_id` convert from the CSV's text, a value that does not convert being NULL rather than an error.
- **R6. `logs` and `recent_logs` are unchanged**: same rows, columns and types, `logs` still one row per run per event (REPORT-146's R4 takes a run's log freshness from it).
- **R7. Degradation**, as the views it reads behave: a log CSV missing on disk contributes no rows, with `logs`' warning; a missing `recent_logs` store file likewise, with its warning; a dataset with neither has an empty `log_events` with R3's columns and types; and a CSV or store that fails to bind leaves `log_events` installed with R3's schema rather than failing the session.
- **R8. Materialization.** `log_events` materializes like the other file-reading views, and its Parquet stops being used when a member CSV, the store file or the view's definition changes.
- **R9. Multi-dataset sessions** get one `log_events` per dataset, under its prefix, as every view does. Events are not matched across datasets.

### Tests

- **R10.** A dataset holding two overlapping Student Actions runs, a Student Actions with Metadata run overlapping them, a Teacher Actions run, and `recent_logs` rows overlapping the runs and one only in `recent_logs`, gives each `id` once, with `count(*)` equal to `count(DISTINCT id)` over `logs` and `recent_logs`, and `logs` still one row per run per event. Also: R3's columns, order and types on that dataset, on an empty one and on one with only runs or only `recent_logs`; `run_ids` and `_class_id` for an event in two runs and `recent_logs`, in two runs only, and only in `recent_logs`; R4's winner where a later run and a later recent pull each win, and its run-id and log-run-over-store tiebreaks; a NULL-`id` row kept beside another; a VARCHAR column `DetectCSV` typed BIGINT kept as text; a non-numeric `time` NULL without failing the query; R7's missing CSV, fallback schema and missing store file; R8's Parquet returning the same rows as the raw view; and one view per dataset (R9).

### Guidance

- **R11. `core.md`** (so the skill and the `guide` tool): a `log_events` entry in `## Views` (count events from it, never from `logs` or the union of `logs` and `recent_logs`; take a run's freshness from `logs`; the columns it carries and leaves out, with the `student_metadata` and `student_id_mapping` joins; scoping with `list_contains(run_ids, <run>) OR _class_id = <class>`; ids that differ are not matched; Teacher Actions events present with `user_id` and `run_remote_endpoint` NULL, so student counts add `WHERE user_id IS NOT NULL` and teacher analysis reads `logs`). The `logs` and `recent_logs` entries point to `log_events` instead of "count one view or the other"; the CLUE routing bullet names `log_events` for what the student had on screen; the log column list gains `id`; and "duplicate-free by construction" says overlapping log runs each keep their copy in `logs`.
- **R12. Researcher guide and README**: a `log_events` row in the views table; the "A dataset never holds duplicate data" bullets name log runs as the exception; the `recent_logs` row, the Recent logs "Days before the window" bullet, the Materializing "It is bigger" bullet and the "Comparing over time" bullet follow suit. `README.md`'s query bullet lists `log_events`, and its "Writes are atomic" bullet says queries see complete data, never a page twice, rather than "duplicate-free data".
- **R13. Tool text**: the `get_recent_logs` MCP tool description and `cc-data get recent-logs`' long help say to count from `log_events`.

## Technical Notes

- **Files.** `internal/duck/log_events_view.go` is the builder, registered after `logsView` in `views.go`'s `statements()`; its tests are in `log_events_view_test.go`. `views.go`'s `csvRead` is the one `read_csv(...)` call shared by `csvScan`, `dimensionScan` and `log_events`, so the three agree on dialect handling.
- **How it reads.** Each log CSV is read with every recorded column as VARCHAR, and the four integer columns are `TRY_CAST` to `recent_logs`' pins (`store.MustKind(store.TypeRecentLogs).Pinned`), so a column's type never depends on what `DetectCSV` saw in one file. `recent_logs` comes through `clueSource`. The members are `UNION ALL`ed in R3's order and kept with `QUALIFY id IS NULL OR ROW_NUMBER() OVER (PARTITION BY id ORDER BY _fetched_at DESC, run_id DESC NULLS LAST) = 1`.
- **Why it reads the files itself** rather than selecting from `logs` and `recent_logs`: that would inherit each CSV's detected types (R5) and declare no files, so it would never materialize (R8), the reason REPORT-111's `clue_patches` reads its store directly.
- **`run_ids` needs a coalesce.** `list(DISTINCT run_id) FILTER (WHERE run_id IS NOT NULL) OVER (PARTITION BY id)` is NULL, not `[]`, for an event only `recent_logs` holds. A NULL-`id` row's `run_ids` is its own run alone. `recentLogsKind` is `Strict`, so the store refuses a NULL `id` and only CSV rows reach the NULL partition.
- **Guards that pick the view up.** `StaticViewNames()` builds over an empty manifest, so `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` fail until a new view's catalog entries exist; `materializableViews` admits any static view that declares files. The MCP instructions index names no view, so it did not change.
- **Signatures.** The `csvRead` extraction leaves every other view's primary and fallback statement byte-identical, so no materialized Parquet goes stale on upgrade. An older cc-data ignores a materialized `log_events` entry, since `applyMaterialized` looks up only the views it builds.
- **The `id` is the same in every copy.** The log ingester's transform sets it once (`nanoid(21)`) before S3. Firehose re-delivering a transformed buffer repeats its ids, the one way one CSV can hold an `id` twice (R4's last tie).
- **Checked on real data:** over copies of runs 2412, 2415 (two overlapping Student Actions runs), 2413 (Teacher Actions) and 253, `log_events` gave 77,829 rows, one per distinct `id` of 89,824 `logs` rows, 11,995 with `run_ids` `[2412, 2415]`; 2,103 are run 2413's teacher rows, the only ones without a `user_id`.
- **Cost.** Two 3M-row Student Actions CSVs overlapping by 2M plus a 200,000-row recent store, under the sandbox's 2 GB and 4 threads: 3.5 s for a query reading every column; `run_ids` added no measurable time.

## Out of Scope

- Duplicates under two different `id`s (a client re-posting an event, the ingester's transform retried): nothing short of comparing content can match them.
- Changing `logs` or `recent_logs`, or deduplicating log runs at fetch time into a store.
- Matching events across datasets in a multi-dataset session.
- REPORT-189 switching its `EVENTS` anti-join to `log_events`: it works as written and needs roster columns `log_events` leaves out. Its own `log_events` CTE shadows the view.
- `student_log_events` and `teacher_log_events`. A student view is `log_events`' builder with `user_id IS NOT NULL` added (a view over `log_events` would declare no files and never materialize); a teacher view needs its own builder keeping `username`, the only column naming the teacher.
- REPORT-185 (removing a superseded run).

## Decisions

### A fixed column set, not a union of every source's columns
**Context**: Jira leaves the column set to the spec, and the three log reports differ.
**Options considered**:
- A) The 13 Student Actions columns at fixed types, plus provenance and the derived columns.
- B) Every column of every source (`UNION ALL BY NAME`), the winning copy's values.

**Decision**: A. Under B a row's learner columns and `username` would depend on which copy won (a recent pull usually wins, and carries none), the schema would change with what the dataset holds, and `username` would mix meanings. The 13 are what every student source shares and what `recent_logs` pins; roster columns are one join away.

---

### `run_ids` and `_class_id` rather than the winner's `run_id`
**Context**: A package scopes events to its run and class.
**Options considered**:
- A) Every run holding the event, and the recent copy's class.
- B) The winning copy's `run_id`, as the dimension views keep.

**Decision**: A. Under B, `WHERE run_id = 584` would silently miss every event of run 584 whose newest copy is in another run or in `recent_logs`. The list costs nothing measurable.

---

### A row with no `id` is kept as its own event
**Context**: `PARTITION BY id` would put every NULL-`id` row in one partition and keep one.
**Options considered**:
- A) Keep each NULL-`id` row.
- B) Drop them.
- C) Let them collapse.

**Decision**: A. No real row lacks an `id`, but a CSV from a report without the column would otherwise lose all its rows (B) or all but one (C). Keeping them overcounts only what cannot be matched.

---

### R4's last tie is left open
**Context**: Closing the order with every source column makes the chosen row deterministic even for two different rows with one `id` in one CSV, at roughly double the cost (6.5 s against 3.5 s).
**Options considered**:
- A) Leave it open.
- B) Close it with every source column.
- C) Close it with a cheap column, which settles most but not all differing pairs.

**Decision**: A. One CSV can repeat an `id` two ways: Firehose re-delivering a buffer byte for byte, or a learner row the portal emits twice, whose copies differ at most in the learner columns. R3 carries none of the columns that could differ, so B's cost buys nothing a reader could see.

---

### Teacher events are counted as events (Doug)
**Context**: R1 admits Teacher Actions runs, whose rows have no `user_id` or `run_remote_endpoint`.
**Options considered**:
- A) Keep them, and give `WHERE user_id IS NOT NULL` for counting student events.
- B) Leave Teacher Actions out, which needs a column-sniffing admission rule since `logsView` admits by report type.
- C) Add a source-report column, widening R3 for one report's rows.

**Decision**: A. Every student row has a `user_id` (the log reports inner-join learners) and no teacher row does. Narrower views can follow (Out of Scope).

---

### Fetch times compared at their stored precisions
**Context**: `recent_logs`' `_fetched_at` is whole seconds; a download's is microseconds.
**Decision**: Compare them as stored. Only copies fetched within the same second can order differently from the wall clock, and they carry the same R3 columns.

---

### Extract `csvRead`
**Context**: A third copy of the `read_csv(...)` format string, or a shared helper.
**Options considered**:
- A) One helper used by `csvScan`, `dimensionScan` and `log_events`.
- B) A third copy.

**Decision**: A. The three must agree on dialect handling, and the extraction left every other view's statements byte-identical.

---

### "Duplicate-free by construction" is qualified in `core.md`
**Context**: `core.md`'s first bullet, which the MCP index echoes in its first sentence, is true of re-fetching a run but reads as true of overlapping log runs.

**Decision**: R11 qualifies `core.md`'s bullet, which the skill and the `guide` tool carry; the headers' one-line "duplicate-free" summary is left as it is.
