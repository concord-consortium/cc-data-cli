# cc-data-cli: recent logs, and removing the CLUE pre-pull settings

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-152
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

`cc-data get recent-logs <class-id>` pulls a class's log events from the last day or two, about two minutes behind live, into a dataset, and each later pull asks only for what is new. The CLUE pre-pull's leftovers (`clue_prepull` in new manifests, the local refusal for it, and `clue_source` in `scope.json`) go, in cc-data-cli and in the Researcher Dashboard runner, because packages now read CLUE only through cc-data.

## Project Owner Overview

A Researcher Dashboard package can so far see a class's logs only through a Student Actions run, which reads the nightly copy and so lags by up to a day. REPORT-150 gave report-server a route that answers a class's recent events within minutes; this story is the cc-data side, so a package (and a researcher at a terminal) can show what students did minutes ago. REPORT-189's CLUE Dataflow package is the first consumer and has to be demoed in a running CLUE test class, so the recent-logs path must work for CLUE.

The rest is cleanup. CLUE data used to be pre-pulled by the runner, and three settings described that arrangement. REPORT-110 and REPORT-111 now let a package fetch and read CLUE itself through cc-data, so the settings describe something that no longer exists, and one of them makes `cc-data package run` refuse a CLUE package that would now run fine. The story ends with the check that matters to researchers: a package pulls its own CLUE corpus and reads it through cc-data's views, on a laptop and on the dashboard's VM.

## Background

