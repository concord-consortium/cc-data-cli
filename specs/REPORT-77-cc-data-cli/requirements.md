# cc-data CLI: researcher data download + query tool

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-77
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Design doc**: `/home/doug/tmp/cc-data-app.md` (REPORT-71 deliverable, revised 2026-07-16; the revised local copy is authoritative over the published gist)
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

A Go CLI (`cc-data`) that lets Concord Consortium researchers download report CSVs, student answers, interactive state history, and file attachments into local named datasets and query across all of it with SQL via an embedded DuckDB, with first-class Claude integration (skill + MCP server). This spec covers the full story: auth through release pipeline.

## Project Owner Overview

Today a researcher who wants to analyze student data logs into the report server, runs a report, downloads a CSV, opens it somewhere, and joins it against answer data by hand; raw answer and interactive-history data is not practically reachable at all. `cc-data` collapses that into a local workflow the researcher (or Claude, on their behalf) drives from the terminal: authenticate once per portal, pull runs' data into a dataset, and ask questions in SQL or plain English. Datasets are guaranteed duplicate-free (every downloaded item overwrites the prior copy of the same learner+question record), so combining classes, activities, and repeated pulls is safe by construction.

The CLI is the client half of work already largely landed on the report server (per-user API tokens, the v1 JSON API, audit logging). It classifies its local datasets as sensitive student data and ships with the retention guidance, file permissions, and Claude-access boundaries that classification requires. The story covers MVP through the release pipeline (goreleaser, per-platform builds, Homebrew).

## Background

REPORT-71 produced the full design (architecture, auth model, storage model, API contract); this story is STORY 4 of its breakdown, the researcher-facing Go CLI in its own repository (`concord-consortium/cc-data-cli`, binary `cc-data`). The client/server seam is the versioned HTTP contract (`/api/v1/...`); no code is shared with the report server.

Server-side state (verified against the report-service repo, 2026-07-16): the `ccd_`-prefixed hashed API-token model, the PKCE loopback flow (`/auth/cli`, `/auth/cli/resume`, `POST /auth/cli/token`), token-management UIs, the `data_access_log` audit table, and all v1 endpoints (`GET /reports`, `/:id`, `/:id/download`, `/:id/answers`, `/:id/history`, `POST /:id/attachments`, `GET /:id/jobs`, `/:id/jobs/:job_id/download`) exist. Where this spec and the repo disagree, the repo's routes and shapes are authoritative. Four server tasks are still owed by this story: the logout revoke endpoint and the token-introspection endpoint (see Requirements: Auth), the `total_endpoints` coverage field on the answers/history envelope (see Requirements: Fetch answers + history), and the `report_type` field on run metadata (see Requirements: Fetch report CSVs); all are specced in the report-service repo.

The storage design was revised on 2026-07-16 to the **never-duplicate model**: answer/history records live in identity-keyed stores (exactly one current record per identity, newest fetch wins) with per-fetch membership files, replacing the earlier per-run data files. Report CSVs remain per-run artifacts (no per-row identity exists). This repo currently contains only the README and LICENSE; this spec starts the implementation.

## Requirements

### Auth + credentials (subtask a)

- `cc-data login --portal <portal>` performs the loopback flow: start a listener on `127.0.0.1:<random port>`, open the browser to the server's `/auth/cli` with `portal` (validated when present, https origin of a known portal; the controller falls back to its configured default portal when absent, so the CLI always sends it to select the portal), `redirect_uri` (exactly `http://127.0.0.1:<port>/callback`; the controller rejects any other shape), a `state` nonce, the S256 PKCE `code_challenge`, and `code_challenge_method=S256`, receive the one-time code, exchange it (code + verifier, plus an optional `label` such as `CLI login (<hostname>)` so tokens are distinguishable in the token UI; additive server dependency, older servers ignore it and label the token "CLI login") at `POST /auth/cli/token`, and store the token, recording a local stored-at timestamp for `auth status`.
- `cc-data login --portal <portal> --token <paste>` is the manual fallback (headless/SSH); both paths store the same kind of token.
- Tokens are stored per portal: OS keychain first (macOS Keychain / Linux Secret Service / Windows Credential Manager via `zalando/go-keyring`), falling back to `~/.config/cc-data/credentials.json` at `0600` (Windows: user-profile ACLs).
- `cc-data logout` revokes the current token server-side, then removes it locally. Server dependency owed by this story, decided in the report-service spec: `DELETE /api/v1/tokens/current` revokes the calling bearer token; `Accounts.revoke_api_token/2` exists, but `ReportServerWeb.Api.AuthPlug` must also assign the resolved `api_token`. `logout` is exempt from the generic 401 rule below: a 401 from the revoke endpoint means the bearer is already invalid server-side (e.g. an admin revoked it via the token UI), so logout warns that nothing needed revoking, still removes the local credential, and exits 0 — it never tells the user to log in.
- `cc-data auth status` lists portals with stored credentials (portal, storage backend, local stored-at timestamp) and the configured `default_portal`, without touching the network. `--check` calls `GET /api/v1/tokens/current` per portal (token-introspection server dependency owed by this story, specced with the revoke endpoint) to validate each token and display its server-side metadata: label (nullable: a UI-minted unlabeled token pasted via `--token` has none; render as "(no label)", and `--json` carries the raw `null`), `created_at`, `last_used_at`, and `report_access`, so "token valid but account lacks report access" is distinguishable from "token invalid". Against an older server without the endpoint (contract 404), `--check` falls back to `GET /api/v1/reports?limit=1` for validity and marks the metadata unknown. `auth status` (with or without `--check`) is a report, not an assertion: it exits 0 whenever the command itself completes, with per-portal validity carried in the output (`--json` included), and never exits 3 just because a checked token turned out invalid.
- `cc-data version` prints the binary version.
- Every non-login command sends `Authorization: Bearer <token>`; on 401 it emits the single-line JSON error `{"error":"NOT_AUTHENTICATED","action":"A human must run: cc-data login"}` and exits non-zero (exception: `logout` treats 401 as nothing-to-revoke success; see the logout bullet). The CLI never drives the browser flow itself outside `login`.
- Config file `~/.config/cc-data/config.json` (`0600`) holds `default_portal` and future settings, separate from `credentials.json`.
- Retry policy: transient failures (network errors, 429, 5xx) retry with bounded exponential backoff plus jitter; contract errors (`BAD_REQUEST`, `NOT_FOUND`, `NOT_READY`, `EXPIRED_CURSOR`, and the reserved `NOT_APPLICABLE`) are never blind-retried. Forward compatibility: any unrecognized error code on a 4xx response is treated as a contract error, never blind-retried. The server's landed vocabulary is exactly `BAD_REQUEST`, `NOT_AUTHENTICATED`, `NOT_FOUND`, `NOT_READY`, `EXPIRED_CURSOR`, `SERVER_ERROR` (ownership failures render `NOT_FOUND` by design; there is no `FORBIDDEN`). Presigned S3 GETs (CSV download envelopes, attachment URLs) are outside this contract entirely: a failed or expired S3 GET is handled by re-requesting a fresh envelope/URL from the API within the same bounded retry budget, never by classifying the S3 response body (XML, not the JSON error shape) against the error vocabulary.

### Listing (subtask a)

- `cc-data reports list --portal <portal>` lists the user's report runs via `GET /api/v1/reports`, draining the keyset-paginated envelope; shows run id, slug, state, and filter labels. A programmatically created run (API/console) stores a `NULL` report filter, but the server's `ReportJSON` view normalizes `nil` to the empty filter object (verified `report_json.ex`: `report_filter_json(nil)` → `report_filter_json(%ReportFilter{})`), so the CLI always receives a full `report_filter` object (never JSON null) and such a run simply renders with no filter labels; the CLI never has to special-case a missing filter.
- `cc-data reports jobs <run-id> --portal <portal>` lists a run's post-processing job outputs via `GET /api/v1/reports/:id/jobs`.

### Fetch: report CSVs (subtasks a + jobs)

