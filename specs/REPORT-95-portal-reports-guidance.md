# Integrate Portal reports into the Claude skill and MCP guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-95

**Status**: **Closed**

## Overview

Teach Claude the Portal-report workflow in the shared guidance both surfaces render, so that a researcher's data question reaches for the right report kind and the whole pull can be driven end to end. The guidance documented the two Portal-fed views in detail but never named the report family, never gave the slugs needed to create a run, and never mentioned the flag that re-reads a live report.

cc-data could already create Portal report runs, download them and join them to student answers. Claude could not reliably drive any of it, because the guidance it reads was written for the Athena-only world and had only been patched where individual stories touched it. A researcher asking Claude for class-level results got steered down the Athena path, which is slower, needs a report authored first, and returns a frozen snapshot.

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

**The ticket's background was out of date, and the spec was written against the code instead.** The ticket said the researcher guide "asserts the opposite in two places" and that both must be reversed. REPORT-94 had already removed both strings in `564da81`, replacing them with a full Portal treatment, and had landed the two dimension views the ticket calls "minimal stubs", which are in fact the longest entries in the views catalog. That relocated the work rather than removing it: the guide was in good shape and the *guidance* was where the holes were.

## Requirements

- The shared core names the Portal report family and the Athena/Portal distinction, so both surfaces carry it rather than the MCP surface alone.
- The run sentence at `core.md:24` keeps its list, which is correct for runs, and gains the fact it lacks: a Portal run has no `report_type`, and `execution` is what identifies it.
- The core carries the live-versus-snapshot model as a decision rule, not a fact: a Portal report is computed per request and is refreshed by re-pulling the same run; an Athena report is a frozen artifact and is refreshed by duplicating it into a new run.
- The guidance names every report slug the code knows: `student-id-mapping`, `student-metadata` and the five Athena slugs, so a run can be created from the guidance alone.
- The catalog says it is not exhaustive, in one sentence, because the Portal offers five aggregate metrics reports whose slugs are in no Go inventory and which no endpoint enumerates.
- What the slug guard proves, and what it cannot, is stated where each half can be acted on: the one-direction rule beside the guard, and the warning that cc-data's own inventory can be stale beside `slugToType`.
- The recipe names the joins but points at the rules that govern them rather than restating them.
- The end-to-end recipe is documented: create a Student ID Mapping run, pull answers, history and attachments by its run id, download both Portal CSVs, materialize if it is worth it, then query.
- The recipe's materialize step is a conditional pointer, not a restatement. *(REPORT-113 owns the rule; REPORT-115 templates its CLUE workflow on this recipe and needs the slot.)*
- The re-pull mechanism is named on both surfaces: `--refresh` for the CLI, the `refresh` parameter for the MCP tool.
- Every `cc-data` command the guidance spells out resolves to a real command, checked across both rendered surfaces. Names only: flags are deliberately not checked, since a flag like `--portal` is required only when no default portal is configured and there is nothing declarative to compare against.
- Each surface carries the verb that acts on a slug, or the recipe's first step is unexecutable there.
- The `student_id_mapping` and `student_metadata` view entries gain exactly the two facts they lack, the slug and that such a run's id drives the `get_*` verbs, and are otherwise left alone.
- The MCP descriptions carry named facts rather than a general instruction to mention Portal runs, and a test asserts each one.
- A guard holds that every report slug named in the guidance exists in the code's slug inventories. It scans the whole rendered core for the slug shape rather than the catalog alone, since the prose and the recipe name slugs the catalog listing would not cover.
- Each `report_type` vocabulary the guidance states is guarded against the code list that defines it, in both directions. There are two, and conflating them is what made the original requirement wrong.
- The `downloads` view entry states the download vocabulary its `report_type` column carries, which is where `portal` and `recovered` belong.
- Each vocabulary gets one readable definition in code, the way the view list already has one.
- `ParseCatalog`'s identifier pattern is widened to accept hyphens, without which a slug catalog parses as empty and its guard silently checks nothing.
- The re-pull mechanism is asserted on both surfaces by a test, following the auth remedy. Content placed in `core.md` needs no such test: both surfaces render the core by construction.
- REPORT-112's `logs` view entry is reconciled with the two-families framing this story introduces.
- The researcher guide is checked against the finished guidance and updated only where it is now wrong or silent. The acceptance criterion is that the three documents agree, not that the guide changed.

## Technical Notes