**Decided scope.** Jira (rewritten 2026-10-22) and the stream channel `stream/researcher-dashboard/clue-cc-data` (#904 the scoping check, #906 Doug's decisions) split the CLUE work three ways: REPORT-110 owns the `get clue-*` commands, their stores (per-type identity) and plain views; REPORT-111 owns the derived CLUE views; this story owns recent logs, the pre-pull cleanup, and the end-to-end acceptance. Implementation stacks 110, then 111, then 152, so this story's code sits on theirs.

**REPORT-150's route** (report-service #444, closed spec `specs/REPORT-150-class-scoped-recent-logs-route.md` on its branch, in code review, not on staging as of 2026-10-22: `GET /api/v1/classes/1/recent-logs` on `report-server.concordqa.org` answers 404 where a deployed route answers 401). `GET /api/v1/classes/:class_id/recent-logs`, with cc-data's API token:

- Reads REPORT-107's `logs_by_hour` for the class's learners, joined by `run_remote_endpoint`, from `window_start` (00:00 UTC yesterday) onward.
- Optional `start`/`end` (epoch ms, inside the window) narrow the read; a `cursor` cannot be combined with either (400).
- A request with no `end` answers `next_cursor`, an opaque value whose version 1 holds the class id and `since = query_started_at - 10 minutes`. Responses overlap by design and the caller dedups by `id`. A cursor older than the current `window_start` is 410 `EXPIRED_CURSOR`.
- The body is one JSON object streamed as Athena is read: `class_id, window_start, nightly_through, since, until, query_started_at`, then `rows`, then `count, latest_timestamp, next_cursor`. A failure after the first byte ends the body early as invalid JSON (R12 there).
- Each row has the 13 `student-actions` columns in order (`id, session, application, activity, event, event_value, time, parameters, extras, run_remote_endpoint, timestamp, user_id, primary_user_id`); `time`, `timestamp`, `user_id`, `primary_user_id` are integers, the rest strings, an empty field `null`.
- 503 when the Athena query does not finish within 60 s or more than four reads are in flight per server; 422 above about 2,100 learners.

The cursor and dedup background is `log-cursor-and-dedup.md` in global oob `streams/researcher-dashboard/`. Its REPORT-152 section is stale where it puts a deduplicated view in this story: one row per event across log runs and recent pulls is REPORT-192's.

**CLUE log rows.** CLUE logs only in `authed` mode, the portal launch (`collaborative-learning` `src/lib/logger.ts` lines 70 and 71), and sends a staging-portal user's events to `logger.concordqa.org` (lines 11 to 13 and 200), the staging ingester. Its `run_remote_endpoint` is the launch JWT's `returnUrl`, the learner's `remote_endpoint_url`, which is the key the route joins learners on. So a staging CLUE class answers rows only for students launched from the staging portal; demo and QA modes log nothing. The join holds in production: the local `learn.concord.org/dataflow-cohort` dataset's `logs` has 263,570 CLUE rows, every one with `id` and `run_remote_endpoint`, from `student-actions`, which joins the same way.

**How cc-data stores fetched records today.** `answers` and `history` are merged stores: a fetch writes a segment (`internal/store/segment.go`), and `Dataset.MergeCompact` (`internal/dataset/merge.go`) streams it into a new store version keyed by `store.Identity`. Each run's membership file is **replaced wholesale** by the segment's identities, and a stored record no membership covers is removed. A throwaway test confirmed the consequence for this story: merging a one-row segment for the same `(type, run)` after a two-row one leaves one row (`Removed: 2`). A cursor pull answers only rows since its `since`, so reusing that merge unchanged would delete every earlier recent-log row of the class on each pull. The store this story adds must accumulate.

Everything in a dataset is keyed by run: segment names (`seg_<type>_<run>`), membership keys (`<type>/<run>`), `Download.RunID`, `run_membership`, the per-run views. A recent-logs pull has no run; it has a class.

**The pre-pull's traces.**

- `internal/packages/manifest.go`: `Manifest.CluePrepull` (`json:"clue_prepull"`, no `omitempty`), so `package init` writes `"clue_prepull": false`.
- `internal/packages/run.go` lines 46 to 47 and 78 to 80: `Refused`'s comment and the refusal "the package sets clue_prepull, and cc-data cannot fetch CLUE data yet, so it runs only on the VM"; `run_test.go` lines 197 to 204 test it.
- `internal/packages/scope.go` lines 34 to 55: `ScopeFile.ClueSource` and its place in `MarshalJSON`'s key order; `run.go` line 104 writes `"firebase"`; `run_test.go` line 156 asserts it.
- `cmd/package.go` lines 24 and 25 (`runDifferences`) and `docs/researcher-guide.md` lines 650 and 651 describe the refusal.
- `internal/packages/files_test.go` line 20 and `cmd/package_test.go` line 28 carry `"clue_prepull": false` in fixture manifests.
- researcher-dashboard `origin/main`: `runner/server/package-env.js` lines 50 to 62 (`CLUE_SOURCE = "cc-data"`, written into `scopeFile`), and `runner/test/package-env.test.js` lines 111 and 116.

report-server reads a missing `clue_prepull` as `false` (`Manifest.clue_prepull(nil)` on `master`) and ignores keys it does not name, and REPORT-109's branch no longer parses it, so a manifest without the key is valid against every server, and one with it stays valid. No package reads `clue_source`: `wildfire-responses` reads only `classes` and `assignments` from `scope.json`, and the runner's `clue-reader.js` is already gone from `main` (RD-4 pass 2, R56a). So the two sides of the `clue_source` removal can merge in either order.

## Requirements

### Pulling recent logs

- **R1. The command.** `cc-data get recent-logs <class-id> --dataset <ref> [--start <time>] [--end <time>] [--refresh]`. `<class-id>` is the portal's positive integer class id. It calls REPORT-150's route on the dataset's portal's report server with the stored API token, as every `get` does, and the dataset must already exist.
- **R2. What it asks for.** With no flags, it sends the cursor stored for this dataset and class (R4) when there is one, and otherwise nothing, which reads the whole window. `--refresh` ignores the stored cursor and reads the whole window: the backstop for an event delivered later than the cursor's ten-minute margin. `--start` and `--end` are sent instead of any cursor; each takes epoch milliseconds or an RFC 3339 time and is sent as epoch milliseconds. `--refresh` with either is a usage error, and so is an `--end` not after `--start`. Whether a bound falls inside the window is the server's check, whose 400 names `window_start`.
- **R3. An expired cursor.** When the server answers 410 `EXPIRED_CURSOR` to a stored cursor, the command drops that cursor, re-reads the whole window in the same invocation, and warns that events between the previous pull and `window_start` are not in `recent_logs` and come from a Student Actions run. The result says it happened (R10). Any other refusal of a stored cursor, a 400 included, is an error: a cursor the server can no longer read because its format changed is REPORT-150's to answer with 410 (Self-Review, third pass).
- **R4. The stored cursor.** cc-data keeps one cursor per dataset and class: the `next_cursor` of the last pull that sent no `--start` and no `--end`, stored only after that pull's rows are in the store. A pull with `--start` or `--end` neither uses nor changes it, since its rows do not cover everything since the stored cursor. The cursor is the server's value, echoed as received; cc-data never decodes it and never derives one from the rows.
- **R5. Reading the response.** The body is streamed, with no size cap and no per-attempt deadline beyond the caller's context, since the server alone may wait 60 s before the first byte. Failures before the first byte (transport errors, 429, 5xx) are retried as streamed downloads are (`StreamAPIToFile`'s policy), but with two attempts in all rather than six, because each attempt that reaches Athena starts a query the server does not stop. A body that ends early or does not parse, a `class_id` that is not the one asked for, or a `count` that differs from the rows read stores nothing and leaves the cursor as it was, and the command exits with an error.
- **R6. What is stored.** Each row exactly as received: the 13 columns, `parameters` and `extras` kept as the strings they are (never JSON-decoded at fetch), plus `_fetched_at` (the pull's time) and `_class_id` (the class asked for). A row whose `id` is absent, null, not a string or empty fails the pull as a contract error naming the class, storing nothing and leaving the cursor, as REPORT-110's R5 does for a CLUE item without its identity.
- **R7. The `recent_logs` store.** One store per dataset, keyed by `id` alone, holding every class pulled into the dataset. A row pulled again replaces the stored one (the newer pull wins), so overlapping pulls never duplicate an event. **A pull never removes a row**: rows that have left the route's window, and rows another class's pull added, stay until `dataset purge` or `dataset delete`.
- **R8. Pinned column types.** `id`, `session`, `application`, `activity`, `event`, `event_value`, `parameters`, `extras` and `run_remote_endpoint` are VARCHAR; `time`, `timestamp`, `user_id`, `primary_user_id` and `_class_id` BIGINT; `_fetched_at` TIMESTAMP. The types hold whatever values a pull happened to see, including a column that is null in every row so far, so the view's schema never depends on the data (and matches `logs`' CSV types for these columns, checked on `dataflow-cohort`: BIGINT for the four integers, VARCHAR for `parameters`).
- **R9. The download record.** The dataset's manifest holds one download entry per class pulled, of type `recent_logs`, carrying the class id, the last pull's `window_start`, `since`, `until`, `query_started_at`, whether it completed, its merge counts, and the stored cursor (R4). `dataset show` lists it by class, and its `--json` entry has `class_id` and no `run_id`. `dataset purge` clears it with the store. `dataset reindex` recovers the store and the entry from disk; a reindex with no prior manifest to carry the cursor from loses it, and the next pull reads the whole window.
- **R10. The result.** Progress goes to stderr; the result line (and the MCP tool's result) is a JSON object with `type` (`recent_logs`), `class_id`, `window_start`, `nightly_through`, `since`, `until`, `query_started_at`, `latest_timestamp`, `rows` (rows received), `merge_counts` (`fetched`, `new`, `updated`, `removed`, the last always 0), `cursor` (`stored`, `unchanged` or `none`, the last for a pull with `--end`), and `expired_cursor` (R3).
- **R11. Concurrency and interruption.** Two pulls of the same class into one dataset do not run at once: the second fails "download busy", as other fetches do. Pulls of different classes may run at once and merge one after another. An interrupted pull leaves the store and cursor as they were, or its rows merged with the cursor not yet advanced; never a cursor ahead of the rows it promises, since the next pull would then skip them.
- **R12. The MCP tool.** `get_recent_logs` takes `dataset`, `class_id`, and optional `start` and `end` (strings in either form R2 accepts) and `refresh`, with R2 to R11's behavior and R10's result. Its description also carries the three facts a wrong count most often comes from, since an MCP-only client is not shown `core.md`'s view catalog (Claude Code keeps the first 4,096 characters of a server's instructions, and `## Views` starts after that; REPORT-115 fixes the cap): the rows are in the `recent_logs` view, not `logs`; an event a log run also holds appears in both views; and order by `received_time`.

