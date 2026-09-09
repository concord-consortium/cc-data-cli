# Consume Portal reports in cc-data

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-94
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Teach cc-data that a report run can be a Portal run: download its CSV over the sync streaming path instead of the Athena poll-and-presign path, list it with a meaningful state, and expose the Student ID Mapping and Student Metadata reports as first-class join dimensions. A Student ID Mapping run then drives `get answers`, `get history` and `get attachments` with no Athena run involved.

## Project Owner Overview

Half of report-service's reports are computed live against the portal database rather than run through Athena, and cc-data currently cannot use any of them. The two that matter most, Student ID Mapping and Student Metadata, exist specifically so a researcher can name a set of learners and then pull their answers, history and attachments against that set. Today that pull requires authoring an Athena report first, purely to obtain a run id, which costs money and time for data nobody reads.

After this story a researcher creates or picks a Student ID Mapping run and pulls everything from it, and the mapping and metadata tables arrive as named views that join to the answers and history they already have, rather than as loose CSVs to be joined by hand. That is what the CLUE Dataflow port needs first, and it is the last piece of the Portal side of report-service being usable from a terminal.

## Background

The client is Athena-shaped throughout the download path. `FetchReport` calls `GetReport`, resolves a report type, then `pollUntilReady`, which asks `GET /api/v1/reports/:id/download` for a `DownloadEnvelope` and streams the presigned S3 URL it names (`internal/fetch/report.go:41-119`). Every step of that assumes an async run: a nullable `athena_query_state` to poll, a 409 `NOT_READY` to back off on, and a JSON envelope to follow.

A Portal run answers that same URL by computing the CSV on request and streaming it chunked as `text/csv` (`server/lib/report_server_web/api/v1/report_controller.ex:97-129`). The server distinguishes the two on the run itself: `execution` is `"sync"` for Portal runs and `"async"` for Athena ones, and `report_type` is null for Portal runs by design (`server/lib/report_server_web/api/v1/report_json.ex:66-84`). `api.ReportRun` parses `report_type` today but has no `execution` field (`internal/api/types.go:8-18`).

REPORT-91 shipped the two reports this story exists to consume. `student-id-mapping` emits `learner_id`, `user_id`, `primary_user_id`, `student_id`, `class_id`, `offering_id`, `runnable_url` and `run_remote_endpoint`, and carries no names. `student-metadata` emits `learner_id`, `user_id`, `primary_user_id`, `student_id`, `class_id`, `school_id`, `run_remote_endpoint`, `student_name`, `username`, `class`, `school`, `teacher_user_ids`, `teacher_names`, `teacher_emails`, `teacher_districts`, `teacher_states`, `permission_forms` and `last_run`, with names hidden unless the caller is an admin who cleared the option. Both order by `learner_id` and both join 1:1 on it.

The bulk endpoints are already run-type agnostic. `EndpointSet.derive_endpoint_set/2` derives learners from the run's filter through `LearnerData.fetch/3` for any report that sets `derives_learner_data` (`server/lib/report_server_web/api/v1/endpoint_set.ex:13-38`), which both Portal student reports do by inheriting the struct default. Only the two aggregate metrics reports opt out, and they answer `UNPROCESSABLE` with "This report does not support per-learner endpoints." So no server work is needed for a mapping run to drive `get answers`; the client simply has to stop assuming a run id belongs to an Athena run.

## Requirements

### Downloading a Portal report

