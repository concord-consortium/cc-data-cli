# cc-data-cli: the package commands and the dataset views that replace `_lib`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-146
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Researchers get four `cc-data package` commands (`init`, `run`, `build`, `publish`) for writing a Researcher Dashboard package on a laptop, testing it the way the dashboard's runner will run it, and publishing it to the package catalog with the cc-data token they already have. Two new dataset views (`run_answers` and `learner_endpoints`) replace the counting SQL the spike copied into every package.

## Project Owner Overview

The Researcher Dashboard runs analysis "packages" for a class on a short-lived virtual machine. In the spike, a package was built and uploaded by a script that called AWS directly. Each package also carried a shared library of SQL describing how cc-data lays out a dataset. As a result, developing a package needed AWS credentials, and the only way to find a mistake was to run the package on a VM.

This story moves the whole loop onto the researcher's laptop and onto the token they already use for cc-data. `cc-data package run` runs a package against their own dataset under the runner's rules: the same `scope.json`, the same output files and the same size limit on the result. It asks report-server, rather than a copy of its rules, whether the package is valid and whether it applies to the class. An author therefore hits these failures on their laptop, not on a VM. `cc-data package publish` registers the package in the catalog in one step. The shared SQL becomes two named views in cc-data itself, so a package writes plain SQL against stable names and carries no library. The first package built this way is the Wildfire package (REPORT-147), whose spec waits on this one.

**One rule, one home.** The package rules (what a valid package is) and applicability (whether a package suits a class) are decided by report-server alone. cc-data, the dashboard app and the runner ask it and do not keep their own copies. The new report-service routes this needs are one companion story (see Dependencies).

## Background

The design is `final-design.md` (global oob, `streams/researcher-dashboard/`) sections 5.5 (applicability by URL patterns), 5.7 (publishing and iterating), 10 (`manifest.json`, `scope.json`, the result a package writes) and 13 (data sources). The section numbers below are that document's.

**What `_lib` was.** On cc-data-studies' unmerged spike branch `RIGSE-365-analysis-packages` (`838a603`), `studies/_lib/reports.py` held three queries. `studies/_lib/ccdata.py` held the cc-data calls a class study makes: it creates the Student Answers and Student Actions runs, pulls them, and keeps run ids in `runs.json`. `publish-package.sh` copied `_lib/` into every archive. REPORT-147 builds on cc-data-studies `main`, where `_lib` never landed, so this story adds new views rather than replacing existing code. `_lib` is only the reference for what the views must cover (stream channel, 2026-10-07). The three queries are:

1. **Answers scoped to a run**: `answers JOIN run_membership USING (source_key, remote_endpoint, question_id) WHERE m.run_id = <run> AND m.type = 'answers'`. The query is written this way, and not against `answers_<run>`, because the per-run view exists only once the run has membership on disk. A class with no answers would then fail to bind instead of counting zero.
2. **Learners with at least one answer**: a `UNION ALL` over every `res_<N>_remote_endpoint` column of `report_<run>` (paired with `user_id`), joined to that run's answers, then `count(DISTINCT user_id)`. The column list differs from class to class, so the spike read it with `DESCRIBE` before building the SQL.
3. **Log freshness**: `count(*), max(event_time) FROM logs WHERE run_id = <run>`.

`ccdata.py`'s run creation and pulling stay package code. Section 13 says a package makes its own pulls, and the Jira story says the runner does not create a package's report runs. Nothing replaces that half in cc-data.

**What cc-data already has** (`internal/duck/views.go`, released in v0.2.0, which is `main` at `58c9172`):

- `run_membership` (run_id, type, identity columns), `answers`, `history`, `reports`, `report_<run>`, `answers_<run>`. Each has a typed-empty fallback, so they always bind.
- `logs`, with `run_id` and `event_time` (REPORT-112). The freshness query is already plain SQL against stable names, so query 3 needs no new view.
- A drift guard (`internal/guidance/guard_test.go`): every name in `StaticViewNames()` must be documented in `internal/guidance/src/core.md`'s Views section and in `docs/researcher-guide.md`'s "How datasets are organized" table, and the reverse. `cc-data query --help` and the MCP `query` tool description list the static views from the same function.

**What the runner does to a package.** This is RD-4 pass 2 as spec'd and built in its stage-verification throwaway build (`stage5-throwaway-build.patch`, researcher-dashboard branch oob `RD-4-pass-2-pull-loop`). It is not implemented on any branch yet.

- It validates `manifest.json`:
  - required fields;
  - `expected_duration_seconds` a positive integer and at most `PACKAGE_MAX_DURATION_SECONDS` (default 7200);
  - `clue_prepull` boolean if present;
  - `urls` keys only `all`, `any` and `none`, each a list of non-empty strings;
  - the entrypoint inside the package;
  - `name` equal to the last segment of the identity.
