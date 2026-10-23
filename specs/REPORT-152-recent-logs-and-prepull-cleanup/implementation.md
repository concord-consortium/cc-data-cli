# Implementation Plan: cc-data-cli: recent logs, and removing the CLUE pre-pull settings

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-152
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

The branch stacks on REPORT-111's, which stacks on REPORT-110's. The store and view steps are written against REPORT-110's spec on `REPORT-110-clue-fetch` (its `store.Kind` registry, its streamed membership and `Covered` walk) and REPORT-111's on `REPORT-111-clue-views` (`derivedStoreViews`); those stories' implementations are the reference where a name here drifts from them. Line numbers are `main`'s.

The cleanup steps come first: the only file they share with the recent-logs steps is `docs/researcher-guide.md`, where they delete a bullet in section 7 and the guidance step adds a view-table row and a fetching subsection, so they review and revert on their own.

## Implementation Plan

### Drop `clue_prepull` and `clue_source` from cc-data-cli

**Summary**: R17 to R19. `package init` stops writing `clue_prepull`, `package run` stops refusing it, and `scope.json` loses `clue_source`. One commit: both are the pre-pull's settings, and the docs that describe them are the same two paragraphs.

**Files affected**:
- `internal/packages/manifest.go`: delete `Manifest.CluePrepull`. With the field gone, `init` writes no key and `ReadManifest` ignores one that is present.
- `internal/packages/run.go`: delete the refusal at lines 78 to 80; `Refused`'s comment becomes "a package the runner would not start: it does not apply to the scope." Line 104 drops `ClueSource: "firebase"`.
- `internal/packages/scope.go`: delete `ScopeFile.ClueSource`, its field in `MarshalJSON`'s `ordered` struct and in the positional literal; the type comment keeps only the `Dataset` sentence. `ParseLocalScope`'s comment says "the other three are the runner's to set", which becomes "the other two".
- `internal/packages/run_test.go`: line 156 asserts `scope["clue_source"]` is absent and that the file's keys are exactly the six, in order (decode with `json.Decoder.Token` to read key order, since a map loses it). The `clue_prepull` half of `TestRunRefusesBeforeStarting` (lines 197 to 204) becomes `TestRunIgnoresCluePrepull`: a manifest read from bytes holding `"clue_prepull": true` runs to completion and its entrypoint's marker file exists. Deleting the refusal is the mutation it catches only if the manifest is read through `ReadManifest`, so the test goes through it rather than setting a struct field that no longer exists.
- `cmd/package_test.go` `TestPackageInitRefusesASecondInit` (lines 122 to 136, the one test that reads `Init`'s `manifest.json`): the written file has no `clue_prepull` key, which fails if the field stays on `Manifest`.
- `internal/packages/files_test.go` line 20 and `cmd/package_test.go` line 28: leave the fixture manifests' `"clue_prepull": false` as they are; they now also show that a manifest carrying the key still validates.
- `cmd/package.go`: `runDifferences` drops its last sentence.
- `docs/researcher-guide.md`: delete the "CLUE" bullet at lines 650 and 651.

**Estimated diff size**: ~70 lines.

---

### Drop `clue_source` from the runner (researcher-dashboard)

**Summary**: R20. Its own branch and PR in researcher-dashboard, `REPORT-152-drop-clue-source`, carrying `[REPORT-152]`, linking this spec. Merges in either order with the cc-data-cli PR.

**Files affected**:
- `runner/server/package-env.js`: delete `CLUE_SOURCE` and its comment (lines 49 and 50) and `clue_source: CLUE_SOURCE` from `scopeFile`; the function's comment becomes "Everything but dataset and output_dir comes from the scope block rigse sent...".
- `runner/test/package-env.test.js`: the test title says six fields; line 111's key list drops `clue_source`; line 116 goes.
- Any export of `CLUE_SOURCE` imported elsewhere: `git grep CLUE_SOURCE` on the branch must be empty.

**Estimated diff size**: ~10 lines.

---

### The `recent_logs` store type and its accumulating merge

**Summary**: R7 to R9 and R11's merge half. Declares the store, makes its merge keep every row, gives its download entry a class, and teaches reindex, purge, show and materialize about it. No network code yet; tests drive it with segments written directly, as `merge_test.go` does.

**Files affected**:
- `internal/store/kind.go` (REPORT-110's registry): two fields on `Kind`, each false by default so `answers`, `history` and the CLUE kinds keep their behavior with no edit:
  - `ClassKeyed bool`: the integer in the kind's segment and membership names is a class id, not a run id. `Segment.AppendPage` stamps it as `_class_id` instead of `_run_id`; `run_membership`'s rows and identity columns and the per-run views leave the kind out (REPORT-110's R14 already words its inventory as "every kind that has per-run membership"); `Download.Key()` reads `ClassID`.
  - `Accumulate bool`: a merge adds to a key's membership instead of replacing it (below).

  and the kind itself: the constant `TypeRecentLogs = "recent_logs"` beside `TypeAnswers` and `TypeHistory`, which every other package uses to name it (`store.KindOf(store.TypeRecentLogs)` where it needs the `Kind`, since the variable is unexported), and `recentLogsKind`, appended to `kinds`: `Name: "recent_logs"`, `Identity: []string{"id"}`, `Strict: true` (an absent, null, non-string or empty `id` is an error, which is R6's rule), `DoubleDecode: false`, `Route: ""` (it is not fetched through `FetchPaged`), `ClassKeyed: true`, `Accumulate: true`, and `Pinned`: `id`, `session`, `application`, `activity`, `event`, `event_value`, `parameters`, `extras`, `run_remote_endpoint` VARCHAR; `time`, `timestamp`, `user_id`, `primary_user_id`, `_class_id` BIGINT; `_fetched_at` TIMESTAMP (R8).
- `internal/dataset/merge.go` (on REPORT-110's streamed merge): two changes, both only for an accumulating kind.
  - `openCovered` opens a `MembershipReader` on each key being merged's current membership file too, not only on the other keys'. REPORT-110's walk asks whether anything other than the merging keys holds an old key, because a merging key's membership is about to be replaced; an accumulating key's is not, so its previous file is one more source. Without it, every earlier row of the class fails `Covered.Has` and is removed, which is what the stage 1 throwaway showed on today's engine (`Removed: 2`).
  - The new membership file for each merging key is written by walking the previous file's `MembershipReader` and the segment index's keys together, both in key order, emitting each key once, into REPORT-110's `MembershipWriter`. The output is in key order by construction, so its reader's order check holds, and nothing is held in memory beyond the two cursors.

  A missing membership file named by the manifest fails closed in both places, as REPORT-110's `openCovered` does. The behavior was prototyped on 2026-10-22 on today's engine (the in-memory union in the place `membershipUnionMulti` now runs): a second pull of the same key gave `New: 1, Updated: 1, Removed: 0` with the store at 3, a third pull that swept another key's segment kept both keys' rows, and a reindex afterward adopted the same store. The tests below are written against behavior, so they hold for the streamed form unchanged.
- `internal/dataset/manifest.go` `Download`: add `ClassID int \`json:"class_id,omitempty"\`` and `RecentLogs *RecentLogsPull \`json:"recent_logs,omitempty"\``, where `RecentLogsPull` holds `WindowStart`, `Since` and `QueryStartedAt` (`int64`, epoch ms), `NightlyThrough string` (an ISO date such as `"2026-10-21"`, REPORT-150's R11), `Until *int64`, `LatestTimestamp *int64`, `Rows int` and `Cursor string` (`omitempty`). A `recent_logs` entry has `RunID` 0 and `ClassID` set; `Download.Key()` returns `ClassID` for `recent_logs` and `RunID` otherwise, and is what every code path below uses where it pairs a download with a segment or membership key.
- `internal/dataset/dataset.go`: `sameDownload` also compares `ClassID`; `artifactFileRe` comes from the registry (REPORT-110), so `recent_logs.v<N>.jsonl` and `members_recent_logs_<class>.v<N>.jsonl` are purged.
- `internal/dataset/merge.go` `recordSweptDownloads`: match on `dl.Key()`. A `recent_logs` entry is never incomplete (the fetch step writes it only after its merge), so this loop finds none for that type, which is right: a swept class's cursor is not advanced, and its next pull re-reads.
- `internal/dataset/reindex.go`: the membership rebuild makes a `recent_logs` key into `Download{Type, ClassID: key, Complete: true, Recovered: true}`; `priorDownloadIndex` and `downloadKey` key on `dl.Key()`; `carryProvenance` also carries `RecentLogs` (the cursor with it).
- `internal/dataset/summary.go`: `manifestTotals` adds `recent_logs` (registry-derived under REPORT-110); `DownloadJSON` gains `ClassID int \`json:"class_id,omitempty"\`` and `RecentLogs`, and its `RunID` tag becomes `json:"run_id,omitempty"` so a `recent_logs` entry does not read as run 0 (run ids start at 1, so no other entry changes); `driftWarnings`' INCOMPLETE text never fires for this type, by construction.
- `cmd/dataset_show.go`: the RUN cell prints `class <id>` for a `recent_logs` row.

`internal/duck/materialize.go`'s `incompleteRuns` needs no change: it skips every complete download first, and a `recent_logs` entry is never incomplete.

**Tests** (`internal/dataset/merge_test.go`, `reindex_test.go`, `summary_test.go`, `dataset_test.go`):
- Two pulls of class 590 (`e1,e2` then `e2',e3`): store holds 3, `e2` carries the second pull's data, counts `{2,2,0,0}` then `{2,1,1,0}`, and the class's membership file holds `e1,e2,e3`; a third pull (`e4`) still holds `e1`. Removing the merging key's file from `openCovered` makes the second merge drop `e1` (`Removed: 1`, store 2). Removing only the membership union leaves the second merge correct (the old file still covers `e1`) but writes `{e2,e3}`, so the membership assertion and the third pull's `Removed: 1` catch it.
- Classes 590 and 591 pulled alternately, one merging the other by sweep: neither loses a row.
- Reindex with the manifest present recovers the entry with its cursor; with the manifest deleted, recovers the store and a `recovered` entry with no cursor.
- Purge removes the store and membership files and the entry.
- Show lists the entry with `class_id` and the `recent_logs` total, and its JSON entry has no `run_id` key (fails if `omitempty` is dropped).

**Estimated diff size**: ~300 lines, about half tests.

---

### The `recent_logs` view and `downloads.class_id`

**Summary**: R13 to R15. A static view over the store with `logs`' derived columns, no per-class view, no `run_membership` rows, and a `class_id` column on `downloads`.

**Files affected**:
- `internal/duck/views.go`:
  - `derivedStoreViews` (REPORT-111's map from kind name to view builder, which `statements()`' loop over the kinds consults before falling back to `storeView`) gains `store.TypeRecentLogs: viewSet.recentLogsView`, a method expression, since the map's values are `func(viewSet) viewStmt`. Without the entry, the registry loop registers a plain `recent_logs` view with no derived columns.
  - `(vs viewSet) recentLogsView() viewStmt` builds on the kind's `storeView` `read_json` member and wraps it: `SELECT *, <derived> FROM read_json(...)`, where `<derived>` is each `logDerived` entry's `expr(sqlIdent(src))` aliased to its name. The store's columns are pinned, so every source column exists and the CSV path's `CAST(NULL ...)` branch is not needed. The fallback is the pinned columns' typed-empty `SELECT` plus each derived column as `CAST(NULL AS <typ>)`. The view declares the store file in `files`, so `materializableFrom` admits it with no list edit.
  - `runMembershipView` and `perDownloadViews` skip membership keys of a `ClassKeyed` kind, and `run_membership`'s column inventory leaves its identity columns out (REPORT-110's R14 already excludes a kind without per-run membership). Without the first, a pulled class adds a `recent_logs_590` view and `run_membership` rows whose `run_id` is a class id; without the second, every dataset's `run_membership` gains an always-NULL `id` column.
  - `downloadsView`: header gains `class_id`; each row renders `run_id` as `CAST(NULL AS BIGINT)` for a `recent_logs` entry and `class_id` as `CAST(NULL AS BIGINT)` for every other. A bare `NULL` in a `VALUES` table types the column `"NULL"` when no row has a value (checked in DuckDB), so the cast is required, not tidiness. The fallback adds `CAST(NULL AS BIGINT) AS class_id`.
- `internal/duck/engine_test.go` line 140's static-view list gains `recent_logs`.

**Tests** (`internal/duck/recent_logs_view_test.go`, new):
- A dataset with a two-row store: `parameters_json->>'tileType'` reads the payload, an unparseable `parameters` gives NULL `parameters_json` with `parameters` intact, `received_time` has millisecond precision, and `DESCRIBE recent_logs` gives R8's types.
- A store where `primary_user_id` is null in every row still types it BIGINT (the pin, not the data).
- An empty dataset: the view exists, zero rows, the same `DESCRIBE`.
- No `recent_logs_<n>` view and no `run_membership` row after a pull; `logs` row count unchanged by one.
- `downloads`: `typeof(class_id)` is BIGINT on a dataset with only run downloads; a `recent_logs` row has NULL `run_id` and its class.
- Materialize then query: the Parquet-backed view answers the same rows.

**Estimated diff size**: ~220 lines.

---

### Fetching recent logs

**Summary**: R1 to R6, R10, R11 and R12's shared core. The API call, the body decoder, and `fetch.FetchRecentLogs`, tested against an `httptest` server that speaks REPORT-150's contract.

**Files affected**:
- `internal/api/endpoints.go`: `RecentLogsPath(classID int, q RecentLogsQuery) string` builds `/api/v1/classes/<id>/recent-logs` with `cursor`, or `start`/`end`, URL-encoded; `(c *Client) StreamRecentLogs(ctx, classID, q, dst)` copies the client, sets the copy's `MaxAttempts` to `RecentLogsMaxAttempts` (2), and calls the copy's `StreamAPIToFile(ctx, RecentLogsPath(classID, q), dst)`, so every other call keeps six attempts (R5). `StreamAPIToFile` already gives the retry rule (only before the first byte), no size cap and no per-attempt deadline (R5), and maps a cut-off body to `TRANSIENT` (`partialDownloadError`). That error's message is written for report CSVs ("the server may have exceeded its download budget for this cohort ... or narrow the report filter"), so `api` gains `IsPartialDownload(err) bool` and `FetchRecentLogs` replaces the message on that error with the cut-off one below, keeping `TRANSIENT`.
- `internal/fetch/recentlogs.go` (new):
  - `RecentLogsOptions{DS, Client, ClassID int, Start, End string, Refresh bool, Progress io.Writer}` and `(o) validate() error`: class id positive; `Refresh` with `Start` or `End` is a usage error; `ParseLogBound` on each; `end > start` when both.
  - `ParseLogBound(s string) (int64, error)`: all digits is epoch milliseconds; otherwise `time.Parse(time.RFC3339Nano, s)` to milliseconds.
  - `FetchRecentLogs(ctx, o) (map[string]any, error)`:
    1. `acquireDownload(ds, "seg_recent_logs_<class>.lock", "download busy: another cc-data command is pulling class <id>'s recent logs")`.
    2. The segment `store.OpenSegment(ds.Dir, store.TypeRecentLogs, class)`. A finished cursor means an earlier pull parsed its body and stopped before merging: merge it first (its rows are good; the stored cursor is still the old one, so nothing is skipped). A segment with no cursor is removed.
    3. The query: `Start`/`End` when given; else the entry's stored cursor unless `Refresh`; else none.
    4. Stream to `segments/seg_recent_logs_<class>.body.json` (removed on every exit). On `api.IsExpiredCursor` for a stored cursor, clear it, warn (R3's text), set `expiredCursor`, and stream again with no query, once.
    5. Decode the body file (below) into the segment in 500-row `AppendPage` calls, then check `class_id` and `count`. Any failure removes the segment, so no cursor is ever written for a partial body.
    6. Write the finished cursor (`store.Cursor{Pages: 1, Items: rows}`), then `ds.MergeCompact`.
    7. `ds.UpsertDownload` the entry: `Complete: true`, `FetchedAt` the pull's time (what `dataset show` prints as FETCHED), merge counts, `RecentLogsPull` from the envelope, and `Cursor` = the response's `next_cursor` when the pull sent neither `Start` nor `End`, else the previous entry's cursor (R4). The cursor is written only after the merge has repointed the manifest, so an interruption between them leaves an old cursor and merged rows: the next pull re-reads, dedup absorbs it (R11).
    8. The R10 result map. Its `cursor` is `stored` when step 7 wrote the response's `next_cursor`, `none` when the response had none (a pull with `end`), and `unchanged` otherwise (a pull with `start` only).
  - `decodeRecentLogs(r io.Reader, sink func([]json.RawMessage) error) (recentLogsEnvelope, error)`: `json.Decoder` with `UseNumber`, walking the top-level object's keys; `rows` is read element by element as `json.RawMessage` and handed to `sink` in batches; every other key decodes into the envelope, whose fields have `RecentLogsPull`'s types plus `ClassID int`, `Count int` and `NextCursor *string`; after the closing brace a further token must be `io.EOF`. Each row's `id` is read with REPORT-110's `store.IdentityFromRecord(k, row)`, `k` being `store.KindOf(store.TypeRecentLogs)`, whose `Strict` rule stops the decode with a contract error on an absent, null, non-string or empty `id` (R6); rows are passed through byte for byte, so `parameters` stays a string. The shape was run as throwaway code on 2026-10-22 over bodies built like the controller's: a cut inside `rows` fails at once, and a cut before the closing fields fails only after the rows before it were yielded, which is why step 5 writes no cursor until the decode returns cleanly.
  - Errors: a body that does not decode, or that the connection cut off (`api.IsPartialDownload`), is `TRANSIENT` ("the recent-logs answer was cut off; re-run"); a row without its `id` is `ExitContract` code `MALFORMED_ITEM`, the code REPORT-110 gives a CLUE item without its identity ("class 590 recent_logs: row 17 has no id"), so R6's one rule is one code too; a `class_id` or `count` mismatch is `ExitContract` code `BAD_RESPONSE`; any other `api` error goes through `api.AsCLIError`.
- `internal/fetch/recentlogs_test.go` (new): `recentLogsServer`, an `httptest` handler that records each request's query, answers from a queue of canned bodies built with the controller's opening/rows/closing framing (so a fixture can be cut at any byte), and can answer 410 or 503 first. Cases:
  - first pull sends no parameters and stores the cursor, with `cursor: "stored"` in the result; second sends exactly that cursor;
  - `--start` sends `start` only and leaves the stored cursor (the entry's `Cursor` is byte-identical before and after), with `cursor: "unchanged"`; `--start` with `--end` gives `cursor: "none"`; a mapping that always answers `stored` fails both;
  - `validate`: `End` equal to `Start` is a usage error, which fails if the `end > start` check is removed;
  - `--refresh` sends nothing even with a cursor stored;
  - 410 on the stored cursor: two requests, the second with no parameters, `expired_cursor: true`, the warning on progress;
  - a body cut before its closing fields: error `TRANSIENT`, no store, no segment, cursor unchanged; and a body whose connection is closed mid-`rows` (hijacked, as `stream_test.go`'s `truncatingServer` does): `TRANSIENT` whose message does not mention a report filter, which fails if the `IsPartialDownload` mapping is removed;
  - `count` one higher than the rows: `BAD_RESPONSE`, nothing stored; `class_id` of another class likewise;
  - a null-`id` row and an empty-string one: each `MALFORMED_ITEM` naming the class, nothing stored, cursor unchanged;
  - a finished, unmerged segment left from a crash is merged before the next request;
  - a second `FetchRecentLogs` for the same class while the first holds the lock: busy; for another class: both complete;
  - a server answering 503 to every request: exactly 2 requests, then `TRANSIENT`, nothing stored (removing the `MaxAttempts` override makes it 6);
  - `ParseLogBound`: `"1792540800000"`, `"2026-10-21T12:00:00Z"`, `"2026-10-21T12:00:00.5-04:00"`, and `"yesterday"` refused.

**Estimated diff size**: ~480 lines, more than half tests. If review wants it smaller, the decoder and its tests split cleanly into their own commit ahead of the rest.

---

### The command, the MCP tool and the guidance

**Summary**: R1, R12 and R16: the surfaces over `FetchRecentLogs`, and the docs the drift guards hold to them.

**Files affected**:
- `cmd/get_recent_logs.go` (new): `newGetRecentLogsCmd()`, `Use: "recent-logs <class-id> --dataset <ref>"`, flags `--dataset`, `--start`, `--end`, `--refresh`; `Long` says what the window is, how the cursor works, that `--refresh` is the backstop for late events, and that days before the window come from a Student Actions run. `RunE` mirrors `newPagedGetCmd` (`openDatasetForFetch`, `emitResult`).
- `cmd/get.go`: register it; `Short` adds recent logs to REPORT-110's wording, keeping its CLUE mention (REPORT-110's R2 requires it).
- `internal/mcpserver/types.go`: `getRecentLogsIn` tagged as `getPagedIn` is: `Dataset string \`json:"dataset"\``, `ClassID int \`json:"class_id"\``, `Start string \`json:"start,omitempty"\``, `End string \`json:"end,omitempty"\`` and `Refresh bool \`json:"refresh,omitempty"\``, with `jsonschema` descriptions for the two time forms. The schema takes property names and `required` from the tags: a field with no `json` name keeps its Go name, and one without `omitempty` is required.
- `internal/mcpserver/tools.go`: `get_recent_logs`, description: "Pull a class's log events from the last day or two into a dataset's recent_logs view, not logs. Each later pull asks only for what is new; pass refresh to re-read the whole window. Earlier days come from a Student Actions run. An event a log run also holds appears in both logs and recent_logs, so count one view or the other, not their union; order rows by received_time."
- `internal/mcpserver/server_test.go`: the tool is listed and a stubbed call returns the result map; its listed description contains `recent_logs view, not logs`, `appears in both logs and recent_logs` and `received_time`, so dropping any of R12's three facts from it fails the test; its input schema's properties are `dataset`, `class_id`, `start`, `end` and `refresh`, and its `required` is exactly `dataset` and `class_id`, which fails if a tag loses its name or `refresh` loses `omitempty`.
- `internal/guidance/src/core.md`: a `recent_logs` view entry after `logs` (R16's content, saying "a recent-logs pull", no command); the `downloads` entry names `class_id`; the "Identity columns" section's guarded list is unchanged, since it is the `run_membership` join key and `recent_logs` has no membership rows there; the `recent_logs` view entry names `id` as its key, and one prose line after that list (a sentence, not a bullet, so `ParseCatalog` ignores it) says `recent_logs` is keyed by `id` alone with no `run_membership` rows. `TestGuidanceDocumentsEveryIdentityColumn` stays green only while no `id` bullet is added. The existing view entries use an em-dash after the name, and the new entries match that list's style.
- `internal/guidance/src/tools.md`: `get_recent_logs` after `get_answers, get_history`.
- `internal/guidance/src/skill_header.md`: `cc-data get recent-logs <class-id> --dataset <ref>` in "Fetching data", with `--refresh`.
- `README.md`: the command in both lists; `recent_logs.v<N>.jsonl` and `members_recent_logs_<class>.v<N>.jsonl` in the dataset layout; `recent_logs` in the "Query everything with SQL" view list; a log event's `id` in the duplicate-free bullet's identities (R16).
- `docs/researcher-guide.md`: a `recent_logs` row in the view table and a "Recent logs" subsection in the fetching chapter.

**Tests**: the existing guards (`TestGuidanceDocumentsEveryStaticView`, `TestGuidanceDocumentsEveryTool`, `TestResearcherGuideDocumentsEveryStaticView`, `TestCoreNamesNoCommand`) are the tests for the docs; a `cmd` test runs `get recent-logs` against the stub server with `--start abc` (usage, exit 2) and `--refresh --end ...` (usage).

**Estimated diff size**: ~200 lines, mostly prose.

---

### Acceptance runs

**Summary**: R21 and R22, after merge. No code; results go in the PR description.

1. **R21**, once REPORT-150 is on staging: a fresh dataset `learn.portal.staging.concord.org/rl-accept`; a staging test class with a CLUE offering; a test student launched from the portal works for a few minutes; `get recent-logs <class>` (rows, CLUE, the learner's endpoint); more work; `get recent-logs <class>` again (`cursor` sent, `updated` = overlap, `new` = newer, `count(*) = count(DISTINCT id)`); `--start`/`--end` an hour apart (rows in the hour, `cursor: none` in the result, and the entry's stored cursor byte-identical to before).
2. **R22 laptop**: a scratch package from `cc-data package init`, its `run.py` reading `scope.json`, making or finding the class's Student ID Mapping run (as the template already does), running `cc-data get clue-documents <run>`, then `clue-history <run>`, then `clue-content <run>` (documents first: REPORT-110's R12 counts history coverage only when the history lands) and `cc-data get recent-logs <class_id>`, and writing counts from REPORT-111's views and `recent_logs` to `display.md`; `cc-data package run` against staging.
3. **R22 pre-release**, on Doug's go once REPORT-110, REPORT-111 and this story have merged: tag `v0.3.0-pre.2` on cc-data-cli `main` and push it; then a researcher-dashboard branch and PR, `REPORT-152-pin-cc-data-pre-2`, that changes only `runner/Dockerfile`'s `ARG CC_DATA_REF=v0.3.0-pre.1` to `v0.3.0-pre.2`, carrying `[REPORT-152]`; its image build is the check that the tag builds. Separate from R20's PR, which merges in either order.
4. **R22 VM**: once the runner image with that pin is on the staging VM, `cc-data package publish` the same zip privately to staging and run it from the dashboard for the same class; compare `display.md`.

## Open Questions

### RESOLVED: Judgment call: the class rides in the run's integer slot inside the store, and in its own field on the download
**Context**: segments, membership keys, sweep and reindex all key on `(type, int)`; downloads key on `(type, run_id, job_id)` and surface `run_id` to researchers.
**Options considered**:
- A) Use the class id as the integer key in segment and membership names, and a separate `ClassID` on the download.
- B) Store the class id in `Download.RunID` too.
- C) A new string key everywhere.

**Decision**: A. The internal names never reach a researcher, and reusing the integer slot keeps the merge, sweep and reindex code unchanged apart from the union. B would put a class id in `run_id` in `downloads`, `dataset show` and the manifest, where every join reads it as a run. C rewrites the store machinery REPORT-110 is generalizing, for no behavior.

### RESOLVED: Judgment call: stream to a file, then decode
**Options considered**: decode straight off the HTTP body; stream to a file with `StreamAPIToFile` and decode the file.
**Decision**: the file. It reuses the one streamed-download path's retry, truncation and local-I/O rules unchanged, and the decode is then local and restartable. The cost is writing the body once to disk, which a few MB to tens of MB of a class's logs makes negligible.

### RESOLVED: Judgment call: the entry is written only once the pull is merged
**Options considered**: an incomplete entry at start, as `FetchPaged` writes; no entry until the end.
**Decision**: the end. An incomplete `recent_logs` entry would have to carry the previous cursor through, raise an INCOMPLETE warning for rows that are all intact, and appear in materialize's incomplete-run caveat, all for a state the next pull clears anyway. Writing once removes the state rather than reporting it.

### RESOLVED: Judgment call: two fields on `store.Kind`, not three
**Context**: The stage 8 draft asked REPORT-110 for `StampColumn`, `Accumulate` and `RunViews`; REPORT-110 (its open question on the fields) leaves them to this story's steps.
**Options considered**:
- A) The three fields as drafted.
- B) `ClassKeyed` (which drives the stamp column, the per-run views, `run_membership` and the download key) and `Accumulate`.

**Decision**: B. The stamp, the views and the download key all follow from one fact, that the kind's integer key is a class, so one field keeps them from disagreeing; three would allow a kind stamped `_class_id` that still gets `run_membership` rows. Both default to false, so no existing kind is edited.

### RESOLVED: Low confidence: `AppendPage`'s stamp column
**Context**: `Segment.AppendPage(records, fetchedAt, runID)` hard-codes `_run_id`, and REPORT-110 keeps that for every kind it adds.
**Options considered**:
- A) Choose the column inside `AppendPage` from the segment's kind (`_class_id` when `ClassKeyed`).
- B) Pass the column name in.