- `cc-data get report <run-id>` downloads any API-exposed Portal report, taking the streamed CSV path rather than the poll-and-presign path. The branch is on the run's `execution`, which the client learns from the run metadata it already fetches before downloading.
- Nothing about the Athena path changes: an async run still polls `athena_query_state`, still backs off on `NOT_READY`, still follows the presigned envelope, and still retries a failed presign by re-minting.
- The streamed path writes straight to the dataset file rather than buffering the response, since a Portal CSV over a real cohort is large and the endpoint has no size bound.
- The streamed path keeps `Client.do`'s error classification and its retry of an idempotent request on 429 and 5xx within the backoff budget, with an exhausted budget surfaced as a transient failure. That is easy to lose, because the streaming helpers have no retry loop of their own.
- It does not keep `do`'s per-attempt deadline. `RequestTimeout` bounds a JSON call, and a report body legitimately outlives it, so the bound on a streamed download is the caller's context. The server allows a Portal download 120 seconds and the client's per-attempt deadline is 60, so carrying it over would abandon large cohorts mid-body.
- It retries only while nothing has been written to the destination. Once bytes have landed the download is terminal, because the response was already committed, there is no resume, and the most likely cause is the server exceeding its own download budget, which a retry reproduces exactly. Every refusal that is worth retrying, including the concurrency limiter's 503, arrives before any body bytes and so is unaffected.
- A 422 from a run with no filters is surfaced as itself. It is the one refusal only the Portal path can produce, and nothing should retry it.
- The download lock and the manifest entry work identically for both paths, because they are keyed on the run id and not on how the bytes arrived. The `EXISTS` guard also stays, but its message for a sync run names `--refresh` as the way to re-pull live data, since re-downloading is the whole point of a Portal run and REPORT-93's duplicate guard tells callers to do exactly that.

### Listing Portal runs

- `cc-data reports list` and the `reports_list` MCP tool distinguish a Portal run from an Athena one. Today `STATE` is derived solely from `athena_query_state`, so every Portal run prints `(none)`, which reads as a broken Athena run rather than as a live report.
- The JSON payload carries the run's `execution` alongside the fields it already has, so an MCP client can tell the two apart without inferring it from a null state.

### Pulling learner data from a mapping run

- `get answers`, `get history` and `get attachments` accept a Student ID Mapping run id and pull those learners' records, with no Athena run involved and no change to how records are stored or identified.
- The stored-record identity is unchanged: `(source_key, remote_endpoint, question_id[, history_id])`. `learner_id` is not part of it and does not appear in the fetched records, in `run_membership`, or in the stores; it exists only in the two new CSVs, which is what makes them join dimensions rather than another identity.
- A run whose report cannot derive learners returns the server's `UNPROCESSABLE` as an actionable message naming the report, not as an internal error.

### The two dimension views

- The query engine exposes `student_id_mapping` and `student_metadata` as named views, recognized by report slug through the download provenance the manifest already carries (`downloads` exposes `run_id`, `type`, `slug`, `report_type`, `complete`).
- `student_id_mapping` joins to `answers` and `history` on `run_remote_endpoint = remote_endpoint`, and `student_metadata` joins to `student_id_mapping` on `learner_id`. Both joins are stated in the guidance so a caller does not have to rediscover them, along with the view's contract: one row per `learner_id`, and a `run_remote_endpoint` that matches at most one learner or is NULL.
- Both views are deduplicated by `learner_id`, latest fetch winning, because a dataset can hold several overlapping mapping runs and the alternative is a view that silently multiplies rows on every join. This is a new pattern: the existing dimension views do no dedupe, and the record-level merge keys on the identity tuple rather than on a learner.
- The recency signal driving the dedupe is not part of the view's columns. A caller sees the same columns the CSV has, plus the `run_id` every union member already carries.
- A duplicate `learner_id` *within* one run is possible, because the Portal query groups by the report-learner row rather than by the learner id, so the dedupe's ordering is closed with the remaining columns and names a defined survivor instead of an arbitrary one. The condition is not instrumented at fetch time: it has never been observed, the drop is deterministic once the ordering is closed, and the check that would mean anything runs against a real cohort in REPORT-118. The guidance carries the one-liner that surfaces it on demand, comparing `report_<run_id>`'s row count with the view's rows for that run.
- `run_remote_endpoint` is NULL in both views when it is the bare trailing-slash form a learner with no `secure_key` produces, because that one string is shared by every such learner and would otherwise attribute one learner's answers to all of them. The learner stays in the view with all its other columns; only the join key is withheld, so the documented join cannot fan out rather than being documented as able to.
- `student_metadata` exposes whether the run it came from hid names. Without it a dataset holding runs fetched under different roles silently mixes real names and numeric student ids in one column, with nothing to tell them apart.
- A report download records the run's filter in the manifest. The `filters` and `filter_labels` fields already exist and are already carried across a reindex, but nothing has ever written them, so `hide_names` is not currently derivable from what is on disk.

