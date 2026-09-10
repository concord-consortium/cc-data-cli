# Implementation Plan: Surface Athena failure reasons and their guidance outside the web UI

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-127
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

Two repositories, two PRs. The report-service steps land first and independently, since the client cannot render a field the server does not send; the cc-data-cli steps land after REPORT-94, which rewrites the same two client files.

Stage 4 was re-run against shipped `types.go` and `internal/fetch/report.go` on 2026-09-09, after REPORT-94 merged as `ee3e9ca`. Every assumption below survived; the client `file:line` citations were corrected to the merged code and one branch-ordering fact REPORT-94 introduced was added to the requirements' Technical Notes.

That re-run covered the client only. The server citations were re-measured separately on 2026-09-10 against report-service `master` at `860bc79`, which had moved twice underneath them: REPORT-93 added the create and duplicate actions above the download path and REPORT-128 added the streaming budget, displacing the download path by about eighty-five lines. Every server citation in both files now names the shipped line, and the two test files this plan touches were checked to exist.

## Implementation Plan

### Server: pin what the NOT_READY body is allowed to carry

**Summary**: the client is about to forward this body verbatim, so what it contains stops being incidental. The guard lands first and on its own, pinning the contract as it stands, so that every later change to the body is a visible edit to a stated set rather than an unnoticed addition.

**Files affected**:
- `server/test/report_server_web/api/v1/report_controller_test.exs`: extend.
- `server/test/report_server_web/api/v1/report_job_controller_test.exs`: extend.

**Estimated diff size**: ~70 lines

The requirements define the `NOT_READY` body as caller-visible, and this is where that becomes enforceable: a test asserts the body's keys are exactly the intended set, for the report route and the job route separately, since the two bodies differ and only one has Athena fields.

The key set is a module attribute rather than an inline list, which is how `@run_keys` and
`@filter_keys` are already written in the same file, and it gives the next step one place to edit:

```elixir
  # cc-data forwards this body into the error it prints and into its MCP tool result, so a key
  # added here reaches every API caller.
  @not_ready_keys ~w(athena_query_error athena_query_id athena_query_state error message)

  test "the NOT_READY body carries exactly the caller-visible keys", %{raw_token: raw_token, user: user} do
    run = run_fixture(user, %{athena_query_id: "qid-failed", athena_query_state: "failed"})

    body = json_response(get(authed_conn(raw_token), ~p"/api/v1/reports/#{run.id}/download"), 409)

    assert Enum.sort(Map.keys(body)) == Enum.sort(@not_ready_keys)
  end
```

It asserts the key set as it stands today, so this commit passes on its own; the next step adds `athena_query_guidance` and updates the list in the same commit, which is where that addition is deliberate. An earlier draft had this assert the future set so the guard would be seen failing, which is a broken build between two commits in exchange for nothing the guard needs.

This completes a convention rather than inventing one, which is the honest way to argue for it. Pinning a caller-visible body is already what this suite does: `@run_keys` pins the run JSON (`report_controller_test.exs:16-17`, asserted at 268), `report_duplicate_test.exs:96` pins the `PORTAL_DUPLICATE_UNNECESSARY` body, and `filter_options_controller_test.exs:37` pins the filter-options envelope. The `NOT_READY` body is the one context-carrying body with no pin, which is the same shape of omission as the client's, where `AsCLIError` has forwarded `Extra` wholesale for every other coded error since it was written (`errors.go:99`) and only the terminal-failure branch opted out.

### Server: return the guidance the run page already shows

**Summary**: `AthenaFailure.guidance_for/2` is called from one place, the web UI. Two API responses start calling the same function.

**Files affected**:
- `server/lib/report_server_web/api/v1/report_controller.ex`: pass the report into `athena_download/3`; add the key.
- `server/lib/report_server_web/api/v1/report_json.ex`: add the key; resolve the report once.
- `server/test/report_server_web/api/v1/report_controller_test.exs`: extend, including `@run_keys`.
- `server/test/report_server_web/components/custom_components_test.exs`: extend, for the cross-surface assertion.

**Estimated diff size**: ~120 lines

`download/2` already resolves the report and hands it to `portal_download/4` while giving `athena_download/3` only the run (`report_controller.ex:128-140`). The Athena branch takes it too, which makes the two symmetrical and supplies what the mapping needs:

