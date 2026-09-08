# Surface Athena failure reasons and their guidance outside the web UI

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-127
**Repo**: https://github.com/concord-consortium/cc-data-cli and https://github.com/concord-consortium/report-service
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Make a failed Athena run explain itself everywhere, not only in the browser: expose REPORT-106's reason-to-guidance mapping over the v1 API, and stop cc-data discarding the failure fields the server already sends. Spans both repositories, like REPORT-92 and REPORT-106 before it.

## Project Owner Overview

When an Athena report fails, report-service knows exactly why and what to do about it. REPORT-106 captured Athena's own `StateChangeReason`, stored it, and built a mapping from reason to next step: a partition limit means narrow the cohort, a timeout or an S3 throttle means retry off-peak, and an S3 `Slowdown` means wait rather than narrow anything, because narrowing is the wrong advice there. The run page shows both.

A researcher working from the terminal, or through Claude, gets none of it. `cc-data get report` on a failed run says the run is in terminal state "failed" and stops. The reason is already in the response the client received and is thrown away before anyone sees it; the guidance never leaves the server at all. Both audiences hit the same failures, and only one is told how to recover.

## Background

**The server stores and maps, but only the browser reads the map.** `ReportJSON.run_json/1` emits `athena_query_id`, `athena_query_state` and `athena_query_error` (`report_json.ex:30-32`), and the `NOT_READY` download response carries all three (`report_controller.ex:165-172`). The mapping, `AthenaFailure.guidance_for/2`, is called from exactly one place: `custom_components.ex:158`. It is derived at render time rather than stored, and returns `nil` for a reason it does not recognize, which the UI renders as the raw reason alone.

**The client discards what it does receive, and the defect is one function.** `internal/api/types.go`'s `ReportRun` declares `AthenaQueryState` and neither of the other two fields. In `internal/fetch/report.go`, `pollUntilReady` builds its terminal-failure error with `Extra: stateExtra(state, isJob)`, and `stateExtra/2` constructs a fresh map holding only `athena_query_state` (`report.go:275-280`). The reason and query id are dropped there. The user sees `run 584 is in terminal state "failed"; nothing to download`.

**Everything downstream of that function already works.** `AsCLIError` returns an error that is already a `CLIError` untouched, so `Code`, `Message`, `Action` and `Extra` all survive it (`errors.go:66-69`); `CLIError.Envelope/0` merges every `Extra` key into the single-line JSON error the CLI prints (`output.go:43-55`); and `codedError/1` renders that same envelope for MCP, explicitly so "a tool caller sees the same code, message and action a terminal user does" (`mcpserver/server.go:51-61`). `Action` exists for precisely the kind of text the guidance mapping produces.

**Why one story across two repos.** REPORT-106 was split by repo in practice and closed with its client half unbuilt, which is what produced this story. The lesson is not that cross-repo stories are wrong, since REPORT-92 spans both and carries both fix versions by the project's own rule, but that a story must not close while its acceptance criteria are unmet. Splitting the server and client halves here would split one user-visible outcome along an implementation seam, and would leave a server change nothing consumes.

## Requirements

### Server

- The mapped guidance is returned alongside the raw reason, in both the `NOT_READY` download body and the run JSON, so any client sees what the run page shows.
- It is derived per request from the stored reason, as the web UI already derives it. Nothing new is stored and no migration is needed.
- A reason with no mapping returns the raw reason and no guidance, rather than a placeholder. The mapping is an aid and the raw reason is always the authority.
- The `NOT_READY` body is contractually caller-visible, and a test asserts its keys are exactly the intended set. cc-data forwards it verbatim, so that test is where a newly added field gets considered rather than disclosed by default.
- The web UI keeps rendering guidance through the same function, so the browser and the API cannot disagree about what a reason means.

### Client

