# Stop waiting on a report run that never starts

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-169
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

`cc-data get report` stops polling an Athena report run that report-server keeps failing to start, and says so within seconds instead of after its 30-minute timeout. Release builds also pin their Go toolchain, so every build of a tag zips a package to the same checksum.

## Project Owner Overview

A researcher, or the runner on a VM, asks cc-data for a report run's CSV and cc-data waits for the run's query to finish. When report-server can never start that query (most often because the run's assignments are in none of the researcher's projects), cc-data currently waits the full 30 minutes and ends with a "still not ready" line that hides the cause. This story makes cc-data recognize that pattern after a few polls, stop, and tell the researcher what the likely cause is and how to check it. The server half, REPORT-168, makes report-server fail such a run with its error; cc-data already reports a failed run's error, so this story's own detection is what protects researchers against servers without REPORT-168, which production will run for a while.

The second part is a release fix found during the staging publish: the `v0.3.0-pre.1` binaries were built with a newer Go than `go.mod` names, and Go's zip compression differs between versions, so a researcher could not reproduce a published package checksum without knowing which Go built the release. Pinning the toolchain makes that checksum reproducible from any build of the same tag.

## Background

Found 2026-10-09 by RD-4 pass 2's smoke check on staging (stream channel #111, #113, #114): report run 209 (Student Answers, class 590) could never start because material 604 was in no cohort or material list of the researcher's projects, so report-server's learner query found no learners. `get report --refresh` polled for 30 minutes and exited 4 with `run 209 still not ready after 30m0s (last state "")`. The server's real error, "No learners were found matching the filters you selected.", never reached the client.

**What the server does, checked in report-service at `f5adf65` (report-server 1.12.0):**

- `cc-data get report` polls `GET /api/v1/reports/:id/download`, not `GET /api/v1/reports/:id`. Both call `AthenaRunOps.ensure_current/1` (`server/lib/report_server/reports/athena_run_ops.ex`).
- For a run with a null state and no query id, `ensure_current/1` claims it atomically (state `queued`) and runs `start_query/1` synchronously inside the request. On success the reply already carries the query's state and its `athena_query_id`. On failure it logs "API self-start failed", resets the state to null, and the reply carries `athena_query_state: null`, no query id and no error.
- If the claim loses to a concurrent start (the background task `reports create` launches, or another client's poll), the reply says `queued` with no query id.
- So on the download route, **a null state with no query id is always the answer to a start attempt that just failed**. A run that is starting successfully answers `queued`, or a state with a query id, and never null. This holds for every report-server cc-data talks to: the API self-start has been there since `report-service-v1.7.0`.
- Servers before 1.11.0 send only `athena_query_state` in the `NOT_READY` body; `athena_query_id`, `athena_query_error` and `athena_query_guidance` arrived with REPORT-106 (`84e1be7`, first in 1.11.0).

**What the client does** (`internal/fetch/report.go`, `pollUntilReady`):

- `failed` and `cancelled` are terminal: it exits 5 with code `NOT_READY`, forwards every body key except `athena_query_guidance` in the envelope, and promotes the guidance to `action` (REPORT-127).
- The `oscillation` detector exits 5 after it sees `queued` turn into null twice. On the observed path, with one reader, the client never sees `queued` (the server resets before it replies), so it cannot trip, and the poll runs out `--poll-timeout` (default `DefaultPollTimeout`, 30 minutes). Backoff starts at 2 s, doubles to a 30 s cap, with full jitter.
- The same function serves the MCP `get_report` tool, which always uses the default timeout.

**Relation to REPORT-168.** REPORT-168 (report-server) fails a run that can't start with state `failed` and the reason in `athena_query_error`, which the existing terminal path already surfaces, so this story only proves that with a test (R9). While it retries a temporary failure it answers null with a fixed message in `athena_query_error` (R18). See "REPORT-168 alignment" below.

**Pinned consumers.** RD-4 pass 2's runner image (`CC_DATA_REF`) and cc-data-studies' publish workflow pin `v0.3.0-pre.1`. Neither has to move: this story changes no command, flag or output that they read. Two consequences worth knowing, though: a pinned `v0.3.0-pre.1` client already fails fast against a server with REPORT-168 (through the terminal path above), and against a server without it the runner keeps the 30-minute wait until its pin moves to the tag carrying this story. One caveat: the pinned client's oscillation detector (`report.go` at the tag, `osc.observe`) exits 5 after two `queued`-then-null cycles, so against a server with REPORT-168 it can stop a run during the retry window if another reader (the run's web page, a second client) holds the claim on its polls. The runner is normally a run's only reader, so this is unlikely there.

