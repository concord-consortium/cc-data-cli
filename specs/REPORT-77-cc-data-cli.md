# cc-data CLI: researcher data download + query tool

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-77

**Status**: **Closed** (implemented, tested, and verified end-to-end against a live report-service)

## Overview

A Go CLI (`cc-data`) that lets Concord Consortium researchers download report CSVs, student answers, interactive state history, and file attachments into local named datasets and query across all of it with SQL via an embedded DuckDB, with first-class Claude integration (skill + MCP server). This spec covered the full story: auth through release pipeline.

Today a researcher who wants to analyze student data logs into the report server, runs a report, downloads a CSV, opens it somewhere, and joins it against answer data by hand; raw answer and interactive-history data is not practically reachable at all. `cc-data` collapses that into a local workflow the researcher (or Claude, on their behalf) drives from the terminal: authenticate once per portal, pull runs' data into a dataset, and ask questions in SQL or plain English. Datasets are guaranteed duplicate-free (every downloaded item overwrites the prior copy of the same learner+question record), so combining classes, activities, and repeated pulls is safe by construction. The CLI classifies its local datasets as sensitive student data and ships with the retention guidance, file permissions, and Claude-access boundaries that classification requires.

This story was STORY 4 of the REPORT-71 breakdown: the researcher-facing Go CLI in its own repository (`concord-consortium/cc-data-cli`, binary `cc-data`). The client/server seam is the versioned HTTP contract (`/api/v1/...`); no code is shared with the report server.

## Requirements

### Auth + credentials (subtask a)

- `cc-data login --portal <portal>` performs the PKCE loopback flow (127.0.0.1 listener, browser to `/auth/cli` with the validated param set, one-time code exchanged at `POST /auth/cli/token` with an optional `label`), storing the token per portal in the OS keychain (macOS Keychain / Linux Secret Service / Windows Credential Manager via `zalando/go-keyring`), falling back to `~/.config/cc-data/credentials.json` at `0600` (Windows: user-profile ACLs). `--token -` reads a manual token from stdin (piped or an echo-off TTY prompt), the recommended headless form; the bare `--token <value>` form is kept but documented as discouraged.
- `cc-data logout` revokes the current token server-side (`DELETE /api/v1/tokens/current`) then removes it locally; a 401 (already invalid) or a 404 (older server) still removes locally and exits 0.
- `cc-data auth status` lists portals with stored credentials offline (portal, backend, stored-at, `default_portal`); `--check` validates each token via `GET /api/v1/tokens/current` (label, created_at, last_used_at, report_access), falling back to `GET /reports?limit=1` against older servers. It exits 0 whenever the command completes; per-portal validity travels in the output.
- `cc-data version` prints the ldflags-injected binary version.
- Every non-login command sends `Authorization: Bearer <token>`; a 401 emits the single-line `NOT_AUTHENTICATED` error and exits 3 (logout excepted). The CLI never drives the browser outside `login`.
- Config `~/.config/cc-data/config.json` (`0600`) holds `default_portal`, `data_root`, and `server_url`. The report server origin defaults to the built-in `https://report-server.concord.org`, is overridable via config and a `--server` flag on `login`, and is validated against an allowlist (`concord.org`/`concordqa.org` host or subdomain, or a loopback host; http only for loopback). Each portal records the minting server in its credential metadata; later commands always talk to that origin.
- Retry policy: transport errors, 429, and 5xx retry with bounded exponential backoff plus full jitter; every 4xx coded error (`BAD_REQUEST`, `NOT_FOUND`, `NOT_READY`, `EXPIRED_CURSOR`, the reserved `NOT_APPLICABLE`, and any unrecognized code) is a contract error, never blind-retried. Presigned S3 GETs are outside the contract: a failure re-requests a fresh envelope/URL within the bounded budget and never classifies the XML body.

### Listing (subtask a)

- `cc-data reports list --portal <portal>` drains the keyset-paginated `GET /api/v1/reports`, showing run id, slug, state, and resolved filter labels; a filter-less (programmatic) run renders with zero labels.
- `cc-data reports jobs <run-id> --portal <portal>` lists a run's post-processing outputs.

### Fetch: report CSVs (subtasks a + jobs)

