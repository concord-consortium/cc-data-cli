# cc-data-cli: the package commands and the dataset views that replace `_lib`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-146

**Status**: **Closed**

## Overview

Researchers get four `cc-data package` commands (`init`, `run`, `build`, `publish`) for writing a Researcher Dashboard package on a laptop, testing it the way the dashboard's runner will run it, and publishing it to the package catalog with the cc-data token they already have. A new dataset view, `run_answers`, and documented joins on views cc-data already has replace the counting SQL the spike copied into every package.

## Requirements

### Dataset views

- **R1. `run_answers`.** A static view with every row of `answers`, plus `run_id`: one row for each run whose answers fetch holds that answer in membership (`run_membership.type = 'answers'`), whether a Student Answers or a Student ID Mapping run. Its columns are `run_id` (BIGINT) followed by the `answers` columns. Like every static view, it registers on every dataset: with no answers store or no membership it is empty with the same columns, never a bind error.
- **R2. Dropped (Doug, 2026-10-08): no `learner_endpoints` view.** It would have unioned each Student Answers CSV's `res_<N>_remote_endpoint` columns, so it is empty for a Student ID Mapping run and rests on the route REPORT-147 measured as failing on real classes. Nothing uses it, so nothing builds it.
- **R3. The documented counts.** For a run whose answers were fetched:
  - answers: `SELECT count(*) FROM run_answers WHERE run_id = <run>`;
  - learners with at least one answer, for a Student ID Mapping run: `SELECT count(DISTINCT m.user_id) FROM student_id_mapping m JOIN run_answers a ON a.remote_endpoint = m.run_remote_endpoint WHERE a.run_id = <run>`, by `user_id` and never by endpoint, since a student has one endpoint per assignment. It is scoped by the answers' run only: `student_id_mapping` keeps each learner's row from whichever mapping run was fetched last, so filtering it by `run_id` drops a learner another mapping run of the class also holds.

  The tests run both on a synthetic dataset built so that each wrong answer differs from the right one. It has:
  - a learner with answers in two assignments, so counting endpoints gives 2 where counting users gives 1;
  - a learner with a bare `.../` endpoint, who must not join;
  - an answer whose membership puts it in two runs, so it counts once per run, the second run being a later mapping run of the same class that holds that learner's row in `student_id_mapping`;
  - a history membership for one of the answers, which an untyped join would count;
  - a second Student ID Mapping run for another class, which must not leak into the first run's counts.

- **R4. Freshness needs no new view.** `logs.run_id` and `logs.event_time` already exist. The guidance documents the per-run form `SELECT count(*) AS logs, max(event_time) AS log_freshness_at FROM logs WHERE run_id = <run>`, the same names `counts.json` carries.
- **R5. `run_answers` is documented where the drift guard checks,** in `core.md`'s Views section and the researcher guide's table. The guidance gives R3's learner count through `student_id_mapping`, says why it counts by `user_id`, and says why a Student ID Mapping run is the route for answers and learners (computed live, no assignment limit). The `run_answers` entry also says that its `run_id` is membership (every run that holds the answer), while the store's own `_run_id` is only the run that last fetched it. Filtering `answers` by `_run_id` undercounts a run whose answers a later run re-fetched.

### Package contract checks

- **R6. The package rules are report-server's, and cc-data asks for them.**
  - **Where the check runs.** `build` and `run` send the zip they built to `POST /api/v1/packages/validate` before writing or running anything. A refusal keeps report-server's code and message and exits 5, and nothing is written or run.
  - **What cc-data keeps.** Besides `init`'s name check (R9), which runs before report-server has seen anything and is one of the copies `final-design.md` section 17 records as deliberate, cc-data checks only what it must know to act: the manifest parses, the entrypoint is a file in the package, `expected_duration_seconds` is a positive integer (it sets `run`'s time bound), and `urls` and `clue_prepull` are read as written. It keeps no copy of report-server's limits.
  - **Before the route exists.** If report-server answers 404 (the route is not deployed yet), `build` and `run` print a warning that the package is unchecked until publish, and continue.
- **R7. Applicability is report-server's, and cc-data asks for it.** cc-data has no glob matcher and no copy of the pattern fixture. `run` asks `POST /api/v1/packages/applies` (R12).
- **R8. The display cap is 65,536 bytes.** The cap is counted in UTF-8 bytes of `display.md`. One Go constant holds it, and its comment cites `final-design.md` section 10 as the only place the number is decided. A test pins both edges: 65,536 bytes passes and 65,537 is refused. This is one of the runner behaviors `run` reproduces (R11 to R14), which have no server to ask, since the runner lives on a VM.

### `cc-data package init`

