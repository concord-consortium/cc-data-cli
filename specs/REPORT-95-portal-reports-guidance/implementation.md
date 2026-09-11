# Implementation Plan: Portal reports in the Claude skill and MCP guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-95
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Shape of the change

This is a documentation story with three code-shaped constraints, all verified:

- **The core cannot name a command.** `TestCoreNamesNoCommand` rejects any "`cc-data `" string in `core.md`, so the recipe names operations ("create a `student-id-mapping` run") and the command spellings live in `skill_header.md`. The draft was run against the real guard and passes.
- **The slug guard needs a one-character regex change first.** `leadingNames` excludes hyphens, so a slug catalog parses as empty today and `ParseCatalog` reports success anyway. Verified that widening it leaves all four existing catalogs parsing to identical names.
- **Anything in `core.md` reaches both surfaces by construction; anything in `skill_header.md` or a tool description reaches one.** That decides where each fact goes and which facts need a both-surfaces test.

Both inputs have landed in this branch's base as of 2026-09-11: it is stacked on `REPORT-113-dataset-materialization`, which carries REPORT-112's merge, so the `core.md` conflict the Precondition anticipated is already resolved and stage 4 has been re-run against the result. When REPORT-113 merges, rebase onto `main` before retargeting this PR, so its diff is this story's work alone; GitHub does not retarget a stacked child on its own, and this repo does not delete branches on merge, so the child is not at risk of being closed.

---

## Widen the catalog identifier pattern

**Summary**: Makes a hyphenated catalog possible. Independent of everything else and worth landing alone, because it changes a regex three shipped guards depend on.

**Files affected**:
- `internal/guidance/catalog.go` — `leadingNames`
- `internal/guidance/catalog_test.go` — the identical-parse evidence

**Estimated diff size**: ~60 lines

```go
// leadingNames matches the backticked identifiers that open a catalog entry, in either
// markup the guarded files use: a bulleted list item ("- `x`, `y` — ...") or a markdown
// table row ("| `x`, `y` | ..."). Hyphens are allowed because report slugs carry them;
// without that a slug catalog parses as empty and its guard passes while checking nothing.
var leadingNames = regexp.MustCompile("^(?:- |\\| )((?:`[a-z0-9_-]+`(?:, )?)+)")
```

The property that matters is not that the new pattern accepts a hyphen, which is true by inspection. It is that the four shipped catalogs parse to the *same* names as before, so this cannot quietly change what the existing guards check. That property needs no new test: `TestGuidanceDocumentsEveryStaticView`, `TestGuidanceDocumentsEveryTool`, `TestGuidanceDocumentsEveryIdentityColumn` and `TestResearcherGuideDocumentsEveryStaticView` already compare each catalog against its code-derived set in both directions, so any name the widened pattern gained or lost fails one of them. A new test asserting the same comparison could not fail unless one of those four also failed. Measured before and after regardless: core Views 12 names, core Identity columns 4, tools Tools 21, researcher-guide table 12, all identical.

A second test covers the failure this exists to prevent: a section of hyphenated names parses to all of them, where the old pattern yielded none and returned no error.

---

## Guard the report-type vocabularies

**Summary**: Gives each vocabulary one readable definition and holds the sentence that states it
against the right one. Lands before the core edit, so the core edit is what turns the guards green
rather than the guards being written to fit whatever the core happens to say.

**Files affected**:
- `internal/dataset/reporttype.go`: the union vocabulary as a slice with `IsAllowedReportType`
  reading it, and an accessor for the run vocabulary
- `internal/guidance/guard_test.go`: the two guards and their exemption lists

**Estimated diff size**: ~120 lines

There are two vocabularies and they are not the same set, which is the trap this step exists to
close. Measured:

```
run report_type   (what the server sends on a run):  [answers log usage]  + null for Portal
union report_type (IsAllowedReportType, downloads):  [answers usage log portal recovered]
```

The run set is `slugToType`'s values; `portal` and `recovered` are never on a run, they are
assigned locally to a download by `internal/fetch/report.go:162` and by reindex. A single guard
over `AllowedReportTypes()` would therefore force `portal` into a sentence about runs, which the
wire contradicts.

```go
// AllowedReportTypes is the reports-union allowlist, in one place so the guidance
// guard and the predicate cannot disagree about what the vocabulary is.
func AllowedReportTypes() []string

// RunReportTypes is what the server sends as a run's report_type. A Portal run
// sends none, which is why execution and not this list identifies one.
func RunReportTypes() []string
```