### Report-type recognition

- Every Portal run is typed `portal`, derived from the run's `execution` rather than from a slug list, so a Portal report added to the server later needs no cc-data change to be recognized. Today an unknown slug is recorded verbatim as the report type and then dropped from the `reports` union view with a warning, so a Portal CSV downloads successfully and then vanishes from every query.
- The mapping and metadata CSVs appear in the `reports` union as well as in their own views, and each downloaded run stays individually queryable as `report_<run_id>`, which the engine already builds for every report download.
- An ordinary `dataset reindex` keeps both views populated, because the manifest already carries the slug across it. Nothing is recovered from CSV shape. When the manifest itself is gone, the two views come back empty and the existing per-run provenance warning says so, naming each run and the re-fetch that restores its report type, its filter and any dimension view it feeds.

### Guidance

- The two views get their minimal entries in the shared guidance source in the same change that registers them, so REPORT-104's drift guard stays green. The guard enumerates registered views and documented views and compares in both directions, so registering without documenting fails the build.
- The entries are minimal by design: name, one-line purpose, join key. The workflow prose that teaches create-a-mapping-run-then-pull is REPORT-95.

### Tests

- Fake-server tests pinned to wire captures of the Portal download and of a Portal-run answers pull.
- View tests over real CSVs on disk that assert the dedupe picks the later fetch, that a learner present in only one run survives, and that the joins produce one row per learner.

## Technical Notes

**A Portal run does not fail slowly today, it fails immediately and confusingly.** The Jira description predicts that `get report` on a Portal run "would poll a never-set state until the timeout and fail". Verified against a fake server that answers `/download` the way the real one does: the run fails at once with exit 1, code `INTERNAL`, message `invalid character 'l' looking for beginning of value`. `ReportDownloadEnvelope` calls `getJSON` (`internal/api/endpoints.go:81-91`), the 200 response body is CSV, and `json.Unmarshal` chokes on the first letter of `learner_id`. Because that error is not an `APIError`, `pollUntilReady` returns it through `AsCLIError` on the first iteration and never polls. The consequence for the plan is the same, the work is a real branch rather than a thin one, but the symptom to reproduce is a parse error, not a timeout.

**The streaming helpers cannot be reused as they stand.** `streamURL/3` sends no `Authorization` header, has no retry loop, no per-attempt timeout, and collapses every non-2xx into `fmt.Errorf("presigned download failed with HTTP %d", status)` (`internal/api/s3.go:38-54`). All four are right for a presigned S3 URL and all four are wrong for an authenticated API endpoint: the download would 401, the 422 the requirements ask to surface would be flattened into an untyped error, and the retry semantics the envelope path gets from `Client.do` would be gone. The Portal path therefore needs its own client method that keeps `do`'s auth, backoff and `decodeAPIError` handling while streaming the 2xx body instead of buffering it. It deliberately does not keep `do`'s per-attempt deadline: `New/2`'s own comment says streaming downloads rely on the caller's context "so a large CSV or attachment is never cut off mid-body" (`internal/api/client.go:36-39`), and `streamURL/3` applies no deadline for that reason.