- Command spellings cannot go in the core: `TestCoreNamesNoCommand` rejects any "`cc-data `" string there. So the *rule* goes in the core and the spelling goes in `skill_header.md`, with the MCP surface carrying the parameter on the tool description. This is the same split the auth remedy already uses.
- Two code-side slug inventories exist and do not overlap: `dataset.slugToType` holds the five Athena slugs, and `duck.dimensionViews[].slug` holds the two Portal ones. The guard unions them.
- `ParseCatalog` reads a named section and pulls the backticked identifiers that open each bullet or table row, which is the shape any new guarded section has to take. It closes a section at the next heading of any level, so the recipe's `###` subheading ends the `## Report slugs` section; harmless as written, because the slug bullets precede it, and the ordering is now a stated constraint rather than an accident.
- The `report_type` guards cannot use `ParseCatalog`, because the sentences carrying the vocabularies open with prose rather than a backticked identifier. Matching `` `report_type` (...) `` against the rendered core extracts each list from its sentence as written, so the guards cost no restructuring and no bytes. The core is hard-wrapped, so the match has to tolerate a list spanning lines.
- A tool's `Description` is registered with the MCP server and is **not** part of `guidance.Instructions()`, which renders `mcp_header.md` + `core.md` + `tools.md` only. So the MCP surface's *guidance* learns about `refresh` only if `tools.md` says so.
- `cobra.Find` returns the deepest command it matched plus the args it could not consume, and errors only when the first word is unknown, so a wrong subcommand resolves to its parent with leftovers. The leftovers are the signal the command guard keys on.
- Measured on the finished implementation: core 17,003 bytes, skill 20,291, MCP instructions 20,504. This story added 2,669 / 3,385 / 2,782 against a baseline that had itself grown 39% since the spec was drafted. The skill grows most because it alone carries the command spellings the core is forbidden to name.

## Out of Scope

- **Writing the "when to materialize" rule.** REPORT-113 owns that prose. This story references the rule from the recipe and must not restate the condition.
- **CLUE document guidance.** REPORT-115 owns that, and its description names this story's prose as its template.
- Rewriting the researcher guide's Portal-reports treatment, which REPORT-94 landed.
- Any change to the views themselves, or to what `reports_create` accepts.
- Naming the five aggregate Portal metrics reports. Their slugs are in no Go inventory and no endpoint enumerates them; the catalog says so in one sentence instead. *(Filed as REPORT-130.)*

## Decisions

### Should the slug guard be bidirectional, and what happens to the aggregate reports?

**Context**: A guard that every guidance-named slug exists in code would have caught the fact that no slug was documented at all. But the two code inventories cover only seven slugs, and the Portal aggregate reports have slugs in neither.

**Options considered**:
- A) One direction only: every slug the guidance names must exist in a code inventory.
- B) Bidirectional, with the aggregate slugs added to a Go inventory first.
- C) No guard; the slugs are few and the researcher guide already lists them.

**Decision**: A. A wrong slug fails at the server with an error that says nothing about spelling, and that is the failure worth catching; the code having no constant for a report the server offers is not a defect the guidance should be blocked on. The discovery that made this actionable: `ParseCatalog`'s `leadingNames` regex excluded hyphens, so a slug catalog parsed as empty and the guard would have reported success while checking nothing. Widening it is a prerequisite, and the four shipped catalogs parse identically before and after.

---

### How much of the recipe belongs in the core, given the context budget?

**Context**: The core is rendered into every session on both surfaces, and REPORT-89 is separately trying to protect the model's context budget.

**Options considered**:
- A) Full recipe in the core.
- B) Decision rule in the core, worked example in the researcher guide only.
- C) Full recipe now, and let REPORT-89's cold walk-through decide whether it earns its bytes.

**Decision**: A, and the measurement decides it. Both versions were drafted and measured against the real file: the minimal decision rule is +765 bytes, the full recipe +1,532. That is not where a context budget is won or lost, and the story exists precisely so the model can execute the workflow rather than narrate it. A and C are compatible rather than alternatives.

---

### Does this story still own researcher-guide work?

**Context**: The ticket's guide requirement was to reverse two assertions, which REPORT-94 already did.

**Options considered**:
- A) Yes, narrowed to a consistency pass.
- B) No; drop the guide and note that REPORT-94 discharged it.

**Decision**: A. The guide is the only one of the three documents a human reads end to end, and this story adds slugs and a workflow to the other two. The acceptance criterion is that the three documents agree, not that the guide was edited; a pass finding nothing is a passing outcome.

---

### Which `report_type` vocabulary does the core's run sentence state?