- `cc-data get report <run-id>` on a failed Athena run reports Athena's reason, the query id, and the suggested next step.
- The guidance is rendered into `CLIError.Action`, the field that already exists for it, rather than concatenated into the message. It is promoted into that field rather than copied into it, so the envelope carries the sentence once. `Action` stays the client's own field and is free to say something the server did not, which a second copy pinned beside it would have forbidden.
- The same information reaches an MCP caller through the existing envelope, with no MCP-specific code path.
- `stateExtra/2` is deleted rather than corrected. Its one caller forwards what the server put in the body instead, minus the guidance key it has just rendered, so a field the server adds later needs no client release to become visible. The job download path keeps today's envelope exactly, which is what the passthrough must not change.
- `athena_query_id`, `athena_query_error` and the guidance are optional on the run wire type, and appear wherever a run is rendered as JSON: `reports list --json`, `reports create` and `reports duplicate`, and the `reports_list`, `reports_create` and `reports_duplicate` MCP payloads. One function shapes all six, so naming only the listing would understate what changes. A newly created or duplicated run has no query yet and carries none of them. The human table is unchanged.
- A server that sends none of them is not an error: nothing appears in the envelope and the message is today's.
- A **current** server reporting a failure it has no reason for is the same: the envelope carries no null-valued keys. `CLIError.Envelope/0` drops nil `Extra` values, as it already drops an empty message and action, so a run that failed without a recorded reason does not print `"athena_query_error":null` alongside its message.
- Portal runs carry none of them, having no Athena query.

### Both

- Tests pinned to wire captures of a failed run with a mapped reason, a failed run with an unmapped reason, and a server that sends none of the three fields, following the `const ...Wire` convention in `internal/api/filter_options_test.go`.
- One definition of the guidance. Neither repo gains a second copy of the reason-to-suggestion table.

## Technical Notes

**The server change is a parameter, not a lookup.** `download/2` already resolves the report with `Tree.find_report/1` and passes it to `portal_download/4` while `athena_download/3` takes only the run (`report_controller.ex:128-140`). Passing the report to the Athena branch as well makes the two symmetrical and gives `guidance_for/2` what it needs. `run_json/1` already calls `Tree.find_report/1` twice, for `report_type` and `execution`, so the guidance is a third use of a value it should resolve once.

**English over the wire is the existing convention.** The alternative, returning a machine-readable code and letting each surface render its own text, reads cleaner as API design and is wrong here: it would put the wording in two places, which is what the single-mapping requirement exists to prevent. The API already returns human text in every `message`, and the guidance is the same kind of value.

**The reason can echo the query, and REPORT-106 settled what that means.** The generated SQL inlines learner secure keys as literals (`report_query.ex:129`) and, for `teacher-actions`, portal usernames, so Athena's `StateChangeReason` can carry them back. That is why `AthenaFailure.error_code/1` exists and why `athena_query_poller.ex:24,27` logs the code alone. The restriction is about the sink, not the string: a log is long-lived, shared, and outside the owner gate, while this response is scoped to a run the caller owns. REPORT-106 resolved the same question for the API and the run page, in `specs/REPORT-106-athena-failure-reasons.md`, on the ground that the owner supplied or already received every identifier in their own query, so the reason shows them nothing new. This story adds no surface that changes that: cc-data's MCP tools already return the rows those secure keys identify, up to 1000 of them from `query` alone. Recorded here so the next reader of `athena_failure.ex`'s logging comment does not have to re-derive it. The one case worth naming is `teacher-actions`, whose echo can include teacher usernames, which `hide_names` does not cover as a matter of policy rather than as a gap.

**Passing `Extra` through is a smaller change than adding keys to it, and a better one.** The narrow fix adds two keys to `stateExtra/2`. Carrying `apiErr.Extra` forward instead removes the class: the present bug exists because the server gained fields and a client function kept building a map with one key in it.

**The decoder already passes unknown fields through, verified rather than assumed.** A `NOT_READY` body carrying an `athena_query_guidance` key the client has never heard of arrives intact in `APIError.Extra`, so the client needs no knowledge of the new field for it to reach the envelope. The same check turned up the null case: a body with explicit JSON nulls decodes them into `Extra` as nil values, and `Envelope/0` currently copies every key unconditionally (`output.go:51-53`), so they would be printed. That is why dropping nils is a requirement rather than a nicety; without it the "absent rather than empty" property holds only for old servers that omit the keys, and breaks for a current server reporting a reasonless failure.