- **R9.** `cc-data package init [dir]` (default `.`) creates `dir` if needed and writes `manifest.json` and `run.py`. It reads nothing in the directory except to check that neither file already exists; it refuses, changing nothing, if either does.
  - The manifest has `name` (from `--name`, else the directory's base name when that matches the name grammar; otherwise `--name` is required), `title` (the name), `version` `0.1.0`, a placeholder one-line `description`, `urls` with empty `all`, `any` and `none`, `clue_prepull` `false`, `entrypoint` `run.py` and `expected_duration_seconds` `300`. report-server accepts it.
  - The stub is stdlib-only Python and is the smallest complete package. It reads `RD_SCOPE_FILE` and `RD_DATASET`.
    - **Runs.** It takes the researcher's newest Student ID Mapping run filtered to exactly the scope's class (from `cc-data reports list --json`: slug `student-id-mapping`, `report_filter.filters` equal to `["class"]` and `report_filter.class` equal to `[<class_id>]`), as REPORT-147's R7 does. Otherwise it creates one with `cc-data reports create --report-slug student-id-mapping --report-filter '{"class":[<class_id>]}'`.
    - **Pulls and counts.** It re-reads the run with `get report --refresh` (computed live, so learners who joined since appear) and pulls `get answers` on it, then counts with the R3 queries through `cc-data query`, never by locating dataset files.
    - **Guard.** It keeps the empty-scope guard that section 13 places in package code: no rows in the run's own `report_<run>` is an error, which, unlike `student_id_mapping`, holds every learner of that run.
    - **Output.** It writes `display.md`, `summary.txt` and `counts.json` to the scope's `output_dir`.
  - **Why the stub creates runs.** On the VM its dataset starts empty, so a stub that only read an existing run would report nothing there. The VM path is what a package exists for.

### `cc-data package run`

- **R10. Inputs.** `cc-data package run [dir] --dataset <ref> --scope <file>`.
  - `--dataset` names the researcher's own dataset, resolved like every other dataset ref. `package run` neither requires that it exists nor creates it, since a package creates and fills its own dataset on the VM too.
  - `--scope` names a local scope file. It holds the four keys rigse supplies on the VM: `kind`, `id`, `classes` (`[{class_hash, class_id}]`) and `assignments` (`[{offering_id, runnable_id, name, url}]`). The shape is checked; any other key is refused.
  - `package init` does not write a scope file, so the file comes from the author.
- **R11. The package sees the runner's layout.** Before the entrypoint runs:
  - **A fresh run directory** under `dir/.cc-data-run/`, with `in/` and `out/`, both emptied. `.cc-data-run` and everything `package run` empties or creates under it must be a real directory, never a link. A link is refused before anything is removed, so a stray link can never point the emptying at a directory outside the package, and a `.gitignore` that is a link is refused rather than written through.
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
  - **A short passthrough list a laptop needs.** It covers `HOME`, `PATH`, `USER`, `LOGNAME`, `LANG`, `LC_*`, `TZ`, `TMPDIR`, `DBUS_SESSION_BUS_ADDRESS`, `XDG_RUNTIME_DIR` and the proxy variables `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` in either case (the runner sets the first two when it has a proxy, and `NO_PROXY` keeps a laptop's exemptions with them), plus Windows's `SYSTEMROOT`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `PATHEXT` and `COMSPEC`. There is no Windows release, but CI builds and tests on `windows-2022`, and these variables are absent on Linux and macOS. These are the variables the package's own `cc-data` calls need to find the researcher's stored credential (the keychain or the credentials file), to reach the server, and to run at all. Nothing else is inherited.
  - **No storage variables.** `RD_BUCKET` and `RD_STORAGE_PREFIX` are never set.
- **R12. Applicability before the entrypoint.**
  - **The question asked.** When the manifest declares any pattern, `package run` sends the patterns and the scope's assignment URLs to `POST /api/v1/packages/applies`. report-server derives the interactive URLs inside those assignments, so the URLs matched are the ones the runner matches.
  - **A refusal.** If the package does not apply, `run` refuses with report-server's reason, prefixed as the runner prefixes it ("the package does not apply to this class: "), and does not start the entrypoint.
  - **Partial profiles.** Each URL the deriver could not read, and a truncated profile, is reported on stderr.
  - **The call's timeout** is 5 minutes, since report-server waits up to 270 seconds for a deriver that may spend 240; the client's 60-second default would cut it off.
  - **Before the route exists (404).** `run` warns that applicability cannot be checked yet and that the VM's runner is the check, and runs the package.
- **R13. Execution.**
  - **The interpreter.** A `.py` entrypoint runs with `python3.11` when it is on `PATH`, else with `python3`, after a one-line note that the VM runs 3.11. Any other entrypoint is executed directly.
  - **The working directory** is a staged copy of exactly the files R17's `build` would include, at `dir/.cc-data-run/pkg/`, rebuilt on every run. The runner's working directory is the unpacked archive (`steps.js` `runPackage`, `cwd: manifest.dir`). A package that reads a file `build` leaves out, such as something under `local-data/`, therefore fails locally just as it would on the VM. The same collection refuses a link, so `run` and `build` cannot disagree about what the package is.
  - **Output streams.** The package's stdout and stderr stream to `package run`'s stderr.
  - **The time bound.** A run longer than `expected_duration_seconds` plus 10 minutes is killed and reported as having run past its bound, as the runner does.
  - **Nothing left behind.** When the entrypoint exits, anything still running in its process group is killed before the output is read, as the runner kills every process of the package's uid (RD-4 pass 2's R28a). A background process the package started therefore neither outlives the run nor writes into a later one. A process that leaves the group (`setsid`, a daemon) escapes this locally, as it escapes the time bound.
  - **An interrupt.** Ctrl-C (or `SIGINT`) during the run kills the package and everything it started, and is reported as interrupted. The package runs in its own process group so the bound can kill its children, which also puts it out of reach of the terminal's own Ctrl-C.
  - **No sandbox.** No network namespace, no other uid and no egress proxy: those are the VM's, and the story forbids needing researcher-dashboard infrastructure locally.
- **R14. The result, read as the runner reads it.** After the entrypoint exits 0, the output is read without following links:
  - `display.md` is required, and is refused over R8's cap with its size and the limit (never truncated).
  - `summary.txt` is optional, and its first line, trimmed, is the summary. A `summary.txt` that is a link or not a file refuses the result, as the runner's reader does.
  - `counts.json`, when present and valid, contributes `answers`, `logs` and `log_freshness_at` only. One that is a link, not a file or not JSON is ignored, as on the VM.

  On success, stdout carries one JSON line: the display path and size in bytes, the summary, the counts and the elapsed seconds.
- **R15. A `clue_prepull: true` package is refused locally.** The message says CLUE data cannot be fetched by cc-data yet (section 13), so such a package runs only on the VM.
- **R16. Failures follow the CLI's exit-code contract.**
  - A manifest cc-data cannot act on, an invalid scope file, or a package tree `build` cannot collect (a link, a non-executable entrypoint), is a usage error (exit 2, codes `INVALID_MANIFEST`, `INVALID_SCOPE`, `INVALID_PACKAGE`).
  - A refusal or failure from report-server's validate or applies route keeps the server's code and exit class: 5 for a contract error, 3 for `NOT_AUTHENTICATED`, as every server call does.
  - A package the scope does not satisfy, or a `clue_prepull` package, is `PACKAGE_REFUSED`.
  - A non-zero exit, a timeout or an interrupt is `PACKAGE_FAILED`.
  - A missing, linked or oversized `display.md`, or a linked `summary.txt`, is `PACKAGE_OUTPUT_REFUSED`.

  The last three exit 1 with the single JSON error envelope on stdout.

### `cc-data package build`

- **R17. Contents.**
  - **What goes in:** `cc-data package build [dir] [--out <file>]` zips the regular files under `dir`.
  - **What is left out:** any path with a segment beginning with `.` (which covers `.cc-data-run/`, `.git/` and `.DS_Store`), `__pycache__/`, `*.pyc` and `local-data/`.
  - **What is refused:** a symbolic link, or any file that is neither regular nor a directory. Nothing outside `dir` is ever included, so no shared library can be vendored by the tool.
  - **Before writing:** `manifest.json` must be at the root, it must pass R6, and the entrypoint must be one of the files collected.
  - **Modes:** each file ships 0755 when any execute bit is set on disk, else 0644. `unzip` on the VM restores these, and the runner executes a non-`.py` entrypoint directly, so such an entrypoint must be executable or `run` and `build` refuse it. `run`'s staged copy uses the same modes. (Windows records no execute bit, so the check is skipped there.)
- **R18. Reproducible.** The same tree builds to the same bytes: entries in sorted order, a fixed modification time (which Go's writer also records as an extended-timestamp extra field), and nothing that varies between builds. The same package therefore always has the same checksum.
- **R19. `build` refuses what `publish` would refuse,** by asking report-server (R6). It takes `--portal` (default: the configured portal), `--origin projects/<id>` and `--official` so the check is made as the publish will be. A refused archive is not written. When validate answers `already_published`, `build` still writes the zip and warns "<identity> <version> is already published; publish will refuse it until the version changes". When `publishing_unavailable` names a reason, `build` warns "this package cannot be published yet: <reason>" and still writes the zip. `run` ignores both, since running a package needs neither a new version nor a bucket.
- **R20. Output.**
  - The default output path is `dir/.cc-data-build/<name>-<version>.zip`. It sits inside an excluded dot directory, which must be a real directory as R11's are, with the same `.gitignore` as R11's, so building from inside the package directory never feeds one build's zip into the next. A name or version holding a path separator is `INVALID_MANIFEST` rather than a path, since it is unchecked until validate answers, and for good on a server without the route.
  - An explicit `--out` inside `dir` is refused unless R17 would exclude that path anyway.
  - stdout carries one JSON line: the path, `sha256:<hex>`, the size and the file count.

### `cc-data package publish`

- **R21. The request.**
  - **The command:** `cc-data package publish <zip> [--portal <portal|env>] [--origin projects/<id>] [--official] [--json]`.
  - **No local check:** publish sends the zip as it is. report-server's publish applies every rule, including the runner's stricter ones once REPORT-167 lands, so a hand-made zip is held to the same rules as a built one.
  - **The upload:** `POST /api/v1/packages` on the portal's report server, with the portal's stored cc-data token. The body is the zip bytes, sent with `Content-Type: application/zip` and `Content-Length`. `origin` and `official=true` are sent only when given.
  - **Retries:** the request is never retried.
  - **Never S3:** it never talks to S3.
- **R22. Success is checked, not assumed.** On 201, the response's `checksum` must equal the local `sha256:<hex>` of the zip; a mismatch is an error. Output is the response: as JSON with `--json`, otherwise one line each for identity, version, visibility, official, current version and checksum.
- **R23. Errors pass through.** A server error keeps its code and message: `ALREADY_EXISTS`, `FORBIDDEN`, `UNPROCESSABLE`, `BAD_REQUEST` and `SERVICE_UNAVAILABLE` exit 5, and `NOT_AUTHENTICATED` exits 3. A failure where the server never answered carries an action: publishing again is safe, and `ALREADY_EXISTS` then means the first attempt landed.

### Documentation

- **R24.** The following are updated: the README's command sketch, a new researcher-guide section on developing and publishing a package, and the skill header's command list. `cc-data package <cmd> --help` explains the local/VM differences R11 and R13 leave (the passthrough variables, the interpreter, no sandbox).

### Release

- **R25. A pre-release tag is safe to cut** (Doug, 2026-10-08, stream channel #21 and #22). After this story merges, `main` is tagged `v0.3.0-pre.1`, and RD-4 pass 2's runner image and REPORT-147's publish workflow pin that exact tag, rather than waiting for 0.3.0, whose fix version also holds the CLUE export chain.
  - **The release workflow tells the two kinds of tag apart.** A tag with a semver pre-release suffix (a `-` after the version) still builds, checks and uploads every archive and its `.sha256`, and creates a GitHub release, marked as a pre-release so GitHub never shows it as the latest.
  - **Homebrew never sees it.** The formula step is skipped for a pre-release, so `brew upgrade` never hands it to researchers.
  - **One classification.** Both steps read one decision made once in the workflow, and a test pins both uses.
  - The change ships in this story's PR, so the first tag after merge is already safe.

### Done when

- A package made with `init`, edited and run with `package run` against a real dataset, produces `display.md`, `summary.txt` and `counts.json` under the runner's layout.
- `display.md` over 65,536 bytes fails `package run` at the same size the runner refuses.
- With REPORT-167's routes deployed: `build` and `run` refuse what report-server refuses, and `run` refuses a package report-server's matcher says does not apply. Before they are deployed, both warn and proceed.
- `cc-data package publish` creates a private package and a version row on staging (whose `PackageBuckets` was set 2026-10-08, stream channel #34).
- No package imports a shared library from its archive: `build` ships only the package's own files.
- After merge, `main` is tagged `v0.3.0-pre.1`, and its GitHub release is marked as a pre-release, carries `cc-data_0.3.0-pre.1_linux_amd64.tar.gz` and its `.sha256` (which REPORT-147's workflow downloads), and pushed no Homebrew formula.
- Moved to REPORT-147 (Doug, 2026-10-08): a package built on a laptop runs on the VM and produces the same output.

## Technical Notes

### Dependencies

**REPORT-167** (report-service: one home for the package rules and applicability, created 2026-10-08) owns the routes cc-data calls. REPORT-146 codes against the contract below, which that story's spec may refine. Until those routes are deployed, `build` and `run` warn and proceed (R6, R12), so REPORT-146 is not blocked on it.

- **`POST /api/v1/packages/validate`** (cc-data token). The body is the raw zip, sent exactly as for publish, with `origin` and `official` as optional query parameters, as publish takes them. It runs every check `POST /api/v1/packages` runs, including permission and origin, and stops before writing any row or object. It answers 200 with `{identity, version, checksum, visibility, already_published}` (what a publish would record now), or the same coded error a publish would give for any other refusal. **A version that is already published is reported, not refused:** `already_published` is true and the answer is still 200, while only the publish itself answers `ALREADY_EXISTS` (REPORT-167's R12, refined after REPORT-146's first spec commit). **A portal that cannot store packages yet is reported, not refused:** `publishing_unavailable` is `null` or publish's own message (today only "publishing is not configured for <server>", before the portal has a bucket in `PackageBuckets`), the answer is still 200, and every other check still runs; only the publish answers that 422. Without this, `package run` would refuse every package on staging until its bucket is set (REPORT-167, `1ff2fc0`). The answer is thus `{identity, version, checksum, visibility, already_published, publishing_unavailable}`.
  - It is a separate route, not a `dry_run` parameter on publish. Today's publish ignores parameters it does not know, so a `dry_run=true` sent to a server without the feature would publish for real. A missing route answers 404.
- **`POST /api/v1/packages/applies`** (cc-data token, or the researcher token the runner holds). The body is JSON: `{urls: {all, any, none}, assignment_urls: [...], scope_urls: [...]}`.
  - **Assignment URLs** are followed by report-service's existing deriver (`deriveProfile` in `functions/src/researcher-dashboard/derive-profile.ts`). report-server reaches it through a new `derive_urls` route on the `api` function, the way it already calls `bulk_read`.
  - **Scope URLs** are matched as given; that is the runner's case, since it already holds the class profile.
  - **`urls` is required**: a missing or `null` `urls` is 400 `BAD_REQUEST`, while a `null` group inside it counts as empty. cc-data always sends it.
  - **The answer** is `{applies, reason, interactive_urls, unread: [{url, reason}], truncated}`. The `reason` uses the runner's wording.
  - **The matcher** behind it is the only glob matcher in the system. The app gets the same answer through the package-list call it already makes.
- **One contract fixture** of identity, version and package-key cases lives in report-service, asserted by report-server, the function, the runner and the app. cc-data's only copy of that grammar is `init`'s name check (R9), kept on purpose.
- **Neither route answers 404 on a report-server that has it** (REPORT-167). So a 404 still means only that the route is not deployed. A deriver failure answers 400 or 503, and report-server waits up to 270 seconds for the deriver, inside cc-data's 5-minute timeout.
- **report-server adopts the runner's stricter rules,** so that anything it accepts the runner will run: `expected_duration_seconds` at most 7,200 (today 28,800), and no symbolic-link entries in an archive.

### Notes

- **Views live in `internal/duck/views.go`.** Add one builder to `viewSet.statements()`, after `runMembershipView` (`run_answers` reads `answers` and `run_membership`, so it must register after both). Use `vs.prefix` for every referenced view so multi-dataset sessions resolve to the right schema, as `perStoreView` does.
- **What the tests can see.** `StaticViewNames()` and the guard pick up the view automatically. `cmd/query.go`'s help and `internal/mcpserver/tools.go`'s `query` description list it with no edit.
- **Verified against real staging data.** On `learn.portal.staging.concord.org/staging-smoketest`, run 164: `_lib`'s answers SQL gives 5, and `run_answers` gives 5. REPORT-147 measured the Student ID Mapping learner join against the Student Answers union on production for both Wildfire classes (runs 2390 to 2393): the two count the same today.
- **The answers-type CSV also carries the class's shape**: `class_id`, and per assignment `res_<N>_name`, `res_<N>_offering_id` and `res_<N>_resource_url`. `runnable_id` and `class_hash` are not in it. A local scope therefore cannot be derived from a pulled report alone, which is why R10 takes the scope from a file.
- **report-server already calls report-service's `api` function** through `ReportServer.ReportService` (`server/lib/report_server/report_service.ex`), with the shared `REPORT_SERVICE_TOKEN`, for `bulk_read`, `fetch_attachment_meta`, `answer`, `plugin_states` and `resource`. REPORT-167's `derive_urls` route follows that same path, so `deriveProfile` stays the only copy of the walk and no new credential is created.
- **The publish request needs a new client method.** `internal/api/client.go`'s `do` always sends `Content-Type: application/json` with a body, so publishing needs a sibling that sends raw bytes with `application/zip`. `net/http` sets `Content-Length` for a `*bytes.Reader` body. `isIdempotent` already makes POST non-retried. `AsWriteCLIError` is the existing pattern for a write that must not invite a blind retry.
- **The Go `archive/zip` writer sets data-descriptor flag `0x8` and zero local-header sizes.** report-server's `Archive` reads offsets and sizes from the central directory for this reason (REPORT-142). The runner unpacks with `unzip`, which handles it. A reproducible build sets each header's `Modified` to a fixed time and uses `Deflate`.
- **cc-data releases.** The Jira fix version stays `cc-data-cli 0.3.0`, but the view and the package commands reach a VM through the pre-release tag `v0.3.0-pre.1` (R25). The runner image pins `CC_DATA_REF=v0.2.0` (`runner/Dockerfile`) and builds cc-data from the git tag, so RD-4 pass 2 raises that pin to the tag (channel #15 and #21). REPORT-147's publish workflow downloads the tag's linux/amd64 archive instead, which is why the pre-release must carry its assets. The R11 environment adds nothing the runner does not already set.
- **What staging answers before `PackageBuckets` is set.** report-server validates the archive and manifest before it looks up the bucket (`Packages.publish`). Until the parameter is set for a portal server, a publish that passes every other check answers 422 `UNPROCESSABLE` "publishing is not configured for <server>". That answer is itself evidence that `publish`'s request was well formed.
- **Checked with throwaway code (stage 4), then deleted.**
  - **The pattern fixture, for REPORT-167.** RD-4 pass 2's `fixtures/url-patterns.json` holds 27 match cases and 12 applicability cases (sha256 `35a994a1...c420`); the stream docs say 26. A Go port of the runner's matcher passed all of them, and making `?` a wildcard failed the `a?c` case, so the fixture catches that mutation. The port was then dropped with the decision to match only in report-server, where this fixture belongs.
  - **The archive.** A Go `archive/zip` build with sorted entries, `Deflate` and a fixed `Modified` gives identical bytes after a source file's mtime changes. `unzip` restores each entry's mode, so a 0644 shell entrypoint is "Permission denied" after unpacking (R17's mode rule). `unzip -p` and Python's `zipfile` read it, and a symbolic link in the tree is refused during the walk. report-server's own tests already cover reading a Go-written zip (`server/test/support/fixtures/packages/class-counts-go-1.0.6.zip`).
  - **The view.** `run_answers` was added to a scratch copy of `views.go` and run through the real engine: 5 answers for run 164 on `staging-smoketest`, as `_lib` gives; empty with its columns on a dataset with no answers (`log-test`); resolved per schema in a two-dataset session; and, after `dataset materialize`, a join over the materialized `answers` and `run_membership` with unchanged counts. The full test suite passed apart from the two documentation guards, which named exactly the new view. (A `learner_endpoints` view was also prototyped and verified, then dropped with R2.)
  - **The stub.** It was run through `package run`'s engine against a synthetic dataset with a Student ID Mapping run and its answers, with a wrapper `cc-data` faking only the server calls. Out of a `reports list` holding a run filtered by class and school, a Student Answers run and another class's run, it picked the class-only Student ID Mapping run, and wrote 3 answers and 2 learners (a learner in two assignments counted once, a learner with no answers not at all). With no matching run it created one with the class filter, and with no rows it stopped at the empty-scope guard's message.
- **Out-of-band references.** The stream docs (`plan.md`, `fy26-sprint-26.md`, `speccing.md`, `final-design.md`) are global oob files under `streams/researcher-dashboard/`. RD-4 pass 2's throwaway build is branch oob `researcher-dashboard/RD-4-pass-2-pull-loop/stage-verification/`.

## Out of Scope

- REPORT-167's routes and rule changes (Dependencies), and the dashboard app's and runner's use of them (RD-3, RD-4).
- MCP tools for the package commands; an agent can run the CLI.
- Changing a package's visibility, `official`, `archived` or `current_version` after publish (report-server's `POST /api/v1/packages/:kind/:owner_id/:name/:state`), and listing the catalog.
- Creating report runs or pulling data on a package's behalf, locally or on the VM.
- The VM's sandbox (network namespace, package uid, egress proxy) and the CLUE pre-pull.
- Bumping the runner image's cc-data pin; deleting `_lib` or the spike packages; a dashboard control that hands an author a ready-made scope file (RD-3).
- Recording the cc-data version in a package's result, which is the runner's image version (section 13).

## Not Yet Implemented

- Tagging `main` as `v0.3.0-pre.1` after this story merges, and checking that its GitHub release is marked as a pre-release with the linux/amd64 archive and its `.sha256` and no Homebrew formula (R25, Done when). It is a release step after the merge, not code on the branch.
- Publishing a private package and a version row on staging with `cc-data package publish` (Done when). It runs against staging once the branch is released.
- `build` and `run` refusing what report-server refuses, and `run` refusing a package that does not apply, as observed against a live server (Done when): both wait on REPORT-167's routes being deployed, and until then both warn and proceed.
- A package built on a laptop producing the same output on the VM: moved to REPORT-147 (Doug, 2026-10-08).

## Decisions

### Which URLs does `package run` match a package's patterns against?
**Context**: The runner matches against the class profile's `assignment_urls` ∪ `interactive_urls`, while `scope.json` carries only assignment URLs, so a pattern on an interactive (the Wildfire case) would be refused locally and accepted on the VM. The profile is derived by report-service's function from public authoring JSON.
**Options considered**:
- A) A Go port of the deriver's walk in cc-data.
- B) An author-supplied `profile_urls` list in the scope file.
- C) Assignment URLs only.
- D) Ask report-server, which reaches report-service's existing deriver.

**Decision**: D, taken further (Doug, 2026-10-08): report-server holds the only matcher, and cc-data, the dashboard app and the runner all ask it (Dependencies, `POST /api/v1/packages/applies`). The deriver keeps its one copy, and there is no glob matcher anywhere else. The app and the runner already call report-service at startup, so the call adds no new dependency for either. Before the route is deployed, `run` warns and proceeds (R12).

---

### Where does the shared pattern fixture live?
**Decision**: The question dissolves under the decision above (Doug, 2026-10-08). With one matcher in report-server, `url-patterns.json` is an ordinary test file beside it in report-service, and nothing vendors a copy. cc-data has none.

---

### How is "a package built on a laptop runs in the VM and produces the same output" verified?
**Decision**: REPORT-147 owns it (Doug, 2026-10-08). Its done-when already requires the Wildfire package to run "identically under `cc-data package run` on a laptop and on a VM", and it is the first story that can put a package on a VM. REPORT-146 closes on the criteria in "Done when" below.

---

### Doing applicability and the rules in report-server rather than in cc-data
**Options considered**:
- A) cc-data keeps Go copies of the manifest rules, the archive limits and the matcher, guarded by fixtures.
- B) cc-data asks report-server's validate and applies routes, keeping only what it needs to stage and start a package.

