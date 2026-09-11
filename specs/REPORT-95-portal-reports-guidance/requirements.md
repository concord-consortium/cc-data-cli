# Integrate Portal reports into the Claude skill and MCP guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-95
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Teach Claude the Portal-report workflow in the shared guidance both surfaces render, so that a researcher's data question reaches for the right report kind and the whole pull can be driven end to end. Today the guidance documents the two Portal-fed views in detail but never names the report family, never gives the slugs needed to create a run, and never mentions the flag that re-reads a live report.

## Project Owner Overview

cc-data can now create Portal report runs, download them, and join them to student answers. Claude cannot reliably drive any of that, because the guidance it reads was written for the Athena-only world and has only been patched where individual stories touched it. The result is a tool whose capabilities have outrun its instructions: a researcher asking Claude for class-level results gets steered down the Athena path, which is slower, needs a report authored first, and returns a frozen snapshot.

This story closes that gap in the single shared source both the Claude Code skill and the MCP server render, so the two surfaces cannot drift apart on it.

## Background

REPORT-104 made one source of guidance rendered into two surfaces (`internal/guidance/guidance.go:27`, `:32`). The split matters here and is the root of several gaps below:

- `Skill()` renders `skill_header.md` + `core.md`.
- `Instructions()` renders `mcp_header.md` + `core.md` + `tools.md`.

So anything written in `tools.md` reaches the MCP server only, and the Claude Code skill never sees it. The drift guard compares *names* (views, tools, identity columns) in both directions but cannot notice a concept that reached one surface and not the other.

**The ticket's background is out of date, and the spec is written against the code instead.** REPORT-95's description says the researcher guide "asserts the opposite in two places" and that both must be reversed. Both strings were removed by REPORT-94 in `564da81`, which replaced them with a full Portal-reports treatment: the Athena/Portal split with `execution` `async` vs `sync`, the `live` state, `get report --refresh`, both Portal report slugs, the `learner_id` join, the hide-names warning, and the one-row-per-learner dedup property (`docs/researcher-guide.md:346-431`). The same story landed the two dimension views and their guidance, which the ticket calls "minimal stubs"; they are not stubs, they are the longest entries in the views catalog.

That does not leave this story with nothing. It relocates the work: the researcher guide is in good shape and the *guidance* is where the holes are.

## Verified gaps

Measured by rendering each surface and probing it, rather than by reading the sources:

| Fact Claude needs | Skill | MCP instructions |
| --- | --- | --- |
| The phrase "Portal report" | **absent** | present |
| `student-id-mapping` slug | **absent** | **absent** |
| `student-metadata` slug | **absent** | **absent** |
| `--refresh` | **absent** | **absent** |
| `student_id_mapping` / `student_metadata` views | present | present |
| Re-pull rather than duplicate | present | present |

Four specific defects follow:

- **The core never says how a Portal run is recognized.** `core.md:24` reads "Report runs have a `report_type` (`answers`, `log`, `usage`)", and for a *run* that list is complete: it is exactly `slugToType`'s value set. A Portal run carries no `report_type` at all, which the wire pins (`portalRunWire` has `"report_type":null`, asserted in `internal/api/portal_download_test.go:52`) and the API type states as design: "report_type is null for a Portal run by design" (`internal/api/types.go:33`). The discriminator is `execution`. So the gap is not a missing enum value, it is that a model told runs have a `report_type` will reach for it on a Portal run, find null, and have nothing to fall back on. `portal` is a value cc-data synthesizes locally from `execution` (`internal/fetch/report.go:162`) and stores on the *download*, which is a different vocabulary in a different place.
- **Neither surface names a report slug.** `reports_create` takes a slug, so the first step of the documented workflow is unexecutable from the guidance alone: Claude has no way to know the string is `student-id-mapping`. The slugs exist in the researcher guide, which the model does not read.
- **Neither surface mentions `--refresh`.** The guidance tells the model to re-read a Portal report rather than duplicating it, and never says how. The flag exists (`cmd/get_report.go:71`), the fetch layer's own error message explains it (`internal/fetch/report.go:144`), and the MCP `get_report` tool already accepts the parameter, but its description is the bare sentence "Download a report CSV into a dataset."
- **The Portal/Athena distinction is MCP-only.** It lives on the `reports_duplicate` entry in `tools.md`, so the skill surface, which is the CLI-driving one, never learns it.