**Decision**: A. The segment already knows its type (`Segment.Type`), so no call site changes signature and no existing kind's stamp changes.

## Self-Review

Roles: commit reviewer, test writer, senior Go engineer, operator, security engineer. Each finding was checked against the code, REPORT-110's and REPORT-111's draft requirements, or the stage 5 prototype before it was written, and each had one defensible correction, made in place.

#### RESOLVED: The merge test's mutation claim was wrong
The plan said deleting the union block makes the second pull report `Removed: 2`. With `e1,e2` then `e2',e3`, the replaced membership is `{e2,e3}`, so only `e1` goes (`Removed: 1`); the `Removed: 2` seen at stage 1 came from a different pair of segments. A test asserting 2 would fail for the wrong reason. Corrected.

#### RESOLVED: Two views named `recent_logs`
REPORT-110's R8 derives the store views from its registry, so registering `recent_logs` as a store type also registers a plain `recent_logs` view; this plan's derived view would be a second statement with the same name. The view step now registers `recent_logs` through REPORT-111's `derivedStoreViews`, as REPORT-111 does for its three views.

#### RESOLVED: The membership column inventory
REPORT-110's R14 puts every registered identity column on `run_membership`. Registering `recent_logs` would add an always-NULL `id` there unless the opt-out covers the inventory too. Added to the view step and to requirement A5.