**Context**: The original requirement said the sentence "gains `portal`, so the vocabulary it states is the vocabulary the code accepts". A later review found that conflates two different vocabularies.

**Options considered**:
- A) Add `portal` (and `recovered`) to the run sentence, guarded against `IsAllowedReportType`.
- B) Keep the run sentence's list, add the missing discriminator, and state the download vocabulary where downloads are described, each guarded against the list that defines it.

**Decision**: B. Measured, the two sets differ: a run carries `[answers log usage]` and nothing else, because a Portal run's `report_type` is null on the wire, pinned by `portalRunWire` and stated as design in the API type; the reports union additionally admits `portal` and `recovered`, both assigned locally. Adding `portal` to a sentence about runs would have told the model a Portal run carries `report_type: portal` while this story's own `reports_list` description teaches that `execution` is the discriminator. The gap was never a missing enum value, it was a missing discriminator.

---

### Hand-fix the drifted vocabulary, or guard it?

**Context**: This story exists because `core.md`'s report-type list drifted from the code.

**Options considered**:
- A) Add the missing values by hand.
- B) Guard each vocabulary against the code list that defines it, in both directions, with an explicit exemption list.

**Decision**: B. Correcting a list by hand answers today's question and leaves the next value free to drift, in the one story whose subject is guidance that drifted, and next to five guards that already do this for views, tools, identity columns, the auth remedy and command spellings. `IsAllowedReportType` was a `switch`, which offers a guard nothing to read, so the allowlist became a slice the predicate consults. The exemption sets ship empty and exist so a value added later has to make a decision rather than be forgotten.

---

### Should the guidance hardcode a slug list at all?

**Context**: The server owns which reports exist, and every local copy is a cache with no invalidation. `slugToType` is a static map nothing reconciles, and cc-data already warns "unknown to this cc-data version" on a slug it lacks.

**Options considered**:
- A) List the aggregate slugs too, widening or exempting the guard.
- B) State the boundary in one sentence without the slugs.
- C) Out of scope, filed.

**Decision**: B now, with the end state filed as REPORT-130. The repo already refused a slug map once for this reason: deriving a report's type from `execution` rather than a slug lookup is what lets "a Portal report added to the server later be recognized without a cc-data release". A was not available regardless, since three of the five slugs are not in this repo to copy. A server-side catalog the guidance points at, instead of enumerating, is the answer that removes the drift class entirely.

---

### Should the guidance's command spellings be guarded, and how far?

**Context**: The guidance spells out commands because the core is forbidden to, and nothing compared those spellings against the commands that exist.

**Options considered**:
- A) No guard.
- B) Guard command names only.
- C) Guard names and flags.

**Decision**: B. C is not available: `--portal` is not a cobra-required flag but a runtime fallback to `default_portal`, so there is nothing declarative to compare against, and the repo marks no flag required anywhere. That same fact settled how the commands are written: the lines show only what is unconditionally needed, matching the rest of the file, with `--portal` named once as a condition. The guard must check `Find`'s leftover args, not just its error, or a renamed subcommand resolves to its parent and the guard passes.

---

### Resolved during review, and worth keeping

- **"Enrich the stubs" named no actual gap.** The ticket's framing was that the two dimension-view entries are minimal stubs; they are the longest entries in the catalog. Fixed by naming the two facts they actually lack. An instruction to "enrich" would have invited a rewrite of correct prose.
- **"Mention Portal runs where relevant" was a requirement that could not fail.** The drift guard compares tool *names* and never reads a description. Fixed by naming the specific fact each description must carry and asserting each, which is a test with a mutation to catch.
- **A both-surfaces test on core content is decorative.** The core is rendered by both surfaces by construction, so asserting it on each is true no matter what. Only the per-surface halves, the `--refresh` spelling and the `refresh` parameter, need holding together.
- **The both-surfaces test would have failed on the day it was written.** `refresh` was planned for `skill_header.md` and the tool description only, and a tool's description is not part of the rendered instructions. The tempting fix, reading the registered tools instead, would have made it pass while no longer holding the thing it exists for. `tools.md` gains the parameter instead, which is a precondition rather than a nicety.
- **The regex widening gets a parser test, not a catalog test.** `TestParseCatalogReadsHyphenatedNames` pins what the pattern now accepts. What was deliberately not added is a fifth guard re-comparing the shipped catalogs: the view, tool, identity-column and researcher-guide guards already compare each against its code-derived set in both directions, so a name the widened pattern gained or lost fails one of them, and a new test asserting the same comparison could not fail unless one of those four also failed.
