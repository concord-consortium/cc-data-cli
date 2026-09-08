# Single-Source Claude/MCP Guidance and Drift Guard

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-104

**Status**: **Closed**

## Overview

The MCP server shipped no guidance at all, so a client connecting to it got a bare list of tool names and had to infer the data model from scratch; the only description of that model lived in the Claude Code skill file. This creates one embedded source that both surfaces render, wires it into the MCP server's `instructions` block so it arrives in the `initialize` response, and adds a test that fails when the guidance and the code disagree, so neither surface can rot as views and tools are added.

Every later story in the backlog documents into it: REPORT-92, REPORT-93 and REPORT-94 each add a tool or a view Claude has to be told about, and REPORT-95 teaches the guidance a new workflow. The guard makes "register a view and document it" one indivisible act rather than a convention someone has to remember.

## Requirements

All requirements were implemented. Rationale for the non-obvious ones is in Decisions below.

### The shared guidance source

- One embedded markdown file is the single source of the data-model prose: the no-raw-PII norm, the data model, the view catalog, the `res_<N>` positional trap, `TRY_CAST` on VARCHAR answer columns, the `run_membership` type-qualified join recipe, and the portal facts.
- The auth **instructions** are the deliberate exception and live in the per-surface wrappers: the `NOT_AUTHENTICATED` remedy, and the spelling of the status check (`cc-data auth status --check` on the skill, `auth_status` with `check=true` on MCP).
- Both surfaces render from it: the skill file `cc-data init` writes, and the MCP server's `instructions`. There is no second copy of the data-model prose in the repo.
- Neither surface renders content that is wrong for it. The skill keeps its Claude Code frontmatter and CLI framing; the MCP instructions carry no frontmatter and nothing directing the **model** to execute a shell command or read `cc-data <cmd> --help`. Telling the model to *relay a terminal instruction to the user* is a different act and is required.
- Retiring the skill later is deleting a consumer, not migrating content.
- The version stamp and freshness-refresh behavior of the installed skill file are unchanged.

### MCP server instructions

- `instructions` appear in the `initialize` response, asserted by a test reading `ClientSession.InitializeResult().Instructions` over the in-memory transport.
- They state the auth remedy as: run `cc-data login` in a terminal on `NOT_AUTHENTICATED`. REPORT-89 replaces that one wrapper section with `login(portal)`.
- They name `auth_status` with `check=true` for the status check, never `cc-data auth status --check`, which the model can read but not run.

### The drift guard

- A test cross-checks the guidance against the code-derived inventory and fails in both directions: a registered name no entry documents, and an entry naming something that no longer exists.
- The inventory is derived from the code, never a hand-maintained list in the test.
- It covers static view names, MCP tool names, and the identity columns with a code-side inventory (`source_key`, `remote_endpoint`, `question_id`, `history_id`). `learner_id` is a server-produced CSV column with no local inventory, so it stays documented prose.
- It also covers the view table in `docs/researcher-guide.md`, a second catalog that had already drifted. `README.md` is not covered; its list is illustrative.
- Only static view names are guarded. The per-run and per-job names (`report_<run>`, `answers_<run>`, `history_<run>`, `report_<run>_job_<job>`) cannot be enumerated in prose, so the guidance describes their shape and the guard does not match them.
- The guard matches a **documented entry**, not any occurrence of a name in prose: it parses catalog sections and reads the leading backticked identifiers of their entries.
- The identity columns are documented as their own catalog section and guarded by the same parse, in both directions, so a column removed from `membershipColumns` while its documentation stays is caught too.
- The parse takes the first matching section only, so a later heading carrying the same word cannot fold unrelated entries in.
- An entry may document more than one name, and the two guarded files use different markup (bulleted list, markdown table). One parse handles both.
- A catalog section the guard cannot locate is a **failure**, not an empty result.
- The backfill is closed before the guard is turned on, so it passes on introduction.
- Registering a view or a tool without documenting it fails CI.

### Tool descriptions