### The view

- **R13. `recent_logs`.** A static view registered on every dataset: every stored row with all its columns, plus `logs`' four derived columns computed the same way (`parameters_json`, `extras_json`, `event_time` from `time` in seconds, `received_time` from `timestamp` in milliseconds). With no store it is empty with the same columns. It is materializable as the store views are.
- **R14. Apart from `logs`.** `logs` is unchanged and does not include recent rows. No per-class view is added, and `run_membership` gains no rows for `recent_logs`, which has no runs.
- **R15. `downloads` names the class.** The `downloads` view gains a `class_id` column, NULL for run downloads; a `recent_logs` row has a NULL `run_id`.

### Guidance

- **R16. Documented where the drift guards check.**
  - `core.md` gains the `recent_logs` view entry: what the rows are, the window and the ten-minute overlap, that the store holds each `id` once while a log run covering the same hours holds those events again in `logs` (stated as today's behavior, naming no Jira story, as no guidance file does; REPORT-192 rewrites the sentence when it lands), to order by `received_time`, that `_class_id` is the class asked for, and the CLUE note (rows only for portal-launched students). It names no `cc-data` command (`TestCoreNamesNoCommand`); it says "a recent-logs pull". Its `downloads` entry names `class_id`. The "Identity columns" section stays the guarded list of `run_membership` join keys, which `id` is not: the `recent_logs` view entry names `id` as its key, and a prose line after that section's list (not a bullet, so the guard does not parse it) says `recent_logs` is keyed by `id` alone and has no `run_membership` rows.
  - `tools.md` gains `get_recent_logs`; `skill_header.md`'s command list and the README's command list and usage block gain `get recent-logs <class-id>`.
  - The researcher guide gains a row in its view table (`TestResearcherGuideDocumentsEveryStaticView`) and a recent-logs section: the command, the cursor and `--refresh`, `--start`/`--end`, and that days before `window_start` come from a Student Actions run.
  - The `get` command's short help names recent logs.

### Removing the pre-pull's settings

- **R17. `clue_prepull`.** `package init` writes no `clue_prepull`. cc-data no longer reads the key: a manifest that still has it, `true` or `false`, is run, built and published like any other, with the key left in the file as the author wrote it.
- **R18. No refusal.** `cc-data package run` no longer refuses any package for CLUE. `Refused`'s comment, `runDifferences` in `cmd/package.go` and the researcher guide's "CLUE" bullet under what `run` cannot reproduce lose their mention of it.
- **R19. `clue_source`.** `scope.json` as `package run` writes it holds `kind`, `id`, `classes`, `assignments`, `dataset` and `output_dir`, in that order, and no `clue_source`. Nothing in cc-data-cli reads or writes `clue_source`.
- **R20. The runner.** researcher-dashboard's runner writes `scope.json` without `clue_source`: `CLUE_SOURCE` and its comment go from `runner/server/package-env.js`, and `runner/test/package-env.test.js` asserts the six keys. It is its own PR in researcher-dashboard, carrying `[REPORT-152]`, and can merge before or after cc-data-cli's, since nothing reads the key.

### Acceptance

- **R21. Recent logs for a running CLUE class, on staging.** After REPORT-150 is on staging and this story's build is installed: in a staging class with a CLUE offering, a test student launched from the portal does some work; `cc-data get recent-logs <class>` into a fresh dataset returns rows whose `application` is CLUE and whose `run_remote_endpoint` is that learner's; the student does more; a second pull with no flags sends the stored cursor, its `merge_counts.updated` is the overlap, its `merge_counts.new` the newer events, and `SELECT count(*), count(DISTINCT id) FROM recent_logs` gives two equal numbers matching the store count. A pull with `--start` and `--end` an hour apart returns only rows in that hour and leaves the cursor unchanged.
- **R22. A package pulls its own CLUE corpus, on a laptop and on a VM.** A scratch package (not committed) whose `run.py` reads `scope.json`, makes or finds the run REPORT-110's commands take for the scope's class, pulls `clue-documents`, then `clue-history`, then `clue-content` with REPORT-110's commands (documents before history, since REPORT-110 counts history coverage only when the history lands, from documents already pulled) and recent logs with R1, reads them through REPORT-111's views and `recent_logs`, and writes the counts to `display.md`. It runs with `cc-data package run` on a laptop against staging, then is published privately to the staging catalog and run from the Researcher Dashboard on its VM, and both runs show the same document and history counts for the class. The VM run needs a cc-data pre-release tag carrying REPORT-110, REPORT-111 and this story, pinned by the runner image, and REPORT-109's routes, CLUE-742's `clueExport` and REPORT-150's route on staging (the last for the laptop run too).
- **R23. Tests.** Unit tests cover R2 to R15 and R17 to R19 against a stub server, including: a second pull merges rather than replaces (the throwaway test above, inverted); a `--start` pull leaves the cursor; a 410 re-reads once; a truncated body stores nothing; a `count` mismatch stores nothing; a null `id` fails the pull and stores nothing; pinned types hold for an all-null column; `package run` runs a `clue_prepull: true` package; `scope.json` has the six keys.

## Technical Notes

- **The route's contract** is REPORT-150's closed spec and `server/lib/report_server_web/api/v1/recent_logs_controller.ex` and `server/lib/report_server/recent_logs.ex` on its branch, `REPORT-150-report-server-the-class-scoped`. Facts this spec leans on: the body order above; `next_cursor` is `nil` exactly when `until` is set; `rows: []` with `count: 0` and a cursor for a class with no learners.
- **Why not the existing JSON call.** `Client.getJSON` gives each attempt a 60 s deadline and caps the body at 64 MiB (`internal/api/client.go`), and retries a timeout, which on this route starts a second Athena query. The route's 60 s Athena wait plus streaming can exceed the first, and two days of a CLUE class's logs can exceed the second. `StreamAPIToFile` (`internal/api/stream.go`) has neither limit and the right retry rule (only before the first byte), used here with a budget of two attempts (R5).
- **Dataset files.** Store versions and membership files use the existing naming with `recent_logs` as the type and the class id where a run id goes; the type joins the store-file patterns in `merge.go` (`storeVersionRe`), `reindex.go`, `dataset.go` (`artifactFileRe`) and `summary.go` (`finalArtifactRe`), and `materialize.go`'s `isStoreType`.
- **Size.** Each merge rewrites the whole store, as answers and history do. A production day is about 170 MB of logs across every application (REPORT-107), of which one class is a small part, so a semester of one class's recent pulls is tens to a few hundred MB rewritten per pull. Acceptable for a package's dataset; not optimized here.
- **Mixed versions.** Downgrading is unsupported, as for REPORT-110's CLUE stores. A cc-data older than this release cannot open a query session on a dataset holding a `recent_logs` membership: its per-run view loop builds `recent_logs_<class>` over a `recent_logs` view it lacks, the view and its fallback both fail to register, and `Open` returns the error (checked on `main`). An older cc-data that only rewrites the manifest (a `get` of another type) keeps the store and membership maps but drops `class_id` and `recent_logs` from the download entry; the next pull reads the whole window and adds an entry for the class beside the classless one, and `dataset reindex`, which rebuilds entries from membership keys, drops the classless one.

### Checked before implementation (2026-10-22)

- **The staging pull (R21) cannot run yet.** REPORT-150 is in review and not on staging (404 on `report-server.concordqa.org`). Staging has CLUE offerings to use (139 assignments match "CLUE" in its assignment filter options), so the check needs only a test class with one, a student launched from the portal, and the route deployed.
- **The route takes cc-data's request as it is.** Its pipeline forces JSON whatever the `Accept` header says (`force_json`), and `CatalogCors` passes a request with no `Origin`, which Go's client never sends.
- **The body decodes as a stream.** A throwaway decoder over bodies built the way the controller builds them (opening envelope, `"rows":[`, 500-row chunks, closing fields) read a full and an empty body; a body cut inside `rows` failed at once, but one cut before the closing fields failed only **after yielding the rows before the cut**. So rows reach the store only once the whole body, closing fields included, has parsed (R5).
- **A re-merge of the same key replaces.** The throwaway merge test in Background (`Removed: 2`).
- **The view's SQL.** `logs`' four derived expressions over a JSONL store with R8's types, in DuckDB: `parameters_json->>'tileType'` read a CLUE payload, a non-JSON `parameters` gave NULL, and `event_time` and `received_time` came out as TIMESTAMP.

### What this story takes from REPORT-110

REPORT-110 generalizes the stores for its three CLUE types, and this story's store reuses that work. Each item below was checked against REPORT-110's spec on its branch, `REPORT-110-clue-fetch`, and holds:

- **A1.** Store types are declared once in the `store.Kind` registry (name, identity columns, `Strict`, pinned columns, route, double-decode), and the store-file patterns, `isStoreType`, reindex adoption and the summary's totals derive from it, so `recent_logs` is one more entry plus the two fields it adds (`ClassKeyed`, `Accumulate`).
- **A2.** Identity is per type: `store.Identity` and membership files carry the type's own key fields, so `recent_logs` can key on `id` alone.
- **A3.** Pinned column types are per type rather than the single global `pinnedColumns` map, so `recent_logs`' pins (R8) do not leak into other stores.
- **A4.** No double-decode is per type, as REPORT-110's `entry_json`/`content_json` stay byte for byte; `recent_logs` never decodes.
- **A5.** `run_membership`, the per-run store views and coverage are generalized per type. REPORT-110's R14 gives `run_membership` the identity columns of "every registered kind that has per-run membership", naming `recent_logs` as a kind that adds none; this story's `ClassKeyed` field is what marks it.
- **A6.** REPORT-110's commands take a run id (REPORT-109's export is scoped by report run), and a Student ID Mapping run of the class is a valid source, as for answers; R22's package relies on it.

