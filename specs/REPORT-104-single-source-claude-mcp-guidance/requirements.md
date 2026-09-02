# Single-Source Claude/MCP Guidance and Drift Guard

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-104
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

> The Jira ticket is the authoritative scope and carries the verified code references for the
> existing surface. This spec does not repeat them. What it adds is the set of findings from the
> code dive and the throwaway Go probes run while writing it, including two that change the shape of
> the work: the shared source cannot be one rendered body, and a naive drift guard passes on prose
> coincidence.

## Overview

The MCP server ships no guidance at all today, so a Claude client connecting to it gets a bare list
of tool names and has to infer the data model from scratch. The only description of that model lives
in the Claude Code skill file. This story creates one canonical, embedded guidance source that both
surfaces render, wires it into the MCP server's `instructions` block so it arrives in the
`initialize` response, and adds a test that fails when the guidance and the code disagree, so
neither surface can rot as views and tools are added.

## Project Owner Overview

Every later story in this backlog documents into something. REPORT-92, REPORT-93 and REPORT-94 each
add a tool or a view that Claude has to be told about, and REPORT-95 is entirely about teaching the
guidance a new workflow. Right now there is nowhere shared to put any of that: the Claude Code skill
is the only guidance that exists, and the MCP server, which is how Claude Desktop will talk to
cc-data, has none. This story builds that shared place first, so the stories after it add one entry
in one file rather than two entries in two files that quietly diverge.

The second half is the guard. Guidance rots silently: nothing breaks when a view is renamed and the
prose is not, and the only symptom is Claude giving a researcher a query against a view that no
longer exists. A test that cross-checks the guidance against the real inventory turns that into a
CI failure at the moment the change is made, and makes "register a view and document it" one
indivisible act rather than a convention someone has to remember.

It was split out of REPORT-89 (Claude Desktop readiness) on 2026-09-02. Desktop is deliberately the
last stage of the backlog, but three earlier stories need this substrate, so the substrate moves
forward and Desktop keeps only its Desktop-specific scope.

## Background

Today's state, verified:

- `mcpserver.NewServer` passes `nil` for `*mcp.ServerOptions`, so no `instructions` are advertised.
- The only guidance is `internal/claude/skill/SKILL.md`, embedded via `//go:embed` into
  `internal/claude/skill.go` and written to `~/.claude/skills/cc-data/SKILL.md` by
  `cc-data init`, stamped with the binary version and refreshed on every invocation.
- `cc-data init` also runs `claude mcp add`, so in Claude Code **both** surfaces are live at once.
- Tool descriptions live inline in `internal/mcpserver/tools.go`, one string per `mcp.AddTool` call.
- The static query views are created by `internal/duck/views.go` from inline name literals.

The go-sdk supports what is needed. `mcp.ServerOptions.Instructions` is plumbed into the
`InitializeResult`, and a probe using the repo's existing in-memory transport confirmed the value
round-trips to `ClientSession.InitializeResult().Instructions` unchanged, so the acceptance
criterion is directly testable in-process with no new harness.

## Requirements

### The shared guidance source

- One embedded markdown file is the single source of the data-model prose: the no-raw-PII norm, the
  data model, the view catalog, the `res_<N>` positional trap, `TRY_CAST` on VARCHAR answer columns,
  the `run_membership` type-qualified join recipe, and the portal facts (auth, datasets and data are
  per portal; a portal is a full hostname; an environment alias is accepted where a `portal` selects
  a server to read from but refused by `dataset_create`).
- The auth **instructions** are the deliberate exception, and they live in the per-surface wrappers
  rather than the core: the `NOT_AUTHENTICATED` remedy, and the spelling of the status check
  (`cc-data auth status --check` on the skill, `auth_status` with `check=true` on MCP). Neither can
  be shared. The status command would render into the MCP instructions as a shell command the model
  cannot run, which the next bullet forbids; and the remedy cannot be split between core and wrapper,
  because each surface renders its wrapper above the core, so the fix would be stated before the
  condition that triggers it. Each surface therefore carries one self-contained auth section, and
  what remains shared is a fact about portals rather than a fact about authenticating.
- Both surfaces render from it: the skill file `cc-data init` writes, and the MCP server's
  `instructions`. There is no second copy of the data-model prose in the repo to keep in sync.
