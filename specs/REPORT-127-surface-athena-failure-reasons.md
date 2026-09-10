# Surface Athena failure reasons and their guidance outside the web UI

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-127

**Repos**: https://github.com/concord-consortium/cc-data-cli and https://github.com/concord-consortium/report-service

**Status**: **Closed**

## Overview

Make a failed Athena run explain itself everywhere, not only in the browser: expose REPORT-106's reason-to-guidance mapping over the v1 API, and stop cc-data discarding the failure fields the server already sends. Spans both repositories, like REPORT-92 and REPORT-106 before it.

## Requirements

### Server

- The mapped guidance is returned alongside the raw reason, in both the `NOT_READY` download body and the run JSON, so any client sees what the run page shows.
- It is derived per request from the stored reason, as the web UI already derives it. Nothing new is stored and no migration is needed.
- A reason with no mapping returns the raw reason and no guidance, rather than a placeholder. The mapping is an aid and the raw reason is always the authority.
- The `NOT_READY` body is contractually caller-visible, and a test asserts its keys are exactly the intended set.
- The web UI keeps rendering guidance through the same function, so the browser and the API cannot disagree about what a reason means.

### Client

- `cc-data get report <run-id>` on a failed Athena run reports Athena's reason, the query id, and the suggested next step.
- The guidance is promoted into `CLIError.Action` rather than copied into it, so the envelope carries the sentence once and `Action` stays free to say something the server did not.
- The same information reaches an MCP caller through the existing envelope, with no MCP-specific code path.
- `stateExtra/2` is deleted rather than corrected. Its one caller forwards what the server put in the body, minus the guidance key it has just rendered, so a field the server adds later needs no client release to become visible. The job download path keeps today's envelope exactly.
- `athena_query_id`, `athena_query_error` and the guidance are optional on the run wire type and appear wherever a run is rendered as JSON: `reports list --json`, `reports create` and `reports duplicate`, and the three matching MCP payloads. A newly created or duplicated run has no query yet and carries none of them. The human table is unchanged.
- A server that sends none of them is not an error, and a current server reporting a failure it has no reason for prints no null-valued keys: `Envelope/0` drops nil `Extra` values as it already drops an empty message and action.
- Portal runs carry none of them, having no Athena query.

### Both

- Tests pinned to wire captures of a failed run with a mapped reason, a failed run with an unmapped reason, and a server that sends none of the three fields.
- One definition of the guidance. Neither repo gains a second copy of the reason-to-suggestion table.

## Technical Notes

**The server change is a parameter, not a lookup.** `download/2` already resolves the report and passes it to `portal_download/4` while the Athena branch took only the run. Passing it to both makes them symmetrical and gives `guidance_for/2` what it needs. `run_json/1` resolves the report once and passes it to `report_type`, `execution` and the guidance.

**The guidance depends on the report, not only on the reason.** `guidance_for/2` chooses between two tables by `offers_app_filter?(report)`, so the partition-limit case names the application filter only where it exists, and a `nil` report silently serves the narrower table. `student-actions` and `student-actions-with-metadata` are the only API reports that offer it; `teacher-actions` is the Athena report that does not.

**English over the wire is the existing convention.** Returning a machine-readable code and rendering text per surface reads cleaner as API design and is wrong here: it would put the wording in two places, which the single-mapping requirement exists to prevent.

**The reason can echo the query, and REPORT-106 settled what that means.** The generated SQL inlines learner secure keys as literals (`report_query.ex:129`) and, for `teacher-actions`, portal usernames, which is why `AthenaFailure.error_code/1` exists and why the poller logs the code alone. The restriction is about the sink, not the string: a log is long-lived, shared and outside the owner gate, while this response is scoped to a run the caller owns. cc-data's MCP tools already return the rows those secure keys identify. `teacher-actions` is the one case worth naming, since its echo can include teacher usernames, which `hide_names` does not cover as policy rather than as a gap.

**The decoder already passes unknown fields through, verified rather than assumed.** A body carrying a key the client has never heard of arrives intact in `APIError.Extra`. The same check found that explicit JSON nulls decode as nil values, which is why dropping nils is a requirement rather than a nicety.

**The `--no-wait` path needs nothing, because of one branch ordering.** `isTerminalFailure` is tested before `opts.NoWait` and before the poll deadline, so `notReadyResult/2` only ever serves `queued`, `running` and a null state. Reordering those checks would silently return `--no-wait` to reporting a bare state, and nothing pins the order.

**The reason is bounded server-side.** `AthenaFailure.truncate/1` caps it before storage, so the client needs no length handling and must not add a second cap that could disagree.

**Fix versions.** Ships code in both repos, so it carries `cc-data-cli 0.2.0` and report-server `1.11.0`. REPORT-93's decision to leave partition estimation to Athena's own failure reason depends on this being in 0.2.0.