- `cc-data get report <run-id> --dataset <ref>` polls the download endpoint (succeeded → stream the presigned CSV; running/queued → poll with backoff up to a default 30-minute budget, `--poll-timeout` override; a terminal failure state → exit 5, write nothing), streaming `.tmp` → fsync → atomic rename to `report_<run>.csv`. Terminal states are server-verified (`failed`/`cancelled` for runs, `failed` for jobs); the CLI detects the `null`→`queued`→`null` self-start oscillation and stops early.
- `--no-wait` emits the standard result line (`complete: false`, the current state, no file) and exits 4. `--job <id>` downloads a post-processing output to `report_<run>_job_<id>.csv`. Re-pull without `--refresh` is a usage error.
- Run metadata carries `report_type` (`answers` | `usage` | `log`, a render-time slug derivation server-side); the CLI records it, deriving it from the known slugs against an older server, and quarantines an unknown value at query time (downloaded, excluded from the `reports`/`report_prompts` union, per-run view still available, with a warning). Each CSV's column types and dialect are detected full-file at download and recorded in the manifest.

### Datasets, stores, and the never-duplicate model (subtask b)

- Dataset CRUD: `create` (auto-names `{date}_{slug}`), `list`, `show`, `rename`, `edit --description`, `delete` (whole folder), `purge` (downloaded artifacts only, keeping the shell), `reindex`. Datasets live under `<data_root>/<portal-host>/datasets/<name>/`; `data_root` is `CC_DATA_ROOT` env, then config, then `~/cc-data`. A ref is `<portal>/<name>`; a bare `<name>` uses `default_portal`.
- **Never-duplicate invariant**: at rest a dataset holds at most one record per identity (answer = `(source_key, remote_endpoint, question_id)`; history = same + `history_id`; attachment = `(source, publicPath)`), newest fetch wins. Answer/history records live in versioned identity-keyed stores with versioned per-fetch membership files; report CSVs stay per-run.
- Write path is segment-and-merge: pages append to a per-download segment (stamped `_fetched_at`/`_run_id`) with the cursor persisted under the per-download lock; the final page merge-compacts store + segment into a new version with the load-bearing durable-write order (store rename → membership → manifest repoint → cursor `merged_as` → segment removal last), reporting honest `{fetched, new, updated, removed}` counts. Crash windows converge on an idempotent resume.
- Locking: the per-dataset lock (`<dataset>/.dataset.lock`) serializes merges and manifest writes; the shared whole-fetch activity lock (`<dataset>/.activity.lock`) lets concurrent fetches proceed while a mutating command (rename/edit/delete/purge/reindex) takes it exclusively, non-blocking, failing fast with the busy error. Every `get` of a `(type, run)` holds an exclusive non-blocking per-download lock for its lifetime. All locks are dedicated files, never renamed or unlinked.
- `manifest.json` carries an integer schema version, migrates forward, and refuses newer versions. `reindex` rebuilds it from the filesystem (newest final store/membership versions, re-derived columns and CSV shapes, partial `report_type` recovery: no `student_id` → log; `student_id` + pseudo-header rows → answers; ambiguous → a distinguished `recovered` value included in `reports` but flagged).

### Summaries + machine-readable output (subtask b)

- `dataset show`/`list` render manifest-only summaries (per-type totals, per-download table, coverage split, drift warnings) with a stable `--json` contract (`ShowJSON`/`ListJSON` structs) and `--full` detail; they never scan data files or start DuckDB.
- Every `get` ends with one machine-readable JSON result line. Stream discipline: for `get`/`query`, stdout carries only machine output (result line, `--json`, query results, or on failure the single JSON error envelope); pure listings put their human table on stdout with `--json` swapping it there; all prose/warnings go to stderr.
- Stable exit codes documented in `--help` and the skill: 0 success, 1 internal/lock-busy, 2 usage, 3 NOT_AUTHENTICATED, 4 not-ready, 5 server contract error, 6 transient-after-retries.

### Fetch: answers + history (subtask d)

- `cc-data get answers|history <run-id> --dataset <ref>` consumes the paged envelope, appending per page and persisting the cursor; resume continues from the cursor; `EXPIRED_CURSOR` (410) discards the segment and restarts from a null cursor once; a malformed `page_token` (400) is a fatal contract error. `report_state` and its inner `interactiveState` are double-decoded into real JSON objects at ingest, with a decode failure routed to a `_raw` sibling field and a `_decode_error` marker (never dropping data or mixing types).
- History always fetches the full snapshot series (`history_mode: "full"`). Coverage records `{queried, with_data, empty}` from the envelope's `total_endpoints` (null when the field is absent).