- Neither surface renders content that is wrong for it. The skill keeps its Claude Code frontmatter
  and its CLI-command framing; the MCP instructions carry no frontmatter, and nothing that directs
  the **model** to execute a shell command or to read `cc-data <cmd> --help`, neither of which an
  MCP-only client can do. Telling the model to *relay a terminal instruction to the user* is a
  different act and is required: that is exactly what the auth remedy below does. See Technical
  Notes: this is why the source is a shared core plus two thin surface wrappers rather than one body
  rendered twice.
- Retiring the skill later is deleting a consumer, not migrating content.
- The version stamp and freshness-refresh behavior of the installed skill file are unchanged.

### MCP server instructions

- `instructions` appear in the MCP `initialize` response, asserted by a test that reads
  `ClientSession.InitializeResult().Instructions` over the in-memory transport.
- The instructions state the auth remedy as: run `cc-data login` in a terminal on
  `NOT_AUTHENTICATED`. REPORT-89 changes that one line to `login(portal)` when it adds the login
  tool; the line is written so that is a one-line change.
- The instructions name `auth_status` with `check=true` for the status check, never
  `cc-data auth status --check`, which the model can read but not run.

### The drift guard

- A test cross-checks the guidance against the real, code-derived inventory and fails when they
  disagree, in both directions: a registered name missing from the guidance, and a name the guidance
  references that no longer exists.
- The inventory is derived from the code, not from a hand-maintained list in the test. A guard whose
  "truth" side is a literal list in a test file is a second inventory with the same rot problem.
- The guard covers **static** view names, MCP tool names, and the identity columns that have a
  code-side inventory (`source_key`, `remote_endpoint`, `question_id`, `history_id`). `learner_id`
  is a server-produced report CSV column with no local inventory to check against, so it is
  documented prose rather than a guarded name.
- The guard also checks the view table in `docs/researcher-guide.md`, which is a second view catalog
  and has already drifted. `README.md` is not covered; its list is illustrative, not a catalog.
- The guard is scoped to static view names only. The engine also generates per-run and per-job view
  names (`report_<run>`, `answers_<run>`, `history_<run>`, `report_<N>_job_<M>`) that cannot be
  enumerated in prose; the guidance describes their shape instead, and the guard does not try to
  match them.
- The guard matches a **documented entry**, not any occurrence of the name in prose: it parses the
  guidance's catalog sections and reads the leading backticked identifiers of their entries. A
  substring search over the whole document is not sufficient, because several view and tool names
  are ordinary English words that already appear in the prose for unrelated reasons.
- The identity columns are the single exception, and a narrow one: they are described in sentences
  rather than as catalog entries, so they are matched as backticked names anywhere in the core
  prose. That is safe for these four and for nothing else, because none of `source_key`,
  `remote_endpoint`, `question_id` or `history_id` is an ordinary word that could occur by
  coincidence. Adding a fifth name to that check means first asking whether it could.
- The parse takes the **first** matching section only. Sections are matched by heading substring, so
  a later heading carrying the same word would otherwise reopen the catalog and fold unrelated
  entries into the comparison.
- The guidance carries a Views catalog section and a Tools catalog section in that shape.
- An entry may document more than one name (both catalogs already have such entries), so the parse
  reads every leading backticked identifier of an entry, not just the first.
- The two guarded files use different markup: the guidance's catalogs are bulleted lists and the
  researcher guide's is a markdown table. One parse handles both, since each shape puts the names
  first behind a fixed prefix (`- ` or `| `).
- A catalog section the guard cannot locate is a **failure**, not an empty result. The
  "every documented name still exists" direction passes vacuously against zero documented names, so
  the guard asserts each section was found and is non-empty before comparing. The researcher guide's
  table is the live risk: it sits under a numbered prose heading rather than a "Views" one, so a
  renumber would otherwise silently switch the check off.
- The backfill is closed before the guard is turned on, so it passes on introduction.
- Registering a view or a tool without documenting it fails CI. Downstream stories that add either
  must add the guidance entry in the same change.

### Tool descriptions

- `dataset_create` takes `portal` and `name` as separate arguments rather than one concatenated
  `<portal>/<name>` ref. `portal` is **optional**, falling back to the configured default portal
  exactly as a bare name does today, so the change is a spelling change and not a capability
  removal; with no `portal` and no configured default, the existing error stands. The guidance's
  line about a bare name resolving under the default portal stays true.