## Out of Scope

- Changing the guidance text or the reason patterns, which are REPORT-106's and are covered by its tests.
- Storing the guidance. It is derived from the reason at render time on both surfaces.
- Portal report consumption and the sync/async execution branch, which are REPORT-94.
- Changing the exit-code contract. A failed run keeps its exit code; only what accompanies it changes.
- Surfacing failure reasons for post-processing jobs, which have their own `status` vocabulary and no Athena reason.

## Decisions

### Should the guidance be surfaced at all, or only the raw reason?

**Decision**: surface it. The story was originally scoped to the client alone, where only the reason was reachable; widening it to both repos is what makes the guidance available, and REPORT-106 had already built the mapping for one surface.

### Is this sequenced after REPORT-94?

**Decision**: yes. REPORT-94 rewrites `types.go` and `FetchReport`, which this touches. The reason is not conflict avoidance but which spec pays: both were written against code that would change underneath them, and this one is two files, whereas REPORT-94 spans the client, engine, manifest and reindexer.

### Do the failure fields belong on the run wire type?

**Decision**: yes, without widening the human table. The table is rendered from `api.ReportRun` directly and is pinned row by row by an existing test.

### Does the guidance need the report, or only the reason?

**Context**: reading "derive the guidance from the stored reason" as a one-argument operation and passing `nil` compiles and returns plausible advice.
**Decision**: it needs the report. A `nil` falls through to `offers_app_filter?(_report), do: false` and serves the narrower table for every report, so the failure mode is wrong advice rather than an error. Asserted by a test that the same reason yields different guidance for `student-actions` and `teacher-actions`.

### Should the client forward the server's error context, or copy known keys out of it?

**Options considered**: an allowlist that copies the keys this client knows; forwarding whatever the server sent.
**Decision**: forward. An allowlist is safe by construction and is precisely the shape that produced this defect, where the server grew two fields and `stateExtra/2` kept building a map with one key in it. The guard belongs on the server, where someone adding a field to an error body can see the rule, which is why the `NOT_READY` body's key set is pinned by a test there. `AsCLIError` had already forwarded `Extra` wholesale for every other coded error; this removed the one branch that opted out.

### Is removing the guidance key from the forwarded body an allowlist by another name?

**Decision**: no. An allowlist enumerates what may leave and drops what it does not recognize. This removes exactly one key, the one the client provably did not drop because it rendered it into `Action`, and forwards everything else. Verified by feeding a body carrying a field this client predates and watching it reach the envelope intact.

### Copy the guidance into `Action`, or move it?

**Context**: `action` and `athena_query_guidance` are different keys, so copying prints the same sentence twice on the one line a human reads and an agent parses (447 characters against 316).
**Options considered**: keep both keys and assert in a test that they are equal; promote the key into `Action` and forward the rest; leave `Action` unset and let the wire key stand alone.
**Decision**: promote. Keeping both was rejected on a contradiction of its own making: the case for two fields was that `Action` may one day say something the server did not, and the proposed guard was a test asserting the two are always equal, which forbids exactly that divergence. Leaving `Action` unset loses the field the exit-code contract already defines for the next step.

### Should the full reason be printed by the CLI and returned to an MCP caller, given it can echo the query?

**Options considered**: print the full reason; cut it to `error_code/1` and leave the full reason to the run page; suppress it on the MCP path only.
**Decision**: print it. REPORT-106 answered this for the API and the run page on the ground that the owner supplied or already received every identifier in their own query. Cutting to the code contradicts this spec's own rule that the raw reason is the authority and would make the CLI worse than the browser for the same user on the same run. Suppressing it for MCP needs the MCP-specific path the requirements forbid, next to a `query` tool that already returns the data those identifiers name.

### `NOT_READY` covers every non-succeeded state, not just failure

**Decision**: accepted and recorded. The guidance field is present and null on every poll of a healthy run, which is harmless, and it is computed per poll, which is a string match over a small ordered table with no query behind it. Nobody should read a null guidance as a mapping failure.

### The job download path shares the changed branch

**Decision**: forwarding is behavior-preserving for jobs, whose `NOT_READY` body is `%{status: status}` with no Athena fields, where a hand-written allowlist would have had to remember them separately. Asserted by a test that pins the job envelope byte for byte, which is what fails if the forwarding is ever written to assume report-shaped keys.

### Which surfaces do the three new run fields reach?

**Decision**: all six that `ToRunJSON` shapes, not the listing alone: `reports list --json`, `reports create`, `reports duplicate`, and the three matching MCP payloads. Nothing appears on a created or duplicated run, which has no query yet, and the assertion that keeps it that way is the one that fails if a future field is added without `omitempty`.

