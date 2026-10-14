# Stop waiting on a report run that never starts

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-169

**Status**: **Closed**

## Overview

`cc-data get report` stops polling an Athena report run that report-server keeps failing to start, and says so within seconds instead of after its 30-minute timeout. Release builds also pin their Go toolchain, so every build of a tag zips a package to the same checksum.

When report-server can never start a run's query (most often because the run's assignments are in none of the researcher's projects), cc-data used to wait the full 30 minutes and end with a "still not ready" line that hid the cause. cc-data now recognizes that pattern after a few polls, stops, and says what the likely cause is and how to check it. The server half, REPORT-168, makes report-server fail such a run with its error, which cc-data already reports; this story's own detection protects researchers against servers without REPORT-168.

The toolchain fix was found during the staging publish: the `v0.3.0-pre.1` binaries were built with a newer Go than `go.mod` names, and Go's zip compression differs between versions, so a researcher could not reproduce a published package checksum without knowing which Go built the release.

## Requirements

### Not-started detection

- **R1.** While polling an Athena run, `get report` counts the `NOT_READY` answers whose `athena_query_state`, `athena_query_id` and `athena_query_error` are all null or absent. When that count reaches **4** (`fetch.StartFailureLimit`), it stops polling. Job polls (`--job`) are never counted.
- **R2.** It then exits 5 with code `NOT_READY` and a message that names the run and says it hasn't started because report-server failed to start its query. The envelope's `action` gives the likeliest cause (the run's filters match nothing the researcher's projects reach, most often because its assignments are in none of their projects) and how to check it: a `cc-data reports filter-options` command with the dataset's portal, `--dimension assignment`, the run's `--report-slug`, and the run's own `report_filter` as `--report-filter` (left off when the run has none). The slug and the filter come from the server, so each is shell-quoted. The same command is also printed as one plain line on the progress stream (stderr, or MCP progress) just before the error, because the envelope's JSON escapes the filter's quotes. The last `NOT_READY` body's other keys are forwarded in the envelope as for a terminal failure. `get report --help` says a run report-server keeps failing to start exits 5, and which answers count, with the count taken from `StartFailureLimit`. If a counted answer ever carries `athena_query_guidance`, the client's `action` wins and the guidance stays in the envelope under its own key.
- **R3.** The not-started stop replaces the `oscillation` detector: a server that answers `queued` and null alternately also reaches four null answers with no query id, so one rule covers both patterns.
- **R4.** Only null answers with no query id and no error count. `queued` answers, and any answer carrying a query id, don't count, and nothing resets the count. (A started run never returns to null: the server's reset only touches a run with no query id.)
- **R5.** A run that is queued or running keeps being polled under `--poll-timeout`, and the timeout's "still not ready" line is unchanged.
- **R6.** `--no-wait` is unchanged: it polls once and reports the state, since one null answer can't tell a persistent failure from a transient one.
- **R7.** Post-processing jobs (`--job`) are never counted. The old oscillation detector did run for jobs, and no longer does: a job's `NOT_READY` body is `{status}` and a job has no self-start, so the null and `queued` cycle it caught cannot happen for one.
- **R8.** The MCP `get_report` tool gets the same behavior through the shared code path.

### Failure state from report-server (REPORT-168)

- **R9.** When report-server answers a run that can't start with a terminal state, `athena_query_error` and `athena_query_guidance`, `get report` prints them as it does for any failed run (exit 5, the error in the envelope, the guidance as `action`) on the first poll that carries them, before the not-started count is involved.
- **R18.** A null answer with no query id that does carry an `athena_query_error` is REPORT-168's "retrying after a temporary failure" signal. It is not counted: the poll keeps waiting, since that server fails the run itself within 10 minutes, and each such answer's progress line shows the error.

### Tests

- **R10.** Each test asserts how many polls the server saw, so a change to the threshold fails it. Tests cover: a run that stays null with no query id (stops at the fourth poll); a server older than 1.11.0 whose body has no `athena_query_id` key (same); a slow start with enough `queued` and running answers that counting them would stop it; three null answers then a successful start (the threshold is not off by one); six of REPORT-168's retrying answers then a start; the alternating `queued` and null pattern (stops at its seventh poll); and R9's failed body.

### Go toolchain

- **R11.** `go.mod` has `toolchain go1.27.2`; the `go` line stays `go 1.25.0`.
- **R12.** The release workflow pins goreleaser to an exact version (`v2.18.3`) whose own `go` line is no newer than the pinned toolchain, so building goreleaser cannot switch the build to another Go.
- **R13.** Each release build fails the tag if the binary it built does not report the toolchain `go.mod` names (`scripts/check-go-version.sh`, a goreleaser post hook that runs before `notarize.sh`).
- **R14.** CI builds and tests on the pinned toolchain through `go.mod`, with no second place naming the version.
- **R15.** The README's Development section says releases build with the toolchain `go.mod` pins, why, and how to build the same way locally. A test fails if any toolchain name (`go1.x.y`) in the README disagrees with `go.mod`'s `toolchain` line.
- **R16.** `release_test.go` fails if `go.mod` loses its `toolchain` line, if the release workflow runs goreleaser `@latest`, or if it loses R13's check.