**A truncated Portal download is detectable, and was checked rather than assumed.** The endpoint uses `send_chunked/2`, so the response carries `Transfer-Encoding: chunked` and no `Content-Length`, which raised the question of whether a mid-stream failure would arrive as a short but apparently successful read. Verified against a server that writes part of a CSV and then hijacks and closes the connection: the response has an empty `Content-Length` and `TransferEncoding: [chunked]`, and `io.Copy` stops with `unexpected EOF`. So truncation surfaces as a copy error, and the requirement is simply that the streamed path treats a copy error as a failed download and discards the partial file, which `streamToPath/3` already does for the S3 path.

**Buffering is a second, quieter reason the envelope path cannot be reused.** `Client.do` reads the whole response body before anything inspects it, which is correct for a JSON envelope and wrong for a report CSV. The presigned path avoids this by streaming from S3 through `streamToPath` (`internal/api/s3.go:98-125`); the Portal path needs the same streaming treatment applied to the API response itself.

**The dedupe shape works, and was run rather than reasoned about.** Verified against the DuckDB this project vendors (`duckdb-go/v2 v2.10504.0`): a union of two CSVs, each member injecting its own `run_id` and a `fetched_at` literal, wrapped in `SELECT * EXCLUDE (fetched_at) FROM (...) QUALIFY ROW_NUMBER() OVER (PARTITION BY learner_id ORDER BY fetched_at DESC, run_id DESC) = 1`, returns the later run's row for a learner in both runs, keeps a learner present in only the earlier run, and describes as `run_id, learner_id, run_remote_endpoint` with no `fetched_at`. `EXCLUDE` is what keeps the recency signal out of the view's schema while still ordering by it.

**`run_id` alone is the wrong recency signal.** `csvScan` already injects the run id into each union member (`internal/duck/views.go:159-165`), which makes it tempting to order by that. A higher run id is a later-*created* run, not a later-*fetched* one, and cc-data's whole store model is incoming-wins by fetch. The manifest carries `FetchedAt` per download, so the literal is available; run id remains as the tiebreaker for two downloads in the same instant.

**Report type for a Portal run is null on the wire, by design.** `ReportJSON.report_type/1` returns the report's `api_report_type` and Portal reports declare none (`report_json.ex:66-72`). `resolveReportType` then falls through to `ReportTypeFromSlug`, which knows only the five Athena slugs (`internal/dataset/reporttype.go:11-17`), warns that the slug is unknown, and returns the slug as the type. `IsAllowedReportType` rejects it and `reportsView` quarantines the CSV with a second warning (`internal/duck/views.go:76-84`). So the failure is two warnings and a silently absent table, which is the worst shape a failure can have here.

**Which Portal reports the API exposes.** Seven: `student-id-mapping`, `student-metadata`, `teacher-status`, `school-metrics`, `summary-metrics-by-subject-area`, and the two resource-metrics reports. `school-metrics` and `summary-metrics-by-subject-area` set `derives_learner_data: false` (`server/lib/report_server/reports/tree.ex:227,236`) and so cannot back a learner pull; the rest can. Any decision about report types has to cover all seven, not just the two this story is named for.

**The metadata report is not a superset of the mapping report.** They share `learner_id`, `user_id`, `primary_user_id`, `student_id`, `class_id` and `run_remote_endpoint`. Mapping additionally has `offering_id` and `runnable_url`; metadata additionally has `school_id` and the thirteen human-readable columns. So a join of the two is not redundant, and neither can be dropped in favor of the other.

**`hide_names` changes the metadata CSV's contents, not its shape.** `LearnerHideNames.student_name_sql/1` and `username_sql/1` pick different expressions under the same column names, so two metadata runs fetched under different roles produce union-compatible CSVs whose `student_name` means different things. The dedupe therefore picks by recency across a distinction it cannot see.

**Nothing here needs the create endpoint.** REPORT-93 adds `reports create`, and the two stories are independent: this one consumes a run id however it was obtained, including one authored in the web form. The port story that needs both is REPORT-118.

## Out of Scope