The MCP descriptions for `reports_list`, `get_report`, `get_answers`, `get_history` and `get_attachments` say nothing about Portal runs or about a Student ID Mapping run being a valid source for the `get_*` verbs.

## Requirements

- The shared core names the Portal report family and the Athena/Portal distinction, so both surfaces carry it rather than the MCP surface alone.
- The run sentence at `core.md:24` keeps its list, which is correct for runs, and gains the fact it lacks: a Portal run has no `report_type`, and `execution` is what identifies it. Adding `portal` to that list instead would tell the model a Portal run carries `report_type: portal`, which the wire contradicts, and would undercut the `reports_list` description this story writes to teach exactly the opposite.
- The core carries the live-versus-snapshot model as a decision rule, not a fact: a Portal report is computed per request and is refreshed by re-pulling the same run; an Athena report is a frozen artifact and is refreshed by duplicating it into a new run.
- The guidance names every report slug the code knows: `student-id-mapping` and `student-metadata`, plus the five Athena slugs, so `reports_create` is usable from the guidance alone for any of them. Documenting only the two Portal slugs would leave the guard covering a fraction of the vocabulary while the other five stayed reachable but undocumented.
- The catalog says it is not exhaustive, in one sentence, because it is not. The Portal offers five aggregate metrics reports (`docs/researcher-guide.md:463-468`) whose slugs are in no Go inventory, and REPORT-128 merged three days ago specifically so two of them could be downloaded at all. cc-data never validates a slug before sending it, so they are fully usable and merely undocumented on the surface Claude reads, and there is no discovery path: `ListReports` returns the user's own runs, not the reports the server offers. Without that sentence a model reads seven slugs as the complete set and tells a researcher asking for school metrics that no such report exists.
- What the slug guard proves, and what it cannot, is stated where each half can be acted on. The guard carries its own rule: every documented slug must exist in code, the reverse is not checked, and why. The warning that cc-data's own inventory can be stale sits beside `slugToType`, because that is the map that goes stale and the file someone edits when adding a slug, and the guard points at it. The guard holds the guidance against cc-data's inventories, which catches a typo, since a wrong slug fails at the server with an error that says nothing about spelling. It cannot prove those inventories match the server's: `slugToType` is a static map nothing reconciles, and cc-data already expects it to go stale, warning "report slug %q is unknown to this cc-data version" on an unrecognized one. Green CI must not be read as evidence the slugs are current.
- The recipe names the joins but does not restate the rules that govern them. The withheld-join-key rule already lives on the `student_id_mapping` entry, and a correctness rule stated twice in one file with nothing holding the copies together is how the two drift. The recipe points at it instead, the same treatment the materialize step gets.
- The end-to-end recipe is documented: create a Student ID Mapping run, pull answers, history and attachments by its run id, download the Student ID Mapping and Student Metadata CSVs, materialize first if the pull is large, then query, joining answers and history on `remote_endpoint` and Student Metadata on `learner_id`.
- The recipe's materialize step is a **conditional pointer**, not a restatement: REPORT-113 owns when materializing is worth it, and this step references that rule rather than repeating the condition. The step exists because REPORT-115 templates its CLUE workflow on this recipe and puts materialize in exactly this position ("materialize (REPORT-113) when the history store is large"), for a corpus where it is closest to mandatory. A recipe with no slot for it would force 115 to invent one and the two workflows to diverge structurally.
- The re-pull mechanism is named on both surfaces: `--refresh` for the CLI, the `refresh` parameter for the MCP tool.
- The `student_id_mapping` and `student_metadata` view entries gain exactly the two facts they lack and are otherwise left alone: the slug a run of each is created from, and that such a run's id is a valid source for fetching answers, history and attachments. They already carry the dedup rule, the withheld-join-key rule and the `hide_names` rule.
- The MCP descriptions carry named facts rather than a general instruction to mention Portal runs, and a test asserts each one, since the drift guard checks tool names and never looks at description text: `reports_list` says a run's execution tells Athena from Portal; `reports_filter_options` says it is how a Student ID Mapping run's filter is assembled, since the recipe's first step sends the model straight to it; `get_report` says a Portal report is re-read with `refresh` rather than duplicated; and `get_answers`, `get_history` and `get_attachments` each say a Student ID Mapping run id is a valid source.
- A guard holds that every report slug named in the guidance exists in the code's slug inventories, so a typo cannot ship a slug that fails at the server.
- Each `report_type` vocabulary the guidance states is guarded against the code list that defines it, in both directions. There are two, and conflating them is what made the original requirement wrong: the run sentence is guarded against `slugToType`'s value set, which is what the server sends on a run; the `downloads` entry's vocabulary is guarded against `AllowedReportTypes()`, which is the reports-union allowlist. Every value is named in its own sentence or carries an explicit exemption with a reason. This story exists because guidance drifted from code, and correcting a list by hand would leave the mechanism intact, next to five guards that already do this for views, tools, identity columns, the auth remedy and command spellings.
- The `downloads` view entry states the download vocabulary its `report_type` column carries, which is where `portal` and `recovered` belong: both are values cc-data assigns locally, never values a run arrives with. `recovered` is documented rather than exempted because a reader meets it, on a download: an unclassifiable CSV comes back from a reindex as `"report_type": "recovered"` in `dataset show --json`, as `recovered` in the table's STATUS column, and beside the `RECOVERED_PROVENANCE` warning. It is stated with what produces it and that re-fetching restores the real type, so the value and its remedy arrive together. The core already alludes to such downloads without naming the value (`core.md:151`).
- Each vocabulary gets one readable definition in code, the way the view list already has one. `IsAllowedReportType` is a `switch`, so there is nothing a guard can read; it becomes a slice the switch consults, which is the same move `StaticViewNames()` made and the same reason. The run vocabulary needs no new list, since it is `slugToType`'s value set, but it does need an accessor, as the map is unexported.
- `ParseCatalog`'s identifier pattern is widened to accept hyphens, without which a slug catalog parses as empty and its guard silently checks nothing. The views, tools and identity-column catalogs share that pattern, so a test holds that all three still parse to the same names.
- The `--refresh` spelling is asserted on both surfaces by a test, following the auth remedy, which is the existing precedent for a rule that exists on both surfaces worded differently and that therefore no name-comparison guard can notice. Content placed in `core.md` needs no such test: both surfaces render the core by construction.
- REPORT-112's `logs` view entry is reconciled with the two-families framing this story introduces. It names `student-actions`, `student-actions-with-metadata` and `teacher-actions` inline, which are Athena slugs, so once the core says reports come in two families the entry has to sit visibly under one. This story owns it: it is the capstone that documents the finished surface, and REPORT-112 is already in review.
- The researcher guide is checked against the finished guidance and updated only where it is now wrong or silent. It is not rewritten: REPORT-94 already reversed the two assertions the ticket names.

