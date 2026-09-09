# Consume Portal Reports in cc-data

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-94

**Status**: **Closed**

## Overview

Teach cc-data that a report run can be a Portal run: download its CSV over the sync streaming path instead of the Athena poll-and-presign path, list it with a meaningful state, and expose the Student ID Mapping and Student Metadata reports as first-class join dimensions. A Student ID Mapping run then drives `get answers`, `get history` and `get attachments` with no Athena run involved.

Half of report-service's reports are computed live against the portal database rather than run through Athena, and cc-data could not use any of them. The two that matter most exist specifically so a researcher can name a set of learners and then pull their answers, history and attachments against that set; before this, that pull required authoring an Athena report first, purely to obtain a run id, which cost money and time for data nobody reads. This is what the CLUE Dataflow port (REPORT-118) needs first, and the last piece of the Portal side of report-service being usable from a terminal.

## Requirements

All requirements were implemented. Rationale for the non-obvious ones is in Decisions below.

### Downloading a Portal report

- `cc-data get report <run-id>` downloads any API-exposed Portal report, taking the streamed CSV path rather than the poll-and-presign path. The branch is on the run's `execution`, which the client learns from the run metadata it already fetches before downloading.
- Nothing about the Athena path changes: an async run still polls `athena_query_state`, still backs off on `NOT_READY`, still follows the presigned envelope, and still retries a failed presign by re-minting.
- The streamed path writes straight to the dataset file rather than buffering the response, since a Portal CSV over a real cohort is large and the endpoint has no size bound.
- The streamed path keeps `Client.do`'s error classification and its retry of an idempotent request on 429 and 5xx within the backoff budget, with an exhausted budget surfaced as a transient failure.
- It does not keep `do`'s per-attempt deadline. `RequestTimeout` bounds a JSON call, and a report body legitimately outlives it, so the bound on a streamed download is the caller's context. The server allows a Portal download 120 seconds and the client's per-attempt deadline is 60, so carrying it over would abandon large cohorts mid-body.
- It retries only while nothing has been written to the destination. Once bytes have landed the download is terminal: the response was already committed, there is no resume, and the most likely cause is the server exceeding its own download budget, which a retry reproduces exactly. Every refusal worth retrying, including the concurrency limiter's 503, arrives before any body bytes.
- A canceled caller context is excluded from that classification and surfaces as itself, since the caller's context is the only bound on the download and an interrupt must not be reported as the server exceeding its budget. *(Added during implementation; see Decisions.)*
- A 422 from a run with no filters is surfaced as itself. It is the one refusal only the Portal path can produce, and nothing retries it.
- The download lock and the manifest entry work identically for both paths, because they are keyed on the run id and not on how the bytes arrived. The `EXISTS` guard stays, but its message for a sync run names `--refresh` as the way to re-pull live data.
- `--job` is refused on a sync run. `--no-wait` and `--poll-timeout` stay no-ops rather than usage errors.

### Listing Portal runs

- `cc-data reports list` and the `reports_list` MCP tool distinguish a Portal run from an Athena one. Previously `STATE` was derived solely from `athena_query_state`, so every Portal run printed `(none)`, which reads as a broken Athena run rather than as a live report.
- The JSON payload carries the run's `execution` alongside the fields it already has, so an MCP client can tell the two apart without inferring it from a null state.

### Pulling learner data from a mapping run

- `get answers`, `get history` and `get attachments` accept a Student ID Mapping run id and pull those learners' records, with no Athena run involved and no change to how records are stored or identified. This needed no production change: the bulk endpoints derive learners from the run's filter for any report that sets `derives_learner_data`, and neither bulk path ever fetches the run's metadata, so the run kind is invisible to them.
- The stored-record identity is unchanged: `(source_key, remote_endpoint, question_id[, history_id])`. `learner_id` is not part of it and does not appear in the fetched records, in `run_membership`, or in the stores; it exists only in the two new CSVs, which is what makes them join dimensions rather than another identity.
- A run whose report cannot derive learners returns the server's `UNPROCESSABLE` as an actionable message naming the report, not as an internal error.

### The two dimension views