- `cc-data get report <run-id> --dataset <ref>` checks `athena_query_state` first: succeeded → request the download envelope via `GET /:id/download` (200 JSON `{download_url, filename, expires_in_seconds}`: a 600 s presigned S3 URL, not bytes) and promptly stream the URL to disk; running/queued → poll with backoff up to an overall polling budget (default 30 min, `--poll-timeout` override); a terminal failure state → exit non-zero, write nothing. Terminal states are server-verified: `failed`/`cancelled` for runs, and `failed` for post-processing jobs (the server renders a failed job as `NOT_READY`, so the CLI must treat `status: failed` as terminal, not poll it). Budget expiry exits with the not-ready code and the last observed state; the CLI also stops early if it detects the server's `null`→`queued`→`null` self-start-failure oscillation (which otherwise re-submits an Athena query every poll). Never writes a partial CSV (`.tmp` → fsync → atomic rename to `report_<run>.csv`).
- `--no-wait`: an unfinished run produces the standard structured result line (current `athena_query_state`, `complete: false`, no file written) and exits with the not-ready exit code; a succeeded run downloads immediately, identical to the no-flag case. Acceptance: `--no-wait` on a queued run exits non-zero with a machine-parseable state.
- `cc-data get report <run-id> --job <job-id> --dataset <ref>` downloads one post-processing output to `report_<run>_job_<id>.csv` with the same discipline.
- Re-pull guard (CSV only): `get report` warns and requires `--refresh` when the CSV already exists.
- Run metadata carries `report_type` (`answers` | `usage` | `log`; server-owned vocabulary; server dependency owed by this story). It is not a stored DB column but a value the API's `ReportJSON` view computes from the report slug at render time (verified `report_json.ex`: `Tree.find_report(slug).api_report_type`); "owed by this story" therefore means an already-deployed server build predating this render code omits the field, not that a per-run value is persisted. The CLI records it in the manifest's download entry; against such an older server without the field it derives the value from the known report slugs (the identical slug-to-type mapping the server itself uses, so the CLI-derived value can never disagree with a present server value for a known slug): `student-answers` → `answers`, `student-assignment-usage` → `usage`, the three action/log reports → `log`. An unknown type value or unrecognized slug is recorded verbatim and quarantined at query time: the download succeeds, the run is excluded from the `reports`/`report_prompts` union views (its per-run view stays available, unfiltered), and the fetch and `dataset show` warn that the type is unknown to this cc-data version and suggest upgrading. A new server report type can therefore never silently corrupt aggregates.

### Datasets, stores, and the never-duplicate model (subtask b)