**The `--no-wait` path needs nothing, because of one branch ordering.** REPORT-94 added `notReadyResult/2` (`report.go:282`), which returns a result map carrying the state for a run that is not ready, and it is reached from both `--no-wait` (`report.go:200`) and poll-timeout (`report.go:211`). Neither ever sees a failed run: `isTerminalFailure` is tested first, at `report.go:191`, so `failed` and `cancelled` take the terminal-failure branch this story changes and `notReadyResult/2` serves only `queued`, `running` and a null state, none of which have a reason to surface. Recorded because it is the branch ordering that makes it true rather than anything about the two functions, so reordering those two checks would silently return `--no-wait` to reporting a bare state.

**The reason is bounded server-side.** `AthenaFailure.truncate/1` caps it before storage (`athena_failure.ex:28-36`), so the client needs no length handling and must not add a second cap that could disagree.

**Sequencing against REPORT-94 is unchanged.** REPORT-94 rewrites `types.go` and `FetchReport` and this story touches both, so it lands after 94 as the Jira link records. The reason is not conflict avoidance but which spec pays: both stories are spec'd against code that will change under them, so one re-runs stage 4 either way, and this spec is two files where 94's spans the client, engine, manifest and reindexer.

**Fix versions.** This ships code in both repos, so by the project's rule it carries the next report-server version alongside `cc-data-cli 0.2.0`. REPORT-93's decision to leave partition estimation to Athena's own failure reason depends on this being in 0.2.0.

## Out of Scope

- Changing the guidance text or the reason patterns, which are REPORT-106's and are already covered by its tests.
- Storing the guidance. It is derived from the reason at render time on both surfaces.
- Portal report consumption and the sync/async execution branch, which are REPORT-94.
- Changing the exit-code contract. A failed run keeps its exit code; only what accompanies it changes.
- Surfacing failure reasons for post-processing jobs, which have their own `status` vocabulary and no Athena reason.

## Open Questions

None. Three were raised while this was scoped to the client alone: whether to show the guidance at all, which the widening answers; whether it is sequenced after REPORT-94, which it is, for the reason in the Technical Notes; and whether the two fields belong on the run wire type, which they do, without widening the human table.

## Self-Review

### Senior Engineer

#### RESOLVED: the guidance depends on the report, not only on the reason

`guidance_for/2` chooses between two tables by `offers_app_filter?(report)` (`athena_failure.ex:62-79`), so the same Athena reason yields different advice on a report that offers the application filter and one that does not: the partition-limit case names that filter only where it exists. A first draft could reasonably read "derive the guidance from the stored reason" as a one-argument operation and pass `nil`, which falls through to `offers_app_filter?(_report), do: false` and silently serves the narrower table for every report.

So the requirement that the report reaches `athena_download/3` is load-bearing rather than tidiness, and the failure mode if it does not is wrong advice rather than an error. Asserted by a test that the same reason produces different guidance for `student-actions`, which offers the filter, and `teacher-actions`, which does not.

#### RESOLVED: `NOT_READY` is not the failed case, it is every non-succeeded case

`athena_download/3`'s final clause matches any state that is not `succeeded` (`report_controller.ex:165-172`), so `queued`, `running` and a null state all return `NOT_READY` with the same body. Adding guidance there means the field is present and null on every poll of a healthy run, which is harmless but worth stating so nobody reads a null guidance as a mapping failure. It also means the guidance is computed on each poll; that is a string match over a small ordered table with no query behind it, so the cost is nil.

The client is unaffected: `pollUntilReady` consumes non-terminal `NOT_READY` responses internally and only builds a user-facing error on a terminal state, so the extra field surfaces exactly where it is wanted.

### Security Engineer

#### RESOLVED: passing `Extra` through wholesale is the benefit and the risk in one mechanism