- The query engine exposes `student_id_mapping` and `student_metadata` as named views, recognized by report slug through the download provenance the manifest already carries.
- `student_id_mapping` joins to `answers` and `history` on `run_remote_endpoint = remote_endpoint`, and `student_metadata` joins to `student_id_mapping` on `learner_id`. Both joins are stated in the guidance, along with the view's contract: one row per `learner_id`, and a `run_remote_endpoint` that matches at most one learner or is NULL.
- Both views are deduplicated by `learner_id`, latest fetch winning, because a dataset can hold several overlapping mapping runs and the alternative is a view that silently multiplies rows on every join.
- The recency signal driving the dedupe is not part of the view's columns. A caller sees the same columns the CSV has, plus the `run_id` every union member already carries.
- A duplicate `learner_id` within one run is possible, because the Portal query groups by the report-learner row rather than by the learner id, so the dedupe's ordering is closed with the remaining columns and names a defined survivor instead of an arbitrary one. The condition is not instrumented at fetch time; the guidance carries the one-liner that surfaces it on demand, comparing a run's own row count with its distinct learner count (`SELECT count(*), count(DISTINCT learner_id) FROM report_<run_id>`). Comparing against the dimension view's rows for that run would not answer the question, since that view deduplicates across runs and so drops a learner a later run also holds. The check that would mean anything runs against a real cohort in REPORT-118.
- `run_remote_endpoint` is NULL in both views when it is the bare trailing-slash form a learner with no `secure_key` produces, because that one string is shared by every such learner and would otherwise attribute one learner's answers to all of them. The learner stays in the view with all its other columns; only the join key is withheld.
- `student_metadata` exposes whether the run it came from hid names.
- `downloads` exposes the same `hide_names` for every download, so the distinction is available wherever the ambiguous columns appear rather than only on one view. *(Added during implementation; see Decisions.)*
- A report download records the run's filter in the manifest. The `filters` and `filter_labels` fields already existed and were already carried across a reindex, but nothing had ever written them, so `hide_names` was not derivable from what is on disk.

### Report-type recognition

- Every Portal run is typed `portal`, derived from the run's `execution` rather than from a slug list, so a Portal report added to the server later needs no cc-data change to be recognized. Previously an unknown slug was recorded verbatim as the report type and then dropped from the `reports` union view with a warning, so a Portal CSV downloaded successfully and then vanished from every query.
- The mapping and metadata CSVs appear in the `reports` union as well as in their own views, and each downloaded run stays individually queryable as `report_<run_id>`.
- An ordinary `dataset reindex` keeps both views populated, because the manifest already carries the slug across it. Nothing is recovered from CSV shape. When the manifest itself is gone, the two views come back empty and the existing per-run provenance warning says so, naming each run and the re-fetch that restores its report type, its filter and any dimension view it feeds.

### Guidance

- The two views get their entries in the shared guidance source in the same change that registers them, so REPORT-104's drift guard stays green. There are two such guards, not one: the catalog guard over `internal/guidance/src/core.md` and a second over the researcher guide's own views table. *(The second guard was discovered during implementation; the spec had named only the first.)*
- The entries are minimal by design: name, one-line purpose, join key. The workflow prose that teaches create-a-mapping-run-then-pull is REPORT-95. Correcting statements this story makes false (the researcher guide's "five kinds of report" and "not currently downloadable through cc-data", and the README's description of `get report` as polling) is not that prose; it is removing text that is now wrong.

### Tests

- Fake-server tests pinned to wire captures of the Portal download and of a Portal-run answers pull, captured against the report-service test fixture.
- View tests over real CSVs on disk that assert the dedupe picks the later fetch, that a learner present in only one run survives, and that the joins produce one row per learner.

## Technical Notes

**A Portal run did not fail slowly, it failed immediately and confusingly.** The Jira description predicted that `get report` on a Portal run "would poll a never-set state until the timeout and fail". Verified against a fake server: the run failed at once with exit 1, code `INTERNAL`, message `invalid character 'l' looking for beginning of value`. `ReportDownloadEnvelope` calls `getJSON`, the 200 response body is CSV, and `json.Unmarshal` chokes on the first letter of `learner_id`. Because that error is not an `APIError`, `pollUntilReady` returned it through `AsCLIError` on the first iteration and never polled.

**The streaming helpers could not be reused as they stood.** `streamURL/3` sends no `Authorization` header, has no retry loop, no per-attempt timeout, and collapses every non-2xx into an untyped HTTP-status error. All four are right for a presigned S3 URL and all four are wrong for an authenticated API endpoint. `Client.do` also reads the whole response body before anything inspects it, which is correct for a JSON envelope and wrong for a report CSV.

