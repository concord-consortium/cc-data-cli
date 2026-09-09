# Implementation Plan: Consume Portal reports in cc-data

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-94
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

One repository, one PR. The steps are ordered so the client learns what a Portal run is before anything branches on it, and so the two views are built only after their CSVs can actually land on disk. No step depends on REPORT-93; a run authored in the web form exercises every path here.

## Implementation Plan

### Teach the client that a run has an execution

**Summary**: the wire already carries `execution`, and nothing reads it. Everything else in this plan branches on it, so it arrives first, on its own, with the listing surface that makes it visible.

**Files affected**:
- `internal/api/types.go` — `Execution` on `ReportRun`.
- `internal/reportview/reportview.go` — `Execution` on `RunJSON`; `StateText` gains the sync form.
- `cmd/reports.go` — an `EXECUTION` column on the runs table.
- `internal/reportview/reportview_test.go`, `cmd/reports_test.go` — extend.

**Estimated diff size**: ~110 lines

```go
	// "async" for an Athena run, whose result is fetched later through a presigned envelope, and
	// "sync" for a Portal run, whose CSV is computed and streamed when it is asked for. Every
	// download and listing branch in this client keys off this rather than off a null state.
	Execution string `json:"execution"`
```

`StateText` takes the run rather than a bare state, because the answer now depends on both fields:

```go
// StateText renders a run's readiness. A sync run has no query to be in a state, so it reports
// "live"; an async run reports its Athena state, and "(none)" for a run whose query has not
// started, which REPORT-93 makes a real and common value rather than only a broken one.
func StateText(r api.ReportRun) string {
	if r.Execution == executionSync {
		return "live"
	}
	if r.AthenaQueryState == nil || *r.AthenaQueryState == "" {
		return "(none)"
	}
	return *r.AthenaQueryState
}
```

Changing the signature is deliberate rather than incidental: leaving `StateText(*string)` and calling it with a second argument at each site would let one site forget, which is exactly how every Portal run came to print `(none)` in the first place. The compiler finds every caller.

Tests: a sync run renders `live` whatever its state field holds, including a non-null one; an async run with a null state renders `(none)`; an async run renders its state; the table's header and a sync row both carry the execution column; the MCP payload carries `execution`. The first of those is the assertion that fails if the branch is written as "null state means live".

### Stream an authenticated API response to a file

**Summary**: a client method that keeps everything `Client.do` gives a request except the buffering. The existing streaming helper cannot be reused, for four separate reasons, and reusing it would fail in production and pass in any test that did not check auth.

**Files affected**:
- `internal/api/s3.go` or a new `internal/api/stream.go` — `StreamAPIToFile`.
- `internal/api/stream_test.go` — new.

**Estimated diff size**: ~170 lines

`streamURL/3` builds a bare `http.NewRequestWithContext`, sets no `Authorization` header, applies no per-attempt timeout, has no retry loop, and reduces every non-2xx to `fmt.Errorf("presigned download failed with HTTP %d", status)` (`internal/api/s3.go:38-54`). Correct for a presigned S3 URL; wrong on all four counts for an authenticated endpoint. The new method mirrors `do`'s attempt loop instead:

```go
// StreamAPIToFile GETs an API path and streams the 2xx body to dstPath. It is do() with the
// buffering removed: same bearer token, same backoff, same decodeAPIError on a non-2xx, so a
// coded refusal reaches AsCLIError intact instead of becoming an untyped HTTP-status error.
// It does NOT apply RequestTimeout: that is a per-attempt deadline for JSON calls, and a
// report body legitimately outlives it, so the bound is the caller's context. Each attempt
// truncates the destination, so a retry after a partial write cannot append to it.
func (c *Client) StreamAPIToFile(ctx context.Context, path, dstPath string) error
```

A non-2xx body is read under a bounded `io.LimitReader` and passed to `decodeAPIError`, which is what makes the 422 arrive as a coded error rather than as text. A copy error, including the `unexpected EOF` a truncated chunked response produces, removes the destination file.

Whether that copy error is retried is the one place the attempt loop differs from `do`'s. It retries only while the attempt wrote nothing:

```go
	// The server commits the 200 and the header row before any data, then reraises on a
	// mid-stream failure so the chunked framing aborts (report_controller.ex stream_portal_csv).
	// So bytes on disk mean the failure landed after commit, and the likeliest cause is the
	// server passing its own download deadline, which a retry reproduces exactly. There is no
	// Range support to resume from, so each retry recomputes the whole portal query against a
	// server that admits two at a time. Every refusal worth retrying, the limiter's 503
	// included, arrives before the first byte and is unaffected by this.
	if wrote > 0 {
		return &partialDownloadError{Wrote: wrote, Err: err}
	}
```

Measured on a server that truncates every response: retrying everything costs six server-side downloads and still fails, while stopping at the first written byte costs one. The same harness with a 503 answered twice then succeeding takes three attempts under either rule, which is the assertion that the narrower rule keeps the retries that matter.

A partial download is still transient rather than a contract error, because re-running does fix the network-blip case, and its message names the other cause so the advice is useful when it does not: `download failed after N bytes; the server may have exceeded its download budget for this cohort. Re-run to retry, or narrow the report filter.`

A canceled caller's context is excluded from that classification and surfaces as itself. Because the caller's context is the only bound on this download, an interrupt or a caller deadline is an expected way for a long pull to end, and it arrives as a copy error after bytes have landed, which is the exact shape of the post-commit failure above. Blaming the server's download budget and advising a narrower filter would be wrong on both counts. A local I/O failure is still classified first, since a full disk is accurate whatever the context is doing.

The destination handling is not rewritten. `streamToPath/3` already opens with `O_TRUNC`, records the first write error through `destWriter`, tags open/write/sync/close failures as `localIOError` so the caller can treat them as terminal, fsyncs, and removes the partial file on any failure (`internal/api/s3.go:98-125`). All of that is exactly as right for an API response as for a presigned one, so it is factored to take the request as a seam rather than duplicated:

```go
// streamToPathWith owns the destination file for any streamed download: truncate, copy,
// classify a local failure as localIOError, fsync, and remove the partial file on error.
// fetch performs one request and copies the body to dst.
func (c *Client) streamToPathWith(ctx context.Context, dstPath string, fetch func(context.Context, io.Writer) error) error
```

`streamToPath/3` becomes a one-line caller passing `streamURL`, and `StreamAPIToFile` passes the authenticated request. Two copies of "what counts as a local failure" would otherwise have to stay in agreement, and only one of them would be exercised by the disk-full path anyone actually hits.