- Creating or duplicating runs from the CLI, which is REPORT-93.
- The `logs` view with parsed `parameters` and `extras`, which is REPORT-112 and rides this story's view pattern afterwards.
- Dataset materialization, which is REPORT-113 and rewrites the same engine files afterwards.
- The full skill and MCP workflow prose for Portal reports, which is REPORT-95. This story writes only the catalog entries the drift guard requires.
- Surfacing Athena failure reasons in cc-data's error text, which is REPORT-127. It touches the same two files, `types.go` and `internal/fetch/report.go`, and is deliberately not folded in: it spans both repositories, since its server half exposes REPORT-106's guidance mapping over the API, and its own spec sequences it strictly after this story, re-running assumption verification against whatever these files become.
- A one-shot pull command that chains create, download and fetch. The primitives are chained by the skill.
- Any change to how answers, history or attachments are stored, identified or merged.

## Open Questions

### RESOLVED: What report type do the Portal reports carry?

**Context**: `report_type` is null on the wire for every Portal run, and the client's `slugToType` map knows only the five Athena slugs. Something has to fill the gap, and the choice decides whether the Portal CSVs join the `reports` union view and under what type.

**Options considered**:
- A) Extend `slugToType` with the seven Portal slugs, mapping them to new type values (for example `mapping`, `metadata`, `metrics`), and add those to `IsAllowedReportType`.
- B) Extend the maps only for the two reports this story exposes as views, leaving the other five quarantined.
- C) Give every Portal report the single type `portal`, so the vocabulary does not grow per report and `downloads.slug` remains the discriminator.

**Decision**: C, and derived from the run rather than from a slug list. `resolveReportType` already receives the run, and this story adds `execution` to it, so a run whose `execution` is `sync` and whose `report_type` is null is typed `portal` without consulting any map. That is strictly better than adding seven entries to `slugToType`: a Portal report added to the server later needs no cc-data change at all, where every map-based option needs two edits (`slugToType` and `IsAllowedReportType`) before the new report stops emitting two warnings and vanishing from the union. Avoiding exactly that kind of edit-in-two-places drift is what REPORT-104 exists for.

B is rejected outright on the same ground: it leaves five API-exposed reports downloading successfully and then disappearing with a warning, which is a bug report waiting to be filed. A is rejected because the vocabulary it grows has no consumer: `report_type` decides union membership and the answers pseudo-header split (`csvdetect.go:44`, `views.go:76-84`), neither of which distinguishes a mapping report from a metrics one, while anyone who wants to name the report reads `downloads.slug`, which is already there.

### RESOLVED: Do the mapping and metadata CSVs also appear in the `reports` union view?

**Context**: They are exposed as dedicated deduplicated views. Whether they also belong in the general `reports` union is a separate question: a slug can be recognized, so it is not quarantined, and still be excluded from the union deliberately.

**Options considered**:
- A) Yes, both.
- B) No, recognized but excluded, as the answers pseudo-header rows are split out into `report_prompts`.
- C) Yes for metadata, no for mapping.

**Decision**: A. The objection to it, that the same learner appears once per mapping run in `reports` and once overall in `student_id_mapping`, is real but is not new: `reports` already unions answers rows and log rows whose counts mean different things, and the guidance already tells a caller to scope by `run_id` or filter through `downloads`. Excluding two slugs would add a second rule about what `reports` contains, which the next person has to learn and which nothing enforces, in exchange for removing one instance of a confusion that remains everywhere else. B's precedent does not hold either: `report_prompts` splits *rows* out of the same CSVs by a rule the union itself applies, rather than removing a CSV from the union.

### RESOLVED: How does `reports list` render a Portal run's state?

**Context**: `StateText` renders a nullable `athena_query_state` and yields `(none)` for every Portal run (`internal/reportview/reportview.go:75-81`).

**Options considered**:
- A) An `EXECUTION` column, `STATE` left as `(none)` for Portal runs.
- B) One `STATE` column rendering `live` for a sync run.
- C) Both.