### Where does `omitempty` go?

**Decision**: on `RunJSON`, which is encoded. `api.ReportRun` is only ever decoded and its existing `AthenaQueryState` carries no such tag, so the fields added beside it match their neighbor.

### Does the client need to know anything about the mapping?

**Decision**: one thing, the wire key's name, which is unavoidable and is a named constant (`api.FieldAthenaQueryGuidance`) rather than a literal in the middle of a function. The wording stays the server's, and the wire captures verify the name still matches.

### Which wins in `Envelope/0` when a forwarded body carries `error`, `message` or `action`?

**Context**: forwarding unknown keys made a collision reachable that the merge order had made moot. `decodeAPIError` strips `error` and `message` into their own fields but leaves an `action` key in `Extra`, and `AsWriteCLIError` sets `Action` to the advice that a `reports create` or `reports duplicate` may have landed anyway. Nothing pins the error bodies those two routes return.
**Decision**: the client's three fields win, set after the merge rather than before it, so the precedence is stated rather than a side effect of statement order. Those three name this client's exit-code contract, and a server with something new to say adds a key the forwarding now carries. Losing the write advice is the case that decided it: it invites the duplicate run it exists to prevent.

### Must the nil-drop in `Envelope/0` distinguish nil from falsy?

**Decision**: yes, and a `v != nil` guard does. Built and run: `0`, `false` and `""` all survive and only an untyped nil is removed. A `reflect.DeepEqual` against a zero value or a `v == ""` shortcut would silently drop a legitimate `false`.

### Should `stateExtra/2` be corrected or deleted?

**Decision**: deleted. It had one caller, so replacing that argument orphans it, and Go makes leaving it a build failure rather than a style preference. Its job branch is subsumed, since the passthrough reproduces `{"status": state}` exactly.

### Can the report reaching the Athena download branch be nil?

**Decision**: yes, for a run whose slug has left the tree, and it is accepted rather than guarded. `guidance_for(nil, reason)` does not raise; it returns advice identical to `teacher-actions`. So a stale slug degrades to slightly narrower advice, where the run page would raise on `assigns.report.type` for the same run.

### How is "the browser and the API cannot disagree" actually asserted?

**Context**: sharing a function makes agreement likely, not guaranteed, since either caller can post-process the result.
**Decision**: by a component test rather than a LiveView one. `custom_components_test.exs` already renders the failure block through `render_component/2`, so the assertion is a few lines: build one failed run and assert the run page's rendered HTML contains the guidance `run_json/1` emits for it. It is not tautological, since it fails if either surface post-processes the string; it cannot catch both surfaces being wrong the same way, which is what sharing a function already gives. The rendered side is compared against the HTML-escaped form, because the page escapes what the JSON returns raw.

### Two additions no requirement asked for

**Decision**: both kept, deliberately rather than silently. `run_json/1` resolves the report once instead of making a third `Tree.find_report/1` call, and the wire key becomes a named constant instead of a bare string literal.

### Does the suite already guard any of this?

**Decision**: no, and that changes what the new tests are for. Applied to `main` at `ee3e9ca`, the whole client change left all sixteen packages green with no test edited: nothing asserted the terminal-failure envelope beyond its exit code, and there was no job terminal-failure test at all. The new tests establish the guard rather than protect one, so a green suite was never evidence this change was covered.

### Pinning a caller-visible body is an existing convention, not a new one

**Decision**: recorded, because it is the better argument for the guard. `@run_keys` pins the run JSON, `report_duplicate_test.exs` pins the `PORTAL_DUPLICATE_UNNECESSARY` body and `filter_options_controller_test.exs` pins the filter-options envelope. The `NOT_READY` body was the one context-carrying body with no pin, and adding a key to the run JSON fails `@run_keys` until that list is updated in the same commit.

### Each commit must pass on its own

**Decision**: the body-keys guard asserts the key set as it stands when it lands, and the step that adds the guidance key updates the list in the same commit. An earlier draft had the guard assert the future set so it would be seen failing, which buys a broken intermediate commit for nothing the guard needs.

### The client half needs the server release, and nothing in Jira says so

**Decision**: the coordination belongs at release time rather than in code, and the check is stated here so it travels with the spec. **Before `cc-data-cli 0.2.0` is cut, confirm report-server `1.11.0` is deployed to the environment researchers are pointed at.** Against an older server the CLI prints the reason and the query id with no suggested next step, which is the intended graceful degradation but leaves the story reading as done while half of it is invisible to the person it was built for. Fix versions were verified in Jira rather than assumed: REPORT-127 carries both `cc-data-cli 0.2.0` and `1.11.0`, so the two releases are linked where a release manager will look.