## Precondition

This spec is written ahead of two of its inputs. REPORT-112 adds a `logs` entry to `core.md` and REPORT-113 adds the materialize prose to the same file, so **the branch is rebased and stage 4 re-run before implementation starts**, not after a conflict is discovered. The byte measurements below are pinned to the commit they were taken on and will move; the decision they support does not, since the recipe's cost is absolute and the core only grows.

Verified against REPORT-112's branch already: with its `logs` entry in place, the widened catalog pattern parses the Views section to the same 12 names before and after, and no hyphenated slug leaks in from the entry's inline `student-actions` references.

## Technical Notes

- Command spellings cannot go in the core: `TestCoreNamesNoCommand` rejects any "`cc-data `" string there (`internal/guidance/guard_test.go`). So the *rule* ("refresh a Portal report by re-pulling the same run") goes in the core and the `--refresh` spelling goes in `skill_header.md`, with the MCP surface carrying the parameter on the tool description. This is the same split the auth remedy already uses.
- Two code-side slug inventories exist and do not overlap: `dataset.slugToType` holds the five Athena slugs (`internal/dataset/reporttype.go:16`), and `duck.dimensionViews[].slug` holds `student-id-mapping` and `student-metadata` (`internal/duck/views.go:494`, `:508`). A guard would union them.
- The aggregate Portal metrics reports (Summary Metrics by Assignment and the rest) have slugs that appear in no Go inventory, so a bidirectional slug guard would fail on them. See the open question. The deeper version of the same problem is that the server owns which reports exist and every local copy is a cache with no invalidation, which is why the repo already refused a slug map once: deriving a report's type from `execution` rather than a slug lookup is what lets "a Portal report added to the server later be recognized without a cc-data release" (`internal/fetch/report.go:158-160`). A server-side report catalog that the guidance points at instead of enumerating is the end state; it is filed as REPORT-130 and is out of scope here.
- `ParseCatalog` reads a named section and pulls the backticked identifiers that open each bullet or table row (`internal/guidance/catalog.go:13`), which is the shape any new guarded section has to take.
- The `report_type` guard cannot use `ParseCatalog`, because the sentence that carries the vocabulary opens with prose rather than with a backticked identifier. It does not need to: matching `` `report_type` (...) `` against the rendered core extracts the list from the sentence exactly as written. Verified by running it, which returned `answers`, `log`, `usage` from the current file. So the guard costs no restructuring and no bytes, which matters in a file that has grown 39% since this spec was drafted.
- The rendered skill is currently 12,464 bytes and the MCP instructions 13,462. Both are read on every session, and REPORT-89 is separately trying to protect the model's context budget.