REPORT-110 reworks the merge (its R11, in scope by Doug's stage 8 decision): a compact sorted index, membership read and written as key-ordered streams, and a `Covered` walk asking whether any key other than the ones being merged holds each old record. Each merging key's membership is still replaced, so R7's accumulating merge is this story's change on top of that engine.

## Out of Scope

- The `get clue-*` commands, their stores and plain views (REPORT-110); the derived CLUE views (REPORT-111).
- One row per event across log runs and recent pulls, and any change to `logs` (REPORT-192).
- Pruning rows that have left the window, or capping the store's size.
- report-server's `clue_prepull` parsing and column (REPORT-109); rigse's CLUE mint and `clue_prepull` references (RIGSE-369), and `runner/test/runner.test.js`'s comment about that mint's token, which goes with it.
- cc-data-studies' `wildfire-responses` test fixture, which writes `"clue_source": "firebase"` into its own scope file; nothing reads it, and it changes with that package.
- Closed specs that mention the pre-pull; they record what was true when written.
- A recent-logs read wider than a class (a cohort), or one reaching before `window_start`.

## Open Questions

### RESOLVED: Judgment call: where the per-class cursor lives
**Context**: The scoping check put the cursor "in the manifest"; the question is which part of it.
**Options considered**:
- A) On a `recent_logs` download entry keyed by class, with `downloads` gaining `class_id`.
- B) A separate manifest map keyed by class, invisible to `downloads`.
- C) A sidecar file beside the store.