### Second pass (roles: senior Go engineer, test writer, commit reviewer, operator)

#### RESOLVED: A cut connection would tell the researcher to narrow a report filter
REPORT-150's controller streams with `send_chunked` and re-raises on a failure after the first chunk, which closes the connection mid-body. A throwaway test that hijacked and closed a 200 response after `{"class_id":1,"rows":[{"id":"a"}` got `TRANSIENT` from `StreamAPIToFile` with "download failed after 32 bytes; the server may have exceeded its download budget for this cohort. Re-run to retry, or narrow the report filter." The fetch step now maps that error to its own message, with a test.

#### RESOLVED: A row without its `id` had a different code from REPORT-110's
R6 chose failing the pull so that cc-data has one rule for a record without its identity, but the plan gave it `BAD_RESPONSE` while REPORT-110's implementation spec gives a CLUE item without its identity `MALFORMED_ITEM`. Neither code exists in the tree yet, so aligning costs nothing; `BAD_RESPONSE` stays for the envelope checks.

#### RESOLVED: The merge test missed half of the accumulating merge
The accumulating merge has two parts: the merging key's previous membership in the covered walk, and the union written as the new membership. With only two pulls, deleting the union passes, since the old file still covers `e1` on the second merge; `e1` is lost only on the third. The test now asserts the membership file and adds a third pull.