Tests: the request carries the bearer token (assert on the server side, since a missing header is invisible from the client); a 422 body arrives as an `APIError` with its code and message; a 503 is retried and a persistent one becomes a `TransientError`; a truncated chunked response is an error and leaves no file; a successful download writes exactly the bytes served; a body that takes longer than `RequestTimeout` still completes, which is the assertion that fails if the per-attempt deadline is carried over; a local write failure surfaces as a local I/O error rather than as a transient one, and is not retried; a server that truncates every response is requested exactly **once**, counted server-side, which is the assertion that fails if the copy error is retried like a connect error; a server that 503s twice and then succeeds is still requested three times, which is the assertion that fails if the narrower rule is written as "never retry"; and a context canceled mid-body surfaces as `context.Canceled` rather than as the budget-overrun advice. The truncation test hijacks the connection mid-stream, which is how the behavior was verified while writing the requirements.

### Branch the report fetch on execution

**Summary**: `FetchReport` gains one fork. Everything around it, the lock, the manifest entry, `DetectCSV`, the atomic rename, is shared, because none of it cares how the bytes arrived.

**Files affected**:
- `internal/fetch/report.go` — the fork, and the sync `EXISTS` message.
- `internal/fetch/report_test.go` — extend with a Portal fake server.

**Estimated diff size**: ~140 lines

The fork replaces the unconditional `pollUntilReady` plus `streamReady` pair:

```go
	if isSync {
		if err := opts.Client.StreamReportCSV(ctx, opts.RunID, tmpPath); err != nil {
			os.Remove(tmpPath)
			return nil, api.AsCLIError(err)
		}
	} else {
		env, notReady, cliErr := pollUntilReady(ctx, opts)
		...
	}
```

The route is the client's to build, not the fetcher's: `StreamReportCSV` wraps `StreamAPIToFile`, and both it and `ReportDownloadEnvelope` read the path from one `reportDownloadPath`, so the envelope and the streamed body cannot address different URLs.

`--job` is refused on a sync run, because unlike the two flags below it produces a wrong artifact rather than a harmless no-op. A job is a separate resource on a separate route, `/api/v1/reports/:id/jobs/:job_id/download` (`server/lib/report_server_web/router.ex:70-71`, `internal/api/endpoints.go:80-84`), and Portal reports have none. If the fork simply passes `nil` for the job id, everything downstream still reads `opts.JobID`: the file is named `report_<run>_job_<job>.csv`, the manifest records type `report_job`, and `perDownloadViews` builds a `report_<run>_job_<job>` view over it (`internal/fetch/report.go:87-99`, `internal/duck/views.go:311-315`). The run's own CSV would be stored, and queryable, as a job result. So the sync branch refuses with `output.Usagef` naming the run as a Portal report, which is the "invalid required value" pattern every other `Usagef` in `cmd/` follows.

`--no-wait` and `--poll-timeout` are no-ops on the sync path, not usage errors. `--no-wait` promises "report the current state and exit rather than poll", and a sync run has no query to poll, so the promise is kept by construction rather than ignored; there is no guarantee a caller could wrongly believe they got. Refusing it would also break the obvious script, `cc-data get report` over a mixed list of run ids with `--no-wait` set once, which would then fail on every Portal row. The repository has no precedent for rejecting an inapplicable-but-harmless flag: every `output.Usagef` call in `cmd/` is a missing required value or an invalid one.

The `EXISTS` message differs by execution:

```go
	// A Portal report is recomputed on every request, so re-downloading is the normal way to get
	// current data, and REPORT-93's duplicate guard tells callers to do exactly that. Sending them
	// a message about forcing a redundant download would contradict it.
	msg := fmt.Sprintf("%s already exists; use --refresh to re-download", csvName)
	if run.Execution == api.ExecutionSync {
		msg = fmt.Sprintf("%s already exists; Portal reports are live, so use --refresh to re-pull current data", csvName)
	}
```

That means the `EXISTS` check moves below the `GetReport` call, which is a behavior change worth naming: an already-downloaded run now costs one API call before the refusal. That is the price of the message being correct, and the call is cheap next to the download it guards.

Tests: a Portal run downloads its CSV and records a manifest entry; the Athena path's existing tests are untouched, which is the regression assertion; a second Portal fetch without `--refresh` refuses with the live-data message; with `--refresh` it re-downloads; `--no-wait` on a sync run downloads normally rather than failing; a 422 from a filterless run surfaces its message.

### Type Portal runs, and record the filter

**Summary**: two small changes to what a download records, both of which later steps depend on. Portal runs stop being quarantined, and the manifest's two dead filter fields get their first writer.

**Files affected**:
- `internal/dataset/reporttype.go` — the `portal` type in the allowlist.
- `internal/fetch/report.go` — `resolveReportType` derives from execution; the manifest entry records the filter.
- `internal/dataset/reporttype_test.go`, `internal/fetch/report_test.go` — extend.

**Estimated diff size**: ~90 lines

```go
// A sync run is a Portal report, which declares no api_report_type on the wire by design. The
// type is derived from the execution rather than from a slug map so a Portal report added to
// the server later is recognized without a cc-data release.
if run.ReportType == nil || *run.ReportType == "" {
	if run.Execution == api.ExecutionSync {
		return dataset.ReportTypePortal
	}
}
```

placed before the `ReportTypeFromSlug` fallback, so no Portal slug reaches the "unknown slug" warning.

`Download.Filters` and `Download.FilterLabels` have existed since the manifest was written and are preserved across a reindex (`internal/dataset/reindex.go:245-250`), but nothing in the repository has ever assigned either. `FetchReport` now records both from the run it already fetched, rendering the labels through `reportview.FilterLabels` rather than a second copy of the rules, so the manifest and the listing cannot disagree about what a filter says. This is what makes the `hide_names` column in the metadata view possible, and it gives two fields that a reader would reasonably assume were populated their first writer.

Tests: a sync run with a null report type is typed `portal`; an async run with a null type still falls back to the slug map and still warns for an unknown slug; `portal` is allowed by `IsAllowedReportType`; a report download records the run's filter and labels. The second of those is the regression guard: it fails if the new branch is written without the execution check and swallows the Athena fallback.

### The two dimension views

**Summary**: the deduplicated join dimensions, and the `hide_names` column that keeps a mixed-role dataset honest.

**Files affected**:
- `internal/duck/views.go` — `studentIDMappingView`, `studentMetadataView`, registered in `statements()`.
- `internal/duck/views_test.go` — new cases.