Neither guard can use `ParseCatalog`, since both sentences open with prose rather than a backticked
identifier. Neither needs to: matching `` `report_type` (...) `` against the rendered core returns
the list from the sentence as written. Verified against the current file, which yields `answers`,
`log`, `usage`, so the run guard is readable before the core edit and fails for the right reason.

Both guards run both directions, as the view and tool guards do: every value the sentence names is
in its code list, and every value in the code list is named in its sentence or carries a one-line
exemption in the test. Both exemption sets ship empty, since this story states both vocabularies in
full. They exist so the next value added has to make a decision rather than be forgotten, and so
the decision is recorded beside the guard rather than in a commit message.

Only the run guard lands in this step. The download guard was written here and confirmed to fail
for the right reason, that the core states no such sentence yet, and then moved to the step that
adds the sentence: writing a guard before its prose is the point, but a commit that lands red is
not. Both accessors the guards read are exported for the same reason `StaticViewNames` and
`IdentityColumnNames` already are, since the guards live in a test package that cannot reach
package internals.

---

## Teach the core the Portal report family, the slugs and the recipe

**Summary**: The substance. One file, one commit, because the family, the slugs and the recipe are a single argument and splitting them would leave the core briefly incoherent.

**Files affected**:
- `internal/guidance/src/core.md`
- `internal/guidance/guard_test.go` — the slug guard
- `internal/duck/views.go` and `internal/dataset/reporttype.go` — `DimensionSlugs` and
  `AthenaReportSlugs`, accessors for the two slug inventories, both of which are unexported today
  (`dimensionViews[].slug` and `slugToType`'s keys), so the guard can read either. The second is
  named for what it returns rather than `ReportSlugs`, which would overpromise: it is the Athena
  half, and the call site unions it with the dimension half.

**Estimated diff size**: ~180 lines

Four edits to `core.md`:

**The run sentence keeps its list and gains the Portal clause** (`core.md:24`). The list is correct for runs and stays as it is; what it lacks is that a Portal run carries no `report_type` and is identified by `execution`. Without that, a model told runs have a `report_type` reaches for it on a Portal run and finds null.

**The `downloads` entry states the download vocabulary** (`core.md:154`), which is where `portal` and `recovered` belong: both are assigned locally rather than arriving on a run. `recovered` carries the clause that makes it actionable, that a reindex assigns it to a CSV it cannot classify and re-fetching the run restores the real type, which pairs the value with the remedy `RECOVERED_PROVENANCE` already names. Together these two edits turn both guards from the previous step green.

**The Portal/Athena family and the live-versus-snapshot rule**, in "Runs and their data", phrased as a decision rule because that is what the model needs it for:

> Reports come in two families. **Athena** reports are computed in the background from the log archive: a run has a query state and its result never changes once it succeeds, so a fresh snapshot means duplicating the run. **Portal** reports are computed from the Portal database on every request, so they list as `live` and a fresh read means re-pulling the same run, not duplicating it. Duplicating a Portal run is refused unless forced.

The parenthetical "(`report_type` `portal`)" an earlier draft carried is deliberately gone: a Portal run has no `report_type`, which the sentence above this one now says, and repeating the download-side value here would reintroduce the conflation.

**A `## Report slugs` section**, which is the guarded catalog and the thing that makes `reports_create` usable from the guidance alone:

```markdown
## Report slugs

A run is created from a report's slug. These are the ones a data pull starts from. The Portal also
offers aggregate metrics reports that are not listed here; their slugs come from an existing run or
from the researcher guide.

- `student-id-mapping` — the learners' portal ids and the key that joins them to stored
  records, with no names. A run of it is a valid run id for fetching answers, history and
  attachments.
- `student-metadata` — the same learners with names and roster labels, joined on `learner_id`.
- `student-answers`, `student-assignment-usage` — per-student Athena reports.
- `student-actions`, `student-actions-with-metadata`, `teacher-actions` — Athena clickstream logs.
```

**The recipe**, as a numbered sequence. Verified to parse and to pass the no-commands guard:

```markdown
### Pulling a cohort's work without authoring an Athena report

1. Create a `student-id-mapping` run over the learners of interest, assembling the filter
   from the available filter options.
2. Fetch that run's answers, history and attachments by its run id.
3. Fetch the run's own report CSV, which becomes `student_id_mapping`.
4. Create and fetch a `student-metadata` run over the same learners, which becomes
   `student_metadata`.
5. Materialize the dataset before querying it, when the Materializing a dataset
   section says it is worth doing.
6. Query: `answers` joins `student_id_mapping` on `run_remote_endpoint = remote_endpoint`,
   and `student_id_mapping` joins `student_metadata` on `learner_id`. See the
   `student_id_mapping` entry for what a NULL join key means before filtering on it.

Re-read any Portal run later by re-pulling the same run id; do not duplicate it.
```

The step wording is a constraint, not a style choice: naming the operation rather than the command is what keeps `TestCoreNamesNoCommand` green, and that belongs in a comment beside the guard rather than as folklore.

Step 5 is a pointer to REPORT-113's rule, carrying the ordering only and no size condition, so there is one place that decides what "large" means. Stage 4 corrected it twice against the landed prose: the drafted wording opened "If the pull is large", which is exactly the condition it was meant to delegate, and it pointed at "when to materialize", which is not what the section is called. REPORT-113 titled it `## Materializing a dataset` (`core.md:189`), and the rule is the bullet beginning "It is worth doing once a dataset is large". Naming the operation rather than the command is still what keeps `TestCoreNamesNoCommand` green.

Step 6 names the joins because they are the recipe's payoff, but it does not restate the withheld-key rule that governs them. That rule lives on the `student_id_mapping` entry (`core.md:132-137`): a NULL `run_remote_endpoint` is a learner with no secure key, and every such learner carries the same endpoint string, so the key is withheld rather than attributing one learner's answers to all of them. Restating it would put a correctness rule in two places with nothing holding them together; omitting the pointer would let a model follow the recipe and never meet it. Same treatment as the materialize step, and for the same reason.

The two dimension-view entries gain their two missing facts and nothing else: the slug, and that a run of it drives the `get_*` verbs. Their existing dedup, withheld-key and `hide_names` prose is left untouched.

**The slug guard**, following the three guards already in the file:

```go
func TestGuidanceDocumentsOnlyRealSlugs(t *testing.T) {
	documented, err := guidance.ParseCatalog(guidance.Core(), "Report slugs")
	// every documented slug must exist in code; the reverse is deliberately not checked,
	// because the portal offers aggregate reports that have no Go constant today
}
```

One direction only, for the reason the requirements give. It needs the union of `dataset.slugToType`'s keys and the dimension views' slugs, neither of which is currently exported, so each gets a small accessor rather than the guard reaching into package internals or a third copy of the list appearing in a test.

The other half of what the guard means goes beside `slugToType` rather than in the test, because
that is the map that can be wrong and the file a future reader edits when adding a slug. The guard
carries a one-line pointer to it:

```go
// slugToType is cc-data's own copy of the Athena slugs, not a roster of what the server offers.
// Nothing reconciles it: an unrecognized slug degrades with "unknown to this cc-data version"
// rather than failing, and the guidance guard can only prove the guidance matches this map, never
// that this map matches the server.
```

The comment names no ticket, deliberately. The constraint it states is complete without one, and REPORT-130's replacement of this map is planning state rather than something the code cannot express; no other comment in the tree cites a ticket.

Verified that the union covers the documented set exactly: seven slugs in code, seven documented, none documented that code does not know. So the guard passes on the prose this plan writes, rather than being written and then having the prose trimmed to satisfy it.

---

## Name the re-pull mechanism on each surface

**Summary**: The per-surface half, which is where the two surfaces are allowed to differ and therefore where a test has to hold them together.

**Files affected**:
- `internal/guidance/src/skill_header.md` — `--refresh` under "Fetching data"
- `internal/guidance/src/tools.md` — `refresh` on the `get_report` entry, so the MCP guidance carries it
- `internal/mcpserver/tools.go` — six tool descriptions
- `internal/guidance/guard_test.go` — the both-surfaces assertion
- `internal/mcpserver/server_test.go` — the description assertions

**Estimated diff size**: ~120 lines

`skill_header.md` gains `--refresh` on the `get report` line, since the core states the rule and cannot state the flag.

It also gains a `## Making a run` section, which the plan originally missed. The file had no `cc-data reports` subcommand at all, so the recipe's first step, creating a run from a slug and a filter, was unexecutable on the surface that drives the CLI: the model would know `student-id-mapping` and have no verb to use it with. The MCP surface needed nothing, since `tools.md` already names `reports_create` and `reports_filter_options`. A test holds the skill surface to it, in the same shape as the re-pull assertion and for the same reason: the core cannot carry a command spelling, so no comparison of the core can notice this file losing one. It asserts nothing about the MCP surface, because the shipped tool guard already fails if either tool leaves `tools.md`, which was checked by removing one.

`tools.md`'s `get_report` entry gains `refresh` too, and this is not redundant with the tool description. A tool's `Description` is registered with the MCP server and is **not** part of `guidance.Instructions()`, which renders `mcp_header.md` + `core.md` + `tools.md` only. Verified: none of three distinctive description strings appears in the rendered instructions. So the MCP surface's *guidance* learns about `refresh` only if `tools.md` says so, and a both-surfaces test that looked for it in `Instructions()` without this edit would fail on the day it was written.

Six MCP descriptions gain one named fact each, rather than a general instruction to mention Portal runs:

| Tool | Fact |
| --- | --- |
| `reports_list` | a run's execution tells an Athena run from a Portal one |
| `reports_filter_options` | it is how a Student ID Mapping run's filter is assembled |
| `get_report` | a Portal report is re-read by passing `refresh`, not by duplicating |
| `get_answers`, `get_history`, `get_attachments` | a Student ID Mapping run id is a valid source |

Each is asserted in `server_test.go`. These are substring assertions on shipped strings, so the mutation they catch is real: delete the sentence and the test goes red. The drift guard cannot do this job, because it compares tool names and never reads a description.

The both-surfaces assertion follows `TestBothSurfacesCarryTheAuthRemedy` exactly, and for the same reason: `refresh` is worded differently on each surface, so no name-comparison guard can notice one of them losing it. It compares `Skill()` against `Instructions()`, which is why the `tools.md` edit above is a precondition rather than a nicety.

It is written only for `refresh`, deliberately. Asserting the core's content on both surfaces would be true by construction and is the decorative test to avoid.

---

## Reconcile the researcher guide

**Summary**: Last, because it can only be done once the guidance is final. Its expected outcome is a small diff or none.

**Files affected**:
- `docs/researcher-guide.md`

**Estimated diff size**: ~40 lines, possibly zero

REPORT-94 already gave the guide a full Portal-reports treatment (`docs/researcher-guide.md:346-431`), so this is a consistency pass, not a rewrite. Read the finished core against the guide and fix only contradictions or omissions. The known candidate is section 4's "Make a run without the web form", which does not frame the mapping workflow as the way to pull a cohort.

This step also reconciles REPORT-112's `logs` entry with the two-families framing, which is a core edit rather than a guide one but belongs with the other reconciliation work: the entry names three Athena slugs inline and needs to sit under the family the core now defines.

The acceptance criterion is that the three documents agree, not that the guide changed. A pass that finds nothing is a passing outcome and should be recorded as one in the PR, rather than becoming a reason to edit prose that is already correct.

Note the guide's view table is itself guarded (`TestResearcherGuideDocumentsEveryStaticView`), so any view-table edit here is already held by a shipped test.

---

## Open Questions

None.

## Self-Review

Roles: the engineer writing these tests, and the engineer reviewing the commits. Each finding was checked by building the proposed thing far enough to see whether the claim survived.

### Test author

#### RESOLVED: The both-surfaces test would have failed on the day it was written

The plan asserted `refresh` on both surfaces following the auth-remedy precedent, which compares `guidance.Skill()` against `guidance.Instructions()`. But `refresh` was planned to land in `skill_header.md` on one side and in an **MCP tool description** on the other, and a tool `Description` is registered with the server, not rendered into the instructions. Verified: `Instructions()` contains none of three distinctive description strings, and `tools.md`'s `get_report` entry does not mention refresh today.

So the test would have gone red immediately, and the tempting fix is the wrong one: weakening it to read the registered tools instead would make it pass while no longer holding the thing it exists to hold, which is that the *guidance* on both surfaces carries the rule. Fixed by adding `refresh` to `tools.md`'s `get_report` entry, which is rendered, and keeping the tool description as the separate, separately-asserted surface.

This is the same class of mistake the requirements-stage review caught in the other direction: assuming a guard reads something it does not.

#### Checked and not a problem: the slug guard passes on the prose this plan writes

A guard written against prose that then has to be trimmed to satisfy it is a guard that has been fitted to the answer. Built the union the guard would use: seven slugs in code (five in `dataset.slugToType`, two in `dimensionViews`), seven documented by this plan, and nothing documented that the code does not know. Recorded so the next person does not re-derive it.

### Commit reviewer

#### RESOLVED: The section heading and the recipe subheading interact, and the plan did not say so

`ParseCatalog` closes a section at the next heading of any level, so the `### Pulling a cohort's work` subheading ends the `## Report slugs` section. That is harmless as written, because the slug bullets precede it and the recipe's numbered items would not match the entry pattern anyway. It is a constraint on ordering, though: moving the recipe above the bullets would silently empty the guarded catalog, and `ParseCatalog` only errors when a section documents *no* names, so a partial reorder could shrink it without failing.

Verified the current arrangement parses all seven slugs with the recipe subheading in place. The plan now carries the ordering constraint next to the section rather than leaving it to be rediscovered.