```elixir
      %ReportRun{athena_query_state: athena_query_state} ->
        ErrorHelpers.render_error(conn, "NOT_READY", "The report is not ready to download.", %{
          athena_query_state: athena_query_state,
          athena_query_id: report_run.athena_query_id,
          athena_query_error: report_run.athena_query_error,
          athena_query_guidance: AthenaFailure.guidance_for(report, report_run.athena_query_error)
        })
```

The report argument is load-bearing, not decoration. `guidance_for/2` selects between two tables by `offers_app_filter?(report)` (`athena_failure.ex:62-79`), so the same reason yields different advice depending on whether the report offers the application filter, and a `nil` report silently falls through to the narrower table. Passing the report is what keeps the API's advice identical to the run page's.

The branch that reaches `athena_download` is `download/2`'s catch-all, so the value it binds is whatever `Tree.find_report/1` returned and can be `nil` for a run whose slug has left the tree (`tree.ex:54-59`). That is accepted rather than guarded: measured, `guidance_for(nil, reason)` does not raise, it falls through `offers_app_filter?(_report), do: false` and returns advice byte-identical to `teacher-actions`. So a stale slug degrades to slightly narrower advice, where the run page would raise on `assigns.report.type` for the same run.

`run_json/1` gaining a key breaks an existing assertion, which is the edit most easily missed: `@run_keys` pins the run JSON's key set and must gain `athena_query_guidance` in this same commit.

`run_json/1` calls `Tree.find_report/1` twice already, for `report_type` and `execution`. It resolves it once and passes it to all three, which is a small refactor this change makes worth doing rather than a third lookup.

Tests: a failed run with a mapped reason returns the guidance in both the `NOT_READY` body and the run JSON; the same reason returns *different* guidance for `student-actions` and `teacher-actions`, which is the assertion that fails if the report is not threaded through; an unmapped reason returns the raw reason and a null guidance; a queued run returns nulls rather than a placeholder.

The `student-actions` versus `teacher-actions` pair is the one worth naming: with a single report in the fixture, passing `nil` and passing the report are indistinguishable. Measured on `860bc79`, those are the right two slugs: `student-actions` and `student-actions-with-metadata` are the only API reports carrying `enable_app_filter`, `teacher-actions` is an Athena API report without it, and the fixture's default slug, `student-answers`, is on the narrow table, so a test that does not override the slug proves nothing.

The browser-and-API agreement is asserted rather than argued, and it needs no LiveView: `custom_components_test.exs:95-101` already renders the failure block through `render_component(&CustomComponents.report_header/1, ...)` with a plain `%Report{}` and `%ReportRun{}`. One test resolves `student-actions` from the tree, builds a failed run against it, and asserts the rendered HTML contains the `athena_query_guidance` string `run_json/1` emits for the same run. That is not true by construction: it fails if `run_json/1` emits the code instead of the guidance, if either surface post-processes the string, or if the run page stops rendering it. What it cannot catch is both surfaces being wrong the same way, which is the property that sharing one function already gives.

### Client: stop discarding what the server sent

**Summary**: one function is deleted, its caller forwards what the server sent, and the envelope stops printing nulls. Everything downstream already works.

**Files affected**:
- `internal/fetch/report.go`: delete `stateExtra/2`; forward `apiErr.Extra` at its one call site.
- `internal/output/output.go`: `Envelope/0` skips nil values.
- `internal/fetch/report_test.go`, `internal/output/output_test.go`: extend.

**Estimated diff size**: ~110 lines

`pollUntilReady` carries the server's `Extra` forward instead of constructing a new one:

```go
	// The NOT_READY body is a caller-visible contract, pinned server-side, so every key
	// except the rendered guidance is forwarded: a field the server adds later reaches the
	// envelope with no client release.
	Extra: apiErr.Extra,
```

Verified against the decoder rather than assumed: a body carrying a key the client has never heard of arrives intact in `APIError.Extra`, so a future server field needs no client release. The same check found that explicit JSON nulls decode to nil values and `Envelope/0` copies every key unconditionally (`output.go:51-53`, unchanged by REPORT-94), so a failed run with no recorded reason would print `"athena_query_error":null`. `Envelope/0` skips nil values, alongside the empty-string checks it already makes for message and action.