- When `urls` declares any pattern, it matches the patterns against the class profile's `assignment_urls` ∪ `interactive_urls`, using the glob of section 5.5. It refuses with "the package does not apply to this class: <reason naming the pattern>". It also refuses a package that declares patterns on a class with no profile yet.
- It writes `scope.json` with exactly `kind`, `id`, `classes`, `assignments`, `clue_source`, `dataset` (the bare name `pkg-<catalog id>`) and `output_dir`. It names the file in `RD_SCOPE_FILE`.
- It replaces the package's environment with `HOME`, `PATH=/usr/local/bin:/usr/bin:/bin`, `CC_DATA_ROOT`, `CC_DATA_PORTAL`, `CC_DATA_LOCAL`, `RD_DATASET` (`<portal host>/pkg-<catalog id>`), `RD_SCOPE_FILE`, `RD_DATA_DIR` and `RD_OUTPUT_DIR`, plus `RD_REPORT_SERVER_URL`, `RD_BUCKET` and `RD_STORAGE_PREFIX` when set.
- It runs a `.py` entrypoint with `python3.11`, and any other entrypoint directly. The bound is `expected_duration_seconds` plus a 10-minute margin.
- It empties the output directory before each run. Afterwards it reads, without following links:
  - `display.md`, required and refused over 65,536 bytes (never truncated);
  - `summary.txt`, whose first line, trimmed, is the summary (or null);
  - `counts.json`, for `answers`, `logs` and `log_freshness_at` only.

**What report-server accepts** (`server/lib/report_server/packages/*.ex` and `package_controller.ex` on report-service `master`, `067a1c4`, live on staging as `1.12.0-pre.2`):