- Dataset CRUD: `create` (auto-generates a `{date}_{slug}` name if omitted: slug from `--description` when provided, else a `{date}_{n}` counter, since no run has been fetched yet at create time) / `list` / `show` / `rename` / `edit --description` / `delete` (local only) / `purge` / `reindex`. `delete` removes the entire dataset folder; `purge` deletes all downloaded artifacts (stores, segments, membership files, CSVs, attachments) and clears the manifest's download entries and attachment index, but keeps the dataset folder, name, description, and manifest identity, so the dataset remains listed and re-fetchable. Acceptance: after `purge`, `dataset show` reports zero holdings with no warnings and the dataset still appears in `dataset list`. Datasets live under `<data_root>/<portal-hostname>/datasets/<name>/`; `data_root` comes from `config.json`, overridden by the `CC_DATA_ROOT` env var, defaulting to `~/cc-data`. Manifest file paths are relative to the dataset folder, so datasets and the root are freely movable.
- Every `get`/`query`/`repl` requires a dataset ref `<portal>/<name>`; a bare `<name>` resolves under `default_portal` (error if unset); commands echo the resolved ref. No persisted active dataset.
- **Never-duplicate invariant: at rest, a dataset contains at most one record per identity.** Identities: answer = `(source_key, remote_endpoint, question_id)`; history snapshot = `(source_key, remote_endpoint, question_id, history_id)`; attachment bytes = `(source, publicPath)`; attachment refs derive from current records. Every downloaded item overwrites the existing record with its identity (incoming always wins). Report CSVs are the documented exception (no per-row identity; per-run, no dedup). Acceptance: fetch run A, fetch an overlapping run B, re-fetch A with `--refresh`; the answers store then satisfies `count(*) = count(DISTINCT (source_key, remote_endpoint, question_id))` (history: the 4-part identity), each result line reports honest `{fetched, new, updated, removed}`, and a permission-shrunk `--refresh` reports the dropped identities in `removed`.
- Answer/history records live in versioned identity-keyed stores (`answers.v<N>.jsonl`, `history.v<N>.jsonl`); versioned per-fetch membership files (`members_<type>_<run>.v<N>.jsonl`, current version named by the manifest) record which identities each run's fetch covered. The firebase project is not tracked anywhere (the portal folder determines it: production portal → `report-service-pro`, all others → `report-service-dev`).
- Write path is segment-and-merge: pages append to a per-download segment (rows stamped `_fetched_at` + `_run_id`) with the cursor persisted under the per-download file lock (`gofrs/flock`; all locks are taken on dedicated lock files that are never renamed or unlinked); on the final page the CLI merge-compacts store + segment into a new store version (exactly one record per identity; segment beats store; ties break on `(_fetched_at, _run_id)`; identities covered by no membership are dropped), written as `.tmp`, fsynced, and atomically renamed to its final name (so existence under a final store-version name always means complete), then writes the run's membership file the same way (membership is versioned like the store) and atomically repoints the manifest (store and membership together) and records merge counts `{fetched, new, updated, removed}`. Old store and membership versions and segments are cleaned up best-effort.
- Crash safety: before the merge, segment + cursor resume; during the merge, the old store is untouched and a half-written version is discarded; after the pointer swap, cleanup is idempotent. Queries only ever read the manifest's current store and membership versions plus `complete` CSVs.
- Merge serialization: the merge critical section acquires the per-dataset lock, re-reads the current store pointer inside it, merges store + segment into the next version, repoints the manifest, and releases; a merge that finds a newer store than it prepared against rebases onto it. Page appends and cursor writes remain concurrent (segments and cursors are per-download, guarded by the per-download lock); only manifest writes and the merge hold the per-dataset lock. Each download additionally holds an exclusive non-blocking per-download lock for the command's lifetime, so two commands fetching the same run+type fail fast ("download busy") instead of racing the segment; `--refresh` and the `EXPIRED_CURSOR` restart delete the segment under that lock. Every `get` also holds a shared whole-fetch activity lock for its lifetime, and mutating dataset commands (`rename`, `edit`, `delete`, `purge`, `reindex`) require that lock exclusively, non-blocking, so a mutation can never run against a live fetch. Acceptance: two concurrent fetches of different runs into one dataset lose no records from either; a second `get` of the same run+type while one is running exits non-zero with the busy error and loses nothing; a `purge` issued while a fetch of the same dataset is between pages exits non-zero with the busy error and the fetch completes unharmed.
- `manifest.json` carries an integer schema `version`, is migrated forward on read, and refuses (with an "upgrade cc-data" message) versions newer than the binary understands. It indexes downloads with provenance: filters (raw + resolved labels), `source_key`, coverage, merge counts, `history_mode`, `complete` (false from fetch start until the merge lands; resume state itself lives in the per-download cursor file), `fetched_at`.
- `dataset reindex` rebuilds the manifest: adopts the newest store version present per type and the newest membership version per `(type, run)` (final names only; `.tmp` files are always discarded; the rename discipline guarantees any file under a final name is complete), rebuilds download entries from membership files + CSVs, rebuilds the attachment index by rescanning the stores, and garbage-collects unreferenced attachment files (see Attachments). A CSV's `report_type` is recovered from shape only partially (no `student_id` column → `log`; `student_id` + pseudo-header rows → `answers`; otherwise a distinguished `recovered` value that is included in `reports` unfiltered but flagged as recovered-without-provenance, so reindex never spuriously quarantines healthy data; a re-fetch restores the exact type).
- To compare pulls over time, users create a dataset per pull; within one dataset a re-fetch always means "replace with current" (a `--refresh` replaces the run's membership wholesale and reports `removed` counts).

### Summaries + machine-readable output (subtask b)

- `dataset show <ref>` renders per-type totals, a per-download table (run id, type, slug, resolved filter labels, fetched_at, merge counts, status), and warnings (manifest/filesystem drift, orphaned files, incomplete downloads). Coverage always renders as a split ("2,895 queried, 2,610 with data"), never a bare count.
- `dataset list` renders one summary row per dataset across all portal folders (name, description, age, counts by type, size). Neutral info, no nag.
- Both take `--json` (stable schema; summary by default, `--full` for per-download/per-file detail). All numbers come from the manifest; summaries never scan data files or start DuckDB.
- Every `get` ends with a single machine-readable JSON result line (type, run id, files, merge counts, `complete`, coverage, sources scanned where applicable).
- Stream discipline: for the machine-output commands (`get`, `query`) stdout carries only machine-consumable output (the result line, `--json` documents, `query` results in the chosen format, and on failure the single-line JSON error envelope, which is the only stdout output of a failed command); all progress, polling updates, warnings, and human prose go to stderr. Carve-out for the pure listing/summary commands (`reports list`, `reports jobs`, `dataset list`, `dataset show`, `auth status`, `version`): the human-readable table/report *is* the command's product, so it goes to stdout, and `--json` replaces it there with the machine form; their warnings and progress still go to stderr, and a failure still emits the single JSON error envelope as the only stdout output. Acceptance: `cc-data get answers ... 2>/dev/null | jq .` parses; `cc-data query --format csv ... 2>/dev/null` is clean CSV; `cc-data dataset list --json 2>/dev/null | jq .` parses; a failing `get` with stderr discarded still yields exactly one parseable JSON object on stdout carrying `error` (and `action` where defined).
- Stable exit codes, documented in `--help` and the skill: 0 success (including nothing-new resumes); 1 unexpected/internal/other error, including lock-busy conditions ("dataset is busy", "download busy"); 2 usage error; 3 NOT_AUTHENTICATED; 4 not ready (`--no-wait` on an unfinished run, or the polling budget elapsing on a still-unready run); 5 server contract error (`BAD_REQUEST`, `NOT_FOUND`, a terminal `NOT_READY` failure state — a `failed`/`cancelled` run or a `failed` job — the reserved `NOT_APPLICABLE`, any unrecognized 4xx code, or `EXPIRED_CURSOR` whose automatic restart also failed); 6 transient failure after the retry budget. The JSON error line carries the specific `error` code; the exit code is the coarse class.

### Fetch: answers + history (subtask d)

- `cc-data get answers|history <run-id> --dataset <ref>` consumes the paged JSON envelope (`{items, next_page_token}`) from `GET /:id/answers|history`, appending per page and persisting the cursor; resume continues from the saved cursor; `EXPIRED_CURSOR` discards the segment and restarts from a null cursor. Acceptance: a resume against an expired scratch restarts cleanly and ends `complete: true`.
- History always fetches the full snapshot series. The `--latest-only`/`--latest-N` flags from earlier design drafts are dropped (latest-only is redundant with `get answers`; last-N slicing is one QUALIFY line over the `history` view). The manifest records `history_mode` as the constant `full` so a future mode is an additive change.
- Coverage per download records `{queried, with_data, empty}` for answers and endpoints-with-history vs without for history. The `queried` denominator comes from a `total_endpoints` field on the paged envelope (server dependency: a separate report-service spec adds it; the scratch already holds the endpoint set on every page). `with_data` is computed client-side as distinct `remote_endpoint`s stored; `empty = queried - with_data`. When the field is absent (older server), the CLI records `with_data` only and marks `queried`/`empty` unknown.

### Fetch: attachments (subtask d2)

- `cc-data get attachments <run-id> --dataset <ref>` scans the run's records in the stores (selected via membership) for attachment refs: each record's `attachments` map (keyed by attachment name, values carrying `publicPath` and optional `contentType`), where `audioFile` is a conventional attachment name and `__attachment__` markers inside the decoded interactive state reference map entries by name. It dedups by `(source, publicPath)`, batch-presigns via `POST /:id/attachments` with `(collection, source, doc_id, name)` items in ~100-file chunks (endpoint cap 500; 600 s URLs must not expire mid-chunk), and streams each URL to `attachments/<id12>_<safename>` (`id12` = first 12 hex of `sha256("<source>|<publicPath>")`; bare names are not unique; `<safename>` is the attachment name run through a deterministic cross-platform filename sanitizer, since names are client-written and can contain path- or Windows-reserved characters) with `.tmp` → fsync → rename.
- Resume is existence-driven (skip files already present; re-request URLs only for missing files); `--refresh` re-downloads all; presigned URLs are never persisted. Per-item `not_authorized`/`not_found` results land in `coverage.missing`, never fatal.
- Prerequisite: at least one of the run's answers/history fetches must exist (the refs live only inside those records); the scan covers whatever membership exists and errors only when neither does. The result line and manifest entry name the sources scanned (`scanned: ["answers"]`), and the human output notes an unfetched source ("history not fetched; history-referenced attachments not included") so partial scans are never mistaken for complete coverage.
- Selectors narrow the scan: `--answer <id>`, `--history <id>`, `--question <id>`, `--name <n>`; a bare `--name` matching multiple refs downloads all matches. `attachment` is a command alias for `attachments`. A targeted download prints the resulting local path(s).
- `--url` prints presigned URL(s) instead of downloading (single → bare URL on stdout; multiple → JSONL with `expires_at`); writes nothing to the dataset; opt-in only. `--inline` requests the browser-renderable disposition flavor.
- Attachment garbage collection runs during `get attachments` and `reindex`: files no current record references are deleted, except files still referenced by retained history snapshots.

### DuckDB query layer (subtask c)

- `cc-data query --dataset <ref> "SELECT ..." [--format table|csv|json|jsonl]` and `cc-data repl --dataset <ref>`; ephemeral in-memory DuckDB per invocation (no persistent `.duckdb` file), views registered fresh from the manifest's explicit file list (never a glob).
- Views: `reports` (`read_csv` `union_by_name` over per-run CSVs with a manifest-attached `run_id`; job CSVs excluded, available as per-download views; answers-type CSVs embed two pseudo-header data rows, `student_id` in `('Prompt', 'Correct answer')`, which `reports` and the per-run report views filter out only for downloads recorded as `report_type: answers`, comparing `student_id` cast to VARCHAR, and the manifest's CSV `row_count` excludes (answers-type downloads only). The filter must not be unconditional: usage and log-type CSVs have no such rows, two log-based reports (`student-actions`, `teacher-actions`) have no `student_id` column at all so the filter would be a binder error, and a usage CSV's numeric `student_id` would make the uncast comparison a conversion error. Runs whose `report_type` is outside the CLI's allowlist are excluded from `reports` and `report_prompts` with a warning (per-run views stay available); the filter removes the rows but not their effect on column types: the CLI detects each CSV's column types by a full-file pass at download and records them in the manifest, and the views read with `auto_detect=false` and that recorded column map (never DuckDB's bounded sniff sample and never a dependency on the server's row ordering), so `res_<N>_<question_id>_answer` columns are VARCHAR (Prompt-row text) while columns those rows leave empty, such as scores, keep numeric types; the views expose the recorded types unchanged (no hidden per-column casts) and numeric aggregation over `_answer` columns uses `TRY_CAST`, taught by the skill); `report_prompts` (answers-type runs only: those two rows per run, exposed as metadata: the prompt and correct-answer text keyed by the `res_<N>_<question_id>_*` columns); `answers` and `history` (`read_json` over the current store versions with an explicit column map derived from every record at merge time, never sampling-based inference; no dedup needed); `run_membership` (one row per membership-file line, with manifest-attached `run_id` and `type` columns; the run-scoping surface); `attachment_files` (per-file metadata + local paths from the manifest index; refreshed at `get attachments`/`reindex`); `attachment_states` (`read_text` over downloaded offloaded CODAP/SageModeler `.json` state, exposed as `filename`, `id12`, `name`, raw `content`, and `state = TRY_CAST(content AS JSON)` so one malformed file degrades to a single NULL-`state` row); `downloads` (manifest dimension table); per-download views (`report_584`, `answers_584` as store joined to membership).
- Run-scoped queries join `run_membership` **filtered by `type`** (`JOIN run_membership m USING (source_key, remote_endpoint, question_id) WHERE m.run_id = 584 AND m.type = 'answers'`): membership mixes answers rows (3-part identity) and history rows (one per snapshot), so an unqualified answers join multiplies rows when both types are fetched. History joins add `history_id` to the join key (answers rows NULL-fill it) but use the same type filter for consistency. The stores' `_run_id` column only records the last writer, never scopes.
- **Multi-dataset queries**: `--dataset` is repeatable on `query`/`repl` (and the MCP query tool takes a `datasets` list). Each dataset's views register under their own DuckDB schema (`wildfire_2026.answers`), with dataset names sanitized to SQL identifiers and an alias form (`--dataset pre=<ref>`) required on collision. A single `--dataset` keeps today's unqualified view names. **No implicit cross-dataset union views**: point-in-time pulls contain the same identities by design, so cross-dataset unions/joins are written explicitly by the researcher (the skill teaches the schema-qualified pattern for longitudinal comparisons).
- **DuckDB sandbox**: every `query`/`repl`/MCP session, after registering views, sets `allowed_directories` to the named dataset folder(s), `enable_external_access = false`, disables community extensions and extension autoinstall/autoload, and finishes with `lock_configuration = true`. User SQL is confined to the named datasets (views keep working because their folders are allowlisted; views re-bind at query time, so the allowlist governs every read user SQL performs). Acceptance: `read_csv` of a path outside the named datasets fails from `query`.
- **`--allow-dir <path>`** (repeatable, `query`/`repl` only): appends to the allowlist for that invocation only, never persisted, so joining external files (rosters, rubrics) is an explicit per-call grant visible in the command line.

### Claude integration (subtask e)

- `cc-data init` installs the Claude Code skill and a one-line `~/.claude/CLAUDE.md` pointer, then prompts for login.
- Lifecycle without installer hooks (Homebrew runs none on upgrade and never touches `~/.claude/`): the skill file is stamped with the binary version that wrote it, and every `cc-data` invocation cheaply compares and rewrites the skill + pointer when the binary is newer, covering upgrades from any install method.
- `cc-data uninstall` removes the skill and the CLAUDE.md pointer, optionally (with prompt or flag) config and credentials, and always prints where the user's datasets remain so durable student data is never silently orphaned. When credential removal is requested, each portal's stored token is first revoked server-side (the `logout` path); if a revoke fails, local deletion proceeds with a warning that the token may still be active plus the token-management UI URL. The README documents that `brew uninstall` removes only the binary and recommends running `cc-data uninstall` first.
- The skill delegates detail to `cc-data <cmd> --help` (help is the source of truth), teaches the views (including `run_membership` and the type-qualified membership-join pattern, the reports-to-stores join (`reports.res_<N>_remote_endpoint = answers.remote_endpoint`, with `res_<N>_<question_id>_*` column naming pairing with the stores' `question_id`), the per-run positional semantics of `res_<N>` (the resource's index in that run's list: across runs the same prefix can be different activities and one activity can shift position, so `res_N` reads are run-scoped and cross-run question-level analysis belongs on the stores), the `TRY_CAST` pattern for numeric aggregation over answers-CSV `_answer` columns (VARCHAR; see the views bullet), the `attachment_states` JSON-extraction pattern (`state->>'$.path'`, `unnest(from_json(...))` for arrays, and `state IS NULL AND content IS NOT NULL` to spot a file that failed to parse), and the schema-qualified multi-dataset pattern for longitudinal comparisons), instructs `dataset show <ref> --json` as the orientation step (not reading `manifest.json` directly), documents the `NOT_AUTHENTICATED` contract (tell the user to run `cc-data login`; never drive the browser), and prefers download-to-dataset over `--url`.
- `cc-data mcp` runs a stdio MCP server for Claude Desktop reading the same stored credentials and returning the same JSON payloads as `--json`. Tool surface, pinned to the data-and-analysis commands: `auth_status`, `version`, `reports_list`, `reports_jobs`, `get_report`, `get_answers`, `get_history`, `get_attachments`, the `dataset_*` tools, and `query`, with `readOnlyHint`/`destructiveHint` annotations; `dataset_delete` and `dataset_purge` require an explicit `confirm: true` argument enforced server-side (the MCP mirror of `--force`). Not exposed: `login`/`logout` (credential management stays a terminal act), `repl` (interactive; the `query` tool covers the capability over MCP), `mcp` itself, and `init`/`uninstall` (host-machine installer acts; `uninstall` can delete credentials via the logout path). The `query` tool does not accept `--allow-dir`; the DuckDB allowlist extends over MCP only via `cc-data mcp --allow-dir` launch arguments in the user-controlled client config.

### Sensitive-data documentation (subtask f)

- README, skill, and `--help` classify datasets as sensitive student data; document the `0600` fallback (and Windows ACL reality), retention/`purge` guidance ("purge when no longer needed; don't archive to shared drives"), what Claude may auto-read (the `dataset show --json` summary / manifest, not raw JSONL by default), the presigned-URL caveat for `--url`, and the dataset-folder trust boundary (anyone who can write into a dataset folder can plant files the researcher's queries then read as trusted, so a dataset folder inherits the trust of whoever can write to it; on the bundled DuckDB the sandbox does resolve symlinks, so this is about writable files, not a symlink-escape).

### Release pipeline (subtask g)

- goreleaser + GitHub Actions building on native runners per platform (cgo/DuckDB prevents trivial cross-compilation; `zig cc` is the fallback approach), macOS signing/notarization, a Homebrew formula, and an install README.
- The formula lives in a new general org tap, `concord-consortium/homebrew-tap` (created during (g); no org tap exists today), published by goreleaser on tagged releases with a repo-scoped push token as a CI secret. Install: `brew install concord-consortium/tap/cc-data`. Internal dogfood builds use plain `go build`. The STORY 2 gate on public release (token-management UI) is already satisfied: REPORT-75 is Done.
- Definition of done for the story: a tagged build produces the release artifacts (signed/notarized macOS + Linux amd64) and the Homebrew formula installs them. Public announcement/distribution is a rollout decision outside this story.
- Release targets: macOS arm64 + amd64 (signed/notarized) and Linux amd64. CI also builds and tests Windows amd64 and Linux arm64 on every push without releasing them (go-duckdb ships prebuilt static libs for all five targets), so they stay green and can be promoted to the release matrix by config alone.

## Technical Notes

- **API contract (v1)**: bearer auth; paged envelope `{items, next_page_token}`; single JSON error shape `{error, message, ...}`; path-versioned. Same-cursor retries are safe; the CLI merges by identity so at-least-once delivery cannot corrupt a store. The endpoint set for answers/history is derived server-side once at export start and snapshotted in the export scratch — later pages serve from the frozen snapshot (the per-page live checks are only token revocation and locally-stored role flags; the attachments endpoint, by contrast, re-derives the set on every request); the CLI never supplies endpoints. Both CSV download endpoints (run + job) return a presigned-URL envelope (`{download_url, filename, expires_in_seconds}`, 600 s), and the audit row is written at envelope issuance, so the CLI consumes the URL immediately after requesting it; presigned URLs of any kind (download envelopes, attachment URLs) are never persisted.
- **Server surface already landed** (report-service repo): token model + PKCE grants (`lib/report_server/accounts.ex`, `auth_cli_controller.ex`), `Api.AuthPlug`, all eight v1 routes, `AuditLog`. The API currently lists only Athena-slug runs.
- **Server dependencies owed elsewhere**, specced in the report-service repo's `specs/REPORT-77-cli-server-support.md` (closed spec, amended for item 4; branch `REPORT-77-cli-server-support`): (1) the logout revoke endpoint, `DELETE /api/v1/tokens/current`; (2) `total_endpoints` on the answers/history paged envelope for coverage; (3) the token-introspection endpoint, `GET /api/v1/tokens/current` (`label`, `created_at`, `last_used_at`, `report_access`), backing `auth status --check`, plus an optional additive `label` on the token exchange so CLI-minted tokens are distinguishable in the token UI; (4) `report_type` on run metadata (`answers` | `usage` | `log`), driving the CLI's pseudo-header filter and report-shape allowlist. The CLI degrades gracefully when any is missing (logout falls back to local-delete with a warning; coverage records `with_data` only; `--check` falls back to `GET /reports?limit=1` and marks token metadata unknown; `report_type` derives from the known report slugs, with unknown slugs quarantined at query time). The documented live PKCE check re-runs as each owed dependency lands, so primary paths are verified against a real server, not only against fakes.
- **Identity + storage naming**: stores `answers.v<N>.jsonl` / `history.v<N>.jsonl`; membership `members_<type>_<run>.v<N>.jsonl`; CSVs `report_<run>[_job_<id>].csv`; attachments `attachments/<id12>_<safename>`. Filenames encode the run, type, and version a `reindex` needs; a CSV's `report_type` is the one thing they do not encode, so `reindex` recovers it from CSV shape only partially (see the reindex behavior in Datasets).
- **Key libraries**: `spf13/cobra` (commands; the `attachment` alias assumes it), `zalando/go-keyring`, `gofrs/flock`, `duckdb/duckdb-go` (cgo; formerly `marcboeker/go-duckdb`, deprecated upstream).
- **Go module**: `github.com/concord-consortium/cc-data-cli`; single binary `cc-data`.
- **Double-encoded state**: `report_state` is a JSON string whose `interactiveState` is itself a JSON string; the CLI must double-decode before storage/query. Offloaded states (`__attachment__`) are only queryable via `attachment_states` after `get attachments`.
- **External dependency for the release pipeline (g)**: an Apple **Developer ID Application** certificate + App Store Connect API key (for notarytool) provisioned as GitHub Actions secrets. Known state: Concord has an Apple developer account (mobile apps), so no enrollment lead time; the Developer ID cert type and ASC API key need confirming/creating (Admin/Account Holder access required). Confirm before (g) starts. The pipeline includes an unsigned dev-build mode as a fallback, but the definition of done requires the signed path.
- **Generation-readiness** (server-side follow-ons, not this story): `NOT_APPLICABLE` on `get report` for CSV-less run types, widening the Athena-slug gate. The `report_type` half of the original note moved into this story's owed dependencies (see above); its vocabulary is designed to grow, and the CLI's allowlist quarantines values it does not know, so a future run type is an additive change on both sides.

## Out of Scope

- Creating or re-running report runs from the CLI (author reports in the web UI; generation is a planned follow-on gated on server work).
- All server-side implementation except the owed dependencies named in Technical Notes (logout revoke, token introspection + exchange label, `total_endpoints`).
- Encryption-at-rest, hard TTLs, or time-based retention enforcement for datasets.
- Shared run visibility (`--all-permitted`); the CLI downloads reports you authored.
- Anonymous/offline (`run_key`) runs; they have no `remote_endpoint` and are not part of portal reports.
- Point-in-time snapshots within a dataset (use a dataset per pull).
- Token-management UI (STORY 2) and the answers/history/attachments server endpoints (STORY 3).
- `dataset show --stats` canned analytics (possible later addition).

## Open Questions

### RESOLVED: Where do the coverage counts for answers/history come from on the wire?
**Context**: The manifest records coverage `{queried, with_data, empty}` (and history's endpoints-with-history vs without), but the paged envelope is only `{items, next_page_token}` and the learner endpoint set is derived server-side. The CLI can count endpoints *with* data from the items; it cannot know how many endpoints were *queried* (so "no answer" vs "not fetched" is not computable client-side).
**Options considered**:
- A) Extend the envelope (e.g. the final page, or every page) with coverage metadata such as `endpoints_queried`; small additive change to the STORY 3 endpoints.
- B) Drop `queried`/`empty` from v1 coverage; record only `with_data` and mark the rest unknown.
- C) Add an endpoint-count field to `GET /reports/:id` metadata that the CLI captures at fetch start.

**Decision**: A, with the server change owned by a separate spec in the report-service repo, prefixed with this story's key (e.g. `specs/REPORT-77-server-support/`); this spec records it as a dependency only. Verified in code: the envelope today is exactly `{items, next_page_token}`, but `serve_page` holds the scratch's full endpoint set on every page, so a constant `total_endpoints` field is a trivial additive change there. C was rejected because `GET /reports/:id` never derives the endpoint set and a metadata-time count can disagree with the export-start snapshot. CLI behavior: when `total_endpoints` is present, record coverage `{queried: total_endpoints, with_data, empty}`; when absent (older server), record `with_data` only and mark `queried`/`empty` unknown, never guessed.

### RESOLVED: Which platforms must the v1 release pipeline ship?
**Context**: cgo/DuckDB forces native builds per platform; macOS needs signing/notarization; Windows signing is its own project. The design targets Windows at the code level (keychain, flock, ACL notes) but the release scope is unstated.
**Options considered**:
- A) macOS (arm64 + amd64, signed/notarized) + Linux amd64; Windows binaries deferred to a follow-on.
- B) A + Windows amd64, unsigned.
- C) A + Windows amd64 + Linux arm64.