### Fetch: attachments (subtask d2)

- `cc-data get attachments <run-id> --dataset <ref>` (alias `attachment`) scans the run's stored records' `attachments` map for refs (`doc_id` = `id` for answers, `history_id` for history; offloaded-state files flagged via `__attachment__` markers), dedups by `(source, publicPath)`, batch-presigns in ~100-item chunks, and streams each URL to `attachments/<id12>_<safename>` with a deterministic cross-platform filename sanitizer. Resume is existence-driven; `--refresh` re-downloads; per-item `not_found`/`not_authorized` land in `coverage.missing`, never fatal. Selectors (`--answer`/`--history`/`--question`/`--name`) narrow the scan; `--url` prints presigned URLs (writing nothing); `--inline` flips the presign disposition. GC (also on `reindex`) removes unreferenced files, retaining history-referenced ones, and rebuilds the attachment index. At least one of answers/history must be fetched first.

### DuckDB query layer (subtask c)

- `cc-data query --dataset <ref> "SELECT ..." [--format table|csv|json|jsonl]` and `cc-data repl --dataset <ref>`: an ephemeral in-memory DuckDB registers views from the manifest's explicit file lists, then locks the sandbox (`allowed_directories` to the dataset folders plus `--allow-dir`, `enable_external_access = false`, extensions off, `lock_configuration = true`). Views: `reports`/`report_prompts` (`UNION ALL BY NAME` over per-run CSVs read with `auto_detect=false` and the recorded column map, answers-type pseudo-header filter, allowlist quarantine with a warning), `answers`/`history` (`read_json` over the current store with the merge-derived column map), `run_membership` (type-qualified join surface), `downloads`, `attachment_files`, `attachment_states` (`read_text` + `TRY_CAST(content AS JSON)`), and per-run views. A view whose bind fails degrades to a typed-empty form; a fresh dataset and a zero-item fetch stay queryable with zero rows.
- `--dataset` is repeatable (schema-qualified per dataset, `pre=<ref>` alias on collision, reserved schema names require an alias); no implicit cross-dataset unions. `--allow-dir` extends the allowlist per invocation only.

### Claude integration (subtask e)

- `cc-data init` installs the Claude Code skill and a one-line `~/.claude/CLAUDE.md` pointer; the skill is stamped with the binary version and every invocation cheaply rewrites it when the binary is newer (replacing installer hooks). `cc-data uninstall` removes them, optionally revokes+removes credentials via the logout path, and always prints where datasets remain.
- The skill delegates detail to `--help`, teaches the views (type-qualified `run_membership` joins, reports-to-stores join keys, the per-run positional `res_<N>` semantics, `TRY_CAST` for `_answer` columns, the `attachment_states` JSON-extraction pattern, schema-qualified multi-dataset queries), orients via `dataset show --json`, documents the `NOT_AUTHENTICATED` contract, and carries the sensitive-data guidance.
- `cc-data mcp` runs a stdio MCP server exposing the pinned data-and-analysis surface (auth_status, version, reports_list/jobs, get_report/answers/history/attachments, the dataset_* tools, query) with `readOnlyHint`/`destructiveHint` annotations, a server-enforced `confirm: true` guard on delete/purge, progress notifications and context cancellation on fetch tools, a `max_rows` cap with a truncation marker on query, and capability-widening arguments (url/inline/allow-dir) excluded by design. Handlers call the same internal cores as the CLI, guaranteeing payload parity.

### Sensitive-data documentation (subtask f)

- README, skill, and `--help` classify datasets as sensitive student data and document retention/`purge` guidance, the `0600`/ACL reality, the Claude auto-read boundary, the `--url` caveat, the dataset-folder writable-files trust boundary (the bundled DuckDB resolves symlinks), the `--server` allowlist trust boundary, the `--token -` recommendation, and the exit-code/stream contract.

### Release pipeline (subtask g)

- goreleaser + GitHub Actions build natively per platform (cgo/DuckDB static libs). CI builds and tests all five targets (linux amd64/arm64, macOS arm64/amd64, windows amd64) on every push. On a tag, per-platform jobs render a thin config from `.goreleaser.base.yaml` and run `release --skip=publish`; macOS jobs sign + notarize (no stapling; unsigned dev-build fallback with a failing gate). A publish job assembles the GitHub release and renders the Homebrew formula pushed to `concord-consortium/homebrew-tap`. Release targets: signed/notarized macOS arm64+amd64 and Linux amd64.