- `name` is a name, not a ref: a `name` containing `/` is refused with a message naming the `portal`
  argument, rather than being re-split into a portal and a name. Left alone it would not error at
  all when `portal` is omitted, keeping `<portal>/<name>` alive as a second accepted spelling in the
  schema the model reads fresh every session, which is exactly the deprecated-argument rot the
  straight-replacement decision below rejects.
- `dataset_create`'s `portal` argument takes a **hostname only** and refuses an environment alias,
  which is the opposite of what `portal` means on `reports_list` and `reports_jobs`, where an alias
  is accepted and expanded. The tool description says so explicitly, because giving the argument the
  same name as the alias-accepting ones is what makes the collision inviting.
- The guidance's existing warning about this is rewritten to match. It currently explains the rule in
  terms of ref syntax ("`staging/wildfire` is an error"), a spelling that no longer exists once the
  ref is split, so leaving it would be this story shipping its own first piece of drift.
- `get_attachments` states its precondition: answers or history must be fetched for the run first.
- `query` echoes `TRY_CAST` on VARCHAR answer columns and `UNION ALL BY NAME` for cross-dataset
  unions. If it also lists the available views, that list is generated from the same catalog the
  guidance renders, never typed into the description: a hand-written view list inside a tool
  description is a third copy, and one the drift guard does not read.
- `dataset_delete` and `dataset_purge` carry stronger confirmation language about what is destroyed
  and that it is not recoverable.
- The existing MCP behavior contracts are unchanged: the excluded terminal and installer commands
  stay excluded, no tool exposes `url`, `inline` or `allow-dir`, the read-only and destructive
  annotations stay as they are, and `dataset_show`'s payload stays byte-identical to the CLI's
  `--json`. Each of these already has a test (`TestMCPToolSurface`,
  `TestMCPArgSchemaExcludesCapabilityFlags`, `TestMCPDatasetShowParity`), so this requirement is
  discharged by those continuing to pass unedited rather than by new work. `dataset_create`'s
  argument change is the only thing in this story that could disturb them.

### Testing

- Every guard assertion names the specific missing or stale entry, so a CI failure says what to fix
  rather than that a set comparison failed.
- The guard is proved to fail, not just to pass: a test drives it against a guidance body with a
  known-missing entry and asserts it reports that entry. A guard that has only ever been observed
  passing has not been shown to be capable of failing.
- A test asserts both rendered surfaces carry the auth remedy and the never-drive-the-browser-login
  norm. That norm is the one sentence the wrapper split leaves in two places, worded differently on
  each surface, so no other check can notice a surface losing it. It is a safety rule about not
  attempting a login on the user's behalf, which is why it is guarded rather than trusted.
- The existing tool-surface test's hardcoded `want` list **stays hardcoded**. It is not a third
  inventory to be derived away: it is the specification of the intended surface, and its
  exact-count assertion is what catches a tool added by accident, which a derived list could never
  do. The guard is the opposite check, guidance against registration, and the two coexist.

## Technical Notes

### Correction: one rendered body does not work, because the two surfaces address different runtimes

The ticket describes "one embedded markdown consumed by both the skill writer and the MCP
`instructions`." Rendering `SKILL.md` verbatim as `instructions` would be actively wrong, for three
reasons found in the file:

1. It opens with YAML frontmatter (`name:`, `description:`), which Claude Code requires to register
   a skill and which is meaningless noise in an `instructions` block.
2. It instructs the reader to run shell commands (`cc-data get report <run-id> --dataset <ref>`) and
   to treat `cc-data <cmd> --help` as the source of truth for flags. An MCP client has tools, not a
   shell; Claude Desktop, which is the whole reason the MCP surface is being given guidance, cannot
   run any of it.
3. Its auth remedy tells the reader to run `cc-data login`, which is correct for Claude Code today
   and is exactly the line REPORT-89 will flip for Desktop.

So the shared source is the **data-model core** (views, columns, identity, the `res_<N>` trap,
`TRY_CAST`, the join recipe, the sensitive-data norm) and each surface supplies a thin wrapper: the
skill adds frontmatter plus its CLI framing, the MCP instructions add the tool framing. The core is
the part that rots and the part the guard checks; the wrappers are short and surface-specific. This
is still single-sourcing in the sense the ticket wants, because the data-model prose exists once,
and retiring the skill is still deleting a consumer.