**A truncated Portal download is detectable, and was checked rather than assumed.** The endpoint uses `send_chunked/2`, so the response carries `Transfer-Encoding: chunked` and no `Content-Length`. Verified against a server that writes part of a CSV and then hijacks and closes the connection: `io.Copy` stops with `unexpected EOF`, so truncation surfaces as a copy error and the partial file is discarded.

**The dedupe shape works, and was run rather than reasoned about.** Verified against the DuckDB this project vendors (`duckdb-go/v2 v2.10504.0`): a union of two CSVs, each member injecting its own `run_id` and a `fetched_at` literal, wrapped in `SELECT * EXCLUDE (fetched_at) FROM (...) QUALIFY ROW_NUMBER() OVER (PARTITION BY learner_id ORDER BY fetched_at DESC, run_id DESC) = 1`, returns the later run's row for a learner in both runs, keeps a learner present in only the earlier run, and describes without `fetched_at`. `EXCLUDE` is what keeps the recency signal out of the view's schema while still ordering by it.

**The fetch time has to survive a reindex.** `reindexCSV` rebuilds every CSV download from the filesystem in filename order and stamps each with the current clock, and `carryProvenance` did not restore the prior value, so a reindex reordered overlapping runs by filename and silently changed which one the dimension views return. It also moved the fetch date `dataset show` reports to today, which nothing had depended on before. `carryProvenance` now restores a prior fetch time whenever there is one, leaving the generated value for the manifest-less path that has no prior entry to carry.

**`run_id` alone is the wrong recency signal.** A higher run id is a later-*created* run, not a later-*fetched* one, and cc-data's whole store model is incoming-wins by fetch. The manifest carries `FetchedAt` per download; run id remains as the tiebreaker for two downloads in the same instant.

**Report type for a Portal run is null on the wire, by design.** `ReportJSON.report_type/1` returns the report's `api_report_type` and Portal reports declare none.

**Which Portal reports the API exposes.** Seven: `student-id-mapping`, `student-metadata`, `teacher-status`, `school-metrics`, `summary-metrics-by-subject-area`, and the two resource-metrics reports. `school-metrics` and `summary-metrics-by-subject-area` set `derives_learner_data: false` and so cannot back a learner pull; the rest can.

**The metadata report is not a superset of the mapping report.** They share `learner_id`, `user_id`, `primary_user_id`, `student_id`, `class_id` and `run_remote_endpoint`. Mapping additionally has `offering_id` and `runnable_url`; metadata additionally has `school_id` and the human-readable columns. So a join of the two is not redundant, and neither can be dropped in favor of the other.

**`hide_names` changes a CSV's contents and, in one case, its detected type**, and it is not confined to the metadata report. Verified against a real class pulled under both roles: because a hidden name is the numeric student id, `DetectCSV` records `student_name` as BIGINT for the hidden run and VARCHAR for the shown one. The union coerces and both views behave, but comparing the column across runs needs a cast. `student-answers` and `student-assignment-usage` select the student id as `student_name` and a hashed username under the same names (`shared_queries.ex:57,174-175`), and `student-actions-with-metadata` does the same through `get_learner_cols` (`report_query.ex:84-88`). Four of the five Athena reports therefore feed `reports` with the same ambiguity and always have.

**Nothing here needed the create endpoint.** REPORT-93 adds `reports create`, and the two stories are independent: this one consumes a run id however it was obtained, including one authored in the web form. The port story that needs both is REPORT-118.

## Out of Scope

- Creating or duplicating runs from the CLI, which is REPORT-93.
- The `logs` view with parsed `parameters` and `extras`, which is REPORT-112 and rides this story's view pattern afterwards.
- Dataset materialization, which is REPORT-113 and rewrites the same engine files afterwards.
- The full skill and MCP workflow prose for Portal reports, which is REPORT-95. This story writes only the catalog entries the drift guard requires, plus corrections to prose it makes false.
- Surfacing Athena failure reasons in cc-data's error text, which is REPORT-127. It touches the same two files and is deliberately not folded in: it spans both repositories, and its own spec sequences it strictly after this story.
- A one-shot pull command that chains create, download and fetch. The primitives are chained by the skill.
- Any change to how answers, history or attachments are stored, identified or merged.

## Not Yet Implemented