**Decision**: B (Doug, 2026-10-08, to remove DRY violations without multiplying stories). The copies already disagreed: report-server allows 28,800 seconds where the runner refuses over 7,200. REPORT-167 makes report-server adopt the runner's stricter rules, so its answer is the runner's.

---

### Keep `learner_endpoints`, and which route the `init` stub teaches
**Context**: REPORT-147 (`bca55aa`) counts learners through a Student ID Mapping run with `run_answers` and `student_id_mapping`, after measuring on production that whole-class Student Answers runs fail above three or four assignments and that a reused run's learner list goes stale. That left `learner_endpoints` with no consumer, and the stub teaching the failing route.
**Options considered**:
- A) Drop `learner_endpoints`, document the Student ID Mapping learner count, and switch the stub to Student ID Mapping.
- B) Keep the view for researchers who pull Student Answers runs, and keep the stub.

**Decision**: A (Doug, 2026-10-08). Nothing uses the view, the route it rests on fails on real classes, and the existing `student_id_mapping` view already makes the count without double-counting. The stub is the template authors copy, so it teaches the route that works.

---

### Where a local scope comes from
**Options considered**:
- A) An author-supplied file (`--scope`) carrying the four keys rigse supplies.
- B) Synthesized from a pulled Student Answers report (`class_id`, and `res_<N>_offering_id`, `_name` and `_resource_url` per assignment).