- **The request.** `POST /api/v1/packages` behind the ordinary API token (the cc-data token). The body is the zip, with `Content-Type: application/zip` and a declared `Content-Length`. Query parameters are `origin=projects/<id>` (absent means the caller's own `users/<id>`) and `official=true|false`. Only a publisher may set `official`.
- **Archive limits.** At most 10 MiB, at most 50 MiB declared uncompressed in total, no absolute or `..` entry, exactly one root `manifest.json` of at most 64 KiB.
- **Manifest rules.**
  - `name` matches `^[a-z0-9][a-z0-9-]{0,62}$`.
  - `version` is `MAJOR.MINOR.PATCH[-prerelease]`, at most 64 characters.
  - `title` is non-empty, at most 200 characters.
  - `description` is optional: one line, at most 500 characters.
  - `urls` has only `all`/`any`/`none` arrays, each pattern non-empty with no whitespace or control characters and at most 256 characters, and at most 20 patterns in total.
  - `clue_prepull` is boolean.
  - `entrypoint` is a file in the archive.
  - `expected_duration_seconds` is 1 to 28,800.
  - No `owner`, `maintainer`, `origin`, `visibility`, `project` or `official` key.
- **On success (201).** The response is `{catalog_id, identity, version, checksum, visibility, official, current_version}`, where `checksum` is `sha256:<hex>` of the body.
- **On failure.** The flat envelope `{"error", "message"}` with `BAD_REQUEST`, `UNPROCESSABLE` (422), `FORBIDDEN`, `ALREADY_EXISTS` (409), `NOT_AUTHENTICATED` or `SERVICE_UNAVAILABLE`.
- **Visibility and the version pointer.** A first publish creates the package `private`. For a private package, every publish moves `current_version` to the new version.

## Dependencies

**A companion report-service story (to be created)** owns the routes cc-data calls. REPORT-146 codes against the contract below, which that story's spec may refine. Until those routes are deployed, `build` and `run` warn and proceed (R6, R12), so REPORT-146 is not blocked on it.

- **`POST /api/v1/packages/validate`** (cc-data token). The body is the raw zip, sent exactly as for publish, with `origin` as an optional query parameter. It runs every check `POST /api/v1/packages` runs, including permission and origin, and stops before writing any row or object. It answers 200 with `{identity, version, checksum, visibility}` (what a publish would record), or the same coded error a publish would give.
  - It is a separate route, not a `dry_run` parameter on publish. Today's publish ignores parameters it does not know, so a `dry_run=true` sent to a server without the feature would publish for real. A missing route answers 404.
- **`POST /api/v1/packages/applies`** (cc-data token, or the researcher token the runner holds). The body is JSON: `{urls: {all, any, none}, assignment_urls: [...], scope_urls: [...]}`.
  - **Assignment URLs** are followed by report-service's existing deriver (`deriveProfile` in `functions/src/researcher-dashboard/derive-profile.ts`). report-server reaches it through a new `derive_urls` route on the `api` function, the way it already calls `bulk_read`.
  - **Scope URLs** are matched as given; that is the runner's case, since it already holds the class profile.
  - **The answer** is `{applies, reason, interactive_urls, unread: [{url, reason}], truncated}`. The `reason` uses the runner's wording.
  - **The matcher** behind it is the only glob matcher in the system. The app gets the same answer through the package-list call it already makes.
- **report-server adopts the runner's stricter rules,** so that anything it accepts the runner will run: `expected_duration_seconds` at most 7,200 (today 28,800), and no symbolic-link entries in an archive.

## Requirements

### Dataset views

- **R1. `run_answers`.** A static view with every row of `answers`, plus `run_id`: one row for each Student Answers run whose membership (`run_membership.type = 'answers'`) holds that answer. Its columns are `run_id` (BIGINT) followed by the `answers` columns. Like every static view, it registers on every dataset: with no answers store or no membership it is empty with the same columns, never a bind error.
- **R2. `learner_endpoints`.** A static view with one row per learner per assignment per answers-type report CSV. Its columns are:
  - `run_id` (BIGINT);
  - `user_id` (BIGINT), the Portal user;
  - `offering_id` (BIGINT), from the same assignment's `res_<N>_offering_id`, NULL where the CSV has none;
  - `remote_endpoint` (VARCHAR), from `res_<N>_remote_endpoint`.

  It is built from every `res_<N>_remote_endpoint` column that each answers-type download records. It excludes the pseudo-header rows (`Prompt`, `Correct answer`) and rows whose endpoint is NULL. An endpoint ending in `/`, a learner with no secure key, is emitted as NULL, by the same rule `student_id_mapping` applies to `run_remote_endpoint`. With no answers-type report it is empty with the same four columns. A missing CSV contributes zero rows, with the existing missing-file warning.
- **R3. The documented counts reproduce `_lib`'s.** For a run that has both its report and its answers pulled, these queries return what `_lib.reports.answers_sql` and `learners_sql` return:
  - answers: `SELECT count(*) FROM run_answers WHERE run_id = <run>`;
  - learners: `SELECT count(DISTINCT e.user_id) FROM learner_endpoints e JOIN run_answers a USING (run_id, remote_endpoint) WHERE run_id = <run>`.

  The tests use a synthetic dataset built so that each wrong answer differs from the right one. It has:
  - a learner with answers in two assignments, so counting endpoints gives 2 where counting users gives 1;
  - a learner with a bare `.../` endpoint, who must not join;
  - an answer whose membership puts it in two runs, so it counts once per run;
  - a second answers-type run for another class, which must not leak into the first run's counts.
- **R4. Freshness needs no new view.** `logs.run_id` and `logs.event_time` already exist. The guidance documents the per-run form `SELECT count(*) AS logs, max(event_time) AS log_freshness_at FROM logs WHERE run_id = <run>`, the same names `counts.json` carries.
- **R5. Both views are documented where the drift guard checks.** That is `core.md`'s Views section and the researcher guide's table. Each entry gives the counting query of R3 and says why `learner_endpoints` counts by `user_id`. The `run_answers` entry also says that its `run_id` is membership (every run that holds the answer), while the store's own `_run_id` is only the run that last fetched it. Filtering `answers` by `_run_id` undercounts a run whose answers a later run re-fetched.

### Package contract checks

- **R6. The package rules are report-server's, and cc-data asks for them.**
  - **Where the check runs.** `build` and `run` send the zip they built to `POST /api/v1/packages/validate` before writing or running anything. A refusal keeps report-server's code and message and exits 5, and nothing is written or run.
  - **What cc-data keeps.** cc-data checks only what it must know to act: the manifest parses, the entrypoint is a file in the package, `expected_duration_seconds` is a positive integer (it sets `run`'s time bound), and `urls` and `clue_prepull` are read as written. It keeps no copy of report-server's limits.
  - **Before the route exists.** If report-server answers 404 (the route is not deployed yet), `build` and `run` print a warning that the package is unchecked until publish, and continue.
- **R7. Applicability is report-server's, and cc-data asks for it.** cc-data has no glob matcher and no copy of the pattern fixture. `run` asks `POST /api/v1/packages/applies` (R12).
- **R8. The display cap is 65,536 bytes.** The cap is counted in UTF-8 bytes of `display.md`. One Go constant holds it, and its comment cites `final-design.md` section 10 as the only place the number is decided. A test pins both edges: 65,536 bytes passes and 65,537 is refused. This is one of the runner behaviors `run` reproduces (R11 to R14), which have no server to ask, since the runner lives on a VM.

### `cc-data package init`

- **R9.** `cc-data package init [dir]` (default `.`) creates `dir` if needed and writes `manifest.json` and `run.py`. It reads nothing in the directory except to check that neither file already exists; it refuses, changing nothing, if either does.
  - The manifest has `name` (from `--name`, else the directory's base name when that matches the name grammar; otherwise `--name` is required), `title` (the name), `version` `0.1.0`, a placeholder one-line `description`, `urls` with empty `all`, `any` and `none`, `clue_prepull` `false`, `entrypoint` `run.py` and `expected_duration_seconds` `300`. report-server accepts it.
  - The stub is stdlib-only Python and is the smallest complete package. It reads `RD_SCOPE_FILE` and `RD_DATASET`.
    - **Runs.** It reuses a Student Answers run already in its dataset for the scope's class (a report row with that `class_id` in `reports`, joined type-qualified to `downloads`). Otherwise it creates one with `cc-data reports create --report-slug student-answers --report-filter '{"class":[<class_id>]}'`.
    - **Pulls and counts.** It pulls with `get report --refresh` and `get answers`, then counts with the R3 queries through `cc-data query`, never by locating dataset files.
    - **Guard.** It keeps the empty-scope guard that section 13 places in package code.
    - **Output.** It writes `display.md`, `summary.txt` and `counts.json` to the scope's `output_dir`.
    - **Limits it states.** A comment notes that it filters the whole class in one run (Athena fails above about four assignments; chunking is REPORT-149), and that it creates a run on the server the first time it meets a class.
  - **Why the stub creates runs.** On the VM its dataset starts empty, so a stub that only read an existing run would report nothing there. The VM path is what a package exists for.

### `cc-data package run`

- **R10. Inputs.** `cc-data package run [dir] --dataset <ref> --scope <file>`.
  - `--dataset` names the researcher's own dataset, resolved like every other dataset ref. `package run` neither requires that it exists nor creates it, since a package creates and fills its own dataset on the VM too.
  - `--scope` names a local scope file. It holds the four keys rigse supplies on the VM: `kind`, `id`, `classes` (`[{class_hash, class_id}]`) and `assignments` (`[{offering_id, runnable_id, name, url}]`). The shape is checked; any other key is refused.
  - `package init` does not write a scope file, so the file comes from the author.
- **R11. The package sees the runner's layout.** Before the entrypoint runs:
  - **A fresh run directory** under `dir/.cc-data-run/`, with `in/` and `out/`, both emptied. `.cc-data-run` and everything `package run` empties or creates under it must be a real directory, never a link. A link is refused before anything is removed, so a stray link can never point the emptying at a directory outside the package.
  - **A `.gitignore` containing `*`** written into `dir/.cc-data-run/`. A package directory is usually a git checkout (cc-data-studies ignores only `local-data/`, `__pycache__/` and `*.pyc`), and `out/display.md` holds counts or text drawn from student data.
  - **`in/scope.json`** in the runner's exact shape: the scope file's four keys; `clue_source` `"firebase"`; `dataset` as the ref's bare name, as the runner writes `pkg-<id>`; and `output_dir` as the absolute `out/` path.
  - **The runner's environment:**
    - `RD_DATASET`: the full ref.
    - `RD_SCOPE_FILE`.
    - `RD_OUTPUT_DIR`.
    - `RD_DATA_DIR` and `CC_DATA_LOCAL`: `dir/.cc-data-run/data/`.
    - `CC_DATA_ROOT`: the data root `package run` itself resolved.
    - `CC_DATA_PORTAL`: the ref's portal.
    - `RD_REPORT_SERVER_URL`: the portal's stored credential's server, when one is stored.
  - **A short passthrough list a laptop needs.** It covers `HOME`, `PATH`, `USER`, `LOGNAME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`, `DBUS_SESSION_BUS_ADDRESS` and `XDG_RUNTIME_DIR`, plus Windows's `SYSTEMROOT`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `PATHEXT` and `COMSPEC`. There is no Windows release, but CI builds and tests on `windows-2022`, and these variables are absent on Linux and macOS. These are the variables the package's own `cc-data` calls need to find the researcher's stored credential (the keychain or the credentials file) and to run at all. Nothing else is inherited.
  - **No storage variables.** `RD_BUCKET` and `RD_STORAGE_PREFIX` are never set.
- **R12. Applicability before the entrypoint.**
  - **The question asked.** When the manifest declares any pattern, `package run` sends the patterns and the scope's assignment URLs to `POST /api/v1/packages/applies`. report-server derives the interactive URLs inside those assignments, so the URLs matched are the ones the runner matches.
  - **A refusal.** If the package does not apply, `run` refuses with report-server's reason, prefixed as the runner prefixes it ("the package does not apply to this class: "), and does not start the entrypoint.
  - **Partial profiles.** Each URL the deriver could not read, and a truncated profile, is reported on stderr.
  - **The call's timeout** is 5 minutes, since the deriver may spend up to 240 seconds; the client's 60-second default would cut it off.
  - **Before the route exists (404).** `run` warns that applicability cannot be checked yet and that the VM's runner is the check, and runs the package.
- **R13. Execution.**
  - **The interpreter.** A `.py` entrypoint runs with `python3.11` when it is on `PATH`, else with `python3`, after a one-line note that the VM runs 3.11. Any other entrypoint is executed directly.
  - **The working directory** is a staged copy of exactly the files R17's `build` would include, at `dir/.cc-data-run/pkg/`, rebuilt on every run. The runner's working directory is the unpacked archive (`steps.js` `runPackage`, `cwd: manifest.dir`). A package that reads a file `build` leaves out, such as something under `local-data/`, therefore fails locally just as it would on the VM. The same collection refuses a link, so `run` and `build` cannot disagree about what the package is.
  - **Output streams.** The package's stdout and stderr stream to `package run`'s stderr.
  - **The time bound.** A run longer than `expected_duration_seconds` plus 10 minutes is killed and reported as having run past its bound, as the runner does.
  - **No sandbox.** No network namespace, no other uid and no egress proxy: those are the VM's, and the story forbids needing researcher-dashboard infrastructure locally.
- **R14. The result, read as the runner reads it.** After the entrypoint exits 0, the output is read without following links:
  - `display.md` is required, and is refused over R8's cap with its size and the limit (never truncated).
  - `summary.txt` is optional, and its first line, trimmed, is the summary.
  - `counts.json`, when present and valid, contributes `answers`, `logs` and `log_freshness_at` only.

  On success, stdout carries one JSON line: the display path and size in bytes, the summary, the counts and the elapsed seconds.
- **R15. A `clue_prepull: true` package is refused locally.** The message says CLUE data cannot be fetched by cc-data yet (section 13), so such a package runs only on the VM.
- **R16. Failures follow the CLI's exit-code contract.**
  - A manifest cc-data cannot act on, an invalid scope file, or a package tree `build` cannot collect (a link, a non-executable entrypoint), is a usage error (exit 2, codes `INVALID_MANIFEST`, `INVALID_SCOPE`, `INVALID_PACKAGE`).
  - A refusal from report-server's validate or applies route keeps the server's code and exits 5, as every server contract error does.
  - A package the scope does not satisfy, or a `clue_prepull` package, is `PACKAGE_REFUSED`.
  - A non-zero exit or a timeout is `PACKAGE_FAILED`.
  - A missing, linked or oversized `display.md` is `PACKAGE_OUTPUT_REFUSED`.

  The last three exit 1 with the single JSON error envelope on stdout.

### `cc-data package build`

- **R17. Contents.**
  - **What goes in:** `cc-data package build [dir] [--out <file>]` zips the regular files under `dir`.
  - **What is left out:** any path with a segment beginning with `.` (which covers `.cc-data-run/`, `.git/` and `.DS_Store`), `__pycache__/`, `*.pyc` and `local-data/`.
  - **What is refused:** a symbolic link, or any file that is neither regular nor a directory. Nothing outside `dir` is ever included, so no shared library can be vendored by the tool.
  - **Before writing:** `manifest.json` must be at the root, it must pass R6, and the entrypoint must be one of the files collected.
  - **Modes:** each file ships 0755 when any execute bit is set on disk, else 0644. `unzip` on the VM restores these, and the runner executes a non-`.py` entrypoint directly, so such an entrypoint must be executable or `run` and `build` refuse it. `run`'s staged copy uses the same modes. (Windows records no execute bit, so the check is skipped there.)
- **R18. Reproducible.** The same tree builds to the same bytes: entries in sorted order, a fixed modification time, and no extra attributes. The same package therefore always has the same checksum.
- **R19. `build` refuses what `publish` would refuse,** by asking report-server (R6). It takes `--portal` (default: the configured portal) and `--origin projects/<id>` so the check is made as the publish will be. A refused archive is not written.
- **R20. Output.**
  - The default output path is `dir/.cc-data-build/<name>-<version>.zip`. It sits inside an excluded dot directory, with the same `.gitignore` as R11's, so building from inside the package directory never feeds one build's zip into the next.
  - An explicit `--out` inside `dir` is refused unless R17 would exclude that path anyway.
  - stdout carries one JSON line: the path, `sha256:<hex>`, the size and the file count.

### `cc-data package publish`

- **R21. The request.**
  - **The command:** `cc-data package publish <zip> [--portal <portal|env>] [--origin projects/<id>] [--official] [--json]`.
  - **No local check:** publish sends the zip as it is. report-server's publish applies every rule, including the runner's stricter ones once the companion story lands, so a hand-made zip is held to the same rules as a built one.
  - **The upload:** `POST /api/v1/packages` on the portal's report server, with the portal's stored cc-data token. The body is the zip bytes, sent with `Content-Type: application/zip` and `Content-Length`. `origin` and `official=true` are sent only when given.
  - **Retries:** the request is never retried.
  - **Never S3:** it never talks to S3.
- **R22. Success is checked, not assumed.** On 201, the response's `checksum` must equal the local `sha256:<hex>` of the zip; a mismatch is an error. Output is the response: as JSON with `--json`, otherwise one line each for identity, version, visibility, official, current version and checksum.
- **R23. Errors pass through.** A server error keeps its code and message: `ALREADY_EXISTS`, `FORBIDDEN`, `UNPROCESSABLE`, `BAD_REQUEST` and `SERVICE_UNAVAILABLE` exit 5, and `NOT_AUTHENTICATED` exits 3. A failure where the server never answered carries an action: publishing again is safe, and `ALREADY_EXISTS` then means the first attempt landed.

### Documentation

- **R24.** The following are updated: the README's command sketch, a new researcher-guide section on developing and publishing a package, and the skill header's command list. `cc-data package <cmd> --help` explains the local/VM differences R11 and R13 leave (the passthrough variables, the interpreter, no sandbox).

## Done when

- A package made with `init`, edited and run with `package run` against a real dataset, produces `display.md`, `summary.txt` and `counts.json` under the runner's layout.
- `display.md` over 65,536 bytes fails `package run` at the same size the runner refuses.
- With the companion routes deployed: `build` and `run` refuse what report-server refuses, and `run` refuses a package report-server's matcher says does not apply. Before they are deployed, both warn and proceed.
- `cc-data package publish` creates a private package and a version row on staging, once staging's `PackageBuckets` is set.
- No package imports a shared library from its archive: `build` ships only the package's own files.
- Moved to REPORT-147 (Doug, 2026-10-08): a package built on a laptop runs on the VM and produces the same output.

## Technical Notes

- **Views live in `internal/duck/views.go`.** Add two builders to `viewSet.statements()`, after `runMembershipView` (`run_answers` reads `answers` and `run_membership`, so it must register after both). Use `vs.prefix` for every referenced view so multi-dataset sessions resolve to the right schema, as `perStoreView` does. `learner_endpoints` is built from the manifest (`dl.Columns`) at session open, so the per-class column list is known without a `DESCRIBE`. This is the one thing `_lib` needed a round trip for.
- **What the tests can see.** `StaticViewNames()` and the guard pick up both views automatically. `cmd/query.go`'s help and `internal/mcpserver/tools.go`'s `query` description list them with no edit.
- **Verified against real staging data.** On `learn.portal.staging.concord.org/staging-smoketest`, run 164: `_lib`'s answers SQL gives 5 and its learners SQL gives 2. The R3 forms, written as plain SQL over a hand-built `learner_endpoints` (`SELECT 164 AS run_id, user_id, res_1_offering_id, res_1_remote_endpoint FROM report_164`) and `run_answers`, give 5 and 2. `user_id` and `res_<N>_offering_id` scan as BIGINT and `res_<N>_remote_endpoint` as VARCHAR. Casts in the view keep that true for a CSV where a column is empty throughout.
- **The answers-type CSV also carries the class's shape**: `class_id`, and per assignment `res_<N>_name`, `res_<N>_offering_id` and `res_<N>_resource_url`. `runnable_id` and `class_hash` are not in it. A local scope therefore cannot be derived from a pulled report alone, which is why R10 takes the scope from a file.
- **report-server already calls report-service's `api` function** through `ReportServer.ReportService` (`server/lib/report_server/report_service.ex`), with the shared `REPORT_SERVICE_TOKEN`, for `bulk_read`, `fetch_attachment_meta`, `answer`, `plugin_states` and `resource`. The companion story's `derive_urls` route follows that same path, so `deriveProfile` stays the only copy of the walk and no new credential is created.
- **The publish request needs a new client method.** `internal/api/client.go`'s `do` always sends `Content-Type: application/json` with a body, so publishing needs a sibling that sends raw bytes with `application/zip`. `net/http` sets `Content-Length` for a `*bytes.Reader` body. `isIdempotent` already makes POST non-retried. `AsWriteCLIError` is the existing pattern for a write that must not invite a blind retry.
- **The Go `archive/zip` writer sets data-descriptor flag `0x8` and zero local-header sizes.** report-server's `Archive` reads offsets and sizes from the central directory for this reason (REPORT-142). The runner unpacks with `unzip`, which handles it. A reproducible build sets each header's `Modified` to a fixed time and uses `Deflate`.
- **cc-data releases.** The release target is `cc-data-cli 0.3.0` (the Jira fix version). The runner image pins `CC_DATA_REF=v0.2.0` (`runner/Dockerfile`), so the views reach a VM only when RD-4/RD-1 bump that pin to the release that carries them. The R11 environment adds nothing the runner does not already set.
- **What staging answers before `PackageBuckets` is set.** report-server validates the archive and manifest before it looks up the bucket (`Packages.publish`). Until the parameter is set for a portal server, a publish that passes every other check answers 422 `UNPROCESSABLE` "publishing is not configured for <server>". That answer is itself evidence that `publish`'s request was well formed.
- **Checked with throwaway code (stage 4), then deleted.**
  - **The pattern fixture, for the companion story.** RD-4 pass 2's `fixtures/url-patterns.json` holds 27 match cases and 12 applicability cases (sha256 `35a994a1...c420`); the stream docs say 26. A Go port of the runner's matcher passed all of them, and making `?` a wildcard failed the `a?c` case, so the fixture catches that mutation. The port was then dropped with the decision to match only in report-server, where this fixture belongs.
  - **The archive.** A Go `archive/zip` build with sorted entries, `Deflate` and a fixed `Modified` gives identical bytes after a source file's mtime changes. `unzip` restores each entry's mode, so a 0644 shell entrypoint is "Permission denied" after unpacking (R17's mode rule). `unzip -p` and Python's `zipfile` read it, and a symbolic link in the tree is refused during the walk. report-server's own tests already cover reading a Go-written zip (`server/test/support/fixtures/packages/class-counts-go-1.0.6.zip`).
  - **The views.** Both views were added to a scratch copy of `views.go` and run through the real engine:
    - On `staging-smoketest` they give 5 and 2 for run 164, as `_lib` does.
    - They register empty, with the stated columns, on a dataset with no answers (`log-test`).
    - They resolve per schema in a two-dataset session.
    - `dataset materialize` writes `learner_endpoints.parquet` (it declares CSV files). `run_answers` stays a join over the materialized `answers` and `run_membership`, and the counts are unchanged after materializing.
    - On a dataset whose CSV is malformed (`rd-inspect`, a pseudo-header row with one column too many), `learner_endpoints` fails at query time exactly as the existing `reports` view does.
    - The full test suite passes apart from the two documentation guards, which name exactly the two new views.
  - **One correction the prototype forced.** A CSV member's `run_id` is an integer literal and comes out INTEGER, so the view casts it to BIGINT to keep R2's types.
- **Out-of-band references.** The stream docs (`plan.md`, `fy26-sprint-26.md`, `speccing.md`, `final-design.md`) are global oob files under `streams/researcher-dashboard/`. RD-4 pass 2's throwaway build is branch oob `researcher-dashboard/RD-4-pass-2-pull-loop/stage-verification/`.

## Out of Scope

- The companion report-service story's routes and rule changes (Dependencies), and the dashboard app's and runner's use of them (RD-3, RD-4).
- MCP tools for the package commands; an agent can run the CLI.
- Changing a package's visibility, `official`, `archived` or `current_version` after publish (report-server's `POST /api/v1/packages/:kind/:owner_id/:name/:state`), and listing the catalog.
- Creating report runs or pulling data on a package's behalf, locally or on the VM.
- The VM's sandbox (network namespace, package uid, egress proxy) and the CLUE pre-pull.
- Bumping the runner image's cc-data pin; deleting `_lib` or the spike packages; a dashboard control that hands an author a ready-made scope file (RD-3).
- Recording the cc-data version in a package's result, which is the runner's image version (section 13).

## Open Questions

<!-- Requirements-focused questions only (scope, acceptance criteria, business rules).
     Implementation questions go in implementation.md. -->

### RESOLVED: Which URLs does `package run` match a package's patterns against?
**Context**: The runner matches against the class profile's `assignment_urls` ∪ `interactive_urls`, while `scope.json` carries only assignment URLs, so a pattern on an interactive (the Wildfire case) would be refused locally and accepted on the VM. The profile is derived by report-service's function from public authoring JSON.
**Options considered**:
- A) A Go port of the deriver's walk in cc-data.
- B) An author-supplied `profile_urls` list in the scope file.
- C) Assignment URLs only.
- D) Ask report-server, which reaches report-service's existing deriver.