`stateExtra/2` does not survive the edit in a corrected form, it goes: its only caller is the terminal-failure branch (`report.go:196`), so replacing that argument orphans it, and Go makes this a build failure rather than a preference.

The job download path shares that call site and must not move: its `NOT_READY` body is `%{status: status}` and carries no Athena fields (`report_job_controller.ex:57`), so forwarding reproduces `{"status": state}` exactly, where an allowlist would have had to remember jobs separately.

Tests: a failed run's envelope carries the state, the query id and the reason; a failed run with no reason carries the state and no null keys; a job that is not ready keeps today's envelope exactly, which is the assertion that fails if the forwarding assumes report-shaped keys; `Envelope/0` drops a nil `Extra` value and keeps a false one, since `false` is a value and `nil` is an absence.

These tests establish the guard rather than protect one, and the step should be read that way. Measured by applying this change to `main` at `ee3e9ca` and running the suite untouched: all sixteen packages stay green. Nothing today asserts the terminal-failure envelope beyond its exit code (`report_test.go:252`), and there is no job terminal-failure test at all, the only `--job` coverage being the Portal refusal at `report_test.go:501`. So a green suite is not evidence this change is covered, until these tests exist.

### Client: render the guidance as the action

**Summary**: the guidance becomes `CLIError.Action`, which the envelope and the MCP renderer already carry. It is promoted into that field rather than copied into it, so the sentence appears once.

**Files affected**:
- `internal/fetch/report.go`: promote the guidance out of the forwarded `Extra` into `Action`.
- `internal/api/types.go`: the wire key as a named constant.
- `internal/fetch/report_test.go`: extend.
- `internal/mcpserver/server_test.go`: extend, for the CLI and MCP parity assertion.

**Estimated diff size**: ~70 lines

`Action` exists for this: `Envelope/0` emits it when non-empty and `codedError/1` renders the same envelope for MCP so "a tool caller sees the same code, message and action a terminal user does" (`mcpserver/server.go:51-61`). The guidance is read out of the forwarded `Extra` rather than from a typed field, because the client has no reason to hold a second copy of its wording.

`Envelope/0` merges `Extra` after setting `action` (`output.go:43-55`), so copying the key would print the same sentence twice on the line a human reads and an agent parses: measured, 447 characters against 316 for the mapped-reason envelope, the guidance itself being 91 characters here and about 150 for the slowdown text. The rule is therefore to promote and forward:

```go
// promoteGuidance renders the server's guidance into the envelope's action field and forwards every
// other key untouched, so the sentence reaches the caller once rather than under two names.
func promoteGuidance(extra map[string]any) (map[string]any, string) {
	guidance, _ := extra[api.FieldAthenaQueryGuidance].(string)
	if guidance == "" {
		return extra, ""
	}
	out := make(map[string]any, len(extra)-1)
	for k, v := range extra {
		if k == api.FieldAthenaQueryGuidance {
			continue
		}
		out[k] = v
	}
	return out, guidance
}
```

It is a named helper rather than an inline `delete` so the rule has somewhere to be stated, and it copies rather than mutating the map it was handed, which belongs to the decoded `APIError`.

This supersedes the `Extra: apiErr.Extra` line the previous step introduces, which is why the two code blocks differ. That step stands on its own and is worth landing on its own: forwarding is the fix for the discarded fields, and promoting is how the rendered one is kept from appearing twice.

Two properties were checked by building this and running it against a fake server, not argued. A body carrying `some_future_field`, which this client has never heard of, reaches the envelope intact, so the release-free visibility the passthrough exists for is untouched. And a server that sends no guidance gets the map back unchanged, so the reasonless-failure and old-server envelopes are byte-identical to the forwarding step's.

The client does depend on one thing, the wire key's name, and that is made visible rather than left as a literal in the middle of a function: `athena_query_guidance` becomes a named constant alongside the other API field names, and the wire captures are what verify it still matches the server.

Tests: a failed run with a mapped reason sets `action` in the envelope and carries no `athena_query_guidance` key beside it; the reason, the query id and the state survive the promotion; an unmapped reason leaves `action` absent rather than empty; an unrecognized key in the same body still reaches the envelope; the MCP tool result carries the same fields as the CLI envelope for the same failure, asserted through the real tool registration rather than by constructing an envelope directly.

### Client: carry the fields on the run type

**Summary**: every JSON rendering of a run says why it failed. The human table is unchanged.