### Verified: the regex widening is safe, and the guarded section parses

The slug guard turns on a change to a regex three existing catalogs share, so the claim that widening it changes nothing was run rather than argued. Parsed every catalog before and after widening `` `[a-z0-9_]+` `` to `` `[a-z0-9_-]+` ``:

| Catalog | Before | After |
| --- | --- | --- |
| core, Views | 11 names | identical |
| core, Identity columns | 4 names | identical |
| tools, Tools | 20 names | identical |
| researcher guide, view table | 11 names | identical |

The full guidance suite passes with the widening in place. A draft `## Report slugs` section then parsed all seven slugs (`student-id-mapping`, `student-metadata`, `student-answers`, `student-assignment-usage`, `student-actions`, `student-actions-with-metadata`, `teacher-actions`), where before the widening it would have yielded none of the hyphenated ones and reported success anyway.

### Verified: the proposed core prose survives the no-commands guard

The recipe is the first procedural content proposed for `core.md`, and `TestCoreNamesNoCommand` rejects any "`cc-data `" spelling there. Drafted the Portal family, the slug catalog and the five-step recipe into the real file and ran the suite: `TestCoreNamesNoCommand` passes, because the steps name the operation ("create a `student-id-mapping` run", "fetch that run's answers") rather than the command. That phrasing is a constraint on the prose, not an accident of it, so the implementation spec should say so; a later edit that helpfully adds the command spelling will fail CI.

## Out of Scope

- **Writing the "when to materialize" rule.** REPORT-113's spec assigns that prose to the shared core and the CLI spelling to the skill header, as part of that story. This story references the rule from the recipe and must not restate the condition.
- **CLUE document guidance.** REPORT-115 owns that, and its description names this story's prose as its template.
- Rewriting the researcher guide's Portal-reports treatment, which REPORT-94 landed.
- Any change to the views themselves, or to what `reports_create` accepts.