**Decision**: D, taken further (Doug, 2026-10-08): report-server holds the only matcher, and cc-data, the dashboard app and the runner all ask it (Dependencies, `POST /api/v1/packages/applies`). The deriver keeps its one copy, and there is no glob matcher anywhere else. The app and the runner already call report-service at startup, so the call adds no new dependency for either. Before the route is deployed, `run` warns and proceeds (R12).

### RESOLVED: Where does the shared pattern fixture live?
**Decision**: The question dissolves under the decision above (Doug, 2026-10-08). With one matcher in report-server, `url-patterns.json` is an ordinary test file beside it in report-service, and nothing vendors a copy. cc-data has none.

### RESOLVED: How is "a package built on a laptop runs in the VM and produces the same output" verified?
**Decision**: REPORT-147 owns it (Doug, 2026-10-08). Its done-when already requires the Wildfire package to run "identically under `cc-data package run` on a laptop and on a VM", and it is the first story that can put a package on a VM. REPORT-146 closes on the criteria in "Done when" below.

### RESOLVED: Judgment call: doing applicability and the rules in report-server rather than in cc-data
**Options considered**:
- A) cc-data keeps Go copies of the manifest rules, the archive limits and the matcher, guarded by fixtures.
- B) cc-data asks report-server's validate and applies routes, keeping only what it needs to stage and start a package.