**The Go toolchain** (stream channel #119). The `v0.3.0-pre.1` binaries report go1.27.2 (`go version -m`) while `go.mod` says `go 1.25.0`. Checked for this spec:

- `actions/setup-go@v5` with `go-version-file: go.mod` reads only the `go` line (its `parseGoVersionFile` matches `^go (\d+(\.\d+)*)`), so it installs Go 1.25.0.
- The release then runs `go run github.com/goreleaser/goreleaser/v2@latest`. goreleaser v2.18.3, current at the time of writing, declares `go 1.27.1`, so `go run` switches to a 1.27 toolchain to build it: for a `go` line requirement Go picks the newest patch release of that line, which was go1.27.2.
- A throwaway test confirmed that the switched `go run` puts the downloaded toolchain's `bin` first on its program's `PATH` (and sets `GOROOT` to it), so every `go build` goreleaser runs uses that toolchain, not the one setup-go installed. That is how the release came out as go1.27.2.
- A second throwaway test confirmed that a `toolchain` line only ever upgrades: a `go.mod` with `toolchain go1.25.12`, built under that leaked go1.27.1, still built with go1.27.1. So **the `toolchain` line Jira proposes is necessary but not sufficient**: goreleaser has to be pinned too, and the release has to check what it built.
- A third throwaway test confirmed the upgrade direction that R11 and R12 rely on: under a leaked go1.27.1, a module with `toolchain go1.27.2` builds with go1.27.2.
- This tree at `7b173f0`, built with go1.27.2, passes `go test ./...` on linux, and zips `wildfire-responses` at cc-data-studies `297219d` to `sha256:94353fd5297b900fef1199e093d720cd0f7092a977437057ee2acbdb5b868d16` (10,826 bytes), staging's published checksum.
- go1.27.2 is the newest Go release at the time of writing, and it is what built `v0.3.0-pre.1`, whose zip of `wildfire-responses` is the checksum staging's catalog holds (`sha256:94353fd5…`).

## Requirements

### Not-started detection

- **R1.** While polling an Athena run, `get report` counts the `NOT_READY` answers whose `athena_query_state`, `athena_query_id` and `athena_query_error` are all null or absent. When that count reaches **4**, it stops polling. Job polls (`--job`) are never counted.
- **R2.** It then exits 5 with code `NOT_READY` and a message that names the run and says it hasn't started because report-server failed to start its query. The envelope's `action` gives the likeliest cause (the run's filters match nothing the researcher's projects reach, most often because its assignments are in none of their projects) and how to check it: a `cc-data reports filter-options` command with the dataset's portal, `--dimension assignment` and the run's `--report-slug` filled in, and the run's own `report_filter` as `--report-filter` (left off when the run has none); an assignment the run covers that is missing from that list is outside the researcher's projects. The same command is also printed as one plain line on the progress stream (stderr, or MCP progress) just before the error, because the envelope's JSON escapes the filter's quotes and a person has to be able to paste it. The last `NOT_READY` body's other keys are forwarded in the envelope as for a terminal failure. `get report --help` gains a sentence saying a run report-server keeps failing to start exits 5.
- **R3.** The not-started stop replaces the `oscillation` detector: a server that answers `queued` and null alternately also reaches four null answers with no query id, so one rule covers both patterns, and the oscillation detector and its separate message are removed.
- **R4.** Only null answers with no query id and no error count. `queued` answers, and any answer carrying a query id, don't count, and nothing resets the count. (A started run never returns to null: the server's reset only touches a run with no query id.)
- **R5.** A run that is queued or running keeps being polled under `--poll-timeout` exactly as today, and the timeout's "still not ready" line is unchanged.
- **R6.** `--no-wait` is unchanged: it polls once and reports the state, since one null answer can't tell a persistent failure from a transient one.
- **R7.** Post-processing jobs (`--job`) are unchanged: their `NOT_READY` body is `{status}`, with no self-start.
- **R8.** The MCP `get_report` tool gets the same behavior through the shared code path, with no change of its own.