**Decision**: A, with a CI rider. Release targets: macOS arm64 + amd64 (signed/notarized) and Linux amd64, matching the design doc's stated audience ("researchers on Mac, devs on Linux"). CI additionally builds and tests (but does not release) Windows amd64 and Linux arm64 on every push; go-duckdb bundles prebuilt static libs for all five targets (verified), so promoting either to the release matrix later is a config change, not porting work. Unsigned Windows binaries were rejected: SmartScreen friction for the least-technical audience.

### RESOLVED: What closes this story: internal dogfood or the full release pipeline?
**Context**: The ticket folds the release pipeline in, but external release is gated on STORY 2 (token UI). A working pipeline can exist before any public release.
**Options considered**:
- A) Story closes at internal dogfood (all commands working, `go build` installs); release pipeline becomes a follow-on ticket.
- B) Story closes when the release pipeline works end-to-end (tagged build produces signed artifacts + Homebrew formula), even if nothing is announced publicly.
- C) Story closes at public researcher release (requires STORY 2 shipped first).

**Decision**: B. The story closes when a tagged build produces signed/notarized macOS artifacts + Linux amd64 and the Homebrew formula installs them; announcing/distributing to researchers is a rollout decision outside the story. Verified during the interview: REPORT-74/75/76 are all Done in Jira (the token-management UI is shipped), so no external gate blocks a release; C was rejected because it makes completion hinge on a comms act rather than engineering. The dogfood milestone still happens naturally partway through (subtasks a-e work via go build before (g) lands).