The requirement says `stateExtra/2` stops rebuilding the map so "a field the server adds later needs no client release to become visible". That property has no filter in it: whatever the server puts in a `NOT_READY` body lands in the JSON error envelope the CLI prints and the MCP tool returns. Today that is three intended fields, and the body is already scoped to a run the caller owns, so nothing leaks now. But the same mechanism means a future server field is disclosed by default rather than by decision, in a client that will not be re-reviewed when the server changes.

The alternative is an allowlist: copy the keys this client knows, and gain nothing automatically. That reintroduces exactly the failure this story exists to fix, where the server grew fields and the client kept building a map with one key in it.

**Decision**: pass through, and make it a stated contract rather than an accident.

One key is the exception, and it is worth stating precisely because the rest is unfiltered: the client removes `athena_query_guidance` after promoting it into `Action`, so the envelope carries that sentence once. That is not the allowlist rejected below. An allowlist enumerates what may leave and drops what it does not recognize, which is the failure this story exists to fix; this removes exactly one key, the one the client provably did not drop because it rendered it, and forwards everything else, recognized or not. Verified rather than reasoned: a body carrying a key this client has never heard of still reaches the envelope intact with the promotion in place.

The two repositories are one trust domain and the body is already scoped to a run the caller owns, so the exposure is theoretical today. What makes it worth deciding rather than inheriting is where the guard belongs: on the server, where someone adding a field to an error body can see the rule, not on a client that would silently drop it and be re-edited years later by whoever notices.

So the `NOT_READY` body is defined as caller-visible, and a server-side test asserts that its keys are exactly the intended set. That test fails when a field is added, which is the moment the question should be asked, and it is the only version of this guard that cannot rot: a client-side allowlist would keep passing while the server changed underneath it.

The alternative was rejected on its record rather than its reasoning. An allowlist is safe by construction and is precisely the shape that produced this story: the server grew `athena_query_id` and `athena_query_error`, and `stateExtra/2` kept building a map with one key in it.

### Whoever has to run the tests

#### RESOLVED: the job download path shares the function and must not change

`stateExtra/2` serves both report and job downloads, and the job branch returns `{"status": status}`. The job endpoint's `NOT_READY` body is `%{status: status}` and carries no Athena fields, since a job has no Athena query (`report_job_controller.ex:57`). So passing the server's `Extra` through is behavior-preserving for jobs, where a hand-written allowlist would have had to remember them separately.

That is worth an assertion rather than an argument: a job download that is not ready keeps today's envelope exactly, which is the test that fails if the passthrough is written to assume report-shaped keys.

### Security Engineer

#### RESOLVED: the raw reason is the one value this server deliberately refuses to log

`AthenaFailure.error_code/1` exists because the reason cannot be treated as safe text: its doc says
"Athena's message can echo the query, and the queries this server generates embed secure keys and
learner endpoint urls, so the message must not reach the logs" (`athena_failure.ex:40-48`), and
`athena_query_poller.ex:24,27` logs `error_code(reason)` rather than the reason for exactly that
reason. This story routes the full reason somewhere new. It is already on the wire
(`report_json.ex:32`, `report_controller.ex:171`) and already rendered in the browser
(`custom_components.ex:161`), so the server discloses nothing it did not disclose yesterday, but
cc-data currently drops it and after this story it prints it: `EmitError` writes the envelope to
stdout unconditionally, with no human-versus-JSON split (`output.go:112`, `cmd/root.go:71`), and
`codedError/1` returns the same envelope as an MCP tool result. For an MCP caller that means a
possibly-query-echoing reason enters a model's context and whatever transcript its host keeps.

Requirements say the raw reason is always the authority and the guidance is only an aid, so
truncating to `error_code/1` would contradict the story's own point. The distinction that actually
justifies the difference is the sink, not the string: the log is long-lived, shared across users and
not scoped to a run's owner, while this response is scoped to a run the caller owns and shows them
what their own browser already shows them. That is a defensible answer and it is currently nowhere
in the spec, so the first reviewer to open `athena_failure.ex` will ask.