### Failure state from report-server (REPORT-168)

- **R9.** When report-server answers a run that can't start with a terminal state, `athena_query_error` and `athena_query_guidance`, `get report` prints them as it does for any failed run (exit 5, the error in the envelope, the guidance as `action`) on the first poll that carries them, before the not-started count is involved. A test pins this with a body in the shape REPORT-168 sends.

- **R18.** A null answer with no query id that does carry an `athena_query_error` is REPORT-168's "retrying after a temporary failure" signal. It is not counted: the poll keeps waiting, since that server fails the run itself within 10 minutes, and each such answer's progress line shows the error so the researcher sees why the run is waiting.

### Tests

- **R10.** Each test asserts how many polls the server saw, so a change to the threshold fails it. Tests cover: a run that stays null with no query id (stops at the fourth poll, with R2's message and action); a server older than 1.11.0 whose body has no `athena_query_id` key (same); a run that goes null, then `queued`, then running for several polls, then succeeds (downloads, with enough not-ready answers that counting `queued` or running ones would stop it); three null answers followed by a successful start (downloads, so the threshold is not off by one); six of REPORT-168's retrying answers followed by a successful start (downloads, and the progress lines show the retry message); the alternating `queued` and null pattern (stops at its seventh poll); and R9's failed body.

### Go toolchain

- **R11.** `go.mod` gains `toolchain go1.27.2`; the `go` line stays `go 1.25.0`.
- **R12.** The release workflow pins goreleaser to an exact version whose own `go` line is no newer than the pinned toolchain, instead of `@latest`, so building goreleaser cannot switch the build to another Go.
- **R13.** Each release build job fails the tag if the binary it built does not report the toolchain `go.mod` names.
- **R14.** CI builds and tests on the pinned toolchain, through `go.mod`, with no second place naming the version.
- **R15.** The README's Development section says releases build with the toolchain `go.mod` pins, why (package zip checksums depend on it), and how to build the same way locally. A test fails if any toolchain name (`go1.x.y`) in the README disagrees with `go.mod`'s `toolchain` line; a minimum such as "Go 1.21 or later" is not a toolchain name.
- **R16.** `release_test.go` fails if `go.mod` loses its `toolchain` line, if the release workflow runs goreleaser `@latest`, or if it loses R13's check.

### Carried over

- **R17.** The closed REPORT-146 spec's "Not Yet Implemented" section lists the tag, the staging publish and the live refusals, which are all done (channel #126, #129). This story's PR removes those three lines; #129 assigned that fix to the next cc-data-cli PR.

## Done when

- R10's tests pass.
- **Against a server without REPORT-168,** checked live on staging (`1.13.0-pre.1`) with a build of this branch: a Student Answers run for class 591 ("Researcher Dashboard Wildfire Outside", whose only assignment, material 605, is in no project or cohort; stream channel #173), created as `dougresearcher`, makes `get report --refresh` exit 5 on its fourth not-started answer (seconds, not 30 minutes; a poll answering `queued` while `reports create`'s start attempt still holds the claim isn't counted, so the stop can take a poll or two more), with R2's message and action and the pasteable stderr line. It has to run as a non-admin researcher, because report-server skips the project rule for an admin. Run 209 no longer reproduces (#114). Post the result on the stream channel, with the class 591 run's id, since that post clears REPORT-168's staging deploy and REPORT-168's check reads that run (#184).
- **That check runs before REPORT-168 reaches staging.** Afterward no reachable server lacks REPORT-168 except production, which has no fixture, and the old-server path could only be checked by unit tests. This is a soft gate (Doug, 2026-10-11): if REPORT-169 slips, deploying REPORT-168 without it is Doug's call.
- **Against a server with REPORT-168,** once it is on staging: the same class 591 run (REPORT-168's R12 fails it on its next read), or a fresh one, makes the new cc-data exit 5 on its first poll after the create-time start attempt has finished (a poll while that attempt holds the claim answers `queued`, as REPORT-168's spec notes) with REPORT-168's no-learners `athena_query_error` and its guidance as `action` (R9). The run can double as REPORT-168's own staging check.
- A run that starts (class 590's Student Answers, run 210's shape) is still waited for and downloads.
- A local build from the release tag reports go1.27.2, as does the release binary, and `cc-data package build` of `wildfire-responses` at `297219d` gives `sha256:94353fd5…` with both. The release half can only be checked on the next tag.

## Technical Notes

- Poll loop: `internal/fetch/report.go`, `pollUntilReady`, `oscillation`, `extractState`, `isTerminalFailure`, `promoteGuidance`. Exit codes: `internal/output/output.go` (`ExitContract` = 5). Tests: `internal/fetch/report_test.go`, whose `reportServer` fixture answers `{"error":"NOT_READY","athena_query_state":null}` for `""`, a body with no `athena_query_id` key, which is the pre-1.11.0 shape.
- The dataset's portal (`opts.DS.Ref.Portal`) and the run's slug (`run.ReportSlug`) are both in hand where the action text is built.
- Help text: `cmd/get_report.go`'s long description mentions terminal failures exiting 5; it gains one sentence for the not-started stop. `cmd/root.go`'s exit-code table already says "terminal NOT_READY" for 5.
- Toolchain: `go.mod`, `.github/workflows/release.yml` (two `setup-go` steps and the goreleaser `go run`), `.github/workflows/ci.yml`, `README.md` Development section, `release_test.go`.
- With `toolchain go1.27.2` and Go's default `GOTOOLCHAIN=auto`, setup-go's Go 1.25.0, and any local Go from 1.21 on, switch to go1.27.2 on the first `go` command in the module.

## Out of Scope

- Report-server's side: failing the run with its error is REPORT-168.
- Changing `DefaultPollTimeout`, the backoff, or `--no-wait`.
- Post-processing jobs, which have no self-start.
- A run stuck at `queued` with no query id (a start that raised on the server). On a server without REPORT-168 it is waited out by `--poll-timeout`; a legitimate start also answers `queued` while it runs, for minutes, so no short count can tell the two apart. REPORT-168 removes the raise case (an exception, exit or throw during a start releases the run to retry, so it answers as R18's retrying run and fails within 10 minutes); only a start whose process died stays `queued`, and REPORT-168 fails that 30 minutes after its claim.
- A plain-text rendering of every error's message and action in `internal/output`, so any future action with a quoted argument pastes cleanly. It changes what every command prints on failure, so it would be its own story; R2's one stderr line is easy to drop in its favor.
- Moving RD-4's runner image or cc-data-studies off `v0.3.0-pre.1`; that is their own bump when they want the new behavior.
- Raising the `go` line (the minimum language version) in `go.mod`.

## REPORT-168 alignment

Checked against REPORT-168's code (report-service PR #436, merged to `master` as `1510133`: `AthenaRunOps.ensure_current/1` and `record_transient/2`, `ReportController.athena_download/4`) and R8 of its closed spec, `specs/REPORT-168-report-server-a-report-run-that-can-t-start-fails.md`. The contract is unchanged in shape: the same 409 `NOT_READY` body (plus `message`, which the client never puts in `Extra`) with the same four `athena_query_*` keys. A retrying run's message carries no guidance (`AthenaFailure.guidance_for/2` matches none of its words).

| REPORT-168 state | Body | This story |
|---|---|---|
| Not yet tried, or being started | null or `queued`, no id, no error | On the download route every null read attempts a start, so a null with no error comes only from a server without REPORT-168, where it means a failed attempt. Counted (R1). |
| Retrying after a temporary failure | null (or `queued`), no id, the fixed retry message | Not counted; waits for the server's 10-minute bound and shows the message (R18). |
| Failed to start | `failed`, no id, the reason, guidance for no learners | The existing terminal path: exit 5, the reason in the envelope, the guidance as `action` (R9). `v0.3.0-pre.1` already does this. |
| Started | Athena's states, the query id | Unchanged (R5). |

If REPORT-168's R8 changes, this table and R1, R9 and R18 are what to recheck.

## Open Questions

### RESOLVED: Judgment call: how many null answers before stopping?
**Context**: Jira suggests "for example, the same window the oscillation detector uses" (2). Every null answer is a start attempt that just failed, so the count only guards against transient start failures (a portal database blip, Athena throttling on `StartQueryExecution`).
**Options considered**:
- A) 2, as the oscillation detector: stops within about 2 s, but one transient blip on top of a real one fails a run that would have started.
- B) 4: three sleeps of at most 2, 4 and 8 s (about 7 s on average with full jitter), so a transient failure gets three retries spread over several seconds.
- C) A time window (say 30 s) rather than a count: needs a clock seam the tests don't have, for little gain over B.