**Files affected**:
- `internal/api/types.go`: three optional fields.
- `internal/reportview/reportview.go`: carry them into `RunJSON`.
- `internal/api/reports_test.go`, `internal/reportview/reportview_test.go`: extend, with wire captures.

**Estimated diff size**: ~90 lines

Three pointer fields, so a server that does not send them produces a payload identical to today's, and a Portal run, which has no Athena query, carries none of them. `omitempty` goes on `RunJSON`, which is encoded; `api.ReportRun` is only ever decoded, and its existing `AthenaQueryState` carries no such tag, so the new fields there match it rather than the payload.

`ToRunJSON` is the one shaping function, so this reaches six surfaces rather than the listing alone: `reports list --json`, `reports create` and `reports duplicate` (`cmd/reports.go:67,179`), and the `reports_list`, `reports_create` and `reports_duplicate` MCP payloads (`tools.go:49,105,118`). Nothing appears on a newly created or duplicated run, which has no query yet, so those four payloads are unchanged in practice. Measured with the fields in place: a create payload is `{"run":{"run_id":90070,"slug":"student-answers","state":"queued","execution":"async","filter_labels":null}}`, byte for byte what it is today. That case needs no test of its own, because the mutation that would break it, a field added without `omitempty`, is what the byte-identity assertion below already catches.

The wire captures follow the `const ...Wire` convention in `internal/api/filter_options_test.go`: the exact body with a comment naming where it came from. Three are needed, matching the requirements: a failed run with a mapped reason, a failed run with an unmapped reason, and a server that sends none of the fields.

Tests: a run with the fields decodes them and they reach `RunJSON`; a run from a server without them decodes with all three absent and the JSON payload is byte-identical to today's; a Portal run carries none.

The human table needs no new test and should not get one. It is not rendered from `RunJSON`: `renderRunsTable` builds its row from `api.ReportRun` directly (`cmd/reports.go:411-428`), so a test in `internal/reportview` could not reach it. It is already guarded more strictly than a column count, by `TestRenderRunsTableDistinguishesAPortalRun` (`cmd/reports_test.go:605`), which asserts every row with `slices.Equal`. Proven rather than assumed: adding a sixth column to the renderer fails that test with three errors naming the extra cell.

## Requirements Coverage

| Requirement | Step |
|---|---|
| Guidance returned in the `NOT_READY` body and the run JSON | server: return the guidance |
| Derived per request, nothing stored, no migration | server: return the guidance |
| An unmapped reason returns the raw reason and no guidance | server: return the guidance |
| The web UI keeps rendering through the same function | server: return the guidance |
| The browser and the API cannot disagree, asserted | server: return the guidance |
| The `NOT_READY` body is contractually caller-visible, guarded by a test | server: pin what the body carries |
| `get report` on a failed run reports reason, query id and next step | client: stop discarding; client: render the guidance |
| Guidance rendered into `CLIError.Action`, not the message | client: render the guidance |
| The envelope carries the guidance once | client: render the guidance |
| The same reaches MCP with no MCP-specific path | client: render the guidance |
| `stateExtra/2` is deleted; the job path is unchanged | client: stop discarding |
| No null-valued keys in the envelope | client: stop discarding |
| Fields optional on the run type, present in every `--json` and MCP run payload | client: carry the fields |
| A server sending none of them is not an error | client: carry the fields |
| Portal runs carry none of them | client: carry the fields |
| Wire captures for the three server shapes | client: carry the fields |
| One definition of the guidance | server: return the guidance, by calling the existing function |

### Gaps found, requirement with no step

None. The one gap this plan carried, that "the browser and the API cannot disagree" was asserted by prose rather than by a test, is closed in the second server step. It was left open on the estimate that closing it needed a LiveView test. It does not: `custom_components_test.exs` already renders the failure block as a component, so the assertion is about eight lines.

### Gaps found, step no requirement asked for

**Orphan 1: `run_json/1` resolves the report once instead of three times.** No requirement asks for it. It follows from adding a third `Tree.find_report/1` call to a function that already makes two, and it is the kind of tidying that is right to do while in the file and wrong to do silently, since it changes a function this story is otherwise only adding a key to.

**Orphan 2: the wire key becomes a named constant.** From the Self-Review rather than from a requirement. It is a two-line change and the alternative is a bare string literal that nothing connects to the server that defines it.