### Carried over

- **R17.** The closed REPORT-146 spec's "Not Yet Implemented" section loses the tag, the staging publish and the live refusals, which were all done.

## Technical Notes

**What the server does** (report-service at `f5adf65`, report-server 1.12.0):

- `get report` polls `GET /api/v1/reports/:id/download`. It and `GET /api/v1/reports/:id` both call `AthenaRunOps.ensure_current/1`.
- For a run with a null state and no query id, `ensure_current/1` claims it atomically (state `queued`) and runs `start_query/1` synchronously inside the request. On success the reply carries the query's state and its `athena_query_id`. On failure it resets the state to null, and the reply carries `athena_query_state: null`, no query id and no error.
- If the claim loses to a concurrent start (the background task `reports create` launches, or another client's poll), the reply says `queued` with no query id.
- So on the download route, **a null state with no query id is always the answer to a start attempt that just failed**. This holds for every report-server since `report-service-v1.7.0`.
- Servers before 1.11.0 send only `athena_query_state` in the `NOT_READY` body; the other three `athena_query_*` keys arrived in 1.11.0 (REPORT-106).

**REPORT-168 alignment**, checked against report-service PR #436 (merged as `1510133`) and R8 of `specs/REPORT-168-report-server-a-report-run-that-can-t-start-fails.md`. The 409 `NOT_READY` body keeps its shape and its four `athena_query_*` keys. If REPORT-168's R8 changes, this table and R1, R9 and R18 are what to recheck.

| REPORT-168 state | Body | This story |
|---|---|---|
| Not yet tried, or being started | null or `queued`, no id, no error | A null with no error comes only from a server without REPORT-168, where it means a failed attempt. Counted (R1). |
| Retrying after a temporary failure | null (or `queued`), no id, the fixed retry message | Not counted; shows the message and waits for the server's 10-minute bound (R18). |
| Failed to start | `failed`, no id, the reason, guidance for no learners | The existing terminal path (R9). |
| Started | Athena's states, the query id | Unchanged (R5). |

**Pinned consumers.** RD-4's runner image and cc-data-studies' publish workflow pin `v0.3.0-pre.1`, and this story changes nothing they read. That client already fails fast against a server with REPORT-168, through the terminal path. Its oscillation detector can stop a run during REPORT-168's retry window if another reader holds the claim on its polls, which is unlikely for the runner, normally a run's only reader.

**The Go toolchain.**

- `actions/setup-go` with `go-version-file: go.mod` reads only the `go` line.
- `go run goreleaser@latest` switched to a newer Go to build goreleaser, and put that toolchain first on goreleaser's `PATH`, so it built cc-data too. That is how `v0.3.0-pre.1` came out as go1.27.2.
- A `toolchain` line only ever upgrades, so it is necessary but not sufficient: goreleaser has to be pinned too, and the release has to check what it built.
- In the module root the toolchain line selects go1.27.2 before `go run` resolves goreleaser, and v2.18.3's `go 1.27.1` is satisfied by it. Bumping goreleaser past the toolchain line means raising that line too, which changes the checksum every package zips to.
- go1.27.2 zips `wildfire-responses` at cc-data-studies `297219d` to `sha256:94353fd5297b900fef1199e093d720cd0f7092a977437057ee2acbdb5b868d16` (10,826 bytes), staging's published checksum.

## Out of Scope

- Report-server's side: failing the run with its error is REPORT-168.
- Changing `DefaultPollTimeout`, the backoff, or `--no-wait`.
- Post-processing jobs, which have no self-start.
- A run stuck at `queued` with no query id. A legitimate start also answers `queued` while it runs, for minutes, so no short count can tell the two apart. REPORT-168 turns a start that raises into a retrying run, and fails a run whose start process died 30 minutes after its claim.
- A plain-text rendering of every error's message and action in `internal/output`. It changes what every command prints on failure, so it would be its own story; R2's one stderr line is easy to drop in its favor.
- Moving RD-4's runner image or cc-data-studies off `v0.3.0-pre.1`.
- Raising the `go` line (the minimum language version) in `go.mod`.

## Not Yet Implemented

- The release half of the checksum check: that the release binary reports go1.27.2 and its `cc-data package build` of `wildfire-responses` at `297219d` gives `sha256:94353fd5…`. It can only be checked on the next tag.

## Decisions

### How many null answers before stopping?
**Context**: Every null answer is a start attempt that just failed, so the count only guards against transient start failures (a portal database blip, Athena throttling on `StartQueryExecution`).
**Options considered**:
- A) 2, as the oscillation detector: stops within about 2 s, but one transient blip on top of a real one fails a run that would have started.
- B) 4: three sleeps of at most 2, 4 and 8 s, so a transient failure gets three retries spread over several seconds.
- C) A time window rather than a count: needs a clock seam the tests don't have.

**Decision**: B (Doug, 2026-10-11). Simulated with the client's backoff, 4 stops in 7 s on average (14 s at most). Against 30 minutes the difference from 2 is invisible, while a false stop costs a manual rerun or a failed VM package run. Only servers without REPORT-168 reach this count.