**Decision**: B (Doug, 2026-10-11). Simulated with the client's backoff, 4 stops in 7 s on average (10.5 s at the 90th percentile, 14 s at most), against 1 s for 2 and 15 s for 5; the run lookup before polling makes it five server attempts in all. Against today's 30 minutes the difference from 2 is invisible, while a false stop costs a manual rerun or a failed VM package run, and waiting longer mostly delays the common real failure while every doomed poll reruns the learner query. Only servers without REPORT-168 reach this count.

### RESOLVED: Judgment call: replace the oscillation detector, or keep it alongside?
**Context**: The two patterns (null, null, null… and queued, null, queued, null…) are the same fact: the server failed to start the query. Jira asks that tests still cover the oscillation case.
**Options considered**:
- A) Keep `oscillation` and add a consecutive-null detector beside it: two rules, two messages for one cause, and the consecutive rule never trips on the alternating pattern.
- B) One rule, null answers counted before any query id: covers both, and deletes the old detector.

**Decision**: B. The alternating pattern now stops at its fourth null (seventh poll) instead of its second; still seconds. The existing oscillation test keeps covering it, with the new message.

### RESOLVED: Judgment call: which Go toolchain to pin?
**Context**: Any exact pin makes builds of one tag agree with each other. The choice decides whether they also agree with `v0.3.0-pre.1`.
**Options considered**:
- A) go1.25.12, matching the `go` line's minor: builds of new tags would zip packages differently from `v0.3.0-pre.1`, whose checksum staging's catalog holds.
- B) go1.27.2: what built `v0.3.0-pre.1` and the newest release, so new tags reproduce staging's published checksum.