**Decision**: C, which the Jira description already settles: "the table needs a type/execution column and Portal state semantics". Both halves earn their place. The column is what makes the two kinds of run distinguishable at a glance and is the field an MCP client branches on; the state semantics are what stop a Portal run reading as a broken Athena one. A alone leaves every Portal row printing `(none)`, which is the exact confusion this requirement exists to remove, and REPORT-93 makes `(none)` a genuinely meaningful value for an async run whose query has not started yet, so overloading it would now hide a real state behind a fake one.

### RESOLVED: Are the two views also exposed per-run, like the per-download report views?

**Context**: The deduplicated dimension views collapse across runs, which is right for joining but removes the ability to ask what one mapping run contained.

**Options considered**:
- A) No extra views.
- B) `student_id_mapping_<run_id>` per-run views alongside the deduplicated one.
- C) No extra views, but a per-learner contributing-run count on the deduplicated view.

**Decision**: A, because per-run access already exists and B would build it twice. `perDownloadViews` creates a `report_<run_id>` view for every download whose type is `report`, with no allowlist or report-type condition on it at all (`internal/duck/views.go:301-317`), so a downloaded mapping run is queryable as `report_584` the moment it lands, deduplicated by nothing. That is exactly what B proposes to add under a second name. C solves a problem nobody has raised and puts a column on the view whose meaning ("how many runs held this learner") is not what any documented join needs.

The requirement's phrasing is corrected accordingly: per-run detail is read through `report_<run_id>`, not "through `reports`", which is a union and cannot answer a single-run question without a `WHERE run_id` the caller has to remember.

## Self-Review

### Senior Engineer

#### RESOLVED: what `dataset reindex` does to the two views when the manifest is gone

The views are keyed on the download's `slug`, which raised the question of what a reindex does to them. An ordinary one does nothing: the manifest is the provenance record and it survives. `reindexCSV/2` consults CSV shape only when the manifest cannot vouch for an entry (`internal/dataset/reindex.go:278-285`), and `carryProvenance/2` runs afterwards with `prior.Slug` overwriting whatever shape produced (`reindex.go:239-241`). The uncovered case is the manifest's own loss, which `priorDownloadIndex/0` names "the disaster-recovery path" (`reindex.go:214-218`), and which `WriteManifest`'s atomic lock-guarded write makes rare.

Recovering the two slugs by shape on that path was considered and rejected, twice over. It cannot be done soundly for the mapping report: all eight of its columns also appear in `student-actions-with-metadata`, because `get_athena_query/3` selects `List.flatten([log_cols | learner_cols])` (`server/lib/report_server/reports/report_query.ex:62-75, 105-106`). Mapping's set is a strict subset of the log report's, so a positive column rule recovers a log CSV as a mapping run, which was measured to widen the dimension view from 9 columns to 26 and resolve the learner to a log row. An exact column-set match would be sound, but on this path the filter is gone with the manifest, so `hide_names` is NULL for every recovered metadata row regardless, and what the rule is competing against is one cheap live re-pull.

So the two views come back empty after a manifest-less reindex, and the existing `RECOVERED_PROVENANCE` warning is what says why. It already names each affected run and advises a re-fetch (`internal/dataset/summary.go:216-217`, surfaced by `cmd/dataset_show.go:79-82`); this story widens its wording, because a lost provenance record now costs the filter and a dimension view as well as the exact report type.

#### RESOLVED: the `EXISTS` guard contradicts the advice REPORT-93 gives

`FetchReport` refuses when the CSV already exists unless `--refresh` is passed (`internal/fetch/report.go:53-57`), and the requirement said that guard "works identically for both paths". For an async run that is right: the result is immutable, so a second download is pointless. For a sync run it is exactly backwards, because re-downloading is the *only* way to get current data, and REPORT-93's Portal-duplicate guard tells the caller so in as many words, "Portal reports are live; re-pull run 123 for current data". A caller who follows that advice hits `EXISTS` and has to guess what to do next.

