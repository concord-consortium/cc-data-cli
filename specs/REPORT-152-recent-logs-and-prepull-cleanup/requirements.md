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
- **R3. An expired cursor.** When the server answers 410 `EXPIRED_CURSOR` to a stored cursor, the command drops that cursor, re-reads the whole window in the same invocation, and warns that events between the previous pull and `window_start` are not in `recent_logs` and come from a Student Actions run. The result says it happened (R10).
- **R4. The stored cursor.** cc-data keeps one cursor per dataset and class: the `next_cursor` of the last pull that sent no `--start` and no `--end`, stored only after that pull's rows are in the store. A pull with `--start` or `--end` neither uses nor changes it, since its rows do not cover everything since the stored cursor. The cursor is the server's value, echoed as received; cc-data never decodes it and never derives one from the rows.
- **R5. Reading the response.** The body is streamed, with no size cap and no per-attempt deadline beyond the caller's context, since the server alone may wait 60 s before the first byte. Failures before the first byte (transport errors, 429, 5xx) are retried as streamed downloads are (`StreamAPIToFile`'s policy). A body that ends early or does not parse, a `class_id` that is not the one asked for, or a `count` that differs from the rows read stores nothing and leaves the cursor as it was, and the command exits with an error.
- **R6. What is stored.** Each row exactly as received: the 13 columns, `parameters` and `extras` kept as the strings they are (never JSON-decoded at fetch), plus `_fetched_at` (the pull's time) and `_class_id` (the class asked for). A row whose `id` is absent, null, not a string or empty fails the pull as a contract error naming the class, storing nothing and leaving the cursor, as REPORT-110's R5 does for a CLUE item without its identity.
- **R7. The `recent_logs` store.** One store per dataset, keyed by `id` alone, holding every class pulled into the dataset. A row pulled again replaces the stored one (the newer pull wins), so overlapping pulls never duplicate an event. **A pull never removes a row**: rows that have left the route's window, and rows another class's pull added, stay until `dataset purge` or `dataset delete`.
- **R8. Pinned column types.** `id`, `session`, `application`, `activity`, `event`, `event_value`, `parameters`, `extras` and `run_remote_endpoint` are VARCHAR; `time`, `timestamp`, `user_id`, `primary_user_id` and `_class_id` BIGINT; `_fetched_at` TIMESTAMP. The types hold whatever values a pull happened to see, including a column that is null in every row so far, so the view's schema never depends on the data (and matches `logs`' CSV types for these columns, checked on `dataflow-cohort`: BIGINT for the four integers, VARCHAR for `parameters`).
- **R9. The download record.** The dataset's manifest holds one download entry per class pulled, of type `recent_logs`, carrying the class id, the last pull's `window_start`, `since`, `until`, `query_started_at`, whether it completed, its merge counts, and the stored cursor (R4). `dataset show` lists it. `dataset purge` clears it with the store. `dataset reindex` recovers the store and the entry from disk; a reindex with no prior manifest to carry the cursor from loses it, and the next pull reads the whole window.
- **R10. The result.** Progress goes to stderr; the result line (and the MCP tool's result) is a JSON object with `type` (`recent_logs`), `class_id`, `window_start`, `nightly_through`, `since`, `until`, `query_started_at`, `latest_timestamp`, `rows` (rows received), `merge_counts` (`fetched`, `new`, `updated`, `removed`, the last always 0), `cursor` (`stored`, `unchanged` or `none`, the last for a pull with `--end`), and `expired_cursor` (R3).
- **R11. Concurrency and interruption.** Two pulls of the same class into one dataset do not run at once: the second fails "download busy", as other fetches do. Pulls of different classes may run at once and merge one after another. An interrupted pull leaves the store and cursor as they were, or its rows merged with the cursor not yet advanced; never a cursor ahead of the rows it promises, since the next pull would then skip them.
- **R12. The MCP tool.** `get_recent_logs` takes `dataset`, `class_id`, and optional `start` and `end` (strings in either form R2 accepts) and `refresh`, with R2 to R11's behavior and R10's result.

### The view

- **R13. `recent_logs`.** A static view registered on every dataset: every stored row with all its columns, plus `logs`' four derived columns computed the same way (`parameters_json`, `extras_json`, `event_time` from `time` in seconds, `received_time` from `timestamp` in milliseconds). With no store it is empty with the same columns. It is materializable as the store views are.
- **R14. Apart from `logs`.** `logs` is unchanged and does not include recent rows. No per-class view is added, and `run_membership` gains no rows for `recent_logs`, which has no runs.
- **R15. `downloads` names the class.** The `downloads` view gains a `class_id` column, NULL for run downloads; a `recent_logs` row has a NULL `run_id`.

### Guidance

- **R16. Documented where the drift guards check.**
  - `core.md` gains the `recent_logs` view entry: what the rows are, the window and the ten-minute overlap, that the store holds each `id` once while a log run covering the same hours holds those events again in `logs` until REPORT-192, to order by `received_time`, that `_class_id` is the class asked for, and the CLUE note (rows only for portal-launched students). It names no `cc-data` command (`TestCoreNamesNoCommand`); it says "a recent-logs pull". Its `downloads` entry names `class_id`. REPORT-110 derives the identity-column guard from its store registry (its R8), so `id` joins the "Identity columns" section as `recent_logs`' key.
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
- **R22. A package pulls its own CLUE corpus, on a laptop and on a VM.** A scratch package (not committed) whose `run.py` reads `scope.json`, makes or finds the run REPORT-110's commands take for the scope's class, pulls `clue-documents`, `clue-history` and `clue-content` with REPORT-110's commands and recent logs with R1, reads them through REPORT-111's views and `recent_logs`, and writes the counts to `display.md`. It runs with `cc-data package run` on a laptop against staging, then is published privately to the staging catalog and run from the Researcher Dashboard on its VM, and both runs show the same document and history counts for the class. The VM run needs a cc-data pre-release tag carrying REPORT-110, REPORT-111 and this story, pinned by the runner image, and REPORT-109's routes and CLUE-742's `clueExport` on staging.
- **R23. Tests.** Unit tests cover R2 to R15 and R17 to R19 against a stub server, including: a second pull merges rather than replaces (the throwaway test above, inverted); a `--start` pull leaves the cursor; a 410 re-reads once; a truncated body stores nothing; a `count` mismatch stores nothing; a null `id` fails the pull and stores nothing; pinned types hold for an all-null column; `package run` runs a `clue_prepull: true` package; `scope.json` has the six keys.

## Technical Notes

- **The route's contract** is REPORT-150's closed spec and `server/lib/report_server_web/api/v1/recent_logs_controller.ex` and `server/lib/report_server/recent_logs.ex` on its branch (`95eff9d`). Facts this spec leans on: the body order above; `next_cursor` is `nil` exactly when `until` is set; `rows: []` with `count: 0` and a cursor for a class with no learners.
- **Why not the existing JSON call.** `Client.getJSON` gives each attempt a 60 s deadline and caps the body at 64 MiB (`internal/api/client.go`), and retries a timeout, which on this route starts a second Athena query. The route's 60 s Athena wait plus streaming can exceed the first, and two days of a CLUE class's logs can exceed the second. `StreamAPIToFile` (`internal/api/stream.go`) has neither limit and the right retry rule (only before the first byte).
- **Dataset files.** Store versions and membership files use the existing naming with `recent_logs` as the type and the class id where a run id goes; the type joins the store-file patterns in `merge.go` (`storeVersionRe`), `reindex.go`, `dataset.go` (`artifactFileRe`) and `summary.go` (`finalArtifactRe`), and `materialize.go`'s `isStoreType`.
- **Size.** Each merge rewrites the whole store, as answers and history do. A production day is about 170 MB of logs across every application (REPORT-107), of which one class is a small part, so a semester of one class's recent pulls is tens to a few hundred MB rewritten per pull. Acceptable for a package's dataset; not optimized here.
- **Mixed versions.** An older cc-data that rewrites the manifest keeps the `recent_logs` store and membership (they are generic maps) but drops fields it does not know on the download entry, including the cursor; the next pull reads the whole window, which dedup absorbs.

### Checked before implementation (2026-10-22)

- **The staging pull (R21) cannot run yet.** REPORT-150 is in review and not on staging (404 on `report-server.concordqa.org`). Staging has CLUE offerings to use (139 assignments match "CLUE" in its assignment filter options), so the check needs only a test class with one, a student launched from the portal, and the route deployed.
- **The route takes cc-data's request as it is.** Its pipeline forces JSON whatever the `Accept` header says (`force_json`), and `CatalogCors` passes a request with no `Origin`, which Go's client never sends.
- **The body decodes as a stream.** A throwaway decoder over bodies built the way the controller builds them (opening envelope, `"rows":[`, 500-row chunks, closing fields) read a full and an empty body; a body cut inside `rows` failed at once, but one cut before the closing fields failed only **after yielding the rows before the cut**. So rows reach the store only once the whole body, closing fields included, has parsed (R5).
- **A re-merge of the same key replaces.** The throwaway merge test in Background (`Removed: 2`).
- **The view's SQL.** `logs`' four derived expressions over a JSONL store with R8's types, in DuckDB: `parameters_json->>'tileType'` read a CLUE payload, a non-JSON `parameters` gave NULL, and `event_time` and `received_time` came out as TIMESTAMP.

### What this story assumes of REPORT-110

REPORT-110 generalizes the stores for its three CLUE types. This story's store reuses that work, and stage 8 checks these against REPORT-110's spec:

- **A1.** Store types are declared once (name, identity fields, pinned columns), and the store-file patterns, `isStoreType`, reindex adoption and the summary's totals derive from that declaration, so `recent_logs` is one more entry.
- **A2.** Identity is per type: `store.Identity` and membership files carry the type's own key fields, so `recent_logs` can key on `id` alone.
- **A3.** Pinned column types are per type rather than the single global `pinnedColumns` map, so `recent_logs`' pins (R8) do not leak into other stores.
- **A4.** No double-decode is per type, as REPORT-110's `entry_json`/`content_json` stay byte for byte; `recent_logs` never decodes.
- **A5.** `run_membership`, the per-run store views and coverage are generalized per type, and a type can opt out of all three, including `run_membership`'s column inventory (REPORT-110's R14 puts every registered identity column on it).
- **A6.** REPORT-110's commands take a run id (REPORT-109's export is scoped by report run), and a Student ID Mapping run of the class is a valid source, as for answers; R22's package relies on it.

Nothing here assumes REPORT-110 changes `MergeCompact`'s per-run replacement; R7's accumulating merge is this story's.

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