**Decision**: B.

### RESOLVED: Low confidence: does REPORT-168's contract fit R9?
**Context**: R9 assumes REPORT-168 reports a run that can't start as `athena_query_state: "failed"` (or `"cancelled"`), with the reason in `athena_query_error` and optionally `athena_query_guidance`, in the download route's `NOT_READY` body. Then the existing terminal path needs no code change. If REPORT-168 picks a new state name, the client would treat it as non-terminal and poll until the timeout, and `isTerminalFailure` would have to learn it. marvin-REPORT-168-a had not posted its contract when this was written.
**Options considered**:
- A) Align with REPORT-168's posted contract at the cross-reference stage, and change R9 and the plan if it differs.
- B) Ship R9 on the assumption and let a mismatch surface in testing.

**Decision**: A, and done: see "REPORT-168 alignment" below. R9 fits as written; the contract's retrying state added R18.

### RESOLVED: Low confidence: Windows CI on go1.27.2
**Context**: The release targets (macOS and linux) already built and ran on go1.27.2 for `v0.3.0-pre.1`. CI also builds and tests on windows-2022 with cgo and DuckDB's static libraries, which has only ever run on Go 1.25. The PR's CI run is the first check.
**Options considered**:
- A) Let the PR's CI answer it, and fall back to a different pin only if Windows fails.
- B) Check it before implementation.

**Decision**: A. There is no Windows machine to check on before then, and nothing in the toolchain change is Windows-specific; the PR's CI covers all five targets.