**Decision**: A. The report has no `class_hash` or `runnable_id`, and the scope contract has no room for nulls in either. Any synthesized scope would differ from the VM's in exactly the fields a package might key on. A dashboard control that hands out the real scope is the eventual source (RD-3, out of scope).

---

### A `clue_prepull` package under `package run`
**Options considered**:
- A) Refuse it locally (R15).
- B) Run it with `clue_source: "firebase"` and an empty corpus.

**Decision**: A. B tells the package something false, and the package would produce numbers the VM would not. Section 13 already says a CLUE package is the one package that cannot be developed locally until REPORT-109 to 111.

---

### The passthrough environment in R11
**Context**: The runner replaces the environment entirely. Locally, a package's own `cc-data` calls must find the researcher's credential: the OS keychain via `go-keyring`, or `~/.config/cc-data/credentials.json`.
**Options considered**:
- A) The R11 allowlist.
- B) Inherit the whole environment and set the runner's variables over it.

**Decision**: A, verified on this machine (Linux, keyring backend). `env -i` with the R11 list, then `cc-data reports list --portal learn.concord.org --json`, reads the keyring token and lists runs. So does `env -i HOME PATH USER` alone: go-keyring reaches the session bus without `DBUS_SESSION_BUS_ADDRESS` here, so the D-Bus and XDG variables are kept only for setups that need them. On macOS go-keyring calls `/usr/bin/security`, which needs nothing beyond `HOME` and a `PATH` reaching `/usr/bin`. A cc-data query under the same `env -i` reads a dataset. B would let a package pass locally while depending on a variable the VM never sets.