## Technical Notes

- **API contract (v1)**: bearer auth; paged envelope `{items, next_page_token}`; single JSON error shape `{error, message, ...}`; path-versioned. Same-cursor retries are safe (identity merge tolerates at-least-once delivery). Both CSV download endpoints return a presigned-URL envelope (`{download_url, filename, expires_in_seconds}`, 600 s), audit-logged at issuance; presigned URLs of any kind are never persisted.
- **Server dependencies owed** (specced in report-service `specs/REPORT-77-cli-server-support.md`, verified landed on branch `REPORT-77-cli-server-support`): the logout revoke endpoint, `total_endpoints` coverage field, the token-introspection endpoint (plus the additive exchange `label`), and `report_type` on run metadata. The CLI degrades gracefully when any is missing. A separate Firebase functions deploy is required for the bulk answers/history and attachment endpoints.
- **Identity + storage naming**: stores `answers.v<N>.jsonl` / `history.v<N>.jsonl`; membership `members_<type>_<run>.v<N>.jsonl`; CSVs `report_<run>[_job_<id>].csv`; attachments `attachments/<id12>_<safename>`.
- **Key libraries**: `spf13/cobra`, `zalando/go-keyring`, `gofrs/flock`, `duckdb/duckdb-go/v2` (v2.10504.0, DuckDB 1.5.4, cgo), `modelcontextprotocol/go-sdk` (v1.6.x), `pkg/browser`, `ergochat/readline`. Go 1.25.0 floor (the MCP SDK's binding constraint); `.tool-versions` pins `golang 1.25.12`.
- **External dependency (g)**: an Apple Developer ID Application certificate + App Store Connect API key provisioned as GitHub Actions secrets before the signed release path can run.
- **Wire shapes** were captured live from a running report-service and pinned at commit `7a8c550` during the spec process (the download envelope, error vocabulary, token introspection/revoke, run list/show with `report_type`, bulk answers/history pages with `total_endpoints`, attachment presign, and the EXPIRED_CURSOR body). They were re-verified end-to-end against a live server when the CLI was implemented.

## Out of Scope

- Creating or re-running report runs from the CLI (author reports in the web UI; generation is a planned follow-on gated on server work).
- All server-side implementation except the owed dependencies (logout revoke, token introspection + exchange label, `total_endpoints`, `report_type`).
- Encryption-at-rest, hard TTLs, or time-based retention enforcement for datasets.
- Shared run visibility (`--all-permitted`); anonymous/offline (`run_key`) runs; point-in-time snapshots within a dataset (use a dataset per pull).
- Token-management UI (STORY 2) and the answers/history/attachments server endpoints (STORY 3).
- `dataset show --stats` canned analytics (possible later addition).

## Not Yet Implemented / Deferred

These are items the spec explicitly scoped as outside the code deliverable, external, or manual — not code gaps:

- **The live PKCE end-to-end check** (`test/e2e/pkce-live.mjs`) is a documented, on-demand dev-machine test, never a CI gate. It re-runs as each owed server dependency lands. (Verified once during this story: the implementation was exercised end-to-end against a live local report-service — login, auth status --check, reports list/jobs, get report/answers/history/attachments, cross-view SQL, and logout all passed.)
- **The signed macOS release path** requires the Apple Developer ID Application certificate and App Store Connect API key to exist as CI secrets before a tagged release can produce signed artifacts; the pipeline degrades to an unsigned dev build with a failing gate until then. First deployed dogfooding additionally requires a report-server release plus a Firebase functions deploy.
- **A present-but-corrupt report CSV** (as opposed to a missing one) still collapses the `reports`/`report_prompts` view to a typed-empty fallback rather than degrading per-CSV; missing CSVs degrade per-CSV with a warning. The common drift case (missing file) is handled; the rare corrupt-present case uses the whole-view fallback.

## As-Built Deviations

Small, deliberate structural differences from the implementation plan, each forced by the Go toolchain or a verified DuckDB behavior:

1. **Merge-compact orchestration lives in `internal/dataset/merge.go`, not `internal/store/merge.go`** — the pure `StreamMerge` and lock guards stay in `internal/store` (which imports nothing from `internal/dataset`), while the manifest/lock-holding orchestration is a set of `*dataset.Dataset` methods, the only cycle-free arrangement.
2. **`StreamMerge` returns a `store.MergeResult` struct** (per-run counts, dataset-level removed, total, columns) so the backlog sweep can attribute counts to each swept run's owning segment in one k-way pass.
3. **A `Download.ColumnOrder []string` manifest field was added** because DuckDB's `read_csv` `columns` parameter is applied positionally (verified empirically); the CSV column order is recorded and the report views emit the map in that order. This fixed a real scrambled-column bug the map-only design would have shipped.
4. **An `internal/reportview` package** shares the reports-listing JSON shaping between the CLI `--json` and the MCP `reports_list`/`reports_jobs` tools, guaranteeing payload parity.
5. **`WriteFileAtomic0600` uses `os.CreateTemp` (0600)** rather than a fixed-name `O_EXCL` temp, satisfying the never-broadly-created requirement while avoiding a fixed-name collision.

## Decisions

### Where do the coverage counts for answers/history come from on the wire?
**Context**: The manifest records coverage `{queried, with_data, empty}`, but the paged envelope was only `{items, next_page_token}` and the learner endpoint set is derived server-side, so "no answer" vs "not fetched" is not computable client-side.
**Options considered**:
- A) Extend the envelope with coverage metadata such as `total_endpoints`.
- B) Drop `queried`/`empty`; record only `with_data`.
- C) Add an endpoint-count field to `GET /reports/:id` metadata.

