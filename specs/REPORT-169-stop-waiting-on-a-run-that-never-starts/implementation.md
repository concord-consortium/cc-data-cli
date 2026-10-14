# Implementation Plan: Stop waiting on a report run that never starts

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-169
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

### Stop polling a run report-server keeps failing to start

**Summary**: Replaces the `oscillation` detector with one that counts the server's failed start attempts (null answers with no query id), stops at four with a message and an action that say what to check, and pins the behavior with tests. R1 to R10.

**Files affected**:
- `internal/fetch/report.go`: the `startFailures` counter, its error (imports `bytes` and `encoding/json`), `pollUntilReady` takes the run; `oscillation` is deleted.
- `internal/fetch/report_test.go`: new tests, the oscillation test updated, a `pollCount` assertion helper.
- `cmd/get_report.go`, `cmd/get_report_test.go`: one help sentence and its check.

**Estimated diff size**: ~220 lines, most of it tests.

`report.go`, replacing `oscillation` and its `observe`:

```go
// startFailureLimit is how many failed start attempts stop the poll. On the download route a null
// state with no query id is always the answer to an attempt that just failed: a start that works
// replies with its query id, and one still in flight replies queued. So the count only rides out
// transient start failures.
const startFailureLimit = 4

// startFailures counts the null, query-less answers report-server gives while it fails to start a
// run's query. A server before 1.11.0 sends no athena_query_id key at all, which counts the same.
// An answer carrying an error is a server that retries the start itself and fails the run when it
// gives up, so it is left to do so.
type startFailures int

func (s *startFailures) observe(extra map[string]any) bool {
	state, _ := extra["athena_query_state"].(string)
	id, _ := extra["athena_query_id"].(string)
	if state == "" && id == "" && retryReason(extra) == "" {
		*s++
	}
	return int(*s) >= startFailureLimit
}

// retryReason is the error an unstarted run carries while the server retries its start.
func retryReason(extra map[string]any) string {
	reason, _ := extra["athena_query_error"].(string)
	return reason
}

// notStartedError is the stop for a run report-server keeps failing to start. The likeliest cause
// is a filter that reaches nothing in the researcher's projects, which the filter options show:
// they take a run's filter back as the run reports it, and narrow a dimension by the others.
func notStartedError(opts ReportOptions, run *api.ReportRun, extra map[string]any) *output.CLIError {
	check := fmt.Sprintf("cc-data reports filter-options --portal %s --dimension assignment --report-slug %s",
		opts.DS.Ref.Portal, run.ReportSlug)
	var filter bytes.Buffer
	if json.Compact(&filter, run.ReportFilter) == nil && filter.String() != "null" {
		check += " --report-filter '" + filter.String() + "'"
	}
	// The envelope's JSON escapes the filter's quotes, so a person gets the command unescaped here.
	fmt.Fprintf(opts.Progress, "run %d hasn't started; check with: %s\n", opts.RunID, check)
	return &output.CLIError{
		ExitCode: output.ExitContract,
		Code:     api.CodeNotReady,
		Message:  fmt.Sprintf("run %d hasn't started: report-server failed to start its query %d times", opts.RunID, startFailureLimit),
		Action: "The likeliest cause is that the run's filters match nothing your projects reach, most often because its " +
			"assignments are in none of your projects. List the assignments the run's filters reach with: " + check +
			". An assignment the run covers that is missing there is outside your projects.",
		Extra: extra,
	}
}
```

`pollUntilReady(ctx, opts)` becomes `pollUntilReady(ctx, opts, run)`, called from `FetchReport` with the run it already fetched. In the loop, `var osc oscillation` becomes `var starts startFailures`, and the oscillation branch, which sits after `--no-wait` and before the deadline, becomes:

```go
		if !isJob && starts.observe(apiErr.Extra) {
			return nil, nil, notStartedError(opts, run, apiErr.Extra)
		}
```

The progress line names the retry when there is one (R18):

```go
		if reason := retryReason(apiErr.Extra); reason != "" {
			fmt.Fprintf(opts.Progress, "run %d state=%q; waiting (report-server: %s)...\n", opts.RunID, state, reason)
		} else {
			fmt.Fprintf(opts.Progress, "run %d state=%q; waiting...\n", opts.RunID, state)
		}
```

The terminal check stays first, so a REPORT-168 `failed` answer is reported before the count is consulted (R9); `--no-wait` stays before it (R6); the timeout stays after it (R5). The run's `GetReport` call before polling also triggers a start attempt on the server, but it is not counted: it is a different route and its answer is not a `NOT_READY` body.