---

### `scope.json`'s `dataset` is a bare name
**Context**: `final-design.md` section 10 shows `"dataset": "<cc-data dataset ref>"`. RD-4 pass 2's `scopeFile` writes the bare `pkg-<id>`, while `RD_DATASET` carries the full ref. A package that passes `scope["dataset"]` to `cc-data query --dataset` resolves it under the configured default portal, which may not be the dataset's.
**Options considered**:
- A) Mirror the runner (bare name), have the `init` stub read `RD_DATASET`, and record the discrepancy for RD-4 and the design to settle.
- B) Write the full ref locally, and ask RD-4 to change `scopeFile` to match section 10.

**Decision**: A. This story's contract is "exactly as the runner", and B changes a contract RD-4 owns from a story that consumes it. RD-4 pass 2 has since settled it as the bare name, and `final-design.md` section 10 says so (stream channel #28). The `init` stub reads `RD_DATASET`, the one name that is a full ref on both sides.

---

### Emptying the run directory could follow a link out of the package
**Context**: Raised in self-review (Security Engineer).

**Decision**: R11 empties `dir/.cc-data-run/in` and `out` on every run. If `.cc-data-run` were a symbolic link to another directory, the emptying would delete under that target. Fixed in R11: every directory `package run` empties or creates must be a real directory, and a link is refused before anything is removed.