**Decision**: B (Doug, 2026-10-08, to remove DRY violations without multiplying stories). The copies already disagreed: report-server allows 28,800 seconds where the runner refuses over 7,200. The companion story makes report-server adopt the runner's stricter rules, so its answer is the runner's.

### RESOLVED: Judgment call: where a local scope comes from
**Options considered**:
- A) An author-supplied file (`--scope`) carrying the four keys rigse supplies.
- B) Synthesized from a pulled Student Answers report (`class_id`, and `res_<N>_offering_id`, `_name` and `_resource_url` per assignment).

**Decision**: A. The report has no `class_hash` or `runnable_id`, and the scope contract has no room for nulls in either. Any synthesized scope would differ from the VM's in exactly the fields a package might key on. A dashboard control that hands out the real scope is the eventual source (RD-3, out of scope).

### RESOLVED: Judgment call: a `clue_prepull` package under `package run`
**Options considered**:
- A) Refuse it locally (R15).
- B) Run it with `clue_source: "firebase"` and an empty corpus.

**Decision**: A. B tells the package something false, and the package would produce numbers the VM would not. Section 13 already says a CLUE package is the one package that cannot be developed locally until REPORT-109 to 111.

### RESOLVED: Low confidence: the passthrough environment in R11
**Context**: The runner replaces the environment entirely. Locally, a package's own `cc-data` calls must find the researcher's credential: the OS keychain via `go-keyring`, or `~/.config/cc-data/credentials.json`.
**Options considered**:
- A) The R11 allowlist.
- B) Inherit the whole environment and set the runner's variables over it.