## Open Questions

None.

## Self-Review

### Whoever has to review the resulting commits

#### RESOLVED: the first step's guard was written to fail until the second step lands

The plan had the body-keys test assert a sorted list including `athena_query_guidance`, before the step that adds that key, and justified it as "the guard fails first so it is observed failing". That is a broken build between two commits, and it breaks the rule the rest of this plan follows, that each step compiles and passes without the next one.

The fix keeps the intent without the red build. Step one asserts the key set as it is today, three Athena fields plus `error` and `message`, so it passes on the commit that introduces it and documents the contract as it currently stands. Step two adds the guidance key and updates the assertion in the same commit, which is the moment the addition is deliberate. The guard still fails on a careless future addition, which is the property that mattered; being observed failing during its own introduction was never the point, and buying it with a broken intermediate commit is a bad trade.

#### RESOLVED: the client needs the wire key name, which the plan called a thing it need not know

The requirements say the client has no reason to know the mapping, which is true of the *wording*, and the plan then reads the guidance out of `Extra` by the literal key `athena_query_guidance`. So the client does depend on one thing: the name of the key. That is unavoidable and normal, and it should be visible rather than a bare string in the middle of a function: it becomes a named constant next to the other API field names, and the wire captures are what verify it still matches the server. Nothing else about the guidance crosses the boundary.

### Senior Engineer

#### RESOLVED: dropping nil from the envelope must not drop falsy values

`Envelope/0` gaining a nil check risks over-filtering, since Go's zero values are easy to conflate. Built and run: with a `v != nil` guard, `0`, `false` and `""` all survive and only an untyped nil is removed. That is the correct line, and the test named in the step asserts it directly rather than trusting that `nil` and "empty" are distinguished, because a `reflect.DeepEqual` against a zero value or a `v == ""` shortcut would silently drop a legitimate `false`.

#### RESOLVED: `AsCLIError` does not carry an `Action`, and this story does not need it to

Checked, because the plan puts the guidance in `Action` and most API errors in this client are built by `AsCLIError`, which constructs `CLIError` with `ExitCode`, `Code`, `Message` and `Extra` and no `Action` (`errors.go:90-101`). That is not a problem here: the terminal-failure error is constructed directly inside `pollUntilReady`, so the field is set at the one site that needs it. Recorded so a later reader does not conclude that any coded API error can carry an action, and does not "fix" `AsCLIError` to forward one when nothing sends it.

### Senior Engineer (server)

#### RESOLVED: every server-side citation is stale, and the two files the plan names are wrong

The client citations were re-run against `ee3e9ca` on 2026-09-09 and hold. The server ones were not,
and report-service `master` has moved twice since: REPORT-93 added create and duplicate above the
download path and REPORT-128 added the streaming budget. Measured on `860bc79`:

| Cited | Actual |
|---|---|
| `report_controller.ex:83-88`, the `NOT_READY` render | 165-172 |
| `report_controller.ex:44-58`, `download/2` and `portal_download/4` | 128-140, 176 |
| `report_json.ex:32-34`, the three Athena fields | 30-32 |
| `athena_failure.ex:65-79`, `guidance_for/2` and `offers_app_filter?/1` | 62-79 |

`report_job_controller.ex:57` and `custom_components.ex:158` are still correct.

Two of the plan's files are wrong rather than merely displaced. `report_json_test.exs` does not
exist; the run JSON is tested through `report_controller_test.exs`, and that is where the extension
goes. And the run body already has the guard the plan proposes to invent for the `NOT_READY` body:
`@run_keys` at `report_controller_test.exs:16-17` pins the run JSON's key set and is asserted at
line 268, so adding `athena_query_guidance` to `run_json/1` fails an existing test until that list
is updated. The plan does not mention it, and it is a required edit in the same commit.

The same check answers the plan's implicit question about why the `NOT_READY` body needs a guard at
all. Pinning a caller-visible body is already this suite's convention: `@run_keys` pins the run,
`report_duplicate_test.exs:96` pins the `PORTAL_DUPLICATE_UNNECESSARY` body, and
`filter_options_controller_test.exs:37` pins the filter-options envelope. The `NOT_READY` body is
the one context-carrying body with no pin. That is a better argument for the step than the one in
the requirements, which reads as though passthrough and its guard were both being introduced here,
when `AsCLIError` has forwarded `Extra` wholesale for every other API error since it was written
(`errors.go:99`). This story removes the one place that opted out.