The guard stays, since silently overwriting a downloaded file on a bare re-run would be worse, but its message is different for a sync run: it names `--refresh` as the way to re-pull, rather than as a way to force a redundant download. The two stories then agree instead of pointing at each other.

### cc-data client author

#### RESOLVED: 503 is not a new error shape, and saying it is would hide a real gap

The requirement listed the limiter's 503 as one of "two error shapes only the Portal path can produce". Verified that half of it is wrong: `GET` is idempotent (`internal/api/client.go:70-76`) and `Client.do` already retries any status at or above 500 for an idempotent request within its backoff budget, surfacing an exhausted budget as a `TransientError` that maps to the transient exit code (`client.go:128-131`, `errors.go:68-73`). So a 503 on the envelope path is handled today.

The real risk is the opposite of what was written: the streamed path does not go through `c.do` at all, and `streamToPath/3` has no retry loop of its own, only `StreamToFile` does (`internal/api/s3.go:60-96`). So a naive streamed download would *lose* the retry behavior the envelope path has. The requirement now says the streamed path keeps `c.do`'s retry and error classification, which is a property to assert rather than an error shape to add. The 422 half stands: a run with no filters is a genuine Portal-only refusal and nothing retries it.

Two limits on that retry were added later, in the stage-3 review, and both are visible from the 503 case rather than contradicted by it. The retry stops once bytes have been written, and the per-attempt deadline is not carried over at all. A 503 is unaffected by either: it arrives with no body bytes and long before any deadline.

### QA Engineer

#### RESOLVED: "asserted rather than assumed" named no mechanism

The requirement said a duplicate `learner_id` within one run "is asserted rather than assumed", which is a wish, not a test. The view builder emits SQL strings and runs no queries, so it cannot check this at build time. Two things happen instead, and both can fail: a view test over a fixture CSV that deliberately repeats a `learner_id` within one run asserts that the extra row is dropped and that the drop is reported rather than silent, and REPORT-118, which is the first story to run this against a real cohort, checks the learner count from the view against the count in the CSV. Without the second, the fixture proves only that the SQL behaves as written on data we authored.

### Education Researcher

#### RESOLVED: a mixed-role dataset silently blends real names and student ids in one column

`student_metadata`'s `student_name` column is `rl.student_name` when names are shown and `rl.student_id` when they are hidden, under the same column name, and `username` is either the username or its salted SHA1 (`server/lib/report_server/reports/learner_hide_names.ex:9-20`). Two metadata runs fetched by different people, or by the same person before and after an admin cleared the hide-names option, are therefore union-compatible CSVs whose `student_name` means different things per row. The dedupe then picks by recency across a distinction it cannot see, so a learner's name can become a number because a later run happened to be fetched under a different role.

This is not a data-exposure problem, the server already decided what each fetch may contain, but it is a data-integrity one: nothing in the view says which rows are which, and a researcher counting distinct names would silently count numbers among them. `student_metadata` exposes the run's `hide_names` as a column, which makes the mixing visible, filterable, and assertable in a test, instead of an invisible property of who fetched what.

**Corrected in stage 4.** This finding originally said the manifest "already stores the run's filter per download", so the column was free. It does not. `Download.Filters` and `Download.FilterLabels` exist in the manifest schema (`internal/dataset/manifest.go:55-56`) and `carryProvenance/2` faithfully preserves both across a reindex (`reindex.go:245-250`), but nothing anywhere in the repository ever assigns either one: they are declared, carried, and always empty. So `FetchReport` has to start recording the run's filter on the download, which it does not do today (the entry it builds carries type, run, slug, report type, files, row count, columns, order, dialect, complete and fetched-at, and no filter). That is a small addition and it gives two dead fields their first writer, but it is work this story owns rather than a property it inherits.