### Both surfaces are live at once in Claude Code

`cc-data init` writes the skill **and** runs `claude mcp add`. So a Claude Code user with cc-data
installed will have the data-model core in context twice: once from the skill file and once from the
MCP `instructions`. That is the ticket's intended end state (the skill is retired later, in Desktop's
wake), and it is the reason the core should stay tight rather than absorbing everything.

### Verified: the static view inventory is derivable, with no new hand-maintained list

`internal/duck/views.go` builds its statements from a manifest. A probe built a `viewSet` over an
**empty** manifest and got back exactly the nine static views and nothing else:

```
reports  report_prompts  answers  history  run_membership
downloads  attachment_files  attachment_states  attachment_content
```

The per-run and per-job views come from manifest entries, so an empty manifest excludes them
naturally. An exported helper over that call gives the guard a genuinely code-derived inventory. The
names come back SQL-quoted, so the helper strips the quoting.

The MCP tool inventory is already derivable the same way: a probe over the repo's existing in-memory
transport listed all seventeen registered tools via `ClientSession.ListTools`, which is the real
registration, not a restatement of it.

### Verified: the backfill, and why a substring guard is not enough

Checked against `SKILL.md` as it stands:

- **Views**: all nine are documented. The view-side backfill is zero, as the ticket predicted.
- **Tools**: only `reports_list` and `reports_jobs` are named. Fourteen are absent
  (`version`, `auth_status`, `get_report`, `get_answers`, `get_history`, `get_attachments`,
  `dataset_create`, `dataset_list`, `dataset_show`, `dataset_rename`, `dataset_edit`,
  `dataset_delete`, `dataset_purge`, `dataset_reindex`). That is the real backfill, and it is the
  whole of it.

The seventeenth, `query`, is the finding that matters. A plain substring search reports it as
documented, but only because the word "query" appears in prose about the `cc-data query` command and
about querying in general; the MCP tool named `query` is nowhere described. The same trap applies to
the view names `reports`, `answers`, `history` and `downloads`, all ordinary words that appear
throughout the file. A guard that greps the document would therefore pass today while three of the
things it exists to protect are undocumented. It has to match a structured entry: a catalog section
the guard parses, or at minimum the name in a fixed markup position, so that "documented" means an
entry exists rather than that the characters occur somewhere.

### There are more than two copies of the view catalog

Beyond `SKILL.md`, `docs/researcher-guide.md` carries its own view table and `README.md` its own
partial list. The researcher guide is **already drifted**: it documents eight of the nine views and
omits `attachment_states`. This is the exact failure the guard exists to prevent, in a file the
ticket's scope does not name. Whether the guard covers it is an open question below; the register is
deliberately different (the guide is written for a researcher, not for Claude), so it is not a
candidate for rendering from the shared source.

### Rehearsal of the guard's parse and the `dataset_create` split

Before any implementation spec, the candidate catalog parser and the argument split were written as
throwaway Go tests and run against the real files and the real tool registration. The throwaway code
was deleted rather than committed.

**The parse works, and one regex covers both markup shapes.** Scoping to a named section and taking
every leading backticked identifier behind a `- ` or `| ` prefix recovers, from the files as they
stand today:

| Source | Names recovered |
| --- | --- |
| `SKILL.md` `## Views` | all **9** static views |
| `docs/researcher-guide.md` view table | **8** views, `attachment_states` absent |
| `SKILL.md` Tools catalog | **0**, there is no such section |
| `ListTools` on the real server | **17** tools |

That confirms all three claims the guard rests on in one run: the view-side backfill is zero, the
researcher guide really has drifted and the guard really would catch it, and the tool-side backfill
is the full seventeen. Multi-name entries (`- \`answers\`, \`history\``, `| \`run_membership\`,
\`downloads\``) are recovered whole, so a first-identifier-only parse would indeed have failed on
introduction.

**The `dataset_create` split preserves the fallback and exposes a naming collision.** Driving
`ParseRefForConfig` through a `portal` + `name` join for four cases:

| Case | Result |
| --- | --- |
| explicit portal, default set | `ngss-assessment.portal.concord.org/wf` |
| omitted portal, default set | `learn.concord.org/wf` |
| omitted portal, no default | the existing "no portal in ref … and no default_portal configured" |
| `staging` as the portal | refused: "portal \"staging\" is an environment alias; use the full hostname" |