---

### `publish` would upload a hand-made zip the runner then refuses
**Context**: Raised in self-review (Security Engineer).

**Decision**: report-server checks entry paths but not entry types, and the runner refuses a symbolic link after unpacking (RD-4's `checkTree`). So a zip made outside `build` could publish and then fail on every VM. Resolved by REPORT-167: report-server refuses link entries at publish, so the one check covers every zip.

---

### Run output written inside a git checkout
**Context**: Raised in self-review (Education Researcher).

**Decision**: `out/display.md` is derived from student data and lands inside the package directory. cc-data-studies' `.gitignore` ignores only `local-data/`, `__pycache__/` and `*.pyc` (read 2026-10-07), so `git add -A` would stage it. Fixed in R11 and R20: each working directory gets a `.gitignore` of `*`.

---

### `_run_id` reads like run scoping and is not
**Context**: Raised in self-review (Education Researcher).

**Decision**: The answers store stamps `_run_id` with the run that fetched each stored version (`internal/store/segment.go` `AppendPage`), and a later run's re-fetch overwrites it. Next to `run_answers.run_id`, a researcher could filter `answers` by `_run_id` and silently undercount. Fixed in R5's documentation requirement.

---

### The default build output would be swept into the next build
**Context**: Raised in self-review (Package Author).

**Decision**: With the output in the current directory and `build` run from inside the package, as authors do, R20 as written either refused its own default or zipped the last build into the next. Fixed in R20: the default is `dir/.cc-data-build/`, which R17 already excludes.

---

### An `init` stub that only reads existing runs reports nothing on the VM
**Context**: Raised in self-review (Package Author).

**Decision**: On the VM a package's dataset (`pkg-<catalog id>`) starts empty, and section 13 makes run creation package code. A stub that assumed a pulled run would pass locally and count zero on its first VM run. Fixed in R9: the stub reuses or creates its own run, as `_lib/ccdata.py` and RD-4's smoke package do (a Student ID Mapping run since 2026-10-08).

---

### R3 could pass with a fixture that cannot tell the queries apart
**Context**: Raised in self-review (QA Engineer).

**Decision**: With one assignment per learner, counting endpoints and counting users give the same number, so a broken learner view would pass. Fixed in R3: the synthetic dataset includes a two-assignment learner, a bare endpoint, an answer in two runs and a second class's run.

---

### Staging's answer before `PackageBuckets` exists was unstated
**Context**: Raised in self-review (Operator).

**Decision**: `Store.bucket_for` returns 422 "publishing is not configured for <server>" after every archive and manifest check has passed. Recorded in Technical Notes, so that answer reads as a well-formed request rather than a failure of this story.

---

### How a raw zip body reaches the server
**Options considered**:
- A) Generalize `Client.do` into `send` with a content type.
- B) Build a separate `http.Request` for the zip routes.