## Stage 4 re-run (2026-09-11, rebased onto REPORT-113)

The branch is now stacked on `REPORT-113-dataset-materialization`, so both inputs the Precondition
names are present: REPORT-112's `logs` entry and REPORT-113's materialize prose are both in
`core.md`. Every assumption above that could have moved was re-run against that state rather than
re-reasoned. Throwaway code, not committed.

**Holds: `core.md:24` still reads as quoted.** Both stories appended sections rather than editing
near the top, so the `report_type` line has not moved and still omits `portal`.

**Holds, and is now verified against both inputs: widening `leadingNames` to accept hyphens changes
nothing that parses today.** Measured over the rendered surfaces: Views 12 before and after, Tools
21 before and after, Identity columns 4 before and after, identical name-for-name in each. The
earlier check had only REPORT-112's branch; Tools is 21 because REPORT-113's `dataset_materialize`
entry is now in the catalog, and it parses the same either way.

**Moved: every byte measurement, by more than the recipe it was sizing.** Re-measured on this
branch: core 14,334 bytes, skill 16,906, MCP instructions 17,722, against the 10,292 / 12,464 /
13,462 the spec records. The core grew 39%. The decision the numbers support is unchanged and
better supported, since the recipe's +1,532 is 10.7% of the new core against 14.9% of the old, but
the figures in **Technical Notes** and in the recipe decision are pinned to a commit that is two
merges behind and must be re-measured before they are quoted anywhere.

**Moved, and this one changes the work: the pointer in recipe step 5 has no target.** The step
reads "see when to materialize", which assumed REPORT-113 would leave a rule findable under that
name. It did not: the section is `## Materializing a dataset` (`core.md:189`) and the rule is a
bullet beginning "It is worth doing once a dataset is large and the same questions are being asked
repeatedly". Nothing in the rendered core contains the phrase "when to materialize", so the step as
drafted sends a reader to a heading that does not exist. Step 5 must name the section that does.

**Moved: step 5 carries the size condition it was supposed to delegate.** The implementation spec
says the step is "deliberately phrased without the size condition so there is one place that
decides what 'large' means", but the drafted step opens "If the pull is large". REPORT-113's bullet
already owns that condition, so the step should carry the ordering only, that materializing comes
before querying, and leave "large" to the core.

**RESOLVED, and it grew the story: the core's `report_type` list is short by two, not one, and
hand-fixing it would leave the drift mechanism in place.** `IsAllowedReportType` accepts five values (`internal/dataset/reporttype.go:35`):
`answers`, `usage`, `log`, `portal` and `recovered`. The requirement adds `portal` alone, which
leaves the stated vocabulary still not matching the accepted vocabulary, and the requirement's own
justification ("the vocabulary it states is the vocabulary the code accepts") argues for both.
`recovered` is the synthesized type a reindexed CSV with no provenance gets. Verified that a reader
meets it: an aggregate-shaped CSV that `recoverReportType` cannot classify comes back through a
real reindex as `"report_type": "recovered"` in `dataset show --json`, as `recovered` in the
table's STATUS column, and beside a `RECOVERED_PROVENANCE` warning.

**Resolved by guarding the list rather than by choosing a value.** Correcting three to five by hand
answers today's question and leaves the next report type free to drift, in the one story whose
subject is guidance that drifted. So the vocabulary gets a bidirectional guard like views, tools
and identity columns already have, the code gets one definition of the vocabulary for the guard to
read, and the question becomes structural: a type is documented or it is exempted with a reason,
and neither can be skipped. `recovered` is then documented, because it is reader-facing; an
exemption would have been defensible only for a value nobody sees.

## Open Questions

### RESOLVED: Should the slug guard be bidirectional, and what happens to the aggregate reports?