- **Fetch-time detection of a duplicate `learner_id` within one run.** Deliberately not built: it would instrument a condition never observed, whose drop the closed ordering already makes deterministic. The guidance carries a count one-liner instead, and the check that would mean something runs against a real cohort in REPORT-118.
- **Recovery of the two Portal slugs from CSV shape on a manifest-less reindex.** Dropped rather than repaired; see Decisions. The two views come back empty and the widened `RECOVERED_PROVENANCE` warning names each run and the re-fetch that restores it.

## Decisions

### What report type do the Portal reports carry?

**Context**: `report_type` is null on the wire for every Portal run, and the client's `slugToType` map knew only the five Athena slugs. Something had to fill the gap, and the choice decided whether the Portal CSVs join the `reports` union view and under what type.

**Options considered**:
- A) Extend `slugToType` with the seven Portal slugs, mapping them to new type values, and add those to `IsAllowedReportType`.
- B) Extend the maps only for the two reports this story exposes as views, leaving the other five quarantined.
- C) Give every Portal report the single type `portal`, so the vocabulary does not grow per report and `downloads.slug` remains the discriminator.

**Decision**: C, and derived from the run rather than from a slug list. `resolveReportType` already receives the run, and this story adds `execution` to it, so a run whose `execution` is `sync` and whose `report_type` is null is typed `portal` without consulting any map. A Portal report added to the server later then needs no cc-data change at all, where every map-based option needs two edits before the new report stops emitting two warnings and vanishing from the union. B was rejected outright: it leaves five API-exposed reports downloading successfully and then disappearing with a warning. A was rejected because the vocabulary it grows has no consumer: `report_type` decides union membership and the answers pseudo-header split, neither of which distinguishes a mapping report from a metrics one.

---

### Do the mapping and metadata CSVs also appear in the `reports` union view?

**Context**: They are exposed as dedicated deduplicated views. Whether they also belong in the general `reports` union is a separate question: a slug can be recognized, so it is not quarantined, and still be excluded from the union deliberately.

**Options considered**:
- A) Yes, both.
- B) No, recognized but excluded, as the answers pseudo-header rows are split out into `report_prompts`.
- C) Yes for metadata, no for mapping.

**Decision**: A. The objection, that the same learner appears once per mapping run in `reports` and once overall in `student_id_mapping`, is real but not new: `reports` already unions answers rows and log rows whose counts mean different things, and the guidance already tells a caller to scope by `run_id` or filter through `downloads`. Excluding two slugs would add a second rule about what `reports` contains, which the next person has to learn and which nothing enforces. B's precedent does not hold either: `report_prompts` splits *rows* out of the same CSVs by a rule the union itself applies, rather than removing a CSV from the union.

---

### How does `reports list` render a Portal run's state?

**Context**: `StateText` rendered a nullable `athena_query_state` and yielded `(none)` for every Portal run.

**Options considered**:
- A) An `EXECUTION` column, `STATE` left as `(none)` for Portal runs.
- B) One `STATE` column rendering `live` for a sync run.
- C) Both.

**Decision**: C. The column is what makes the two kinds of run distinguishable at a glance and is the field an MCP client branches on; the state semantics are what stop a Portal run reading as a broken Athena one. A alone leaves every Portal row printing `(none)`, which is the exact confusion the requirement exists to remove, and REPORT-93 makes `(none)` a genuinely meaningful value for an async run whose query has not started, so overloading it would hide a real state behind a fake one.

---

### Are the two views also exposed per-run, like the per-download report views?

**Context**: The deduplicated dimension views collapse across runs, which is right for joining but removes the ability to ask what one mapping run contained.

**Options considered**:
- A) No extra views.
- B) `student_id_mapping_<run_id>` per-run views alongside the deduplicated one.
- C) No extra views, but a per-learner contributing-run count on the deduplicated view.

**Decision**: A, because per-run access already exists and B would build it twice. `perDownloadViews` creates a `report_<run_id>` view for every download whose type is `report`, with no allowlist or report-type condition on it at all, so a downloaded mapping run is queryable as `report_584` the moment it lands. C solves a problem nobody raised and puts a column on the view whose meaning is not what any documented join needs. Per-run detail is read through `report_<run_id>`, not through `reports`, which is a union and cannot answer a single-run question without a `WHERE run_id` the caller has to remember.

---