- `dataset_create` takes `portal` and `name` as separate arguments rather than one concatenated ref. `portal` is optional with the same default-portal fallback, so the change is a spelling change, not a capability removal.
- `name` is a name, not a ref: one containing `/` is refused with a message naming the `portal` argument.
- `dataset_create`'s `portal` takes a hostname only and refuses an environment alias, the opposite of `portal` on `reports_list` and `reports_jobs`. The description says so, because the shared argument name is what makes the collision inviting.
- The guidance's dataset-ref warning is rewritten to match the new argument shape.
- `get_attachments` states its precondition: answers or history must be fetched for the run first.
- `query` echoes `TRY_CAST` on VARCHAR answer columns and `UNION ALL BY NAME`, and generates its view list from the code-derived inventory rather than restating it.
- `dataset_delete` and `dataset_purge` say what is destroyed and that it is not recoverable.
- Existing MCP behavior contracts are unchanged: excluded terminal and installer commands stay excluded, no tool exposes `url`, `inline` or `allow-dir`, annotations are untouched, and `dataset_show`'s payload stays byte-identical to the CLI's `--json`.

### Testing

- Every guard assertion names the specific missing or stale entry.
- The guard is proved able to fail, not merely observed passing.
- A test asserts both rendered surfaces carry the auth remedy and the never-drive-the-browser-login norm, the one sentence the wrapper split leaves in two places.
- A test asserts the core names no `cc-data` command, since a command spelling drifting into it becomes an instruction the MCP client cannot carry out.
- The existing tool-surface test's hardcoded `want` list stays hardcoded.

## Technical Notes

- `internal/guidance` is dependency-free, imported by both `internal/claude` and `internal/mcpserver`, so neither imports the other and the skill's eventual retirement is a deletion.
- Both surfaces are live at once in Claude Code, since `cc-data init` writes the skill **and** runs `claude mcp add`. That is the intended end state until the skill is retired, and the reason the core stays tight.
- Each surface renders its wrapper **above** the core. That ordering is what forces whole auth sections into the wrappers rather than a remedy split across both files.
- The static view inventory comes from a `viewSet` over an empty manifest. A report download only expands into a per-run view once it carries both `Files` and `Columns`, so a fixture missing either proves nothing about the exclusion.
- Test assertions must not compare byte sequences spanning a newline. The guidance sources are plain repo files with no `.gitattributes`, so a Windows checkout embeds them with CRLF.

## Out of Scope

- The `login` MCP tool, the full-dataset export, the `.mcpb` artifact and multi-host `init`: REPORT-89.
- Rewriting the researcher guide's prose or generating it from the shared source. Its audience and register are deliberately different; only its view table is guarded.
- Any change to what the tools do. The only behavioral change is `dataset_create`'s argument shape.
- Retiring the Claude Code skill. This makes retiring it cheap; it does not do it.
- Documenting the Portal reports, `reports create`, or the filter-options workflow. Those land with the stories that build them, into the source this created.

## Decisions

### How is a guidance entry structured so the guard can check it?

Catalog sections the guard parses, in the shape the prose already had. A substring search passes on prose coincidence: it reports the `query` tool as documented because the word appears in unrelated prose, and the same trap covers the `reports`, `answers`, `history` and `downloads` views. Rejected: matching the markup pattern anywhere in the file (that is the unscoped version, and the scoping is the whole point), and a separate structured manifest a renderer expands (a template layer between an author and the prose Claude reads, for nine views and seventeen tools a human should edit directly).

### Does the drift guard cover `docs/researcher-guide.md` and `README.md`?

The guide's view table, yes; README, no. This widened the ticket's stated scope deliberately: the guide is a second view catalog that had already drifted, invisibly, which is the story's own thesis playing out in a file the ticket did not name. Guarding it is the same parse pointed at a second file. README stays out because its list is a feature summary, and holding it to catalog completeness would make it worse.

### How much of the data model belongs in the MCP `instructions` block?

The full core. Claude Desktop has no skill file and no `--help`, so `instructions` are its only source, and a client that has to ask for the catalog will write a query against a view it guessed. Splitting per-view detail into the `query` description would fight both the single-source goal and the guard. Serving the catalog as an on-demand MCP resource is a real optimization but a Desktop-era one, for REPORT-89, where the context budget becomes a measured fact.

### Is `dataset_create`'s argument change breaking?

No, and nothing needs to absorb it. MCP tool arguments are composed by the model from the schema handed to it at `initialize`, so a changed schema is picked up on the next connection with nothing to migrate. Accepting both spellings would leave a deprecated argument in a schema the model reads every session, which is the same rot the story exists to remove, in the most expensive place for it to live.

### Which identity columns does the guard pin, and how?