**Decision**: A. `dataset purge` and `dataset show` already handle download entries, so the cursor is cleared and listed with no new code paths, and a researcher can see which classes a dataset has pulled and when. B and C each need their own purge, show and reindex handling, and leave `downloads` unable to say what produced the `recent_logs` rows. The cost is one added column on `downloads` (R15).

### RESOLVED: Judgment call: which pulls may move the cursor
**Context**: The route answers `next_cursor` to any request without `end`, including one with only `start`.
**Options considered**:
- A) Store the cursor from any response that has one.
- B) Store it only from a pull with no `start` and no `end` (R4).

**Decision**: B. The cursor promises that everything before its `since` is already in the store. A `start`-only pull covers only `[start, now)`, so storing its cursor would skip the gap between the previous cursor and `start` forever.

### RESOLVED: Judgment call: a 410 re-reads in the same command
**Context**: A stored cursor expires when a dataset goes more than a day or two without a pull.
**Options considered**:
- A) Fail with `EXPIRED_CURSOR` and let the caller re-run with `--refresh`.
- B) Drop the cursor and re-read the window once, with a warning (R3).

**Decision**: B, as the scoping check proposed. A package re-run after a weekend would otherwise fail on every first pull; the re-read is exactly what `--refresh` does, and the warning names the gap, which no retry could fill.