#### RESOLVED: `get`'s short help would drop REPORT-110's CLUE mention
REPORT-110's command step makes `cmd/get.go`'s `Short` mention CLUE (its R2); this plan replaced the whole string with one that does not. The step now adds recent logs to REPORT-110's wording.

#### RESOLVED: The acceptance run expected the wrong `cursor` value
R10 reports `cursor: none` for a pull with `--end`, but the acceptance step expected `cursor: unchanged` from the `--start`/`--end` pull. It now expects `none` in the result and checks the stored cursor is unchanged, which is what R21 asks.

#### RESOLVED: A comment that counts the scope keys
`internal/packages/scope.go:58-59` says the keys outside the author's four are "the other three", which is false once `clue_source` goes. Added to the cleanup step.

### Third pass (roles: security engineer, performance engineer, education researcher querying `recent_logs`, REPORT-192 implementer, senior engineer reading cold)

The requirements' third pass records the findings that touch both files (`dataset show --json`'s `run_id`, the identity-columns guard, and the 400 on a stored cursor). These touch only the plan.

#### RESOLVED: The cleanup steps do share a file with the recent-logs steps
The plan said the cleanup steps touch none of the files the recent-logs steps do, but both edit `docs/researcher-guide.md` (the CLUE bullet at lines 650 and 651, and the view table and fetching chapter). The opening paragraph now names the shared file and the separate sections.