### What does `dataset reindex` do to the two views when the manifest is gone?

**Context**: The views are keyed on the download's `slug`, which raised the question of what a reindex does to them.

**Decision**: An ordinary reindex does nothing to them: the manifest is the provenance record and it survives. `reindexCSV/2` consults CSV shape only when the manifest cannot vouch for an entry, and `carryProvenance/2` runs afterwards with `prior.Slug` overwriting whatever shape produced. The only uncovered case is the manifest's own loss, which `priorDownloadIndex/0` names "the disaster-recovery path". Recovering the two slugs by shape there was considered and rejected twice over; see the reindex-discriminator decision below. The two views come back empty and the existing `RECOVERED_PROVENANCE` warning says why, widened to name the filter and any dimension view the run feeds as well as the exact report type.

---

### The `EXISTS` guard contradicted the advice REPORT-93 gives

**Context**: `FetchReport` refuses when the CSV already exists unless `--refresh` is passed, and the requirement originally said that guard "works identically for both paths".

**Decision**: For an async run that is right, since the result is immutable and a second download is pointless. For a sync run it is exactly backwards: re-downloading is the *only* way to get current data, and REPORT-93's Portal-duplicate guard tells the caller so in as many words. The guard stays, since silently overwriting a downloaded file on a bare re-run would be worse, but its message differs for a sync run: it names `--refresh` as the way to re-pull, rather than as a way to force a redundant download. The cost, named rather than discovered: the check moves below `GetReport`, so an already-downloaded run now costs one API call before the refusal.

---

### 503 is not a new error shape, and saying it was would have hidden a real gap

**Context**: The requirement listed the limiter's 503 as one of "two error shapes only the Portal path can produce".

**Decision**: Half of that was wrong. `GET` is idempotent and `Client.do` already retries any status at or above 500 for an idempotent request within its backoff budget, surfacing an exhausted budget as a `TransientError`. The real risk was the opposite: the streamed path does not go through `c.do` at all, and `streamToPath/3` has no retry loop of its own, so a naive streamed download would *lose* the retry behavior the envelope path has. The requirement became "the streamed path keeps `c.do`'s retry and error classification", which is a property to assert rather than an error shape to add. The 422 half stands.

---

### "Asserted rather than assumed" named no mechanism

**Context**: The requirement said a duplicate `learner_id` within one run "is asserted rather than assumed", which is a wish, not a test.

**Decision**: Two things happen instead, and both can fail: a view test over a fixture CSV that deliberately repeats a `learner_id` within one run asserts that the extra row is dropped and which row survives, and REPORT-118, which is the first story to run this against a real cohort, checks the learner count from the view against the count in the CSV. Without the second, the fixture proves only that the SQL behaves as written on data we authored.

---

### A mixed-role dataset silently blends real names and student ids in one column

**Context**: `student_metadata`'s `student_name` is `rl.student_name` when names are shown and `rl.student_id` when they are hidden, under the same column name, and `username` is either the username or its salted SHA1. Two runs fetched by different people, or by the same person before and after an admin cleared the hide-names option, are union-compatible CSVs whose `student_name` means different things per row. The dedupe then picks by recency across a distinction it cannot see.

**Decision**: `student_metadata` exposes the run's `hide_names` as a column, which makes the mixing visible, filterable, and assertable in a test. This is not a data-exposure problem, since the server already decided what each fetch may contain, but it is a data-integrity one: a researcher counting distinct names would silently count numbers among them. A correction followed in a later round: the manifest did **not** already store the run's filter. `Download.Filters` and `Download.FilterLabels` existed in the schema and were faithfully preserved across a reindex, but nothing anywhere in the repository ever assigned either; they were declared, carried, and always empty. So `FetchReport` had to start recording the run's filter, which gives two dead fields their first writer.

---

### The dimension views failed to install when every CSV was missing

**Context**: The plan reused the union's typed-empty stand-in for missing files and wrapped the union in `SELECT * EXCLUDE (fetched_at) ... QUALIFY ... ORDER BY fetched_at`. `csvEmptyMember/1` emits the CSV's recorded columns plus `run_id` and nothing else, so a stand-in member has no `fetched_at`.