### RESOLVED: Low confidence: a row with no `id`
**Context**: The route answers `null` for an empty field, and `id` is the key. The ingester sets `id` on every event and none is null on staging (`log-cursor-and-dedup.md`); none of `dataflow-cohort`'s 263,570 CLUE rows lacks one.
**Options considered**:
- A) Skip and count it.
- B) Fail the pull, as REPORT-110's R5 fails a CLUE pull on an item without its identity (R6).
- C) Key it on a hash of its other columns.

**Decision**: B (stage 8, 2026-10-22). The case cannot arise in practice: REPORT-107 reconciled 1,849,364 production rows and two staging days by `id` with none missing (its R8), and the ingester assigns one to every event (`log-ingester.json` line 140). So the choice is about the contract, not about data loss, and B gives cc-data one rule for a record without its identity across REPORT-110 and this story, with no extra result field to document and test. C invents an identity nothing else shares, so REPORT-192 could never match the row against `logs`.

### RESOLVED: Low confidence: the acceptance package is not committed
**Context**: R22 needs a package to run on a laptop and a VM; the global rule is that a fixture no test runs should not be committed.
**Options considered**:
- A) A scratch package, its steps recorded in this spec and its result in the PR.
- B) A package committed under `test/` with a script that runs R22's laptop half.
- C) Wait for REPORT-189's package and accept on that.