`cmd/get_report.go`'s long help, after "A terminal failure (failed/cancelled) exits 5.":

```
A run report-server keeps failing to start (four answers with no query) also exits 5, with the likeliest cause and how to check it.
```

and `cmd/get_report_test.go`'s list of required phrases gains `"keeps failing to start"`.

**Tests** (`report_test.go`). A helper `assertPolls(t, srv, n)` compares `atomic.LoadInt32(&srv.pollCount)` with `n`. A `stateless11` body constant, `{"error":"NOT_READY","athena_query_state":null,"athena_query_id":null,"athena_query_error":null,"athena_query_guidance":null}`, is the 1.11.0-and-later shape; the fixture's default null body is the older one.

- `TestGetReportStopsWhenRunNeverStarts`: ten `""` states. Exit 5, code `NOT_READY`, message contains `hasn't started`, action contains `--dimension assignment`, `--report-slug student-answers`, and the dataset's portal host, and no `--report-filter` (the fixture run has no filter); stderr's last line is `run 584 hasn't started; check with: ` followed by the same command; four polls.
- `TestGetReportStopsWhenRunNeverStartsNewerServer`: `notReadyBody` is `stateless11` plus one non-null key the client doesn't know, with ten states. Same exit and four polls; the envelope carries no `athena_query_*` key (all null, dropped) and does carry the unknown key, so it fails if the error stops forwarding the body (R2).
- `TestGetReportNotStartedSendsTheRunsFilter`: the fixture's `reportFilter` set to a `teacher-actions`-shaped filter with spacing, `{"filters": ["cohort"], "cohort": [12], "class": null}`; the action contains `--report-filter '{"filters":["cohort"],"cohort":[12],"class":null}'`, compacted, and so does stderr, with no backslashes; four polls.
- `TestGetReportWaitsOutTransientStartFailures`: three `""` states then the CSV. Completes; four polls (three not-ready, one download).
- `TestGetReportWaitsForASlowStart`: `""`, `queued`, then `running` three times, then the CSV. Completes; six polls. Five not-ready answers, so it fails if `queued` or `running` answers are counted.
- `TestGetReportOscillation` (existing): message now contains `hasn't started`; seven polls.
- `TestGetReportUnstartableRunFailsFromServer`: one poll answering REPORT-168's failed-to-start body (R8 of its spec), `{"error":"NOT_READY","athena_query_state":"failed","athena_query_id":null,"athena_query_error":"No learners were found matching the filters you selected.","athena_query_guidance":"<a guidance sentence>"}`. The test needs only that the guidance comes back as `action`, so it does not copy `AthenaFailure`'s wording. Exit 5 with the terminal message, `athena_query_error` in the envelope, the guidance as `action`; one poll.
- `TestGetReportWaitsWhileServerRetries`: `notReadyBody` is REPORT-168's retrying body, `{"error":"NOT_READY","athena_query_state":null,"athena_query_id":null,"athena_query_error":"The report could not be started because of a temporary error; it will be retried.","athena_query_guidance":null}`, for six polls, then the CSV. Completes on poll 7, and stderr contains the retry message. Fails if `observe` counts errored answers or the progress line drops the reason.
- `TestGetReportJobIsNotCountedAsUnstarted`: `--job 3` with `notReadyBody: {"error":"NOT_READY","status":"running"}` for six polls, then the CSV. Completes; seven polls. Fails if the `!isJob` guard goes.

Each test names its mutation: deleting the counter (or raising the limit) fails the first two and the oscillation test; lowering it fails the transient test; counting `queued` or `running` answers fails the slow-start test; dropping the `!isJob` guard fails the job test; dropping the `--report-filter` branch, the compaction or the stderr line fails the filter test.

---

### Pin the Go toolchain releases build with

**Summary**: Pins go1.27.2 in `go.mod`, pins goreleaser so building it can't switch the release to another Go, and fails a release build whose binary reports any other Go. R11 to R16.

**Files affected**:
- `go.mod`: `toolchain go1.27.2` after the `go` line.
- `.github/workflows/release.yml`: `goreleaser/v2@latest` becomes `goreleaser/v2@v2.18.3`, with a comment.
- `scripts/check-go-version.sh`: new.
- `.goreleaser.base.yaml`: a post hook running it, before `notarize.sh`.
- `README.md`: one paragraph in Development.
- `release_test.go`: three tests.

**Estimated diff size**: ~110 lines.

`go.mod`:

```
go 1.25.0

toolchain go1.27.2
```

`release.yml`'s build step:

```yaml
        # Pinned, not @latest: `go run` builds goreleaser with go.mod's toolchain line, unless
        # goreleaser's own go line is newer, in which case Go picks the newest patch release of
        # that line. Either way it hands that toolchain to every build goreleaser runs, so a
        # goreleaser needing a newer Go than the toolchain line builds the release with another Go.
        # check-go-version.sh catches that; a goreleaser that needs a newer Go means raising the
        # toolchain line too, which changes the checksum every package zips to.
        run: |
          go run github.com/goreleaser/goreleaser/v2@v2.18.3 release \
            --clean --skip=publish -f .goreleaser.${{ matrix.target }}.yaml
```

The step runs in the module root, so the toolchain line selects go1.27.2 before `go run` resolves goreleaser; v2.18.3 declares `go 1.27.1`, which go1.27.2 satisfies, so goreleaser is built with go1.27.2 and hands that toolchain to the cc-data build. The `setup-go` steps stay on `go-version-file: go.mod`; they install the `go` line's 1.25.0, which switches to the toolchain line on the first `go` command, so no workflow names the version (R14).

`scripts/check-go-version.sh`:

```bash
#!/usr/bin/env bash
# Fail a build whose binary was compiled by a Go other than the one go.mod's toolchain line pins.
# A package zip's checksum depends on the Go that built cc-data, so every build of a tag has to
# use the same one. The optional second argument replaces go.mod's version, for the tests.
set -euo pipefail

BIN="${1:?binary path required}"
WANT="${2:-$(awk '$1 == "toolchain" { print $2 }' "$(dirname "$0")/../go.mod")}"
if [[ -z "${WANT}" ]]; then
  echo "go.mod has no toolchain line" >&2
  exit 1
fi
GOT="$(go version "${BIN}" | awk '{ print $NF }')"
if [[ "${GOT}" != "${WANT}" ]]; then
  echo "${BIN} was built with ${GOT}, but go.mod pins ${WANT}" >&2
  exit 1
fi
```

`.goreleaser.base.yaml`'s hooks, the check first so an unpinned binary is never signed:

```yaml
    hooks:
      post:
        - cmd: scripts/check-go-version.sh "{{ .Path }}"
        - cmd: scripts/notarize.sh "{{ .Path }}"
```

`README.md`, a paragraph after the `go build ./...` block:

```
Builds use the Go toolchain `go.mod`'s `toolchain` line pins, go1.27.2; with Go 1.21 or later and the default `GOTOOLCHAIN=auto`, `go build` fetches it by itself. Go's zip compression differs between Go versions, so `cc-data package build` gives a package tree the same checksum only from builds made with the same Go, and every release build checks its binary against the pin.
```

`release_test.go`:

- `TestGoToolchainIsPinned`: `go.mod` has a line matching `^toolchain go\d+\.\d+\.\d+$`; every `go1\.\d+\.\d+` in `README.md` equals it; `release.yml` has no `goreleaser/v2@latest` and does match `goreleaser/v2@v\d+\.\d+\.\d+`.
- `TestGoreleaserChecksTheGoVersion`: `.goreleaser.base.yaml` runs `scripts/check-go-version.sh "{{ .Path }}"`, and before `notarize.sh`.
- `TestCheckGoVersionScript` (skipped on Windows, like `TestReleaseWorkflowClassifiesTags`): runs the script against the test binary (`os.Executable()`) with `runtime.Version()` as the second argument (exit 0) and with `go0.0.0` (non-zero, stderr naming both versions). Passing the version keeps the test green for someone running `GOTOOLCHAIN=local`, while still failing if the comparison is removed. A third run passes no version, so the script reads `go.mod`, which is the path the release takes: it must exit 0 when `runtime.Version()` equals `go.mod`'s toolchain, and otherwise exit non-zero with stderr naming that toolchain.

---

### Remove the done items from REPORT-146's closed spec

**Summary**: R17. `specs/REPORT-146-package-commands-and-dataset-views.md`'s "Not Yet Implemented" loses its first three bullets (the tag, the staging publish, the live refusals), all done per channel #126 and #129. The bullet saying laptop and VM parity moved to REPORT-147 stays.

**Files affected**:
- `specs/REPORT-146-package-commands-and-dataset-views.md`

**Estimated diff size**: 3 lines.

## Verified before writing

Both steps were applied as throwaway code and reverted:

- The detector, wired into `pollUntilReady` as above, against the existing `reportServer` fixture: a run that never starts stops at poll 4 (both body shapes), three nulls then the CSV completes at poll 4, null, `queued`, `running` completes, the alternating pattern stops at poll 7, a job answering `running` six times completes at poll 7, and a run filtered on class 590 gets `--report-filter '{"class":[590]}'` in its action. The only existing test that failed was `TestGetReportOscillation`, on its message, as planned. With every body key null, the envelope is just `error`, `message` and `action`. R18's error clause and retry progress line were run separately once REPORT-168's contract arrived: six retrying answers then the CSV completes on poll 7 with `waiting (report-server: The report could not be started because of a temporary error; it will be retried.)...` on stderr, and REPORT-168's 10-minute `failed` body exits 5 on its first poll with the reason in the envelope.
- `check-go-version.sh` against a go1.25.12 build: exit 1 naming both versions with go.mod's go1.27.2; exit 0 with `go1.25.12` passed explicitly; exit 1 with "go.mod has no toolchain line" when the line is missing.

- The final `notStartedError` against a `teacher-actions` run: with no filter the command has no `--report-filter`; with `{"filters": ["cohort"], "cohort": [12], "class": null}` both the envelope and stderr carry it compacted, the envelope with `\"` escapes and stderr pasteable as is; four polls each.

## Open Questions

### RESOLVED: Judgment call: where does the Go version check run?
**Options considered**:
- A) A workflow step after goreleaser that globs `dist/` for binaries: depends on goreleaser's output layout, which varies with the build id and `GOAMD64` suffix.
- B) A goreleaser post hook, like `notarize.sh`: goreleaser hands it each binary's exact path, and the hook fails the build before signing.

**Decision**: B.

### RESOLVED: How does the action narrow the check?
**Context**: `student-assignment-usage` and `teacher-actions` offer no class filter (`include_filters` in report-server's `Tree`), and `teacher-actions` fails with "No teachers or activities found", not no learners.
**Options considered**:
- A) Pass only the run's `class` value, with a placeholder when it has none: wrong for those two reports.
- B) Pass the run's whole `report_filter`, compacted, and word the cause for any report. The filter-options controller narrows a dimension by "the other dimensions in `report_filter`" and accepts the fields `GET /reports/:id` emits so a caller can send a run's filter back.
- C) Drop `--report-filter` when the run has no class: lists every assignment the researcher reaches for those reports.

**Decision**: B (Doug, 2026-10-11). Right for all five Athena reports, and no class-extraction helper. A run with many selections makes a long command; how the message reaches a person is the separate terminal-escaping item in `requirements.md`.

## Self-Review

Roles: commit reviewer, test writer, release engineer, senior engineer. Each item was checked against the code or the throwaway build above.

### Commit Reviewer

#### RESOLVED: Each step stands alone
The detector step compiles and passes on its own (the throwaway run); the toolchain step touches no Go source; the REPORT-146 edit is prose. No step needs a later one.

### Test Writer

#### RESOLVED: The job test needs the fixture to serve the job route
`handleDownload` is chosen by the `/download` suffix, which the job route also ends in, and `notReadyBody` sets the job-shaped body. Confirmed by the throwaway run: seven polls, then the CSV.

### Release Engineer

#### RESOLVED: A later goreleaser bump would fail the release without saying why
With the hook in place, a goreleaser needing a newer Go than the pin fails every build at `check-go-version.sh`. The workflow comment now says that the fix is raising the toolchain line too, and that this changes package checksums, so the person bumping knows it is a decision and not a typo.

### Senior Engineer

#### RESOLVED: REPORT-77's closed spec still describes the oscillation detector
`specs/REPORT-77-cc-data-cli.md` says the CLI "detects the null→queued→null self-start oscillation". It is left as is: a closed spec records what its story shipped, and this spec is where the new rule lives. REPORT-146's spec is edited only because #129 put that fix in this PR.

### Release Engineer (second pass)

#### RESOLVED: The plan misdescribed which Go builds goreleaser
It said `go run` builds v2.18.3 with go1.27.1 and the toolchain line then lifts the cc-data build. A throwaway local module proxy showed otherwise: in the module root the toolchain line selects go1.27.2 first, and a go-line requirement switches to the newest patch of that line (`go 1.27.1` gave go1.27.2), never the exact version. The outcome and R12's rule stand; the paragraph and the workflow comment now say this.

### Test Writer (second pass)

#### RESOLVED: The slow-start test could not fail
`""`, `queued`, `running` is three not-ready answers, under the limit even if every answer counted; a throwaway mutation that dropped the state check left it green. It now has five not-ready answers and asserts six polls. The class and job tests also assert their poll counts, as R10 requires of every test.

#### RESOLVED: `TestCheckGoVersionScript` never exercised the `go.mod` default
Both runs passed the version explicitly, so a broken `awk` that reads `go.mod` would surface only on a tag. A third run now omits it and asserts against `go.mod`'s toolchain either way.