Only the four with a code-side inventory, from `membershipColumns`, documented as a catalog section and compared in both directions like the views and tools. They were first written as sentences and matched as backticked names anywhere in the core, which could only check registration against documentation: a column deleted from `membershipColumns` left its documentation standing and the guard still passed. Giving them a catalog section removed the special case rather than patching it, and a reader writing a join now has the key listed in one place. Pinning `learner_id` too would put a hand-maintained list back on the guard's truth side, and would not even work: no local code can observe the server renaming a CSV column, so the literal would guard nothing but itself. Deliberately not `contractStoreColumns`, which also carries the internal `_fetched_at`/`_run_id` bookkeeping that has no business being required in prose.

### One core file plus per-surface wrappers, or marked regions in one file?

Separate files, concatenated per surface. Each file is valid markdown that reads correctly alone, so an author edits prose rather than a region-marked template and a reviewer sees the core's diff without the wrappers in it. Fusing the core with the MCP wrapper works only until the MCP wrapper needs something the skill must not have, which REPORT-89 is already scheduled to need.

### Where does the guard test live?

A new `internal/guidance` package owning the source and renderers. Once the MCP server renders guidance it must import whatever package owns it, and leaving that as `internal/claude` would make the MCP server depend on the Claude Code *skill installer*, which is backwards and blocks the skill's retirement. Putting the guard in `internal/mcpserver` would place it inside one of the two things it guards.

### Where does the auth guidance live?

Every auth *instruction* is wrapper prose on both surfaces, worded per surface; only the portal facts are shared, and not under an Auth heading. Two reasons, both from the render order: a remedy divided between wrapper and core would state the fix before naming the condition, since the invocation sits in the wrapper and `NOT_AUTHENTICATED` in the core; and `cc-data auth status --check` in the core would reach the MCP instructions as a shell command the model cannot run. The cost is one duplicated sentence, the norm against driving the browser login, which a test pins on both surfaces.

### Where does `query`'s view list come from?

`duck.StaticViewNames()`, the registration, rather than parsing the guidance's catalog. Both avoid the third hand-written copy, which is what the requirement protects; the registration names the views a query can actually run against rather than the ones the prose claims, and it has no error path, where parsing the catalog at registration time would need a silent fallback that could ship a description with its view list missing. The guard already pins the two sets equal. The per-run shapes are left to the guidance for the same reason: written into the description they would be a view-name copy in the one place the guard cannot read.

### Two requirements contradicted each other over shell commands

The shared-source rule barred the MCP instructions from carrying any instruction to run a shell command; the MCP-instructions rule required them to say "run `cc-data login` in a terminal". What an MCP-only client cannot do is *execute* a command or read `--help`; relaying a terminal instruction to the human is exactly the right behavior on `NOT_AUTHENTICATED`, and is what REPORT-89 later replaces with a tool call. The rule now prohibits directing the model to execute, and says relaying is required.

### The `dataset_create` split was silent on the default-portal fallback

`splitRef` substitutes the default portal for a ref with no `/`, so `dataset_create({ref: "wildfire"})` worked against the configured default. A required `portal` would have removed that and falsified a line in the guidance this very story single-sources. `portal` is optional, so the change is a spelling change and the guidance line stays true.

### Deriving the tool-surface test's `want` list would delete a real check

The test does more than list names: after checking presence and exclusions it asserts an exact count, whose entire purpose is to fail when a tool is registered that nobody intended to expose. A derived list can never fail that way. The two lists are opposite by design: the test's is the specification of the intended surface checked against the code; the guard's truth side is the code checked against the documentation.

### The parse must handle two markup shapes and multi-name entries

`SKILL.md`'s Views section is a bulleted list and the researcher guide's is a table, and both already contain entries documenting two names at once (`` - `answers`, `history` ``, `` | `run_membership`, `downloads` ``). A first-identifier-only parse would have reported `history` and `downloads` as undocumented and failed on introduction. One regex covers both shapes, since each puts the names first behind a fixed prefix.

### The guard's comparison ships in the package, not beside its assertions

`Missing` is exported from `internal/guidance` so the negative-control test drives the same comparison the guard does. A comparison defined in the test file would still report the missing name if the guard were deleted, which is a control that cannot fail.

### The researcher-guide guard checked only one direction

It asserted every registered view appears in the table but not the reverse, so a row naming a removed view would have sat there indefinitely: the same rot, in the file the guard was added to protect. Both directions are now asserted, with their own messages.

### Content moved by paragraph, not by section

Two of `SKILL.md`'s six sections could not be assigned whole. Sending "Fetching data" to the skill would have dropped the epoch seconds-versus-milliseconds guidance from the MCP surface, which is what stops Claude passing `timestamp` to `to_timestamp` and getting year 57814. Auth splits for the reasons above.