### RESOLVED: Are `--latest-only` / `--latest-N` history modes in v1?
**Context**: The design names them as optional slimming flags; full series is the default. Latest-only likely needs server cooperation (or client-side filtering after a full read, which defeats the purpose), so including them may touch STORY 3 scope.
**Options considered**:
- A) v1 ships full-series only; the flags are a follow-on (manifest `history_mode` already accommodates them).
- B) Include the flags in v1, coordinating any needed STORY 3 endpoint support.

**Decision**: Neither: the flags are dropped from the design entirely (not merely deferred). Verified in code: the shipped bulk-read walks the full series unconditionally (no mode parameter anywhere in the Node request, BulkParams, or the controller), so the flags would have required new server work in a Done story. Latest-only is redundant with `get answers` (the answer doc is the current state per learner+question); last-N analysis is one QUALIFY line over the `history` view after a full pull. The design doc was updated to match; `history_mode` stays in the manifest as the constant `full` for forward compatibility.

### RESOLVED: Which commands does `cc-data mcp` expose as MCP tools?
**Context**: Claude Desktop drives the CLI via MCP. `login` cannot work over stdio (browser flow), and destructive operations (`dataset delete`/`purge`) from an LLM may deserve a guard.
**Options considered**:
- A) Everything except `login`/`logout`; destructive ops included (MCP clients confirm tool calls themselves).
- B) Reads + fetches + dataset create/show/list only; no delete/purge/rename over MCP.
- C) Read-only (list/show/query); all writes stay in the terminal.

**Decision**: A, with a server-enforced guard. All commands except `login`/`logout` become MCP tools (credential management stays a terminal act by design). Tools carry honest annotations (`readOnlyHint` on list/show/query, `destructiveHint` on delete/purge), and `dataset_delete`/`dataset_purge` additionally require an explicit `confirm: true` argument enforced by the server itself, since the MCP spec makes host confirmation a SHOULD and annotations explicitly untrusted (verified against the 2025-06-18 spec). Parity note: Claude Code already reaches the full CLI via bash, so restricting the MCP surface would not remove any capability, and keeping purge one conversational step away supports the retention guidance.
*(Amended by round-5 self-review: "all commands except `login`/`logout`" was over-broad. The surface is pinned to the data-and-analysis commands; `repl` (interactive), `mcp` (recursive), and `init`/`uninstall` (host-machine installer acts, with `uninstall` reaching credentials through the logout path) stay terminal-only for the same credential-and-host-lifecycle reasons as `login`/`logout`. The decision's intent, full data-and-analysis parity with a guard on destructive ops, is unchanged.)*

### RESOLVED: Is the dataset root fixed at `~/cc-data` or configurable?
**Context**: The design tree shows `~/cc-data/`. A `data_root` config setting is cheap now and painful to retrofit (datasets reference nothing absolute, so moving is easy today).
**Options considered**:
- A) Configurable `data_root` in `config.json`, defaulting to `~/cc-data`.
- B) Fixed `~/cc-data` for v1.