**Estimated diff size**: ~250 lines

Members are selected from `vs.m.Downloads` by slug, so the view is built from provenance rather than from column sniffing. Each member injects its own `run_id` (as `csvScan` already does) and a `fetched_at` literal, and the wrapper drops the recency column after using it:

```sql
CREATE VIEW "student_id_mapping" AS
SELECT * EXCLUDE (fetched_at, run_remote_endpoint),
       CASE WHEN run_remote_endpoint LIKE '%/' THEN NULL ELSE run_remote_endpoint END AS run_remote_endpoint
FROM (
  <member> UNION ALL BY NAME <member> ...
) QUALIFY ROW_NUMBER() OVER (
  PARTITION BY learner_id
  ORDER BY fetched_at DESC, run_id DESC, <every remaining column ASC>
) = 1
```

Verified against the vendored DuckDB while writing the requirements: this returns the later run's row for a learner present in both, keeps a learner present only in the earlier run, and describes without `fetched_at`. Ordering by `fetched_at` rather than by `run_id` is the point: `run_id` orders by when a run was *created*, and cc-data's model everywhere else is that the latest *fetch* wins.

Two details of that expression are not cosmetic.

**The remaining columns close the ordering.** `LearnerBaseQuery.group_by/0` is `"rl.id, u.id, ea.id, pl.id"` while the select list reads `rl.learner_id`, and the REPORT-91 portal fixture declares `INDEX index_report_learners_on_learner_id (learner_id)`, a non-unique index. So one run may legitimately emit a learner twice, and on `fetched_at DESC, run_id DESC` alone those two rows tie completely, leaving the surviving row unspecified. It was measured as stable across `threads=1`, `2` and `8`, so this is a contract gap rather than an observed flake, but a join dimension should not depend on that. Closing the order costs seven column names and nothing measurable: 200,000 CSV rows collapse to 100,000 view rows at the same speed either way. The practical spread is narrow, since two rows sharing a `learner_id` share a `pl` and therefore share `run_remote_endpoint`; only `class_id`, `offering_id` and `runnable_url` can differ.

**The degenerate endpoint is NULLed.** `run_remote_endpoint_sql/1` is `CONCAT('https://<portal>/dataservice/external_activity_data/', COALESCE(pl.secure_key, ''))`, `portal_learners.secure_key` is nullable, and `LearnerBaseQuery`'s moduledoc names the result: "including the trailing-slash form for a learner with no `secure_key`". Every such learner therefore carries one identical endpoint string, which the `learner_id` dedupe cannot touch because they are different learners. Measured on the documented join, two secure-key-less learners and one answers row on that endpoint produce two joined rows: **one student's answer attributed to a second student**. `LearnerData` builds the same string in Elixir for the bulk fetch, so the value does reach `answers.remote_endpoint`. A real endpoint always ends with a non-empty secure key, so a trailing slash identifies the degenerate form unambiguously, and a NULL never joins. Verified: the same join drops to one correct row while all three learners stay listed with every other column intact. Documenting the hazard instead was rejected: it makes the bad state possible and asks every caller to remember it.

`student_metadata` adds one derived column before the wrapper:

```sql
  SELECT <run_id>, <fetched_at>, <hide_names literal> AS hide_names, * FROM read_csv(...)
```

taken from the filter the "Type Portal runs, and record the filter" step now records. Without it, two metadata runs fetched under different roles put real names and numeric student ids in the same `student_name` column with nothing to distinguish them. The column is null for any download recorded before this story and for a reindex-recovered one, since neither has a filter on disk to derive it from; that is stated in the guidance entry rather than papered over, because `WHERE hide_names = false` would otherwise silently exclude every pre-existing run.

Both views follow `reportUnionView/3`'s full three-way degradation, not just its last case: a file missing on disk becomes a typed-empty member with the warning naming the file, a present-but-unbindable CSV falls back to a union of every admitted member's typed-empty schema so the column shape survives, and only a union with no members at all becomes a bare stand-in.

The middle case is reached only when `CREATE VIEW` itself fails. DuckDB binds a `read_csv` view lazily, so a CSV whose *content* no longer matches its recorded types creates fine and fails at query time, exactly as it already does for `reports`. The fallback is therefore insurance whose trigger a test cannot construct without inventing a corruption both paths treat alike, so the assertion is made on the statement instead: the fallback SQL is executed directly and asserted to install a zero-row view that still binds `learner_id` and `hide_names`. That is the property the naive stand-in fails.

The typed-empty member for these two views is **not** `csvEmptyMember/1`. That builder emits the recorded columns plus `run_id` and nothing else, and the Self-Review records what happens when every member is one of those: the wrapper's `EXCLUDE (fetched_at)` fails to bind, taking the fallback down with the primary. These views get their own builder carrying `fetched_at` (and `hide_names`) alongside the recorded columns. It emits the fixed schema as well as the recorded columns, so the wrapper's `EXCLUDE` and `PARTITION BY` bind even for a download whose recorded columns are incomplete; the dedupe's closing order is derived from the recorded columns alone, which is the set present in both the primary and the fallback union.

The zero-member case is different again, and is the common one: a dataset with no Portal downloads at all, which is every dataset until the first one lands and is also the manifest `StaticViewNames()` builds over (`internal/duck/views.go:439-450`). There is nothing to deduplicate, so the wrapper is not applied at all; wrapping the stand-in reproduces the same binder error from the opposite cause, and it would take the drift guard down with it. The stand-in is also not `reports`' bare `run_id`-only shape: these two schemas are fixed and known, which is the premise the dedicated views rest on, so the stand-in declares the full typed column list. Verified against the vendored DuckDB: with the fixed schema, `SELECT count(*) FROM student_id_mapping WHERE learner_id IS NOT NULL` and the documented join to `answers` both answer zero rows on a fresh dataset, while against a `run_id`-only stand-in the same query fails to bind `learner_id`. That is what makes the documented joins safe to copy out of the guidance before anything has been downloaded.

Tests: two overlapping runs deduplicate to the later fetch, and a learner in only the earlier run survives; ordering by fetch time rather than run id is asserted by giving the *lower* run id the later fetch time, which is the mutation that catches an ordering written on `run_id`; the view's columns do not include `fetched_at`; metadata joins to mapping on `learner_id`; `hide_names` differs across two runs fetched under different roles; a CSV repeating a `learner_id` within one run yields one row and it is the row the closed ordering names, asserted by giving the two rows different `class_id` values; both views exist and are empty in a dataset with no such downloads; `hide_names` is null for a download recorded without a filter.