**Decision**: A. Publish and validate then share the bearer, the cross-origin redirect refusal, the no-retry rule for POST and the error-envelope decoding with every other call. B would re-implement each of them.

---

### The exit class for a refused or failed package
**Options considered**:
- A) Exit 1 with codes `PACKAGE_REFUSED`, `PACKAGE_FAILED` and `PACKAGE_OUTPUT_REFUSED`.
- B) A new exit code 7.

**Decision**: A. The root help defines exit 1 as "internal/other error", and the JSON envelope's code already distinguishes the cases. A new exit class is a contract change for every script that reads cc-data's exit codes.

---

### Following no link without `O_NOFOLLOW`
**Options considered**:
- A) `Lstat`, then open, then `os.SameFile`.
- B) `syscall.O_NOFOLLOW`.

**Decision**: A. CI tests on `windows-2022`, where `syscall.O_NOFOLLOW` does not exist. On a laptop the race A leaves open would need the author's own package to swap a file mid-read, and the runner, which runs as root, is the boundary that matters.

---

### What a 404 from the new routes means
**Options considered**:
- A) Any 404 from validate or applies means the route is not deployed: warn and proceed.
- B) Require a specific error code before treating it as "not deployed".

**Decision**: A. report-server's router has no route either path could match today (`/packages/validate` and `/packages/applies` are one segment, and the only other `POST /packages/...` route takes four). So a 404 can only mean the route is missing, and B would need a code that an older server, by definition, cannot send.

---

### The process-group kill on macOS
**Context**: `TestRunKillsAtTheBound` ran on Linux only.

**Decision**: `Setpgid` and `kill(-pgid, SIGKILL)` are POSIX and behave the same on Darwin, and `GOOS=darwin go build ./internal/packages/` compiles. CI's `macos-15` and `macos-15-intel` jobs run the test, so a Darwin difference would fail there.

---

### A shell entrypoint would run locally and be refused on the VM
**Context**: Raised in self-review (Senior Engineer).