#### RESOLVED: The `Init` test was named by guess
The cleanup step said the no-`clue_prepull` assertion goes "in `files_test.go` or `cmd/package_test.go`, wherever `Init`'s output is asserted". Only `cmd/package_test.go`'s `TestPackageInitRefusesASecondInit` reads the written `manifest.json`; the step now names it and the mutation it catches.

### Fourth pass (roles: implementer building each step in order, release manager for cc-data-cli 0.3.0, package author on the VM (REPORT-189), internal-consistency reviewer)

The requirements' fourth pass records the findings that touch both files (R23's missing tests, R16's README passages, and the pre-release R22 needs). These touch only the plan.

#### RESOLVED: Three names the plan uses could not compile as written
The view and fetch steps called `store.TypeRecentLogs`, which no step declared; the decoder passed `recentLogsKind`, unexported in `store`, from `internal/fetch`; and `derivedStoreViews` is a `map[string]func(viewSet) viewStmt` (REPORT-111's spec), which a bare `recentLogsView` does not fit. The store step now declares the constant, the decoder takes the kind through `store.KindOf`, as REPORT-111's `cluePins` does, and the map entry is the method expression `viewSet.recentLogsView`.

#### RESOLVED: The MCP input struct would publish the wrong schema
A throwaway `jsonschema.For` (google/jsonschema-go v0.4.3, the SDK's) over the struct as the plan wrote it gave properties `Dataset`, `class_id`, `Start`, `End`, `Refresh` and `required: [Dataset, class_id, Refresh]`: a field with no `json` name keeps its Go name, and `refresh` without `omitempty` becomes required, against R12. The struct is now tagged as `getPagedIn` is, and the server test asserts the properties and `required`.

#### RESOLVED: `nightly_through` is a date, not epoch milliseconds
REPORT-150's controller renders it with `Date.to_iso8601` (its R11: "`nightly_through` an ISO date"), while `RecentLogsPull` listed it untyped among `int64` neighbors. A throwaway decode of `"nightly_through":"2026-10-20"` into an `int64` failed ("cannot unmarshal string into Go struct field"), which would fail every pull after its body had streamed. The entry and envelope now state their types. Step 7 also sets `FetchedAt`, which `dataset show` prints as the FETCHED date and step 7 had left at the zero time.

#### RESOLVED: The materialize caveat change was unreachable
The store step had `incompleteRuns` and the kind's caveat skip a `ClassKeyed` kind, but `incompleteRuns` (`internal/duck/materialize.go:543-546`) skips every complete download before it looks at the type, and a `recent_logs` entry is written only complete (step 7), rebuilt complete by reindex, and never flipped by `recordSweptDownloads`, which only completes entries. No test could reach the branch, so it is gone, with a line saying why no change is needed.