The join fixture carries a secure-key-less learner, and that is what makes the join test able to fail. A mapping run joins to `answers` on `run_remote_endpoint = remote_endpoint` yielding one row per learner, asserted over a fixture holding **two** learners whose endpoint is the bare trailing-slash form plus one with a real secure key, and an `answers` row on each. Against a fixture where every learner has a secure key the assertion is true by construction and the fan-out ships. The paired assertion is that both secure-key-less learners are still listed in the view, so NULLing the join key is not mistaken for dropping the learners.

Two degradation tests, which are the ones this plan's review found missing: one mapping CSV missing on disk costs that run's rows and warns, leaving the other run queryable, and **every** mapping CSV missing still installs a view that answers a query with zero rows. The second fails against the naive wrapper and passes against the one that carries `fetched_at` into the empty member, so it is the assertion that catches the regression rather than decorating it.

### Say what a lost manifest costs, rather than guessing it back

**Summary**: the manifest already carries both slugs across a reindex. The only case it cannot cover is its own loss, and there the honest answer is to name the runs and say that re-fetching restores them. This step is one warning string.

**Files affected**:
- `internal/dataset/summary.go`: widen the existing `RECOVERED_PROVENANCE` warning.
- `internal/dataset/summary_test.go`: extend.

**Estimated diff size**: ~15 lines

No shape recovery is added, because the manifest is already the provenance record and it already survives a reindex. `reindexCSV/2` reaches `recoverReportType/1` only when the manifest cannot vouch for the entry, and says so in its own comment (`internal/dataset/reindex.go:278-285`); `carryProvenance/2` then runs afterwards and `prior.Slug` overwrites anything shape matching produced (`reindex.go:239-241`). So an ordinary `cc-data dataset reindex` restores `student-id-mapping` and `student-metadata` from the manifest and never inspects a column. `priorDownloadIndex/0` names the only uncovered case for what it is: "the disaster-recovery path, where there is nothing to carry" (`reindex.go:214-218`).

Recovering the two slugs by shape on that path was considered and rejected. It cannot be done soundly for the mapping report: every one of its eight columns is also in `student-actions-with-metadata`, since `get_athena_query/3` selects `List.flatten([log_cols | learner_cols])` and those two lists between them supply `learner_id`, `run_remote_endpoint`, `runnable_url`, `student_id`, `class_id`, `user_id`, `primary_user_id` and `offering_id` (`server/lib/report_server/reports/report_query.ex:62-75, 105-106`). Mapping's column set is a strict subset of the log report's, so no positive column rule can separate them, and a rule written that way recovers a log CSV as a mapping run: verified against the vendored DuckDB, that widens the dimension view from 9 columns to 26 and resolves the learner to a log row rather than to the mapping row.

An exact column-set match would be sound, but it buys little. On the disaster path `Download.Filters` is gone with the manifest, so a shape-recovered `student_metadata` returns `hide_names` NULL for every row anyway, and what it is competing against is one cheap command: a Portal report is live, so `cc-data get report <run> --refresh` restores the slug, the filter, `hide_names` and both views in full. Adding a shape rule that has to stay in step with the server, to avoid a re-pull that this story exists to make easy, is the wrong trade.

The warning that names those runs already exists and already advises a re-fetch (`internal/dataset/summary.go:216-217`). It undersells what is lost now that provenance also drives the filter and the two dimension views, so its text widens:

```go
warnings = append(warnings, "RECOVERED_PROVENANCE: run "+itoa(dl.RunID)+
    " has no provenance; re-fetch to restore its report_type, its filter, and any dimension view it feeds")
```

`dataset show` prints these warnings already (`cmd/dataset_show.go:79-82`), so no plumbing is added and `Reindex()`'s signature is untouched.

Tests: a report download with `Recovered` set raises the warning naming its run id; a store download with `Recovered` set still does not, which is the existing carve-out the message must not break; the two dimension views are empty after a manifest-less reindex and the warning is what says why. The mutation the second catches is widening the condition past `report`/`report_job` while editing the string next to it.

### Verify what the plan relies on but does not build

**Summary**: two properties this plan depends on and neither creates, so nothing currently protects them.

**Files affected**:
- `internal/duck/views_test.go` — one case.
- `internal/api/portal_download_test.go` — the wire captures.

**Estimated diff size**: ~90 lines

`perDownloadViews` builds a `report_<run_id>` view for every download whose type is `report`, with no report-type or allowlist condition (`internal/duck/views.go:301-310`). That is the whole reason this story builds no per-run dimension views, and it holds by omission: one added `if` would remove it silently. A test asserts that a downloaded Portal mapping run is queryable as `report_<run_id>`.

The wire captures follow the convention `filter_options_test.go` already established: `const ...Wire` string literals carrying the exact body, with a comment naming where they came from, "captured from ... against the report-service test fixture", so the decode is pinned to what the server emits rather than to what this package expects. Captured against the fixture, not against production. The captures this story needs are the Portal `/download` response, including its `Content-Type` and the absence of `Content-Length`, and a Portal-run answers page.

### Guidance entries for the two views

**Summary**: the catalog entries REPORT-104's drift guard requires, and no more.

**Files affected**:
- `internal/guidance/src/core.md` — two entries in the Views section.
- `docs/researcher-guide.md`: two rows in the views table, plus the report-kinds prose this story invalidates.

**Estimated diff size**: ~25 lines

There are two guards, not one. `TestGuidanceDocumentsEveryStaticView` enumerates registered views from `duck.StaticViewNames()` and documented views from the catalog and compares both directions (`internal/guidance/guard_test.go:38-53`), and `TestResearcherGuideDocumentsEveryStaticView` does the same against the researcher guide's own table. Both fail on the "two dimension views" step until these exist. That order is deliberate: register, watch it fail, document. Because the build is red between the two, they land in one commit.

The guide also carries prose this story makes false, which the guards do not catch: it says there are five kinds of report and that the Portal ones are "not currently downloadable through `cc-data`". Correcting that is not the workflow prose REPORT-95 owns; it is removing a statement that is now wrong. The same applies to the README's one-line description of `get report`, which named polling as the only path.

Each entry is a name, a one-line purpose and the join key, plus the two things a caller cannot infer from the columns: that a `run_remote_endpoint` of NULL means a learner with no secure key rather than missing data, and that `hide_names` is NULL for any download recorded before this story. The workflow prose that teaches create-a-mapping-run-then-pull-against-it is REPORT-95's, and writing it here would leave two versions of it to keep in agreement.