**Decision**: The zip originally gave every entry mode 0644, and the runner executes a non-`.py` entrypoint directly. A Go zip with a 0644 `plain.sh` and a 0755 `exec.sh`, unpacked with `unzip`, gives "Permission denied" for the first and runs the second, so `unzip` restores the zip's modes. Locally, the staged copy kept the source's 0755, so `package run` passed. Fixed:
- `Files` records a ship mode per file (0755 if any execute bit is set, else 0644).
- `Zip` and the staging copy both use it.
- `CheckEntrypointMode` refuses a non-executable non-`.py` entrypoint in `run` and `build`.

R17 states the rule.

---

### A scope file inside the package ships with it
**Context**: Raised in self-review (Operator).

**Decision**: An author naming the file `scope.json` in the package directory would publish a class hash in every archive. The documentation step says to keep it outside the package or under a dot name, which `Collect` already excludes.

---

### The applicability call would time out before the deriver finishes
**Context**: Raised in self-review (Operator).

**Decision**: The deriver may spend up to 240 seconds on a scope's activities, and the client's per-attempt timeout for JSON calls is 60 seconds. `appliesFor` copies the client with a 5-minute `RequestTimeout`, which also keeps the shared client's default for every other call.

---

### An applies failure lost its code and exit class
**Context**: Raised in self-review (Senior Engineer).

**Decision**: `Run` returns `appliesFor`'s `*output.CLIError` unchanged, and `packageRunError` matched only the three package error types, so it fell through to `Internalf`. A throwaway test fed it applies' 503 `SERVICE_UNAVAILABLE` and `NOT_AUTHENTICATED`, and both came back exit 1 `INTERNAL`, against R16. `packageRunError` now returns a `CLIError` as it is, and `TestPackageRunKeepsTheServersCode` pins it.

---

### Ctrl-C left the package running
**Context**: Raised in self-review (Package Author).

**Decision**: `Setpgid` takes the package out of the terminal's foreground group, and cc-data had no interrupt handling outside `dataset materialize`. Sending `SIGINT` to cc-data's group, as a terminal does, ended cc-data while the package's `sleep 30` kept running. `package run` now cancels on `os.Interrupt` (the `materialize` idiom), which kills the group through the existing `cmd.Cancel`, and `Run` reports "the package was interrupted". Rerun with the fix, the package was gone a second after the signal.

---

### `build`'s default path came from unchecked manifest fields
**Context**: Raised in self-review (Security Engineer).

**Decision**: On a server without the validate route, a manifest named `../../escaped` built to `escaped-1.0.0.zip` two directories above the package, outside `.cc-data-build` and its `.gitignore`. `build` now refuses a name and version that make a path. It is a path check, not a copy of report-server's name grammar.

---

### The proxy variables were not passed through
**Context**: Raised in self-review (Operator).

**Decision**: cc-data's client uses Go's default transport, which reads `HTTPS_PROXY`, and the runner sets the proxy variables when it has a proxy (`package-env.js`). Behind a proxy, `package run`'s own requests would succeed while the package's `cc-data` calls failed. They are now on the passthrough list.

---

### "No extra fields" in the zip was untrue
**Context**: Raised in self-review (Senior Engineer).

**Decision**: `archive/zip` writes `Modified` as an extended-timestamp extra field (`5554...` on every entry of a scratch build). It is as fixed as the time, so R18 holds; the comment and R18 now say so.

---

### `publish` kept a local copy of the origin rule
**Context**: Raised in self-review (One Source of Truth).

**Decision**: `publish` refused an `--origin` without the `projects/` prefix, `build` did not, and report-server answers the same case with 422 "origin must be projects/<id>; a user origin is always your own". The local check is deleted, so both commands give report-server's answer.

---

### Assumptions the second self-review checked and kept
**Context**: Raised in self-review (second round), each checked against the code or the live sources.

**Decision**: Found sound: a second `get answers` on a fetched run re-fetches and merges new answers, so the stub stays current; `reports list --json`'s `report_filter` carries `filters` and `class` as the stub reads them; `query --format json` answers an array of objects; report-server's 270-second wait sits between the deriver's 240 and cc-data's 300; an old report-server answers both new routes with a JSON 404 even without a token; and no publish check refuses an ordinary user's own origin, so `run`'s validate call cannot lock out a researcher who may use the API.

---

### The documented learner count filtered the deduplicated mapping by run
**Context**: Raised in Copilot's review of PR #18. `student_id_mapping` keeps one row per learner across every mapping run, latest fetch winning (`TestPerRunDuplicateCheckIsNotConfusedByLaterRuns` already showed that filtering it by an older `run_id` loses learners). The count's `AND m.run_id = <run>` therefore dropped a learner whom a later-fetched mapping run of the same class also held, and the `init` stub's empty-scope guard, which counted `student_id_mapping` rows for the run, could fail on a class with data.
**Options considered**:
- A) A new per-run mapping view, deduplicated within each run only.
- B) Count over `report_<run>`, the raw per-run CSV, repeating the bare-endpoint rule in the query.
- C) Drop the mapping-side filter: the answers are already scoped by `a.run_id`, a run's answers fetch holds only its own learners' answers, and an endpoint maps to one learner whichever mapping run's row won.

**Decision**: C for the count, and the guard counts `report_<run>`, which the stub has just pulled. No new view, and the bare-endpoint rule stays in `student_id_mapping` alone. The test gains a later mapping run of the same class, so scoping the mapping by run fails it.

---

### Copilot's other findings on PR #18
**Context**: Raised in Copilot's review of PR #18.

**Decision**: The default build zip is written to a temporary file and renamed into place, so a link already at that path is replaced rather than followed. The scope file must hold one JSON object and nothing after it. `summary.txt` and `counts.json` stay read under the display cap, since the runner reads them that way too, and the code now says so.