---

### Replace the oscillation detector, or keep it alongside?
**Context**: The two patterns (null, null… and queued, null, queued, null…) are the same fact: the server failed to start the query.
**Options considered**:
- A) Keep `oscillation` and add a consecutive-null detector: two rules and two messages for one cause, and the consecutive rule never trips on the alternating pattern.
- B) One rule, null answers counted before any query id.

**Decision**: B. The alternating pattern stops at its fourth null (seventh poll) instead of its second; still seconds.

---

### Which Go toolchain to pin?
**Context**: Any exact pin makes builds of one tag agree with each other. The choice decides whether they also agree with `v0.3.0-pre.1`.
**Options considered**:
- A) go1.25.12, matching the `go` line's minor: new tags would zip packages differently from `v0.3.0-pre.1`, whose checksum staging's catalog holds.
- B) go1.27.2: what built `v0.3.0-pre.1`, and the newest release at the time.

**Decision**: B.

---

### Does REPORT-168's contract fit R9?
**Context**: If REPORT-168 had picked a new state name instead of `failed`, the client would have polled it until the timeout.
**Options considered**:
- A) Align with REPORT-168's posted contract and change R9 if it differs.
- B) Ship R9 on the assumption and let a mismatch surface in testing.

**Decision**: A. R9 fit as written (see the alignment table); the contract's retrying state added R18.

---

### Windows CI on go1.27.2
**Context**: The release targets (macOS and linux) already built on go1.27.2. CI also builds and tests on windows-2022 with cgo and DuckDB's static libraries, which had only run on Go 1.25.
**Options considered**:
- A) Let the PR's CI answer it, and fall back to a different pin only if Windows fails.
- B) Check it before implementation.

**Decision**: A. There was no Windows machine to check on, and nothing in the change is Windows-specific. windows-2022 passes on go1.27.2; its one failure was a release test reading `go.mod` with CRLF endings, fixed by converting them in the tests' `readFile`.

---

### Wait or stop when a REPORT-168 server says it is retrying?
**Context**: REPORT-168 answers a temporary start failure with a null state, no query id and a fixed retry message, retries on every read, and fails the run on the first failure after it is 10 minutes old.
**Options considered**:
- A) Wait: don't count these answers, show the message in each progress line, and let the server's bound end the run.
- B) Stop on the first retrying answer.
- C) Count them toward the four-answer stop.

**Decision**: A (Doug, 2026-10-11). The server knows a failure is temporary and already bounds it, so a client limit would only cut short runs that would recover, and would need a client release whenever the server's policy changes. The runner on a VM can't retry, so a brief portal blip must not fail a package run. R18.

---

### How does the action narrow the check?
**Context**: `student-assignment-usage` and `teacher-actions` offer no class filter, and `teacher-actions` fails with "No teachers or activities found", not no learners.
**Options considered**:
- A) Pass only the run's `class` value, with a placeholder when it has none: wrong for those two reports.
- B) Pass the run's whole `report_filter`, compacted, and word the cause for any report. Filter options narrow a dimension by the other dimensions in `report_filter` and accept the fields `GET /reports/:id` emits.
- C) Drop `--report-filter` when the run has no class.

**Decision**: B (Doug, 2026-10-11). Right for all five Athena reports, with no class-extraction helper.

---

### The action's command reaches a terminal user JSON-escaped
**Context**: Errors print as one JSON line, so the action's `--report-filter '{\"class\":[590]}'` doesn't paste into a shell. Claude and MCP callers decode it correctly; a researcher reading the terminal, or the runner's log, does not.
**Options considered**:
- A) Keep the envelope as the machine contract and also print the check as one plain line on the progress stream.
- B) A plain rendering of every error in `internal/output`.

**Decision**: A (Doug, 2026-10-11). B changes the CLI-wide error output, so it is out of scope.

---

### Which `action` wins when a counted answer carries guidance?
**Context**: Under the contract a null answer with no query id and no error carries no guidance, but a server could send one. The terminal path promotes guidance to `action`, which here would replace the client's check.
**Options considered**:
- A) The client's `action`, which names the check; the server's guidance stays in the envelope as `athena_query_guidance`.
- B) The server's guidance, as on the terminal path.

**Decision**: A. The client's check is the actionable part of this stop, and keeping the guidance under its own key loses nothing.

---

### Where does the Go version check run?
**Options considered**:
- A) A workflow step after goreleaser that globs `dist/`: depends on goreleaser's output layout, which varies with the build id and `GOAMD64` suffix.
- B) A goreleaser post hook, like `notarize.sh`: goreleaser hands it each binary's exact path, and it fails the build before signing.

**Decision**: B.

---

### REPORT-77's closed spec still describes the oscillation detector
**Context**: `specs/REPORT-77-cc-data-cli.md` says the CLI detects the null, queued, null self-start oscillation.
**Decision**: Left as is: a closed spec records what its story shipped, and this spec is where the new rule lives. REPORT-146's spec was edited only because R17 assigned that fix to this PR.