**Decision**: all four citations re-measured against `860bc79` in both spec files,
`report_json_test.exs` replaced with `report_controller_test.exs`, `@run_keys` named as a required
edit in the second step, and the first step reframed as completing the suite's existing
pinned-body convention.

#### RESOLVED: the report passed to the Athena branch can be nil, and the plan's snippet cannot bind it

`download/2` matches `%Report{type: :portal} = report` and sends everything else to
`athena_download/3` through a bare `_` (`report_controller.ex:133-136`). `Tree.find_report/1`
returns `nil` for a slug not in the tree (`tree.ex:54-59`), so the catch-all has to bind a value
that may be `nil`, and the plan's snippet passes `report` without saying where it comes from.

Verified rather than assumed: `guidance_for(nil, reason)` does not raise, it falls through
`offers_app_filter?(_report), do: false` and returns the narrower table's advice, which is
byte-identical to what `teacher-actions` gets. So a stale slug degrades to slightly worse advice
rather than to an error, while the run page would raise on `assigns.report.type` for the same run.

**Decision**: recorded in the second server step. The catch-all binds the value deliberately and
`nil` is accepted, since it degrades to narrower advice rather than to an error.

---

### Senior Engineer (client)

#### RESOLVED: `stateExtra/2` does not change, it dies

The step is titled "stop discarding what the server sent" and describes `stateExtra/2` as no longer
rebuilding a map, and the requirement says it "stops rebuilding the error's `Extra` from scratch".
Both read as though the function survives in an altered form. It has exactly one caller
(`report.go:196`), so replacing that argument with `apiErr.Extra` orphans it entirely, and the
project's own review rule is that the last caller going away takes the function with it.

Verified while the change was applied: with the call site switched and `stateExtra` left in place,
the build fails on an unused function, so this is not a style preference. Its job branch is
subsumed too, since a job's `NOT_READY` body is `%{status: status}` and the passthrough reproduces
`{"status": state}` exactly.

**Decision**: both the requirement and the step say deleted.

---

### QA Engineer

#### RESOLVED: the entire client change passes the existing suite untouched

Measured, not inferred. The proposed client change was applied to `report.go` and `output.go` on
`main` at `ee3e9ca`, and `go build ./... && go test ./...` was green across all sixteen packages
with no test edited. Nothing today asserts the terminal-failure envelope beyond its exit code
(`TestGetReportTerminalFailure`, `report_test.go:252`), and there is no job terminal-failure test at
all: the only `--job` coverage is the Portal refusal at `report_test.go:501`.

That is worth stating in the plan because it changes what the new tests are for. They are not
regression protection around an existing guard, they are the guard, including the one the plan
describes as asserting that the job path is unchanged. There is nothing for that assertion to
regress against until it is written.

The four client behaviors were exercised against a fake server before this was written, and all four
hold: a mapped reason yields the action, an unmapped reason leaves `action` absent rather than
empty, a server that sends only `athena_query_state` produces today's envelope byte for byte, and a
current server reporting a reasonless failure emits no null-valued keys.

**Decision**: recorded in the client step, with the measurement, so nobody later reads a green suite
as evidence the change is covered.

#### RESOLVED: Gap 1 is cheaper to close than the plan assumed, so it should be closed

The plan leaves "the browser and the API cannot disagree" asserted by prose and estimates the fix as
a LiveView test. It is not: `custom_components_test.exs:95-101` already renders the failure block
directly through `render_component(&CustomComponents.report_header/1, ...)` with a plain `%Report{}`
and `%ReportRun{}`, no session and no LiveView. The assertion is then about eight lines: resolve
`student-actions` from the tree, build a failed run against it, and assert the run page's rendered
HTML contains the `athena_query_guidance` string that `ReportJSON.run_json/1` emits for the same
run.

It is worth checking that this is not a test that cannot fail. It is not tautological: it fails if
`run_json/1` emits the code instead of the guidance, if either surface post-processes the string,
and if the UI stops rendering guidance. What it does not catch is both surfaces being wrong the same
way, which is the property sharing a function already gives.

**Decision**: promoted into the second server step with that mechanism; the gap is deleted.