The first three confirm the optional-`portal` decision. The fourth was not anticipated and produced
two new requirements above: dataset portals are hostnames only, while `portal` on `reports_list` and
`reports_jobs` accepts an alias, so the same argument name means two different things across the
surface, and the guidance's warning about it is written in a ref syntax that this story deletes.

### Verification environment

Two throwaway Go tests were used and then removed: one asserting `ServerOptions.Instructions`
round-trips to `ClientSession.InitializeResult().Instructions` over `mcp.NewInMemoryTransports`, and
one enumerating `viewSet.statements()` over an empty manifest. Both ran green against the repo as it
stands (go-sdk v1.6.1).

## Out of Scope

- The `login` MCP tool, the full-dataset export, the `.mcpb` artifact and multi-host `init`. Those
  stay in REPORT-89.
- Rewriting the researcher guide's prose, or generating it from the shared source. Its audience and
  register are deliberately different; only its view table is guarded, and only for completeness.
- Any change to what the tools do. This story changes descriptions and adds guidance; the only
  behavioral change is `dataset_create`'s argument shape.
- Retiring the Claude Code skill. This story makes retiring it cheap; it does not do it.
- Documenting the Portal reports, `reports create`, or the filter-options workflow. Those land with
  the stories that build them, into the source this story creates.

## Open Questions

All seven questions raised while drafting were resolved against the code and against Go probes; none
of them turned out to need a project-owner call. They are kept as RESOLVED with their rationale,
because several are the reason a requirement above reads the way it does. The one that widens the
ticket's stated scope is marked as such, with the cut that reverses it.

### RESOLVED: How is a guidance entry structured so the guard can check it?
**Context**: A substring guard passes today while the `query` tool is undocumented, because the word
appears in unrelated prose. The guard needs a structured notion of "documented."
**Options considered**:
- A) Machine-checkable catalog sections the guard parses.
- B) A markup pattern (name in backticks at the start of a list item) matched anywhere in the file.
- C) Free-form prose paired with a separate structured manifest the renderer expands.

**Decision**: **A**, in the specific form the file already has. Probed: extracting the leading
backticked identifiers from the list items inside `SKILL.md`'s existing `## Views` section yields
exactly the nine static views today, with no edit to the file. So the "structured catalog" is not a
new format to invent, it is the shape the prose is already written in; the guard just has to scope
its parse to the catalog sections rather than the whole document, and a Tools section has to be
added in the same shape.

B is the same parse without the scoping, and the scoping is the entire point: an unscoped match is
what lets `query` pass on a coincidence. C is the strongest single-sourcing in the abstract, but it
puts a template layer between an author and the prose Claude reads, for a catalog of nine views and
seventeen tools that a human should be able to read and edit directly.

---

### RESOLVED: Does the drift guard cover `docs/researcher-guide.md` and `README.md`?
**Context**: The guide's view table already omits `attachment_states`. It is not in the ticket's
scope, and its prose is not a candidate for single-sourcing.
**Options considered**:
- A) Guard the shared source only; fix the guide's gap as a drive-by.
- B) Guard the shared source and the researcher guide's view table; leave README alone.
- C) Guard the shared source only; file the guide's gap separately.

**Decision**: **B**. *This widens the ticket's stated scope*, so it is called out rather than
buried. The reason is that the guide is a live counter-example to the story's own thesis: it is a
second view catalog, it has already drifted, and the drift is invisible. Guarding it is the same
parse pointed at a second file plus one added table row, which is a small fraction of the guard's
cost, and leaving it out means shipping a story about guidance rot alongside a known-rotted file.

README stays unguarded: its list is illustrative rather than a catalog, and holding a feature summary
to catalog completeness would make it worse.

**The cut, if scope is tight**: drop to A. The guard's file list becomes one entry, the guide still
gets its missing row, and nothing else in this spec changes.

---

### RESOLVED: How much of the data model belongs in the MCP `instructions` block?
**Context**: `instructions` sit in context for the whole session, for every client. In Claude Code
the user also has the skill file, so the core is present twice.
**Options considered**:
- A) The full core.
- B) A compact core, with per-view detail pushed into the `query` tool description.
- C) A compact core plus an MCP resource carrying the full catalog on demand.