**Context**: A guard that every guidance-named slug exists in code would have caught the fact that no slug is documented at all. But the two code inventories cover only seven slugs, and the Portal aggregate reports (Summary Metrics by Assignment, Detailed Metrics by Assignment, Teacher Status, Detailed Metrics by School, Summary Metrics by Subject Area) exist in the researcher guide and in the server, with no Go constant anywhere. A guard demanding that every documented slug exist in code would fail on those the moment anyone documents them.

**Options considered**:
- A) One direction only: every slug the guidance names must exist in a code inventory. Catches the typo that matters, since a wrong slug fails at the server, and stays silent about slugs the code has no opinion on.
- B) Bidirectional, with the aggregate slugs added to a Go inventory first so both sides can agree. More complete, and it makes the code the roster of known reports, but it adds a list that must track the server.
- C) No guard. The slugs are few and the researcher guide already lists them.

**Decision**: A, one direction. A wrong slug fails at the server with an error that says nothing about spelling, and that is the failure worth catching; the code having no constant for a report the server offers is not a defect the guidance should be blocked on. B would make the Go inventory a roster of every report the server exposes, which is a second thing to keep in sync with a system that changes without us.

**But the guard does not work as assumed, and the discovery changes the work.** `ParseCatalog`'s `leadingNames` regex matches `` `[a-z0-9_]+` `` (`internal/guidance/catalog.go:13`), which excludes hyphens. Ran it against a slug section: `student-id-mapping` and `student-metadata` were both **silently skipped**, and the call returned success because one underscored control entry was present. A guard built on it as-is would pass while checking nothing, which is the exact shape of a test that cannot fail.

So the guard requires widening the character class to accept hyphens. That regex is shared by the views, tools and identity-column catalogs, so the change needs a test proving those three still parse to identical name lists. No existing documented name contains a hyphen, so the widening cannot change their results, but that is an argument for writing the test rather than for skipping it.

### RESOLVED: How much of the recipe belongs in the core, given the context budget?

**Context**: The core is rendered into every session on both surfaces, and is already 12.4 KB. A full end-to-end recipe with the joins spelled out is perhaps 400 to 600 bytes more, on top of the Portal family, the slugs and the live-versus-snapshot rule. REPORT-89 is separately concerned with what the model has to carry. The alternative is a compressed decision rule in the core plus the worked recipe in the researcher guide, which the human reads and the model does not.

**Options considered**:
- A) Full recipe in the core. The model can execute the workflow without the human relaying steps, which is the story's stated point.
- B) Decision rule in the core (which report kind, and that a mapping run's id drives the `get_*` verbs), worked example in the researcher guide only. Smallest context cost; relies on the model composing the steps from the view entries it already has.
- C) Full recipe in the core now, and let REPORT-89's cold walk-through decide whether it earns its bytes once the whole surface is measurable.

**Decision**: A, and the measurement is what decides it. Both versions were drafted and measured against the real file: the minimal decision rule is +765 bytes on a 10,292-byte core, the full recipe is +1,532, so **the recipe itself costs 767 bytes**, roughly 200 tokens, and takes the rendered skill from 12,464 to 13,996.

That is not where a context budget is won or lost, and the story exists precisely so the model can execute the workflow rather than narrate it. B would save 767 bytes by relying on the model to compose the sequence from view entries that document the joins but never say a mapping run's id drives the `get_*` verbs, which is the one fact it cannot infer.

A and C are compatible rather than alternatives: write it now, and REPORT-89's cold walk-through can trim it with the whole surface in view, which is a better place to judge it from than here.

### RESOLVED: Does this story still own researcher-guide work?

**Context**: The ticket's guide requirement was to reverse two assertions, which REPORT-94 already did, and the guide's Portal treatment is now the most complete of the three documents. The remaining candidates are small: it does not mention `reports create` in section 4's "Make a run without the web form" in terms of the mapping workflow, and it will need whatever the guidance decides about slugs to stay consistent.