**Decision**: A, verified on this machine (Linux, keyring backend). `env -i` with the R11 list, then `cc-data reports list --portal learn.concord.org --json`, reads the keyring token and lists runs. So does `env -i HOME PATH USER` alone: go-keyring reaches the session bus without `DBUS_SESSION_BUS_ADDRESS` here, so the D-Bus and XDG variables are kept only for setups that need them. On macOS go-keyring calls `/usr/bin/security`, which needs nothing beyond `HOME` and a `PATH` reaching `/usr/bin`. A cc-data query under the same `env -i` reads a dataset. B would let a package pass locally while depending on a variable the VM never sets.

### RESOLVED: Low confidence: `scope.json`'s `dataset` is a bare name
**Context**: `final-design.md` section 10 shows `"dataset": "<cc-data dataset ref>"`. RD-4 pass 2's `scopeFile` writes the bare `pkg-<id>`, while `RD_DATASET` carries the full ref. A package that passes `scope["dataset"]` to `cc-data query --dataset` resolves it under the configured default portal, which may not be the dataset's.
**Options considered**:
- A) Mirror the runner (bare name), have the `init` stub read `RD_DATASET`, and record the discrepancy for RD-4 and the design to settle.
- B) Write the full ref locally, and ask RD-4 to change `scopeFile` to match section 10.