**Decision**: **A**. The ticket enumerates the instructions block's contents and "view catalog" is on
that list, so this is settled scope rather than an open trade-off. It is also the right call on the
merits for the client that motivates the work: Claude Desktop has no skill file and no `--help`, so
`instructions` are its only source, and a client that has to ask for the catalog will write a query
against a view it guessed. B additionally splits the data model across two places, which fights both
the single-source goal and the guard. C is a real optimization but a Desktop-era one; REPORT-89 is
where the size of the Desktop context budget becomes a measured fact rather than a worry.

---

### RESOLVED: Is `dataset_create`'s argument change breaking, and does anything need to absorb it?
**Options considered**:
- A) Straight replacement.
- B) Accept both, preferring `portal` + `name`.
- C) Keep `ref`, improve the description.

**Decision**: **A**. The ticket already decided the shape; what was open was the compatibility cost,
and it is close to zero. MCP tool arguments are not written by stored client code the way a REST
payload is: the model composes each call from the schema it is handed at `initialize`, so a changed
schema is picked up on the next connection with nothing to migrate. Against that, B leaves a
deprecated argument in a schema the model reads every session, which is the same rot this story
exists to remove, in the one place it is most expensive.

---

### RESOLVED: Which identity columns does the guard pin, and where do they come from?
**Context**: The ticket names `remote_endpoint` and `learner_id`. `remote_endpoint` has a code-side
inventory (`contractStoreColumns` and `membershipColumns` in `views.go`); `learner_id` is a report
CSV column produced by the server and appears in no Go inventory.
**Options considered**:
- A) Pin only the columns with a code-side inventory.
- B) Pin an explicit list including `learner_id`, accepting a literal on the truth side.
- C) Drop the column check entirely.

**Decision**: **A**. The guard's defining property, stated in the requirements, is that its truth
side is derived rather than restated; B would put a hand-maintained list back into the test, which is
the second inventory the story exists to eliminate, and it would not even work: no local code can
observe the server renaming a CSV column, so the literal would guard nothing but itself. So the
guard pins `source_key`, `remote_endpoint`, `question_id` and `history_id`, which it can genuinely
verify, and `learner_id` stays documented prose. C throws away the half that does work.

---

### RESOLVED: Should the shared core be embedded once and split at render time, or kept as separate files?
**Options considered**:
- A) One core file plus one wrapper file per surface, all embedded and concatenated per surface.
- B) One file with marked regions each renderer filters.
- C) One file that is the MCP instructions verbatim, with the skill writer prepending its extras.

**Decision**: **A**. Each file is valid markdown that reads correctly on its own, so an author edits
prose rather than a region-marked template, and a reviewer sees the core's diff without the wrappers
in it. B makes every file unreadable in isolation and puts a filter between the source and both
outputs. C is A with the core and the MCP wrapper fused, which works only until the MCP wrapper needs
anything the skill must not have; REPORT-89 flipping the auth-remedy line for Desktop is exactly that
case, already on the roadmap.

---

### RESOLVED: Where does the guard test live?
**Options considered**:
- A) A new `internal/guidance` package owning the source and renderers; the guard test there.
- B) The source stays in `internal/claude`, with the guard in a top-level test package.
- C) The guard alongside the tool-surface test in `internal/mcpserver`.

**Decision**: **A**. Verified import graph: `internal/mcpserver` already imports `internal/duck`, and
`internal/claude` imports neither. Once the MCP server renders guidance, `mcpserver` must import
whatever package owns it; leaving that package as `internal/claude` makes the MCP server depend on
the Claude Code *skill installer*, which is backwards and blocks the skill's eventual retirement.
A neutral `internal/guidance` that imports nothing, imported by both `claude` and `mcpserver`, has no
cycle and makes "delete a consumer" literally true.

C keeps the guard next to the existing tool-surface test, which is where the third tool-name
inventory lives and would be reconciled, but it puts the drift guard inside one of the two things it
guards. B leaves ownership ambiguous with no upside over A.

## Self-Review