**Decision**: A. The laptop half needs live staging data, a CLUE class with work in it and a logged-in researcher, which no CI run has, so a committed package (B) would be a fixture no test runs. REPORT-189 is blocked by this story (C), so accepting on it inverts the dependency. The steps are R22; the run's output goes in the PR description.

### RESOLVED: Low confidence: the researcher-dashboard side's spec lives here
**Context**: The runner's change (R20) is its own PR in another repo, and the brief asks for that side to be specced too.
**Options considered**:
- A) Spec it in this folder (R20 and its own implementation step); the researcher-dashboard PR links here.
- B) A short spec in researcher-dashboard's `specs/` on its own branch.

**Decision**: A. The change is two lines and a test assertion whose reason is this story's (R19), and RD-4 pass 2's spec already defers the key to REPORT-152 by name (`specs/RD-4-pass-2-pull-loop.md` R37). A second spec would restate R19 and R20 and could drift from them.

## Self-Review

Roles: senior Go engineer, package author, QA engineer, operator, education researcher. Every finding below was checked against the code before it was written; each had one defensible correction, made in place.

#### RESOLVED: The core guidance cannot name the command
`TestCoreNamesNoCommand` fails on any `` `cc-data `` in `core.md`, which R16 as first drafted would have put there. R16 now says the entry speaks of "a recent-logs pull", and the command spelling goes to `skill_header.md`.

#### RESOLVED: Two more command lists name every `get`
`skill_header.md` lines 69 to 74 and the README (lines 18 to 21 and 65 to 68) list each `get` subcommand; R16 named neither. Added.

#### RESOLVED: The researcher guide's view table is guarded
`TestResearcherGuideDocumentsEveryStaticView` requires every static view in the guide's table, not only in a prose section. R16 now names the table row.

#### RESOLVED: The two surfaces took different time forms
R2 accepted RFC 3339 or epoch milliseconds on the command line while R12 took only epoch milliseconds over MCP, so a time copied from one surface failed on the other. R12 now takes the same strings.

### Second pass (roles: senior Go engineer, operator, QA engineer, package author)

#### RESOLVED: The mixed-versions note understated a downgrade
The note said an older cc-data keeps working and loses only the cursor. A throwaway test on `main` with a `recent_logs/590` membership in the manifest failed `duck.Open` with "registering view recent_logs_590 ... fallback also failed", and an older manifest rewrite drops `class_id` as well as the cursor, so the entry no longer matches its class (`sameDownload` would compare 590 with 0). The note now says downgrade is unsupported and what reindex does with the classless entry.

#### RESOLVED: R22 omitted REPORT-150 from its prerequisites
R22's package pulls recent logs with R1 on both the laptop and the VM, but its list named only REPORT-109's routes and CLUE-742. Added; REPORT-150 is still not deployed on staging (Background).

#### RESOLVED: Every attempt on a slow class runs another Athena query
**Context**: R5 retries failures before the first byte "as streamed downloads are", and the Technical Notes call that rule right for this route. `StreamAPIToFile` retries every 5xx up to `MaxAttempts` (6): a throwaway test against a route answering 503 recorded 6 requests before `TRANSIENT`. The route answers 503 both when four reads are in flight and when its Athena query has not finished in 60 s, and REPORT-150's R13 says it "stops waiting but does not stop the query". So a class whose query takes over 60 s gets six queries started, five of them left running on Athena, before the pull fails after about six minutes.
**Options considered**:
- A) Keep the shared rule (six attempts). Retrying is what the 503's "retry shortly" invites, and the limiter-full case benefits from it.
- B) Give the recent-logs call a client copy with `MaxAttempts: 2`: one retry for a full limiter, at most one orphaned query for a slow class, and the caller re-runs.
- C) Ask REPORT-150 for a distinct code or `Retry-After` on the Athena timeout, so only the limiter case is retried.

**Decision**: B (Doug, 2026-10-22). It bounds a slow class at two queries, one of them orphaned, with no change to REPORT-150, and one retry still covers a full limiter. C is the cleaner fix if REPORT-150 is reopened for another reason. R5 and the Technical Note now say two attempts.

### Third pass (roles: security engineer, performance engineer, education researcher querying `recent_logs`, REPORT-192 implementer, senior engineer reading cold)

#### RESOLVED: `dataset show --json` would report the class's entry as run 0
`BuildShowJSON` (`internal/dataset/summary.go:83-97`) copies every download's `RunID` into `DownloadJSON.RunID`, an `int` without `omitempty`, so a `recent_logs` entry would read `"run_id": 0` while R15's `downloads` view says NULL. R9 now says the JSON entry carries `class_id` and no `run_id`; the store step gives `run_id` `omitempty`, which changes no other entry since run ids start at 1.

#### RESOLVED: R16 put a Jira key in shipped guidance
R16 described the `core.md` entry as saying events repeat in `logs` "until REPORT-192". `core.md` renders into the MCP instructions and no guidance file names a Jira story (`grep REPORT- internal/guidance/src` is empty). R16 now asks for today's behavior, which REPORT-192 rewrites.

#### RESOLVED: Whether `id` belongs in the guarded "Identity columns" section
R16 and the guidance step add `id` there because "REPORT-110 derives the identity-column guard from its store registry". Today `IdentityColumnNames()` returns `membershipColumns` (`internal/duck/views.go:717`), its test is `TestIdentityColumnNamesAreTheMembershipKey`, and `core.md` introduces the section as "also the `USING` key when joining a store to `run_membership`"; REPORT-110 keeps that test name and only grows its list. This story removes `id` from `run_membership`'s inventory (A5), so if REPORT-110 keeps deriving the guard from the membership columns, an `id` bullet fails `TestGuidanceDocumentsEveryIdentityColumn` ("documented but no longer registered"); if it derives from every kind, `TestIdentityColumnNamesAreTheMembershipKey` needs `id` and a new meaning, which no step lists. Separately, the step's "gains `recent_logs`: `id`" layout documents the store, not the column: a throwaway `ParseCatalog` run read ``- `recent_logs`: `id`, ...`` as `[recent_logs]` and ``- `id`: keys `recent_logs` ...`` as `[id]`.
- A) The guard stays the `run_membership` key: `id` is not added to the guarded list, the `recent_logs` view entry names `id` as its key, and a prose line after the list (not a bullet, so `ParseCatalog` ignores it) says `recent_logs` is keyed by `id` alone with no `run_membership` rows.
- B) The guard covers every kind's identity: an `id` bullet led by the column name, `TestIdentityColumnNamesAreTheMembershipKey` split into a membership-column test and an identity-column test, and the section's intro reworded so `id` is not a `USING` key.

**Decision**: A (stage 8 follow-up, 2026-10-22; needed no call). REPORT-110's spec keeps `TestIdentityColumnNamesAreTheMembershipKey` and only grows its list (its implementation, `internal/duck/views_test.go`), so A changes nothing of REPORT-110's, and the section's promise (these are the `run_membership` join keys) stays true. Test: the guard itself; adding an `id` bullet, or dropping the `recent_logs` membership exclusion, turns it red. R16 and the plan's guidance step now say A.

#### RESOLVED: A stored cursor the server refuses with 400 blocks every later pull
R3 recovers from 410 only. REPORT-150 answers 400 `BAD_REQUEST` ("The cursor is not valid for this class.") for a cursor of an unknown version or one that does not decode (`recent_logs.ex` `cursor_bounds`), and its cursor is opaque precisely so the server can change what is inside. cc-data never changes a stored cursor except on success (R4), so after one such 400 every pull of that class in that dataset fails the same way until someone passes `--refresh`, which a package on the VM never does. With a stored cursor and no `start`/`end`, the route's other 400s cannot occur (cc-data validates the class id and never sends a cursor with bounds), so a 400 there always means the cursor.
- A) Keep R3 as is; the error names the class, and the remedy is `--refresh`.
- B) Treat a 400 `BAD_REQUEST` on a request that sent the stored cursor as R3 treats 410: drop the cursor, re-read the window once in the same invocation, warn, and set `expired_cursor` (or a sibling `invalid_cursor`) in the result.
- C) Drop the stored cursor on that 400 and fail; the next pull reads the window.

**Decision**: A in cc-data, with the rule placed in REPORT-150 (Doug, 2026-10-23). B would have cc-data infer meaning from a generic 400, which puts knowledge of cursor versions in the client, and it would turn a cc-data bug that mangles the cursor into a silent full-window re-read on every pull. The server owns the cursor's format, so a cursor it issued but can no longer read is expired: it should answer 410 `EXPIRED_CURSOR`, which R3 already handles, and keep 400 for a cursor that does not decode or names another class. Only `v1` exists, so nothing can trip this today; the rule goes into REPORT-150's spec and README as a constraint on whoever introduces a new cursor version (asked on `clue-cc-data` #941). No cc-data change.