### RESOLVED: Wait or stop when a REPORT-168 server says it is retrying?
**Context**: REPORT-168 answers a temporary start failure with a null state, no query id and a fixed retry message, retries on every read, and fails the run on the first failure after it is 10 minutes old. Its contract lets a client stop early on that message if it chooses.
**Options considered**:
- A) Wait: don't count these answers, show the message in each progress line, and let the server's bound end the run.
- B) Stop on the first retrying answer.
- C) Count them toward the four-answer stop.

**Decision**: A (Doug, 2026-10-11). The server is the side that knows a failure is temporary and already bounds it, so a client limit would only cut short runs that would recover, and would need a client release whenever the server's policy changes. The runner on a VM can't retry, so a brief portal blip must not fail a package run. The researcher sees the reason on every poll, and the worst case (a fresh run during a real outage) ends within 10 minutes with the server's error. R18.

## Self-Review

Roles: senior engineer, QA engineer, researcher (the person reading the message), release engineer. Every item below was checked against the code and fixed in place.

### QA Engineer

#### RESOLVED: R10's tests could pass with any threshold
A test that only checks the exit code passes whether the stop comes at the second poll or the fourth. R10 now has each test assert the fixture's poll count (`reportServer.pollCount`), and pins the alternating pattern at its seventh poll.

### Researcher

#### RESOLVED: R2's suggested check was too vague to act on
"`filter-options --dimension assignment` on the run's portal" leaves the researcher to work out the slug and the narrowing. The dataset's portal and the run's slug are both in hand in `FetchReport`, and report-server's filter options narrow a dimension by the rest of `report_filter` (`ReportServer.Reports.FilterOptions.page/4`), with `assignment` among `ReportFilter`'s valid types. R2 now fills both in and says to narrow by the run's class, and what a missing assignment means.

### Senior Engineer

#### RESOLVED: The help text change was only in Technical Notes
`cmd/get_report.go`'s long description documents the exit-5 cases, so a new exit-5 case belongs there. R2 now requires it. R2 also says the last body's keys are forwarded, as the terminal path does, so an MCP caller sees the same envelope shape for both stops.

### Researcher (second pass)

#### RESOLVED: R2's check fits only class-filtered student reports
Two of the five Athena slugs, `student-assignment-usage` and `teacher-actions`, don't filter on `class` (`include_filters` in report-server's `Tree`), so their runs never have one and the action always shows the `{"class":[<class id>]}` placeholder, asking for a dimension the run's report doesn't offer. A failed `teacher-actions` start also says "No teachers or activities found to match the requested filter(s).", not no learners, so R2's stated cause is wrong for it. The assignment dimension is already limited to the caller's projects (`FilterOptions.allowed_projects/2`), so one fix is to drop `--report-filter` when the run has no class, and to word the cause by report family. Doug chose to pass the run's whole `report_filter` back, which filter options are built to accept and which fits every report; R2 and the cause's wording now say so (decision record in `implementation.md`).

#### RESOLVED: The action's command reaches a terminal user JSON-escaped
Errors are printed only as one JSON line (`output.EmitError`, `json.Marshal`), so R2's command shows as `--report-filter '{\"class\":[590]}'`, and the placeholder as `\u003cclass id\u003e` (a throwaway run printed both). Pasted into a shell, the backslashes make the filter invalid JSON. Claude and MCP callers decode it correctly; a researcher reading the terminal, or the runner's log, does not. This is the first action carrying a quoted argument. Doug chose (2026-10-11) to keep the envelope as the machine contract and also print the check as one plain line on the progress stream, where a person already watches this command; R2 now says so. A plain rendering of every error in `internal/output` would fix this for all commands, but it changes the CLI-wide error output, so it is out of scope here. (Question 1's decision removed the placeholder; the escaped quotes remain.)

### QA Engineer (second pass)

#### RESOLVED: R15's guard contradicted its own README paragraph
"Any Go version the README names" would include the paragraph's "Go 1.21 or later". R15 now names `go1.x.y` toolchain names only, which is what the planned regex matches.

#### RESOLVED: R10's slow-start case could not fail
null, `queued`, running is three not-ready answers, under the limit even if every answer counted. R10 now asks for enough running answers that counting them would stop the poll (the implementation spec's test has five).