Multi-role review of this spec, run after it was written. Roles: Senior Engineer (Go), QA Engineer,
Technical Writer, LLM-Interface Designer, Security Engineer, Release Engineer. Every issue below was
checked against the code before being written down; candidates that did not survive were dropped.
Two dropped ones worth naming so they are not re-raised: CI genuinely runs `go test ./...` (both
`ci.yml` and `release.yml`), so "fails CI" is real rather than aspirational; and the skill's version
stamp and `MaybeRefresh` freshness check are indifferent to how the body is assembled, so
concatenating a core and a wrapper does not disturb them.

### Senior Engineer

#### RESOLVED: two requirements contradicted each other over shell commands
The shared-source section required that the MCP instructions carry "no instruction to run a shell
command that an MCP-only client cannot run." The MCP-instructions section, two bullets later,
required them to "state the auth remedy as: run `cc-data login` in a terminal." Those cannot both
hold as written, and the second is the one the ticket mandates.

The distinction the first bullet was reaching for is real but was stated too broadly: what an
MCP-only client cannot do is *execute* a command or read `--help` output, whereas relaying a
terminal instruction to the human is exactly the right behavior on `NOT_AUTHENTICATED` and is what
REPORT-89 later replaces with a `login(portal)` tool call. **Resolution**: the bullet now prohibits
directing the *model* to execute a shell command or read `--help`, and says explicitly that relaying
an instruction to the user is a different act and required.

#### RESOLVED: the `dataset_create` argument split was silent on the default-portal fallback
The requirement said only that the tool takes `portal` and `name` separately. Verified that this
silently decides a live behavior: `ParseRefForConfig` resolves through `splitRef`, which, given a
ref with no `/`, substitutes `defaultPortal.Host()` and returns a specific error only when no
default is configured. So `dataset_create({ref: "wildfire"})` works today against the configured
default portal.

A required `portal` argument would remove that, and would also falsify a line in the guidance this
very story single-sources: `SKILL.md` states that "a bare `<name>` resolves under the configured
default portal." A story about guidance not rotting should not begin by invalidating one of its own
sentences. **Resolution**: `portal` is optional with the same default-portal fallback, so the change
is a spelling change rather than a capability removal, and the guidance line stays true.

### QA Engineer

#### RESOLVED: "reconciling" the tool-surface test's `want` list would delete a real check
The Testing section asked for the existing `TestMCPToolSurface` `want` list to be "reconciled with
the guard rather than left as a third inventory of tool names." Read literally that means deriving
it, which would make the test vacuous.

Verified the test does more than list names: after checking each expected tool is present and each
excluded command is absent, it asserts `len(res.Tools) != len(want)`, an exact-count check whose
entire purpose is to fail when a tool is registered that nobody intended to expose. A list derived
from the registration cannot ever fail that way. The two lists are opposite by design: the test's is
the *specification* of the intended surface checked against the code, and the guard's truth side is
the code checked against the *documentation*. **Resolution**: the requirement now says the `want`
list stays hardcoded and explains why the two coexist.

### Technical Writer

#### RESOLVED: the guard has to parse two different markup shapes, and multi-name entries
The guard was specified as one parse ("the leading backticked identifiers of their entries"), but it
is pointed at two files whose catalogs are written differently: `SKILL.md`'s Views section is a
bulleted list (`- \`reports\` — …`), while `docs/researcher-guide.md`'s is a markdown table
(`| \`reports\` | Report CSV rows … |`).

Both also already contain entries documenting more than one name in a single entry: SKILL.md's
`- \`answers\`, \`history\` — the identity-keyed stores`, and the guide's
`| \`run_membership\`, \`downloads\` | Provenance…`. A parse that takes only the first identifier
of an entry would report `history` and `downloads` as undocumented and fail on introduction, against
a backfill this spec asserts is already closed on the view side. **Resolution**: both properties are
now stated as requirements.

### LLM-Interface Designer

#### RESOLVED: requiring `query`'s description to echo the views recreates the duplication
The tool-description section required `query` to "echo the available views". Taken at face value
that puts a hand-written list of the nine views inside `tools.go`, which is a third copy of the
catalog, in a file the drift guard does not read, in a story whose entire purpose is that there is
one copy and it is guarded. It would rot first and loudest, because a tool description is what the
model reads at the moment it writes SQL.

**Resolution**: the requirement now says `query` echoes the query-writing rules (`TRY_CAST`,
`UNION ALL BY NAME`), and that if it lists views at all the list is generated from the same catalog
rather than typed into the description.