The mapping entry also carries the one-liner that answers "did this run repeat a learner", since nothing instruments that at fetch time: `SELECT count(*) FROM report_<run_id>` against `SELECT count(*) FROM student_id_mapping WHERE run_id = <run_id>`. It is one sentence here and it is the same check REPORT-118 runs against a real cohort.

### A mapping run drives the bulk fetches

**Summary**: no production change is expected, so this step is the tests that prove it, plus the one refusal that needs a readable message.

**Files affected**:
- `internal/fetch/paged_test.go`, `internal/fetch/attachments_test.go` — extend.

**Estimated diff size**: ~120 lines

`EndpointSet.derive_endpoint_set/2` derives learners from a run's filter for any report with `derives_learner_data` and is indifferent to Athena versus Portal (`server/lib/report_server_web/api/v1/endpoint_set.ex:13-38`), and `AsCLIError` already forwards a coded error's code and message unchanged. So the expectation is that `get answers`, `get history` and `get attachments` work against a mapping run id with no client change at all, and a step whose expectation is "nothing changes" is exactly the one that needs a test, because nothing else would notice if it stopped being true.

Tests: a Portal mapping run id drives an answers pull, a history pull and an attachments pull against the fake server, storing records identical to those an Athena run id produces for the same endpoint set; the stored identity tuple is unchanged and carries no `learner_id`; a run whose report cannot derive learners (`school-metrics`) surfaces the server's `UNPROCESSABLE` message naming the report rather than an internal error.

## Open Questions

None.

## Self-Review

### Whoever has to operate the result

#### RESOLVED: the view fails to install when every CSV is missing, which is the case the degradation exists for

The plan reuses the union's typed-empty stand-in for missing files and wraps the union in `SELECT * EXCLUDE (fetched_at) ... QUALIFY ... ORDER BY fetched_at`. `csvEmptyMember/1` emits a zero-row scan carrying the CSV's recorded columns plus `run_id` and nothing else, so a stand-in member has no `fetched_at`. Built and run against the vendored DuckDB rather than reasoned about:

```
empty member WITHOUT fetched_at          -> Binder Error: Column "fetched_at" in EXCLUDE list not found in FROM clause
empty member WITH fetched_at             -> <nil>
mixed: one member missing fetched_at     -> <nil>
```

So one surviving CSV is enough to hide the problem, and the failure appears only when *every* mapping CSV is missing on disk, which is exactly the situation the existing three-way degradation was written to survive. Worse, the same expression is the view's fallback, so the fallback fails too and the view does not install at all: a dataset whose files were moved would lose `student_id_mapping` entirely instead of degrading to zero rows.

These views therefore get their own empty-member builder that carries `fetched_at` (and `hide_names` for metadata) alongside the recorded columns, rather than reusing `csvEmptyMember/1` unchanged. The mixed row above is why a test that keeps one CSV present proves nothing here: the test that matters removes every file.

#### RESOLVED: "the existing fallback convention" named one of its three cases

The plan said these views "follow the existing fallback convention: a typed-empty stand-in when no member exists". `reportUnionView/3` actually does three different things (`internal/duck/views.go:97-131`): a file missing on disk becomes a typed-empty *member* with a warning naming the file and suggesting `dataset reindex`; a present-but-corrupt CSV that breaks the primary `CREATE VIEW` falls back to a union of every admitted member's typed-empty schema, deliberately keeping the full column shape rather than collapsing to a `run_id`-only stand-in; and only a union with no members at all becomes that stand-in. The plan named the third and would have shipped without the first two, so a single missing mapping CSV would have taken the view down instead of costing it one run's rows.

### Senior Engineer

#### RESOLVED: `hide_names` is null for every download that already exists

The column is derived from the run filter that the "Type Portal runs, and record the filter" step starts recording. Every download in every dataset that exists today predates that, so `hide_names` is null for all of them, and a reindex-recovered download has no prior manifest to carry a filter from either, so it is null there as well. That is honest, and it is better than guessing, but it means the column cannot be treated as present: a query filtering `WHERE hide_names = false` silently excludes every pre-existing run rather than including it.

Stated in the guidance entry and asserted by a test over a manifest entry with no filter, so the null is a documented value rather than a surprise. Nothing back-fills it, because the information is not on disk to back-fill from.

### Whoever has to review the resulting commits

#### RESOLVED: the `EXISTS` check moves, and the plan should say what that costs a caller

Verified: today the guard runs before any network call (`internal/fetch/report.go:53-57`, with `GetReport` at line 59), so a re-run against an already-downloaded run refuses instantly and offline. Making the message depend on the run's execution moves it below `GetReport`, so the same re-run now costs one authenticated API call and can fail with a network error where it used to fail with `EXISTS`.

That is the right trade, since the alternative is telling a Portal user to force a redundant download when re-pulling is the entire point, but it is a behavior change a reviewer should see named rather than discover. The plan says so in the step.

### QA Engineer

#### RESOLVED: the `StateText` signature change is the point, not a side effect

Verified that it has exactly one production caller besides its own package (`cmd/reports.go:419`) plus `ToRunJSON`, so widening it to take the run is a three-line change. Recording it here because the temptation is to keep the `*string` signature and add the execution check at each call site, which is how every Portal run came to render `(none)` in the first place; making the compiler visit each site is the cheap guarantee that none is missed.

### Query engine engineer

#### RESOLVED: both views fail to install in a dataset that has no Portal downloads, which is every dataset today

The previous round fixed the case where every mapping CSV is *missing on disk*, by giving these views an empty-member builder that carries `fetched_at`. It did not fix the case where there is no member **at all**, and that case is strictly more common: it is every dataset before the first Portal download, and it is the exact shape `StaticViewNames()` builds.

`StaticViewNames()` constructs the whole statement set over an empty manifest, deliberately, so the per-run views are excluded (`internal/duck/views.go:439-450`). `reportUnionView/3` handles a zero-member union by returning the bare `run_id`-only stand-in, `SELECT CAST(NULL AS BIGINT) AS run_id WHERE false`, for both primary and fallback (`views.go:116-119`). If these views reuse that stand-in and then wrap it in the `SELECT * EXCLUDE (fetched_at) ... QUALIFY` expression, the wrapper cannot bind.

Run against the vendored DuckDB rather than reasoned about:

```
zero-member stand-in INSIDE the QUALIFY/EXCLUDE wrapper -> Binder Error: Column "fetched_at" in EXCLUDE list not found in FROM clause
zero-member stand-in OUTSIDE the wrapper               -> installs, 0 rows
```

Same binder error as the all-missing case, from a different cause, and this one fires on a fresh dataset rather than on a damaged one. Both the primary and the fallback are built from that expression, so neither installs and the view is absent, not empty. That also breaks `TestGuidanceDocumentsEveryStaticView`, which enumerates registered views from `StaticViewNames()`: the names would still be listed while the statements fail at session build.

**Resolved**: the zero-member case is not wrapped at all, and its stand-in carries the fixed schema rather than `reports`' bare `run_id` column. A union with no members has nothing to deduplicate, so the wrapper is applied only when there is at least one member. Declaring the full typed column list costs nothing (these schemas are fixed and known, which is why the views exist) and it is what lets a caller run the documented join on a fresh dataset: verified that the fixed-schema stand-in answers both `WHERE learner_id IS NOT NULL` and the join to `answers` with zero rows, where a `run_id`-only stand-in fails to bind `learner_id`. The requirement "both views exist and are empty in a dataset with no such downloads" was already in the plan's test list; what was missing is that the naive construction fails it.

---

### Data pipeline reviewer

#### RESOLVED: the reindex discriminators also match `student-actions-with-metadata`, so a log CSV is recovered as a Portal mapping run

The plan proposed recovering the two slugs by required column sets:

```go
{"student-id-mapping", []string{"learner_id", "run_remote_endpoint", "runnable_url"}},
{"student-metadata",   []string{"learner_id", "run_remote_endpoint", "student_name"}},
```

Both sets are subsets of the `student-actions-with-metadata` CSV. Verified in the server rather than assumed. `get_athena_query/3` builds its select list as `cols = List.flatten([log_cols | learner_cols])` (`server/lib/report_server/reports/report_query.ex:105-106`). `get_log_cols/1` includes `run_remote_endpoint` (`report_query.ex:62-67`), and `student-actions-with-metadata` passes `get_learner_cols/1`, which includes `learner_id`, `runnable_url`, **and** `student_name` (`report_query.ex:72-75`, `athena/student_actions_with_metadata_report.ex:8`). Column names reach the CSV header bare, since `to_tuple/2` returns the column name as the output alias (`report_query.ex:144`).

So a `student-actions-with-metadata` CSV carries `learner_id`, `run_remote_endpoint`, `runnable_url`, `student_name` and `student_id`. On a reindex with no prior manifest it matches the mapping entry first and is recovered as slug `student-id-mapping`, type `portal`.

The consequence is not a warning. Built and run against the vendored DuckDB, unioning one real mapping CSV with one two-row log CSV for the same learner:

```
columns after the log CSV is unioned in: run_id, learner_id, user_id, primary_user_id, student_id,
  class_id, offering_id, runnable_url, run_remote_endpoint, id, session, application, activity,
  event, event_value, time, parameters, extras, timestamp, class, school, permission_forms,
  username, student_name, teachers, last_run
row count for that learner: 1
learner 1 resolves to run_id=700 endpoint=https://portal/e/1 event=started
```

Three things go wrong at once. The dimension view widens from 9 columns to 26 as the log columns arrive through `UNION ALL BY NAME`. The learner's real mapping row is discarded in favor of a log row, because the log CSV was fetched later. And every documented join through `student_id_mapping` then reads a log event's `run_remote_endpoint` as if it were the learner's mapping. The report also stops being typed `log`, so it leaves the log side of the union.

`student-actions` does not collide, because it passes `get_minimal_learner_cols/1`, which is `user_id` and `primary_user_id` only (`report_query.ex:77-80`), so it has no `learner_id`. The collision is specific to the metadata variant, which is also the log report a CLUE researcher is most likely to have on disk.

**Resolved**: shape recovery is dropped entirely rather than repaired, because the premise behind it was wrong. The manifest already carries both slugs across a reindex, and `reindexCSV/2` reaches `recoverReportType/1` only when the manifest cannot vouch for the entry (`reindex.go:278-285`), with `carryProvenance/2` overwriting the result afterwards (`reindex.go:239-241`). The only uncovered case is the manifest's own loss, which `priorDownloadIndex/0` calls "the disaster-recovery path".

That reframing also settles which repair to make. Exact column-set matching would have been sound, and positive-plus-negative would have worked today, but on the disaster path the filter is gone with the manifest, so a shape-recovered `student_metadata` returns `hide_names` NULL for every row regardless. What shape recovery is competing against is `cc-data get report <run> --refresh`, one cheap live re-pull that restores the slug, the filter, `hide_names` and both views completely. Carrying a shape rule that has to stay in step with the server, to save that, is not worth it.

Verified before deciding, since a positive rule was the plan's own proposal: all eight `student-id-mapping` columns are present in `student-actions-with-metadata`, so mapping's set is a strict subset of the log report's and no positive rule can separate them. `student-metadata` is separable (`school_id` is held by no other report), but splitting the two reports across two different recovery rules to rescue a rare path is worse than not recovering either.

What the step ships instead is the existing `RECOVERED_PROVENANCE` warning, widened to say that a lost provenance record costs the filter and any dimension view the run feeds, not just the exact report type. It already names each affected run id and already advises a re-fetch.

---

### Go networking engineer

#### RESOLVED: "the same per-attempt deadline" caps a Portal download at 60 seconds against a server that allows 120

The plan specifies `StreamAPIToFile` as "do() with the buffering removed: same bearer token, same per-attempt deadline, same backoff". The per-attempt deadline is the part that does not carry over, and `New()` says so in its own comment: "The http.Client carries no overall timeout: JSON calls get a per-attempt deadline via `RequestTimeout`, while streaming downloads rely on the caller's context so a large CSV or attachment is never cut off mid-body" (`internal/api/client.go:36-39`). `RequestTimeout` defaults to 60 seconds and is applied in `attempt/4` (`client.go:139-144`); `streamURL/3` deliberately does not apply it (`internal/api/s3.go:38-54`).

The server's budget is twice that. `portal_download` runs under `portal_download_timeout_ms`, configured at `120_000` in both `config/config.exs:55-57` and `config/runtime.exs:61-63`, with the wall clock checked between batches in `stream_reducer/2`. So the server is willing to spend 120 seconds streaming a cohort that the client would abandon at 60.

Verified with a server that streams a CSV in paced chunks:

```
with a 150ms per-attempt deadline: err=context deadline exceeded, bytes written=94
with no per-attempt deadline:      err=<nil>, bytes written=241
```

The retry budget then multiplies it. `MaxAttempts` is 6, and a deadline is a transport error, which `do/5` treats as retryable for an idempotent request. Same harness, counting server-side hits for one user command:

```
server-side download attempts for ONE user command: 6 (MaxAttempts=6)
final error surfaced to the caller: transient failure after 6 attempts: context deadline exceeded
exit class: TRANSIENT
```

Each of those six is a full portal query recomputed from scratch, and `PortalDownloadLimiter` admits only `max_concurrent: 2` server-wide (`config/config.exs:57`). One researcher pulling one large cohort can therefore occupy the whole Portal download capacity and still fail, and the failure is reported as `TRANSIENT`, which tells the caller to retry the thing that cannot succeed.

**Resolved**, in two parts.

The deadline is not carried over. `StreamAPIToFile` takes its bound from the caller's context, exactly as `streamURL/3` does and for the reason `New/2` already documents. No new bound is introduced: one would have to exceed the server's 120 seconds to be safe, which makes it a number with no job.

The retry rule is narrowed as well, which the deadline fix does not by itself achieve. The loop retries only while the attempt has written nothing. The server commits its 200 and header row before any data and then reraises on a mid-stream failure, so bytes on disk mean the failure landed after commit, and its likeliest cause is the server passing its own deadline, which is deterministic. With no Range support there is nothing to resume from, so each retry recomputes the full portal query against a two-at-a-time limiter. Measured: retrying everything costs six server-side downloads and still fails, where stopping at the first written byte costs one, and a 503 answered twice then succeeding takes three attempts under either rule.

Rejected alternatives: a reduced mid-stream budget, which needs a second unjustifiable number and still pays a 120-second recompute to learn what the first attempt implied; and no retry at all, which discards the 503 handling the limiter explicitly asks for.

---

### Senior Engineer

#### RESOLVED: the streamed path drops the local-I/O classification and the fsync that `streamToPath` provides

The plan describes `StreamAPIToFile` as writing the body to `dstPath` with each attempt truncating the destination. `streamToPath/3` does more than that, and the extra parts are the ones that decide the exit code (`internal/api/s3.go:98-125`). It wraps a failed `os.OpenFile`, a failed `Write`, a failed `Sync` and a failed `Close` in `localIOError`, which `StreamToFile` then treats as terminal rather than retryable, precisely because "re-minting a fresh presigned URL cannot fix a local disk problem" (`s3.go:11-14`, `s3.go:79-84`). It also fsyncs before the rename.

Verified side by side against the same local write failure:

```
naive streamed path:  err=open .../001: is a directory   classified as localIOError? false
existing streamToPath: err=open .../001: is a directory   classified as localIOError? true
```

Unclassified, a disk-full download is retried the full six times and then surfaces as `TRANSIENT`, telling a researcher whose disk is full to try again. The fsync gap is quieter but real: the Athena path fsyncs its CSV before `os.Rename`, and a Portal CSV that skipped it would be a weaker artifact under the same rename.

**Resolved**: `streamToPath/3`'s destination handling is factored into `streamToPathWith`, taking the request as a seam, and both the presigned path and `StreamAPIToFile` call it. Reimplementing was the alternative and it is worse: two places would have to agree about what a local failure is, and only one of them would be exercised by the disk-full path anyone actually hits.

---

### QA Engineer

#### RESOLVED: within a single run the dedupe's ordering is fully tied, so the surviving row is unspecified

The requirements treat a repeated `learner_id` inside one run as a hazard to be reported. Checking the query that produces the CSV shows it is a live possibility rather than a defensive one: `StudentIdMappingReport.get_query/2` selects `rl.learner_id` but groups by `LearnerBaseQuery.group_by/0`, which is `"rl.id, u.id, ea.id, pl.id"` (`server/lib/report_server/reports/learner_base_query.ex:32`, `portal/student_id_mapping_report.ex:7-9`). The grouping key is the report-learner row, not the learner id, so nothing in the query guarantees one row per `learner_id`.

For two such rows the view's `ORDER BY fetched_at DESC, run_id DESC` is fully tied: same fetch, same run. Run against the vendored DuckDB with one run repeating a learner:

```
2 CSV rows for one learner in ONE run -> 1 view row, endpoint=https://portal/e/AAA
```

One row survives and the SQL does not say which. That is worse than the plan's framing, which is that the drop should be "reported rather than silent". A reported drop is fine; an unspecified winner is not, because the same dataset can answer the same query differently across DuckDB versions, thread counts or vector sizes, and `student_id_mapping` is a join dimension whose whole job is to be stable.

**Correction to this finding**: it claimed the surviving row could vary. That was never demonstrated. Across 5,000 duplicated learners the tied ordering picked the same row at `threads=1`, `2` and `8`. The accurate statement is that the winner is unspecified, not that it is unstable, so this is a contract gap rather than a live bug.

**Resolved**: the ordering is closed with the remaining columns, which costs seven column names and nothing measurable, and the fixture test asserts which row wins rather than only that one was dropped. The fetch-time duplicate detector that was also proposed is deliberately **not** built: it would instrument a condition never observed, whose drop the closed ordering already makes deterministic, and the check that would mean something runs against a real cohort in REPORT-118. The guidance carries the count one-liner instead, at the cost of no code.

Chasing this finding turned up a larger one in the same expression, recorded separately below, which is what the effort here was actually worth.

---

### Education Researcher

#### RESOLVED: the documented join can attribute one student's answers to another student

The views deduplicate on `learner_id`, and the requirements assert that a mapping run "joins to `answers` on `run_remote_endpoint = remote_endpoint` yielding one row per learner". That guarantee rests on the join key being unique across the deduplicated view, which it is not, and no amount of `learner_id` deduplication can make it so.

`LearnerBaseQuery.run_remote_endpoint_sql/1` is `CONCAT('https://<portal>/dataservice/external_activity_data/', COALESCE(pl.secure_key, ''))` (`server/lib/report_server/reports/learner_base_query.ex:38-40`). `portal_learners.secure_key` is nullable, the REPORT-91 portal fixture ships a learner with a NULL one (`server/test/support/portal_fixture.sql`, learner 902), and the moduledoc names the outcome: "including the trailing-slash form for a learner with no `secure_key`". So every secure-key-less learner carries the same endpoint string. These are different learners, so the dedupe correctly keeps them all, and the collision is entirely in the join key.