**Decision**: Built and run against the vendored DuckDB rather than reasoned about: an empty member without `fetched_at` gives `Binder Error: Column "fetched_at" in EXCLUDE list not found in FROM clause`, one *with* it succeeds, and a mixed union succeeds. So one surviving CSV hides the problem, and the failure appears only when *every* CSV is missing, which is exactly the situation the degradation was written to survive. Worse, the same expression is the view's fallback, so the fallback failed too and the view did not install at all. These views therefore get their own empty-member builder carrying `fetched_at` (and `hide_names`), and it emits the fixed schema as well as the recorded columns, so the wrapper binds even for a download whose recorded columns are incomplete. The test that matters removes every file.

---

### "The existing fallback convention" named one of its three cases

**Context**: The plan said these views "follow the existing fallback convention: a typed-empty stand-in when no member exists".

**Decision**: `reportUnionView/3` actually does three different things: a file missing on disk becomes a typed-empty *member* with a warning naming the file; a present-but-unbindable CSV that breaks the primary `CREATE VIEW` falls back to a union of every admitted member's typed-empty schema, keeping the full column shape; and only a union with no members at all becomes the bare stand-in. The plan named the third and would have shipped without the first two, so a single missing mapping CSV would have taken the view down instead of costing it one run's rows.

---

### Both views failed to install in a dataset with no Portal downloads, which is every dataset

**Context**: `StaticViewNames()` constructs the whole statement set over an empty manifest, and `reportUnionView/3` handles a zero-member union by returning a bare `run_id`-only stand-in.

**Decision**: Run against the vendored DuckDB: the zero-member stand-in *inside* the `QUALIFY`/`EXCLUDE` wrapper gives the same binder error from a different cause, and this one fires on a fresh dataset rather than on a damaged one, which would also break the guidance drift guard. So the zero-member case is not wrapped at all, and its stand-in carries the fixed typed column list rather than `reports`' bare `run_id` shape. Verified that the fixed-schema stand-in answers both `WHERE learner_id IS NOT NULL` and the documented join to `answers` with zero rows, where a `run_id`-only stand-in fails to bind `learner_id`. That is what lets a caller copy the documented joins out of the guidance before anything has been downloaded.

---

### The reindex discriminators also matched `student-actions-with-metadata`

**Context**: The plan proposed recovering the two slugs by required column sets on a manifest-less reindex.

**Decision**: Shape recovery is dropped entirely rather than repaired, because the premise behind it was wrong. Verified in the server: `get_athena_query/3` builds its select list as `List.flatten([log_cols | learner_cols])`, and a `student-actions-with-metadata` CSV therefore carries `learner_id`, `run_remote_endpoint`, `runnable_url`, `student_name` and `student_id`. Mapping's eight columns are a strict subset of the log report's, so no positive column rule can separate them. Built and run against the vendored DuckDB, unioning one real mapping CSV with one two-row log CSV for the same learner: the dimension view widens from 9 columns to 26, and the learner's real mapping row is discarded in favor of a log row, so every documented join reads a log event's `run_remote_endpoint` as if it were the learner's mapping. An exact column-set match would be sound but buys little: on the disaster path the filter is gone with the manifest, so a shape-recovered `student_metadata` returns `hide_names` NULL for every row regardless, and what it competes against is one cheap live re-pull. What ships instead is the widened `RECOVERED_PROVENANCE` warning.

---

### "The same per-attempt deadline" would cap a Portal download at 60 seconds against a server that allows 120

**Context**: The plan specified `StreamAPIToFile` as "do() with the buffering removed: same bearer token, same per-attempt deadline, same backoff".

**Decision**, in two parts. The deadline is not carried over: `RequestTimeout` defaults to 60 seconds while `portal_download_timeout_ms` is 120,000, so the client would abandon a cohort the server was willing to finish, and `MaxAttempts` of 6 would multiply that into six full portal queries against a limiter that admits two at a time, reported as `TRANSIENT`, telling the caller to retry the thing that cannot succeed. Verified with a server streaming a CSV in paced chunks: with a 150 ms per-attempt deadline the copy died at 94 bytes; with none it completed. No new bound is introduced, since one would have to exceed the server's 120 seconds to be safe, which makes it a number with no job. The retry rule is narrowed as well: the loop retries only while the attempt has written nothing. Measured: retrying everything costs six server-side downloads and still fails, where stopping at the first written byte costs one, and a 503 answered twice then succeeding takes three attempts under either rule. Rejected alternatives: a reduced mid-stream budget, which needs a second unjustifiable number; and no retry at all, which discards the 503 handling the limiter explicitly asks for.