**Decision**: keep the full reason, unchanged. The finding was right that the spec had to say
something and wrong that the question was open: REPORT-106 asked it and answered it, and its answer
extends to the CLI and MCP without amendment. A Technical Note now records the sink distinction, the
REPORT-106 citation, and the `teacher-actions` username case. The two alternatives were rejected on
their own terms: cutting to `error_code/1` contradicts this spec's rule that the raw reason is the
authority and would make the CLI strictly worse than the browser for the same user on the same run,
and suppressing the reason for MCP alone needs the MCP-specific code path the requirements forbid,
next to a `query` tool that already returns the data itself.

---

### API Consumer (CLI and MCP)

#### RESOLVED: the guidance is printed twice in one envelope

Two requirements are individually right and jointly produce a duplicate. The guidance renders into
`CLIError.Action`, and `stateExtra/2` stops filtering so the server's `Extra` reaches the envelope
whole. `Envelope/0` merges `Extra` after setting `action` (`output.go:43-55`), so both survive.
Measured, by applying the change and running a fake server that sends the mapped-reason body:

```
{"action":"This query covers too many Athena partitions. Narrow it with a date range and run it
again.","athena_query_error":"HIVE_EXCEEDED_PARTITION_LIMIT: too many","athena_query_guidance":"This
query covers too many Athena partitions. Narrow it with a date range and run it again.",
"athena_query_id":"qid-1","athena_query_state":"failed","error":"NOT_READY","message":"run 584 is in
terminal state \"failed\"; nothing to download"}
```

The same sentence twice on the line a human reads and an agent parses.

Options: keep both, so the wire field and the contract field are each intact; or move rather than
copy, deleting `athena_query_guidance` from `Extra` once it is in `Action`. Moving does not
reintroduce the allowlist this story removes: the client already has to name this one key to lift
it, which is why the plan makes it a named constant, and every other key still passes through
untouched.

**Decision**: promote rather than copy. The client lifts the guidance into `Action` and forwards
every other key untouched.

Keeping both copies was considered at length and rejected on a contradiction of its own making. The
case for two fields was that `action` is the client's own field and may one day say something the
server did not, and the guard proposed alongside it was a test asserting the two are always equal.
Those cannot both hold: the test forbids exactly the divergence that justified the second copy, and
the first person who wants it has to delete the guard.

The measurement is secondary but real: 447 characters against 316 on the mapped-reason envelope, a
91-character sentence twice, up to about 150 for the slowdown text.

#### RESOLVED: `ToRunJSON` is also the create and duplicate payload

The requirement scopes the three new run fields to "`reports list --json` and the `reports_list` MCP
payload". `ToRunJSON` also builds the `reports create` and `reports duplicate` result lines
(`cmd/reports.go:179`) and the `reports_create` and `reports_duplicate` MCP payloads
(`tools.go:105,118`), so those three surfaces change too. In practice nothing appears there, since a
newly created or duplicated run has no query yet, but the MCP output schema for all three tools
gains the properties. Verified harmless: adding the three pointer fields with `omitempty` and
running the mcpserver suite is green, so schema reflection accepts them.

**Decision**: the requirement names all six surfaces, and the "a newly created run carries none of
them" case is kept as an assertion, since it is the one that fails if a future field is added
without `omitempty`.

---

### Release Manager

#### RESOLVED: nothing tells the release which server version the client half needs

The client renders guidance only when the server sends it, and a server without REPORT-127 makes
`get report` print reason and query id with no action. That is the graceful degradation the
requirements ask for, so it is not a defect, but it means the story's user-visible outcome depends
on report-server `1.11.0` reaching the environment, exactly like REPORT-128's entry in
`follow-ups.md`. There is no entry for this one, and `follow-ups.md` currently carries a REPORT-93
entry whose entire trigger is this story landing.

Fix versions were checked in Jira rather than assumed: REPORT-127 already carries both
`cc-data-cli 0.2.0` and `1.11.0`, so the rule the requirements state is satisfied and no Jira edit
is needed.

**Decision**: an entry was added to `follow-ups.md`, keyed on `cc-data-cli 0.2.0` being cut, saying
that the client half degrades to reason-and-id with no advice against a server without this story.
The REPORT-93 entry it sits beside is discharged when this merges.