**Decision**: A. This story's contract is "exactly as the runner", and B changes a contract RD-4 owns from a story that consumes it. If RD-4 changes `scopeFile`, R11's `dataset` follows it. Either way the `init` stub reads `RD_DATASET`, the one name that is a full ref on both sides.

## Self-Review

Roles: senior engineer, security engineer, QA engineer, education researcher, package author, operator. Every finding below was checked against the code before it was written down. Each had one defensible correction, which has been applied to the requirements above.

### Security Engineer

#### RESOLVED: Emptying the run directory could follow a link out of the package
R11 empties `dir/.cc-data-run/in` and `out` on every run. If `.cc-data-run` were a symbolic link to another directory, the emptying would delete under that target. Fixed in R11: every directory `package run` empties or creates must be a real directory, and a link is refused before anything is removed.

#### RESOLVED: `publish` would upload a hand-made zip the runner then refuses
report-server checks entry paths but not entry types, and the runner refuses a symbolic link after unpacking (RD-4's `checkTree`). So a zip made outside `build` could publish and then fail on every VM. Resolved by the companion story: report-server refuses link entries at publish, so the one check covers every zip.

### Education Researcher

#### RESOLVED: Run output written inside a git checkout
`out/display.md` is derived from student data and lands inside the package directory. cc-data-studies' `.gitignore` ignores only `local-data/`, `__pycache__/` and `*.pyc` (read 2026-10-07), so `git add -A` would stage it. Fixed in R11 and R20: each working directory gets a `.gitignore` of `*`.

#### RESOLVED: `_run_id` reads like run scoping and is not
The answers store stamps `_run_id` with the run that fetched each stored version (`internal/store/segment.go` `AppendPage`), and a later run's re-fetch overwrites it. Next to `run_answers.run_id`, a researcher could filter `answers` by `_run_id` and silently undercount. Fixed in R5's documentation requirement.

### Package Author

#### RESOLVED: The default build output would be swept into the next build
With the output in the current directory and `build` run from inside the package, as authors do, R20 as written either refused its own default or zipped the last build into the next. Fixed in R20: the default is `dir/.cc-data-build/`, which R17 already excludes.

#### RESOLVED: An `init` stub that only reads existing runs reports nothing on the VM
On the VM a package's dataset (`pkg-<catalog id>`) starts empty, and section 13 makes run creation package code. A stub that assumed a pulled run would pass locally and count zero on its first VM run. Fixed in R9: the stub reuses or creates its Student Answers run, as `_lib/ccdata.py` and RD-4's smoke package do.

### QA Engineer

#### RESOLVED: R3 could pass with a fixture that cannot tell the queries apart
With one assignment per learner, counting endpoints and counting users give the same number, so a broken learner view would pass. Fixed in R3: the synthetic dataset includes a two-assignment learner, a bare endpoint, an answer in two runs and a second class's run.

### Operator

#### RESOLVED: Staging's answer before `PackageBuckets` exists was unstated
`Store.bucket_for` returns 422 "publishing is not configured for <server>" after every archive and manifest check has passed. Recorded in Technical Notes, so that answer reads as a well-formed request rather than a failure of this story.