---

### The streamed path would drop the local-I/O classification and the fsync

**Context**: `streamToPath/3` does more than write bytes: it wraps a failed open, write, sync or close in `localIOError`, which the caller treats as terminal precisely because re-minting cannot fix a local disk problem, and it fsyncs before the rename.

**Decision**: Verified side by side against the same local write failure: the naive streamed path reports the error unclassified, the existing `streamToPath` classifies it. Unclassified, a disk-full download is retried the full six times and surfaces as `TRANSIENT`, telling a researcher whose disk is full to try again. `streamToPath/3`'s destination handling is therefore factored into `streamToPathWith`, taking the request as a seam, and both the presigned path and `StreamAPIToFile` call it. Reimplementing was the alternative and is worse: two places would have to agree about what a local failure is, and only one of them would be exercised by the disk-full path anyone actually hits.

---

### Within a single run the dedupe's ordering was fully tied, so the surviving row was unspecified

**Context**: `StudentIdMappingReport.get_query/2` selects `rl.learner_id` but groups by `LearnerBaseQuery.group_by/0`, which is `"rl.id, u.id, ea.id, pl.id"`. The grouping key is the report-learner row, not the learner id, so nothing in the query guarantees one row per `learner_id`. For two such rows the view's `ORDER BY fetched_at DESC, run_id DESC` is fully tied: same fetch, same run.

**Decision**: The ordering is closed with the remaining columns, which costs seven column names and nothing measurable, and the fixture test asserts which row wins rather than only that one was dropped. A correction to the finding as first written: it claimed the surviving row could vary, which was never demonstrated; across 5,000 duplicated learners the tied ordering picked the same row at `threads=1`, `2` and `8`. The accurate statement is that the winner is unspecified, not that it is unstable, so this is a contract gap rather than a live bug, and a join dimension whose job is to be stable should not depend on it. The fetch-time duplicate detector that was also proposed is deliberately not built.

---

### The documented join could attribute one student's answers to another student

**Context**: `LearnerBaseQuery.run_remote_endpoint_sql/1` is `CONCAT('https://<portal>/dataservice/external_activity_data/', COALESCE(pl.secure_key, ''))`, `portal_learners.secure_key` is nullable, and the REPORT-91 portal fixture ships a learner with a NULL one. Every secure-key-less learner therefore carries the same endpoint string. These are different learners, so the `learner_id` dedupe correctly keeps them all, and the collision is entirely in the join key.

**Decision**: Measured against the vendored DuckDB over two secure-key-less learners plus one normal one, with an `answers` row on each endpoint: two answers rows joined to three rows, with one question attributed to *both* secure-key-less learners. This is a research-data-integrity defect rather than a query inconvenience, and it is structural rather than anomalous, present whenever a cohort contains a learner with no secure key. Both views therefore expose `run_remote_endpoint` as NULL when it is the bare trailing-slash form, since a real endpoint always ends with a non-empty secure key and a NULL never joins. Verified: the same join drops to one correct row while all three learners remain listed with every other column intact. Documenting the hazard instead was rejected: it makes the bad state possible and asks every caller to remember it. The test that was supposed to cover this could not fail, because "one row per learner" is true by construction on any fixture where every learner has a secure key, so the join fixture gains two secure-key-less learners.

---

### The sync fork ignored `--job`, recording a full report CSV as a job download

**Context**: The fork was written with `nil` hardcoded where the async path passes `opts.JobID`, but the flag is reachable and everything downstream still reads `opts.JobID`: the file is named `report_<run>_job_<job>.csv`, the manifest records type `report_job`, and `perDownloadViews` builds a `report_<run>_job_<job>` view over it.

**Decision**: The sync branch refuses `--job` with `output.Usagef`, naming the run as a Portal report. Portal reports have no post-processing jobs, so this is an invalid required value, the pattern every other `Usagef` in `cmd/` follows. It is a different case from `--no-wait`, where the flag's promise is kept by construction and no wrong artifact is produced; here the artifact is wrong and is recorded as authoritative.

---

### `--no-wait` on a sync run is a no-op, not a usage error

**Context**: A step with no requirement asking for it: whether the two polling flags should be refused on a run with nothing to poll.