**Decision**: A, with the server change owned by a separate report-service spec. `serve_page` already holds the scratch's full endpoint set per page, so a constant `total_endpoints` field is a trivial additive change; C was rejected because a metadata-time count can disagree with the export-start snapshot. When present the CLI records `{queried: total_endpoints, with_data, empty}`; when absent it records `with_data` only and marks the rest unknown, never guessed.

### Which platforms must the v1 release pipeline ship?
**Context**: cgo/DuckDB forces native builds per platform; macOS needs signing/notarization; the design targets Windows at the code level but the release scope was unstated.
**Options considered**:
- A) macOS (arm64 + amd64, signed/notarized) + Linux amd64; Windows deferred.
- B) A + Windows amd64, unsigned.
- C) A + Windows amd64 + Linux arm64.

**Decision**: A, with a CI rider. Release targets are signed/notarized macOS arm64+amd64 and Linux amd64, matching the audience. CI additionally builds and tests (without releasing) Windows amd64 and Linux arm64 on every push; go-duckdb bundles static libs for all five, so promotion is a config change. Unsigned Windows was rejected for SmartScreen friction.

### What closes this story: internal dogfood or the full release pipeline?
**Context**: The ticket folds in the release pipeline, but external release is gated on STORY 2 (token UI, since shipped).
**Options considered**:
- A) Close at internal dogfood; release pipeline becomes a follow-on.
- B) Close when the release pipeline works end-to-end (signed artifacts + formula).
- C) Close at public researcher release.

**Decision**: B. The story closes when a tagged build produces signed/notarized macOS + Linux amd64 artifacts and the formula installs them; announcing is a rollout decision. C was rejected because it makes completion hinge on a comms act.

### Are `--latest-only` / `--latest-N` history modes in v1?
**Context**: The design named them as optional slimming flags; full series is the default.
**Options considered**:
- A) Ship full-series only; the flags are a follow-on.
- B) Include the flags, coordinating STORY 3 endpoint support.

**Decision**: Neither — the flags are dropped entirely. The shipped bulk-read walks the full series unconditionally (no mode parameter anywhere), so the flags would have required new server work in a Done story. Latest-only is redundant with `get answers`; last-N is one QUALIFY line over the `history` view. `history_mode` stays in the manifest as the constant `full` for forward compatibility.

### Which commands does `cc-data mcp` expose as MCP tools?
**Context**: Claude Desktop drives the CLI via MCP; `login` cannot work over stdio, and destructive ops from an LLM may deserve a guard.
**Options considered**:
- A) Everything except `login`/`logout`; destructive ops included.
- B) Reads + fetches + dataset create/show/list only.
- C) Read-only.

**Decision**: A, refined to the data-and-analysis surface with a server-enforced guard. Tools carry honest annotations, and `dataset_delete`/`dataset_purge` require an explicit `confirm: true` enforced server-side (host confirmation is only a SHOULD). `repl`, `mcp`, `init`, and `uninstall` stay terminal-only alongside `login`/`logout` (interactive / recursive / host-machine installer acts).