**Decision**: A. `data_root` in `config.json`, `CC_DATA_ROOT` environment variable override (env wins), default `~/cc-data`. The env override is required for CI test isolation regardless, so the config knob is nearly free. The default stays the visible `~/cc-data` (not an XDG dot-directory) deliberately: researchers should see the sensitive data they hold. Corollary pinned down by this decision: manifest file paths are stored relative to the dataset folder, so datasets and the whole root are freely movable (verified that nothing else in the design references the literal path).

## Self-Review

### Security Engineer

#### RESOLVED: `query`/`repl`/MCP can read arbitrary local files through DuckDB
DuckDB SQL includes filesystem functions (`read_csv`, `read_text`, `read_json` over any path), so `cc-data query` as specced executes SQL that can read any file the user can, and over MCP that power is handed to the model. Without a sandbox, the "what Claude may auto-read" boundary is fiction: a single `query` call could read `~/.ssh/id_rsa` via SQL.

**Resolution**: sandbox by default with a scoped, per-invocation opt-out (verified against the DuckDB securing docs). Every `query`/`repl`/MCP session sets `allowed_directories = [<dataset folder>]` + `enable_external_access = false` (views are lazy, so the dataset folder must be allowlisted for the views themselves to work), disables community extensions and extension autoinstall/autoload, then `lock_configuration = true`. A repeatable `--allow-dir <path>` flag on `query`/`repl` appends to the allowlist for that invocation only, never persisted, covering the "join a foreign CSV" case as an explicit visible grant. The MCP `query` tool does not accept the flag (the model controls tool args and host confirmation is only a SHOULD); over MCP the allowlist extends only via `cc-data mcp --allow-dir` launch args in the user-controlled Desktop config. Requirements updated accordingly.

---

### Senior Engineer

#### RESOLVED: Concurrent `get` merges need an explicit serialization contract
The requirements state cursor/manifest writes hold the per-dataset lock, but two simultaneous `get`s (e.g. answers 584 and answers 612) both end in a merge that rewrites the store and repoints the manifest. Without a stated contract, the second merge could read `v3`, write its own `v4`, and silently discard the first merge's records: lost data with no error.

**Resolution**: the merge critical section runs under the per-dataset lock and re-reads the current store pointer inside it; a merge prepared against `vN` that finds `vN+1` current rebases by re-running the same streaming merge against `vN+1` (segments are per-download, so this is cheap). Page appends stay concurrent and unlocked except for cursor writes. Requirement added with an acceptance criterion (two concurrent fetches of different runs lose no records from either).

#### RESOLVED: "Refreshed on upgrade / removed on uninstall" has no mechanism under Homebrew
Homebrew upgrades do not run our post-upgrade hooks, and `brew uninstall` will not delete files under `~/.claude/`. As written, the skill/CLAUDE.md lifecycle requirement was assigned to an installer that cannot implement it.

**Resolution**: mechanism changed, outcome kept. (1) The skill file is stamped with the binary version that wrote it; every `cc-data` invocation cheaply compares and silently rewrites the skill + pointer when the binary is newer, which covers upgrades regardless of install method. (2) A new `cc-data uninstall` command removes the skill and the CLAUDE.md pointer, optionally config/credentials, and explicitly tells the user their datasets remain and where (`~/cc-data` or the configured root); the README documents that `brew uninstall` removes only the binary and recommends `cc-data uninstall` first. The dataset notice is part of the sensitive-data retention story, not just cleanup hygiene. Requirements updated.