**Options considered**:
- A) Yes, narrowed to a consistency pass: after the guidance is written, check the guide against it and fix only contradictions or omissions, with the acceptance criterion being that the three documents agree.
- B) No. Drop the guide from this story and note in the ticket that REPORT-94 discharged it.

**Decision**: A, narrowed to a consistency pass. The guide is the only one of the three documents a human reads end to end, and this story is about to add slugs and a workflow to the other two; leaving it out would let the three drift on their first change. The pass is cheap because the guide is already correct: it looks for contradictions and omissions against the finished guidance, and changes nothing else.

The acceptance criterion is that the three documents agree, not that the guide was edited. A pass that finds nothing to change is a passing outcome, and it should be recorded as one rather than treated as a reason to edit something.

## Self-Review

Roles: Senior Engineer, QA Engineer, Technical Writer. Findings that did not survive a check against the code are not recorded.

### Senior Engineer

#### RESOLVED: "Enrich the stubs" named no actual gap

The requirement inherited the ticket's framing that the two dimension-view entries are minimal stubs to be enriched, which is both wrong and unactionable: they are the longest entries in the views catalog. Read them against the workflow and the gap is exactly two facts, neither of which is inferable from what is there: the slug a run is created from, and that such a run's id is a valid source for the `get_*` verbs. Everything else the workflow needs, the joins included, is already written.

Fixed by naming the two facts. An instruction to "enrich" would have invited a rewrite of prose that is already correct, which is how a documentation change becomes a merge conflict for no gain.

#### RESOLVED: The byte baseline is measured on a commit two unlanded stories both change

The spec records the rendered surfaces at 12,464 and 13,462 bytes and uses the difference between a minimal and a full recipe to decide the context question. Checked what is in flight: REPORT-112's branch adds **32 lines to `core.md`** for the logs view, and REPORT-113's spec adds the materialize prose to the same file. So the baseline is stale before this is implemented, and `core.md` is very likely to conflict on merge.

The decision it supports does not move: 767 bytes stays 767 bytes whatever the denominator, and the ratio only shrinks as the core grows. But the number is now pinned to the commit it was measured on, and the stage-4 re-run when 112 and 113 land is recorded as expected work rather than a surprise, along with the conflict.

### QA Engineer

#### RESOLVED: "Mention Portal runs where relevant" is a requirement that cannot fail

The drift guard compares tool *names* in both directions and never reads a description (`internal/guidance/guard_test.go`). So a requirement phrased as "the descriptions mention Portal runs where relevant" has no way to be checked and no way to be wrong: any description satisfies it under a generous reading, and no test would go red if every description were left untouched.

Fixed by naming the specific fact each of the five descriptions must carry and asserting each with a test. That is a test with a mutation to catch: delete the sentence and it goes red.

#### RESOLVED: The one rule that needs a both-surfaces test was not distinguished from the ones that do not

The spec asked for `--refresh` to be named on both surfaces without saying how that would be held, and asked for the Portal family to be in the core in the same breath, as though both needed the same protection. They do not, and conflating them would have produced either a redundant test or a missing one.

Content in `core.md` is rendered by both surfaces by construction, so a both-surfaces assertion on it is true no matter what and is exactly the decorative test to avoid. The `--refresh` spelling is the opposite case: it lives in `skill_header.md` on one surface and in a tool description on the other, worded differently, which is precisely the shape `TestBothSurfacesCarryTheAuthRemedy` exists for. That precedent is now cited as the pattern to follow.

### Technical Writer

#### RESOLVED: The story had no stated success condition for the document it does not change

The researcher-guide requirement said the guide is "checked and updated only where it is now wrong or silent", which leaves a reviewer unable to tell a completed pass from a skipped one. The resolution now states that the acceptance criterion is the three documents agreeing, and that a pass finding nothing to change is a passing outcome to be recorded rather than a prompt to edit something.