Measured against the vendored DuckDB, over two secure-key-less learners plus one normal one, with an `answers` row on each endpoint:

```
the learner_id dedupe works: 3 distinct learners, no rows dropped
  joined row: learner_id=901 question_id=q2
  joined row: learner_id=902 question_id=q1
  joined row: learner_id=905 question_id=q1
2 answers rows joined to 3 rows: q1 was attributed to BOTH secure-key-less learners
```

This is a research-data-integrity defect rather than a query inconvenience: a researcher counting per-learner responses would count one student's answer twice, under two different learners, with nothing in the output marking it. It is also structural rather than anomalous, present whenever a cohort contains a learner with no secure key, and `LearnerData` builds the identical string in Elixir for the bulk fetch (`athena/learner_data.ex:121`), so the value genuinely reaches `answers.remote_endpoint`.

The test that was supposed to cover this could not fail. "A mapping run joins to `answers` yielding one row per learner" is true by construction on any fixture where every learner has a secure key, which is what a fixture written without this in mind would contain.

**Resolved**: both views expose `run_remote_endpoint` as NULL when it is the bare trailing-slash form, since a real endpoint always ends with a non-empty secure key and a NULL never joins. The fan-out becomes impossible rather than documented, which is the right side of that trade. Verified: the same join drops to one correct row while all three learners remain listed with every other column intact, so the learners are withheld from the join, not from the dimension. The join fixture gains two secure-key-less learners, which is what gives the assertion something to fail against, paired with an assertion that both are still present in the view.

Documenting the hazard in the guidance instead was rejected: it makes the bad state possible and asks every caller to remember it. The guidance still states the resulting contract, since a NULL join key is something a caller should be able to read rather than deduce: one row per `learner_id`, and a `run_remote_endpoint` that matches at most one learner or is NULL.

---

#### RESOLVED: the sync fork ignores `--job`, recording a full report CSV as a job download

The fork is written as

```go
if run.Execution == api.ExecutionSync {
    if err := opts.Client.StreamAPIToFile(ctx, reportDownloadPath(opts.RunID, nil), tmpPath); err != nil {
```

with `nil` hardcoded where the async path passes `opts.JobID`. The flag is reachable: `cc-data get report <run> --job N` sets `opts.JobID` (`cmd/get_report.go:50-52`), and jobs are a distinct server route, `/api/v1/reports/:id/jobs/:job_id/download`, handled by `ReportJobController` rather than by the run download action (`server/lib/report_server_web/router.ex:70-71`, `internal/api/endpoints.go:80-84`).

Everything downstream of the fork still reads `opts.JobID`. `reportCSVName/2` names the file `report_<run>_job_<job>.csv`, `dlType` becomes `report_job`, and `perDownloadViews` builds a `report_<run>_job_<job>` view from it (`internal/fetch/report.go:87-99`, `internal/duck/views.go:311-315`). So `--job` against a Portal run downloads the run's own CSV and records it, in the manifest and in a view, as a job result. No warning, and the bytes look plausible.

**Resolved**: the sync branch refuses `--job` with `output.Usagef`, naming the run as a Portal report. Portal reports have no post-processing jobs, so this is an invalid required value, the pattern every other `Usagef` in `cmd/` follows. It is a different case from `--no-wait`, where the flag's promise is kept by construction and no wrong artifact is produced; here the artifact is wrong and is recorded as authoritative.

---


## Requirements Coverage

| Requirement | Step |
|---|---|
| `get report` downloads a Portal report over the streamed path, branching on `execution` | branch the report fetch |
| The Athena path is unchanged | branch the report fetch, as a regression assertion |
| The streamed path writes straight to the file rather than buffering | stream an authenticated API response |
| The streamed path keeps `Client.do`'s retry and error classification | stream an authenticated API response |
| The streamed path does not apply `RequestTimeout`; the caller's context is the bound | stream an authenticated API response |
| The streamed path retries only while nothing has been written | stream an authenticated API response |
| A 422 from a filterless run surfaces as itself | stream an authenticated API response, branch the report fetch |
| Lock and manifest identical; `EXISTS` message names re-pull for a sync run | branch the report fetch |
| `reports list` and `reports_list` distinguish a Portal run | teach the client about execution |
| The JSON payload carries `execution` | teach the client about execution |
| A mapping run id drives answers, history and attachments | a mapping run drives the bulk fetches |
| The stored identity tuple is unchanged and carries no `learner_id` | a mapping run drives the bulk fetches |
| A non-learner-derivable report gives an actionable refusal | a mapping run drives the bulk fetches |
| `student_id_mapping` and `student_metadata` exist, keyed by slug | the two dimension views |
| The join keys and the views' contract are stated in the guidance | guidance entries |
| Both views deduplicate by `learner_id`, latest fetch winning | the two dimension views |
| The recency signal is not a column of either view | the two dimension views |
| A duplicate `learner_id` within one run yields a defined survivor, not an arbitrary one | the two dimension views, plus REPORT-118 against real data |
| A degenerate `run_remote_endpoint` is NULL, so the documented join cannot fan out | the two dimension views |
| `student_metadata` exposes `hide_names` | the two dimension views |
| A report download records the run's filter | type Portal runs, and record the filter |
| Every Portal run is typed `portal`, derived from `execution` | type Portal runs, and record the filter |
| The two CSVs appear in the `reports` union | type Portal runs, and record the filter |
| Each run stays queryable as `report_<run_id>` | verify what the plan relies on |
| A manifest-less reindex says which runs lost provenance and what restores them | say what a lost manifest costs |
| Catalog entries keep the drift guard green, and stay minimal | guidance entries |
| View tests over real CSVs assert the dedupe and the joins | the two dimension views |
| Fake-server tests pinned to live wire captures | verify what the plan relies on |

### Gaps found, requirement with no step

**Gap 1 (closed)** and **gap 2 (closed)** both became the "verify what the plan relies on but does not build" step: the `report_<run_id>` property gets a test, and the wire captures get the provenance convention `filter_options_test.go` already uses.

### Gaps found, step no requirement asked for

**Orphan 1 (resolved): `--no-wait` on a sync run is a no-op, not a usage error.** The flag promises "do not poll, report and exit", and a sync run has nothing to poll, so the promise is kept rather than ignored and no false guarantee is available to believe. Rejecting it would break `get report --no-wait` over a mixed list of run ids, and nothing in `cmd/` sets a precedent for refusing an inapplicable-but-harmless flag: every `Usagef` there is a missing or invalid required value.