#### RESOLVED: `get attachments` prerequisite is ambiguous
"Requires the run's answers/history to already be fetched" did not say whether that means both or at least one. Context for the prerequisite itself: attachment refs exist only inside answer/history records (no server endpoint enumerates a run's attachments; the presign endpoint signs client-supplied refs), so the scan is the only source of the ref list.

**Resolution**: at least one. The scan covers whatever membership exists for the run (answers, history, or both) and errors only when neither exists. The result line and manifest entry name the sources scanned (e.g. `scanned: ["answers"]`), and the human output notes any unfetched source ("history not fetched; history-referenced attachments not included"), so a partial scan is visible rather than implied complete. Requirement reworded.

---

### QA Engineer

#### RESOLVED: `--no-wait` outcome is not machine-testable as specced
`get report --no-wait` "exits immediately with status" but the spec did not say what that looks like: exit code, output shape, and whether the every-get-ends-with-a-result-line invariant still holds.

**Resolution**: the invariant is absolute. With `--no-wait` on an unfinished run, the CLI emits the standard structured result line (current `athena_query_state`, `complete: false`, no file written) and exits with the not-ready exit code from the exit-code conventions (Finding: exit codes and stream separation); a succeeded run behaves identically to the no-flag case. Acceptance: `--no-wait` on a queued run exits non-zero with a parseable state. Requirement amended.

---

### DevOps Engineer

#### RESOLVED: Apple signing/notarization credentials are an unstated external dependency
The definition of done includes signed/notarized macOS artifacts, which requires an Apple Developer ID Application certificate and notarization credentials provisioned as CI secrets. If the org did not have these ready, the DoD would be blocked by procurement, not engineering.

**Resolution**: recorded as a named external dependency with the known state: Concord has an Apple developer account (used for mobile apps), so enrollment lead time is not a risk; what needs confirming before subtask (g) starts is whether a Developer ID Application certificate exists (it's a different certificate type from iOS distribution) and creating/exporting it plus an App Store Connect API key for notarytool, which requires Admin/Account Holder access. Fallback defined: the pipeline supports an unsigned dev-build mode so work is never blocked, but the DoD's signed path must work before the story closes. Added to Technical Notes.

#### RESOLVED: Homebrew formula location is unspecified
homebrew-core has notability requirements a niche research CLI will not meet; the formula must live in an org tap, and none exists (verified: zero `homebrew-*` repos under concord-consortium).

**Resolution**: create a general org tap, `concord-consortium/homebrew-tap`, as part of subtask (g); goreleaser publishes the generated formula there on tagged releases (repo-scoped push token added to the CI secrets list). Install documented as `brew install concord-consortium/tap/cc-data`. A general tap was chosen over per-tool `homebrew-cc-data` so future Concord CLI tools share it. Requirement amended.

---

### Education Researcher

#### RESOLVED: Cross-dataset querying should be an explicit documented boundary
The dataset-per-pull pattern (for comparing answer data over time) produces multiple datasets, but `query`/`repl` targeted exactly one dataset, so the tool's own longitudinal advice hit a wall at analysis time.

**Resolution**: resolved by feature rather than by documentation. `--dataset` becomes repeatable on `query`/`repl` (and a `datasets` argument on the MCP query tool): each dataset's views register under their own DuckDB schema (`wildfire_2026.answers`), names sanitized to SQL identifiers with an alias form (`--dataset pre=<ref>`) for collisions; the sandbox allowlist is the list of named dataset folders. One principled rule: **no implicit cross-dataset union views**; point-in-time pulls contain the same identities by design, so researchers write unions/joins explicitly with provenance in hand. Single-dataset invocations keep unqualified view names, unchanged. Lands in subtask (c) where view registration is built anyway. Requirements updated.

---

### LLM-Agent UX

#### RESOLVED: Exit codes and stream separation are unspecified
Claude and scripts branch on exit codes and parse the structured result line; neither was pinned down.

**Resolution**: stream discipline (stdout = machine-consumable only: result line, `--json` documents, `query` output; stderr = all progress/warnings/prose) and a stable exit-code table (0 success, 1 internal, 2 usage, 3 NOT_AUTHENTICATED, 4 not-ready, 5 server contract error, 6 transient-after-retries), documented in `--help` and the skill. The single-line JSON error still carries the specific `error` code; the exit code is the coarse class. Requirements added with acceptance criteria.

---

### Security Engineer (round 2, after resolutions)

#### RESOLVED: `cc-data uninstall` can orphan live tokens
The Finding 3 resolution lets `uninstall` optionally delete credentials, but local deletion does not revoke: the server-side token stays live, and an uninstalling user will never revisit the token UI, manufacturing exactly the forgotten-token case the design worries about.

**Resolution**: ordering, not new machinery. When credential removal is requested, `uninstall` first attempts `logout` (server-side revoke) for each portal with a stored token, then deletes locally; if a revoke fails it proceeds with local deletion and prints a warning that the token may still be active, with the token-management UI URL. Requirement amended.

---

### Data/Storage Engineer (round 3, code-verified)

#### RESOLVED: `run_membership` mixes per-type membership rows; the documented answers join pattern multiplies results
The unified `run_membership` view reads all `members_<type>_<run>.jsonl` files, but answers membership rows are 3-tuple identities while history membership rows are 4-tuple (one row per snapshot). When a run has both answers and history fetched (the normal case), the taught pattern `answers JOIN run_membership USING (source_key, remote_endpoint, question_id) WHERE run_id = 584` matches the answers membership row AND every history snapshot row for the same learner+question. Verified with a throwaway DuckDB test (v1.3.0): 2 answer records joined to 5 rows (learner with 3 history snapshots matched 4 times). Every run-scoped answers query silently multiplies rows. History joins are already safe (they add `history_id` to the USING list, and answers rows NULL-fill it), so only the answers pattern breaks.

**Resolution**: the view carries a manifest-attached `type` column per membership file, and the taught pattern is type-qualified (`... WHERE m.run_id = 584 AND m.type = 'answers'`; history joins add `history_id` to the join key and use the same filter for consistency). Per-download views (`answers_584`) are already type-scoped by construction and need no change. Requirements (views list, run-scoping bullet, skill bullet) updated; the design doc's `run_membership` description and both example queries updated to match.

#### RESOLVED: Answers-report CSVs embed two pseudo-header rows; the `reports` view counts them as data
`generate_resource_sql` in report-service `shared_queries.ex` appends two extra data rows to every answers-type result (`if report_type == :answers`): a "Prompt" row and a "Correct answer" row, with `student_id` holding those literal strings and the per-question columns holding prompt/correct-answer text. Verified in code and by DuckDB test: `read_csv` over a shaped CSV returns 4 rows for 2 students. As specced, `reports` view row counts and per-student aggregates are off by 2 per answers CSV, manifest `row_count` overcounts, and a text correct-answer value in an otherwise numeric question column flips the sniffed type to VARCHAR.

**Resolution**: the `reports` and per-run report views filter `student_id NOT IN ('Prompt', 'Correct answer')`; the manifest's CSV `row_count` excludes the two rows; a new `report_prompts` view exposes them as metadata, since the prompt/correct-answer text is exactly what interprets the `res_<N>_<question_id>_*` columns. Usage-type CSVs have no such rows and are unaffected by the filter. Views requirement updated; design doc's `reports` view bullet updated to match.

*(Amended 2026-07-16 after a throwaway DuckDB 1.3.0 test: the row filter does not restore sniffed column types, because `read_csv` infers types from the whole file including the two pseudo-header rows. Columns those rows leave empty, such as scores, keep their numeric types, but every `_answer` column sniffs VARCHAR from the Prompt row's text, so `sum()` over a numeric-answer column fails through the filtered view. Decision: keep the sniffed types rather than adding hidden per-column casts (answer columns are inherently mixed-type across question types anyway) and teach `TRY_CAST` for numeric aggregation in the skill. Views and skill requirements updated to match.)*
*(Amended by round-3 performance review: "`read_csv` infers types from the whole file" is factually wrong, DuckDB sniffs a 20,480-row sample, and the VARCHAR outcome held only because report-service `shared_queries.ex` orders the two pseudo-header rows first (REPORT-58, commit `4e01de3`), a cross-repo dependency this repo must not silently rely on, plus a NULL-prompt column past the sample could hard-fail the whole `reports` view. Superseding decision: the CLI detects each CSV's column types full-file during the download stream, records them in the manifest, and the views read with `auto_detect=false` + the recorded column map. The VARCHAR-`_answer`/`TRY_CAST` outcome is unchanged; it is now order- and size-independent. The REPORT-58 ordering is recorded as a pinned server behavior in wire-captures.md.)*

---

### Senior Engineer (round 3, code-verified)

#### RESOLVED: Server-surface references drifted from the landed and decided server work
Two spec statements no longer match the report-service repo. (1) The Auth requirement names the owed revoke endpoint "e.g. `POST /auth/cli/logout`" and cites `Accounts.revoke_api_token/1`; the server-side spec this story owns (report-service `specs/REPORT-77-cli-server-support/requirements.md`) has since resolved the route to `DELETE /api/v1/tokens/current`, and the function is `revoke_api_token/2` (token, revoked_by_user_id). (2) The login requirement lists the `/auth/cli` query params as `redirect_uri`, `state`, `code_challenge`; the landed controller also requires `portal` and `code_challenge_method=S256`, and validates `redirect_uri` as exactly `http://127.0.0.1:<port>/callback` (verified in `auth_cli_controller.ex`).

**Resolution**: the logout bullet and Technical Notes now name `DELETE /api/v1/tokens/current` and `revoke_api_token/2`, and the login bullet carries the full landed param list including `portal`, `code_challenge_method=S256`, and the exact `redirect_uri` shape.
*(Amended by round-2 implementation review: `portal` is validated when present but not required by the landed controller, which falls back to its configured default portal when absent; the CLI always sends it to select the portal.)*

---

### LLM-Agent UX (round 3, code-verified)

#### RESOLVED: The spec's error-code vocabulary does not match the server's
The retry policy and exit-code table name `FORBIDDEN` as a contract error, but no server code path emits it: the landed vocabulary (`ErrorHelpers`) is exactly `BAD_REQUEST`, `NOT_AUTHENTICATED`, `NOT_FOUND`, `NOT_READY`, `EXPIRED_CURSOR`, `SERVER_ERROR`, and ownership failures deliberately render `NOT_FOUND` (so a known run id cannot probe existence). Meanwhile `BAD_REQUEST` (400), which the server really returns (malformed `limit` or `page_token`, bad attachments body), appears nowhere in the spec's contract-error list, exit-code classes, or skill. Claude and scripts taught to branch on `FORBIDDEN` branch on a phantom; a real `BAD_REQUEST` falls into the unexpected-error class.

**Resolution**: retry policy and exit-code table aligned to the landed vocabulary: `FORBIDDEN` dropped, `BAD_REQUEST` added to the contract-error class (exit 5, never blind-retried), `NOT_APPLICABLE` kept and marked reserved, and a forward-compatibility rule added (any unrecognized 4xx error code is a contract error, never blind-retried). The retry bullet now records the exact landed vocabulary so the skill inherits the correct list.

#### RESOLVED: The single-line JSON error's output stream is unspecified
The stream-discipline requirement enumerates what stdout carries (result line, `--json` documents, `query` output) but never assigns the JSON error envelope to a stream. The design doc calls the result line "the success-path sibling of the single-line error envelope", implying stdout, but the requirement as written permits errors on stderr, where the spec's own acceptance pattern (`2>/dev/null | jq .`) would silently discard `NOT_AUTHENTICATED` and break the documented "Claude relays the error" contract.

**Resolution**: the stream-discipline bullet now states that on failure the single-line JSON error envelope is the only stdout output (success and failure paths stay symmetric: one JSON line on stdout either way), with a matching acceptance criterion.

---

### QA Engineer (round 3)

#### RESOLVED: The never-duplicate invariant, the headline guarantee, has no acceptance criterion
Concurrent merges, `--no-wait`, the DuckDB sandbox, and stream discipline all carry acceptance criteria, but the core promise (at most one record per identity at rest; overwrite on re-fetch; `removed` on shrunk refresh) has none.

**Resolution**: acceptance added to the never-duplicate requirement (overlapping-runs plus `--refresh` scenario with the SQL distinctness check and honest merge counts) and to the answers/history fetch requirement (`EXPIRED_CURSOR` resume restarts cleanly and ends `complete: true`).

---

### Education Researcher (round 3)

#### RESOLVED: The skill should teach the reports-to-stores join key
Joining report CSV rows to raw answers is the original pain this tool exists to remove, but the skill requirement teaches only store-side patterns (`run_membership`, multi-dataset schemas). The join key exists and is non-obvious: answers CSVs carry per-resource `res_<N>_remote_endpoint` columns (verified in `shared_queries.ex`) equal to the stores' `remote_endpoint`, and per-question columns named `res_<N>_<question_id>_*` that pair with the stores' `question_id`.

**Resolution**: the Claude-integration skill bullet now includes the reports-to-stores join pattern and the `res_<N>_<question_id>_*` column naming, alongside the type-qualified membership pattern.

---

### Data/Storage Engineer (round 4)

#### RESOLVED: "Newest complete store version" is undefined for `reindex` without a rename discipline on store files
`reindex` "adopts the newest complete store version per type", but nothing defines how completeness is detectable once the manifest (the normal arbiter, via its store pointer) is lost or corrupt. The merge requirement writes the new version and fsyncs but never says the version file itself goes through `.tmp` then rename; a merge that crashes mid-write can therefore leave a truncated `answers.v4.jsonl` under its final name, and if the truncation happens to land on a line boundary the file is indistinguishable from a complete store. `reindex` adopting it would silently lose every record the crashed merge had not yet written, defeating the "a lost/corrupt manifest is never fatal" recovery promise.

**Resolution**: the write-path requirement now routes store versions through `.tmp`, fsync, atomic rename before the manifest repoint, and the `reindex` requirement adopts the newest version present under a final name, always discarding `.tmp` files. Design doc's write model and store-recovery bullets updated to match.

---

### API Contract Engineer (round 5, code-verified)

#### RESOLVED: report/job CSV download is a two-step presigned-URL flow; the spec read as a one-step byte download
Verified: `GET /reports/:id/download` and `GET /reports/:id/jobs/:job_id/download` return 200 JSON `{download_url, filename, expires_in_seconds}` (600 s TTL from `AthenaDB.download_url_ttl_seconds`), never bytes; the CLI then GETs the presigned S3 URL. The fetch bullet ("succeeded → download via `GET /:id/download`") and the API-contract note never mention the envelope. Three requirements-level consequences are unstated: (1) the S3 GET is a second call outside the v1 error contract (an expired URL fails as an S3 403 XML body, which must be handled by re-requesting a fresh envelope, never blind-retried and never classified against the JSON error vocabulary); (2) "presigned URLs are never persisted" is stated only for attachments but applies equally to CSV download envelopes; (3) the audit row is written when the envelope is issued (`AuditLog.issue_download_url`), so the CLI should consume the URL promptly after requesting it.

**Resolution**: the fetch bullet now names the envelope (`{download_url, filename, expires_in_seconds}`, a 600 s presigned URL, not bytes) and requires prompt consumption; the retry policy gained the S3-GET clause (re-request a fresh envelope/URL within the bounded budget; never classify the S3 XML response against the JSON error vocabulary); the API-contract note records the envelope on both download endpoints, the audit-at-issuance behavior, and the generalized rule that presigned URLs of any kind are never persisted. The design doc already described the endpoints as "mint a fresh presigned CSV URL", so no design change was needed.

---

### Senior Engineer (round 5, code-verified)

#### RESOLVED: `auth status` promises "each token's label", which the CLI cannot learn
Verified: the token exchange returns only `{token}` (`auth_cli_controller.ex`); every PKCE-minted token gets the constant label "CLI login" (`Accounts.exchange_auth_grant/2` calls `create_api_token(user, "CLI login")`); and no v1 route returns token metadata (the router has exactly the eight data routes, and the owed server spec adds only `DELETE /api/v1/tokens/current`). Manual `--token` pastes carry user-chosen labels in the token UI, but the CLI never sees those either. Relatedly, `--check` "validates each token against its server" without naming a call; the only landed candidates are data routes (e.g. `GET /api/v1/reports?limit=1`).

**Options considered**:
- A) Reword `auth status` to locally-knowable facts (portal, storage backend, stored-at timestamp recorded at login) and pin `--check` to a cheap authenticated data call (`GET /reports?limit=1`). No server change.
- B) Add `GET /api/v1/tokens/current` (label, created_at, last_used_at) to the owed server-support spec as the natural sibling of the DELETE route, giving `--check` a principled endpoint and real labels.

**Decision**: B, with two riders. `GET /api/v1/tokens/current` returns `{label, created_at, last_used_at, report_access}` (the `report_access` flag makes "valid token, de-provisioned account" distinguishable from "invalid token"), and the token exchange gains an optional additive `label` param so the CLI can send `CLI login (<hostname>)` instead of every PKCE token sharing the constant "CLI login". Split of duties settled at the same time: plain `auth status` is offline (portal, storage backend, local stored-at timestamp, `default_portal`); `--check` is the network half, calling the introspection endpoint and falling back to `GET /reports?limit=1` plus unknown metadata against older servers. Requirements, Background, Technical Notes, and Out of Scope updated; the introspection endpoint and exchange label added to the server-support spec in report-service; design doc's `auth status` bullet updated to match.

---

### MCP Integration Engineer (round 5)

#### RESOLVED: "every command except `login`/`logout`" sweeps `repl`, `mcp`, `init`, and `uninstall` into the MCP tool surface
Spec-internal contradiction (no CLI code exists yet to check against): `repl` is an interactive terminal session and cannot be a stdio MCP tool (tools are single request/response; the `query` tool already covers the capability); `mcp` inside `mcp` is recursive; `init` prompts for login and exists to install host-machine files; `uninstall` optionally deletes credentials via the logout path, which contradicts the resolved MCP question's own rationale ("credential management stays a terminal act by design") and reaches through to `logout`, an excluded command.

**Resolution**: the tool surface is pinned to the data-and-analysis commands: `auth_status`, `version`, `reports_list`, `reports_jobs`, `get_report`, `get_answers`, `get_history`, `get_attachments`, the `dataset_*` tools (delete/purge keep the `confirm: true` guard), and `query`; `repl`, `mcp`, `init`, and `uninstall` stay terminal-only alongside `login`/`logout`. The MCP requirement now enumerates the surface and the exclusions with their reasons, and the resolved Open Question's decision log carries a matching amendment so the decision and the requirement agree.

---

### QA Engineer (round 5)

#### RESOLVED: `purge` is never defined in this spec
The dataset CRUD bullet lists `purge` beside `delete (local only)` and the sensitive-data section instructs "purge when no longer needed", but only the design doc defines what purge does (remove downloaded data files, keep the dataset shell re-fetchable). A reader of this spec cannot distinguish `purge` from `delete`, and there is no testable statement of what survives a purge (manifest? attachment files? membership files?). Minor sibling gap: the auto-generated dataset name `{date}_{slug}` never says what the slug derives from.

**Resolution**: the CRUD bullet now defines both commands (`delete` removes the entire dataset folder; `purge` deletes all downloaded artifacts, stores, segments, membership files, CSVs, and attachments, keeping the dataset folder, name, description, and manifest identity so it remains listed and re-fetchable) with an acceptance line (after `purge`, `dataset show` reports zero holdings with no warnings and the dataset still lists). The auto-generated name is pinned: slug from `--description` when provided, else a `{date}_{n}` counter (at create time no run has been fetched, so there is no report slug to borrow). Matches the design doc's existing purge definition ("remove downloaded data files, keep the dataset shell").

---

### API Contract Engineer (round 6, code-verified during implementation planning)

#### RESOLVED: Attachment refs and the presign request use different shapes than the spec described
The attachments bullet described scanning for "`audioFile` and `__attachment__` refs plus doc coordinates" and implied presigning `(source, publicPath)` pairs. Verified 2026-07-16 against the report-service working tree and the Node bulk-read/attachment-meta sources: records carry an `attachments` map keyed by attachment name (values: `publicPath`, optional `contentType`); there is no dedicated `audioFile` field and no `__attachment__` sentinel anywhere on the wire ("audioFile" is a conventional attachment name, and `__attachment__` markers live inside the interactive state, referencing map entries by name). The presign endpoint takes `{"attachments": [{collection, source, doc_id, name}, ...], "disposition"}` (cap 500 items) and returns per-item `{doc_id, name, url|error}` with error strings exactly `"not_found"` and `"not_authorized"`, plus `expires_in_seconds: 600`.

**Resolution**: the attachments requirement now names the `attachments` map as the ref source and the `(collection, source, doc_id, name)` presign item shape; the `(source, publicPath)` dedup identity and `id12` filename derivation are unchanged (`publicPath` comes from the map values). The exact request/response shapes are carried in implementation.md.