### Is the dataset root fixed at `~/cc-data` or configurable?
**Context**: A `data_root` setting is cheap now and painful to retrofit.
**Options considered**:
- A) Configurable `data_root` in `config.json`, defaulting to `~/cc-data`.
- B) Fixed `~/cc-data` for v1.

**Decision**: A, with a `CC_DATA_ROOT` env override (env wins) required for CI test isolation anyway. The default stays the visible `~/cc-data` (not an XDG dot-directory) so researchers see the sensitive data they hold; manifest paths are stored relative to the dataset folder so datasets and the root are freely movable.

### How should the multi-runner release be orchestrated: goreleaser Pro, free goreleaser + scripted assembly, or single-runner zig cross-compile?
**Context**: cgo/DuckDB forces native builds per platform, but one tagged release needs artifacts from three runners plus one Homebrew formula. goreleaser's split/merge feature is Pro (paid).
**Options considered**:
- A) goreleaser Pro with split/merge.
- B) Free goreleaser per-runner, then a final publish job assembling the release and rendering the formula.
- C) Single Linux runner cross-compiling all targets with `zig cc` and signing with `quill`.

**Decision**: B — no procurement dependency, no unproven toolchain on the signed-macOS critical path, and small inspectable scripting. Realized as thin per-platform configs rendered from one base config (`goreleaser release` has no free single-target mode).

### What is the production report server URL, and is one `server_url` config default enough?
**Context**: The design keys everything by portal but never states how the CLI locates the report server (a separate origin serving multiple portals).
**Options considered**:
- A) Single built-in production default + config override + `--server` on login; per-portal server recorded at login.
- B) A per-portal `servers` map with no built-in default.

**Decision**: A, with the constant `https://report-server.concord.org` (verified live and as the `PHX_HOST` default; staging `https://report-server.concordqa.org`). One deployment serves all portals via per-portal DB connections, so B's multi-server scenario does not exist today, and A's per-portal `Server` credential field already carries the association if it ever does.

### Does subtask (a) include a live end-to-end PKCE test against a locally running report-service, or is a fake-server integration test enough?
**Context**: The auth implementation carries a fake-server integration test either way; only a live handshake proves the two implementations agree. Neither deployed server carries the CLI-support code yet.
**Options considered**:
- A) Fake-server integration test only; first live verification is manual dogfooding.
- B) A plus a scripted Playwright check against a local report-service, part of subtask (a)'s definition of done.

**Decision**: B. report-service's `dev.exs` already authenticates a local server against the staging portal with the existing OAuth client, so the prerequisite is just the standard dev environment; and it is the only way to prove the real handshake before a server deploy. Scoped as "script exists, is documented, and has passed once".

### Self-review decisions (design changes adopted during the spec's review rounds)

The spec went through many adversarial self-review rounds; the following resolved findings each changed the design and were carried into the implementation.

**Storage / concurrency**
- **Identity keys encode length-prefixed** (`<decimal byte length>:<raw bytes>` per field), because the identity fields come from client-written Firestore docs and cannot be assumed control-byte-free; a crafted `question_id` must not collide two identities. Byte comparison gives a total order; the encoding never leaves memory.
- **The merge's durable-write order is pinned** (store rename → membership → manifest repoint → cursor `merged_as` → segment removal last) with an idempotent resume that converges every crash window; `WriteMembership`, `ReadManifest`, `CurrentStore`, and `MarkMerged` errors are all checked so a failed write/read aborts before the repoint rather than wiping the manifest or spinning the lock.
- **A per-download lock** (`seg_<type>_<run>.lock`) covers every `get` of a `(type, run)` for the command's lifetime, making the wholesale membership replacement safe; a second same-run command fails fast with "download busy".
- **Lock semantics pinned**: each lock is one process-wide guard per path (a sync primitive over a single flock), acquired once per critical section; inner helpers assert the guard rather than re-acquiring — closing both the vacuous-test and the deadlock traps for the long-lived MCP process.
- **A whole-fetch activity lock** (`.activity.lock`, reader-counted) added so mutating dataset commands can never interleave with a live fetch; lock files are never renamed or unlinked.
- **The segment sort is memory-bounded** to identity tuples plus a single-record buffer, streaming records back by offset, because a history segment is a run's entire snapshot series and can reach GBs.
- **Merge counts are crash-idempotent** via the cursor's `merged_as` (resume short-circuits with a zero-count already-merged result); two residual skews are accepted and named.
- **The write-amplification O(N^2) cost is stated and accepted**, mitigated by a lock-free backlog sweep that collapses only finished-but-unmerged segments whose per-download lock is free (never a live pull).