**Decision**: No-ops. `--no-wait` promises "do not poll, report and exit", and a sync run has nothing to poll, so the promise is kept by construction rather than ignored and there is no false guarantee a caller could believe. Refusing it would break the obvious script, `get report --no-wait` over a mixed list of run ids, and nothing in `cmd/` sets a precedent for refusing an inapplicable-but-harmless flag: every `output.Usagef` there is a missing or invalid required value.

---

### `hide_names` is null for every download that already exists

**Context**: The column is derived from the run filter that this story starts recording. Every download in every dataset that existed before predates that, and a reindex-recovered download has no prior manifest to carry a filter from either.

**Decision**: The null is honest and better than guessing, but it means the column cannot be treated as present: a query filtering `WHERE hide_names = false` silently excludes every pre-existing run rather than including it. Stated in the guidance entry and asserted by a test over a manifest entry with no filter, so the null is a documented value rather than a surprise. Nothing back-fills it, because the information is not on disk to back-fill from.

---

### The `StateText` signature change is the point, not a side effect

**Context**: `StateText` had one production caller besides its own package, plus `ToRunJSON`, so widening it to take the run is a three-line change.

**Decision**: Take the run. The temptation is to keep the `*string` signature and add the execution check at each call site, which is how every Portal run came to render `(none)` in the first place; making the compiler visit each site is the cheap guarantee that none is missed.

---

### Where does the hide-names discriminator belong?

**Context**: Raised in the final review. `student_metadata` exposes `hide_names`, but the same rows also reach the `reports` union, which has no such column, and the hazard is not confined to this story's two reports: four of the five Athena reports emit `student_name` and `username` under the same two meanings and always have. Verified against the vendored DuckDB: `reports` returns a real name and a student id side by side in one column, counts them as two distinct names, and has no column to separate them, while `student_metadata` separates them correctly.

**Options considered**:
- A) Put `hide_names` on `downloads`, the manifest dimension table.
- B) Inject `hide_names` into every `reports` row through `csvScan`.
- C) Document the hazard in the guidance and change no code.
- D) Exclude the metadata CSV from the `reports` union.

**Decision**: A. It is a per-download fact, and `downloads` is where per-download facts already live: `slug` and `report_type` are exactly as useful per-row and are not injected into `reports` either, and the guidance already tells callers to filter through `downloads`. It covers all five report types at once, including the four that predate this story, in about ten lines. Verified against the vendored DuckDB: a `VALUES` column mixing NULL, true and false types as `BOOLEAN`, answers all three states, and joins to `reports` on `run_id`; no report emits a column named `hide_names`, so nothing collides. The value comes from `hideNamesLiteral`, the same helper the metadata view uses, so the two copies cannot drift, and the metadata view keeps its own column because that is the view where per-learner name work happens. B was rejected as changing the union's schema for every caller and every report type, including those with no name columns, and as inventing a second pattern for per-download facts. C is the weakest option: it records the bad state and asks the user to remember it. D contradicts the resolved union decision above. The limit is stated rather than hidden: this does not make the bad state impossible, because the hidden value is a real student id and withholding it would destroy legitimate data; it makes it visible and one join away everywhere it occurs.

---

### A canceled context must not be reported as a server budget overrun

**Context**: Raised during implementation. The streamed path classifies a copy error after bytes have landed as a `partialDownloadError`, whose message says the server may have exceeded its download budget and advises narrowing the report filter. A canceled or timed-out caller context produces exactly that shape.

**Decision**: The caller's context is the only bound on this download by design, so an interrupt or a caller deadline is an expected way for a long pull to end. `ctx.Err()` is checked and returned before the post-commit classification, so a cancellation surfaces as itself. The local I/O branch stays first, since a full disk is accurate whatever the context is doing.

---

### The route belongs to the client, not the fetcher

**Context**: The plan's fork called `StreamAPIToFile` with a path built in `internal/fetch`.

**Decision**: `StreamReportCSV` wraps it on the client instead, and both it and `ReportDownloadEnvelope` read the path from one `reportDownloadPath`, so the envelope and the streamed body cannot address different URLs and `internal/fetch` never constructs an API path.

---

### Filter labels are rendered through the shared helper

**Context**: The download now records the run's filter labels as well as its filter.

**Decision**: They are rendered through `reportview.FilterLabels` rather than a second copy of the label rules, so the manifest and the `reports list` output cannot disagree about what a filter says. The package doc names the manifest as a third consumer whose output is persisted rather than printed.