**DuckDB / query**
- **CSV column types are detected full-file at download** and the views read with `auto_detect=false` + the recorded column map, because DuckDB samples only 20,480 rows and the correctness of a sniff-based design rode on an unstated REPORT-58 server ORDER BY plus could hard-fail on a NULL-prompt column past the sample.
- **`report_type` becomes an owed server dependency with an allowlist**: the answers pseudo-header filter is metadata-driven and cast-defensive (`student_id::VARCHAR`), applied only to answers-type scans, because the five report shapes make an unconditional filter a binder or conversion error; unknown types are quarantined.
- **Store views use `read_json` with an explicit merge-derived column map** (never sampling inference), and decode failures never mix types (`_decode_error` + `_raw` sibling), because a late raw-string `report_state` or a late-appearing column would otherwise fail the whole view scan.
- **Empty inputs stay queryable**: every view registers with its full typed schema even with nothing to read (zero-row `VALUES`/unions/`read_text([])` are typed empties), because a fresh dataset or a zero-item fetch would otherwise make the dataset unqueryable.
- **Views bind at CREATE and re-bind per query**; a broken artifact degrades to a typed-empty form with a warning rather than aborting the session, and the allowlist governs every read user SQL performs.
- **`res_<N>` report columns are positional per run**; taught (not restricted) so cross-run reads use the position-independent stores plus `report_prompts`.
- **The sandbox resolves symlinks on the bundled DuckDB 1.5.4** (canonicalizes both allowlist sides), so the residual trust boundary is writable files, not a symlink escape; path discipline canonicalizes both the allowlist and embedded view literals, and every embedded literal is single-quote-doubled.
- **`attachment_states` is `read_text` + `TRY_CAST(content AS JSON)`** (five columns), not `read_json` schema inference, because inference over schema-divergent CODAP/SageModeler docs explodes columns and one malformed file would poison the view.

**API contract**
- **`Job.ID` is an integer** on the wire; the **history presign `doc_id` is `history_id`** (the embedded `id` is the copied answer doc's id — a trap); **history records do carry `source_key`** (the download-context stamp is a defensive fallback, and `source_key` is never used as the presign `source`); **`/auth/cli`'s `portal` param is optional-with-fallback**, validated only when present.

**Go / library**
- Moved to `duckdb/duckdb-go/v2` (the `marcboeker` path is deprecated); declared `go 1.25.0` (the MCP SDK floor); switched to `ergochat/readline` (upstream frozen); redirected `pkg/browser`'s launcher output to stderr so it never breaks the one-JSON-line stdout contract; and canonicalized manifest paths before embedding them under the sandbox.

**Security**
- All client-minted security tokens (PKCE verifier, state) come from `crypto/rand` (a source-level test asserts `math/rand` is not imported in `internal/auth`); sensitive `.tmp` files are created `0600` at open; non-login commands pin their origin to the credential's recorded server; the loopback listener serves until a state-matching callback (a mismatch cannot break login); the `--server` origin is allowlist-validated; and the MCP `get_attachments`/`query` tools exclude the capability-widening `url`/`inline`/allow-dir arguments.

**QA / DevOps**
- Crash injection uses a `testHookAfterWrite(n)` seam plus one subprocess SIGKILL case; the fake servers are pinned to live wire captures with their source commit; SQL identifier sanitization, the success-path stream discipline, CSV pseudo-header row-count exclusion, `repl` statement-splitting, and the MCP argument-schema exclusions all gained tests; and the release matrix was corrected to live GitHub runner labels (`macos-15`/`macos-15-intel`, Intel-image sunset fallback documented), with notarization run as a build post-hook before archiving (no stapling of a bare Mach-O).

**Cross-platform (Windows)**
- Fixed-name durable writes and their lockless reads go through robustio-style retry wrappers (Windows sharing violations); the `0600` permission assertions are gated to non-Windows (with an ACL check on the Windows leg); attachment filenames run through a deterministic sanitizer (reserved chars/device names/trailing dots, and no NTFS alternate-data-stream via `:`); the portal folder name encodes `:` → `_` (port-bearing dev portals); and `~` is `os.UserHomeDir()` on every platform (config/credentials under `%USERPROFILE%\.config\cc-data`).
