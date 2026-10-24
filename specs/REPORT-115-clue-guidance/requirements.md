# cc-data: the MCP instructions cap, and CLUE document data in the Claude guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-115
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

The MCP server's instructions shrink to an index under Claude Code's 4,096-character cap, and a new `guide` tool returns the full guidance, so a client with only the MCP server sees the views catalog and the traps instead of a text cut off mid-sentence. The guidance then gains the CLUE pieces REPORT-110, REPORT-111 and REPORT-152 did not add: how to route a CLUE question to the right fetch and view, `entry_uid`, and the UI state in CLUE log events.

## Project Owner Overview

Researchers ask Claude questions and Claude answers them through cc-data. Claude learns how cc-data's data is shaped from one body of guidance, delivered two ways: the Claude Code skill, and the instructions an MCP server hands every client when it connects. Claude Code keeps only the first 4,096 characters of those instructions and drops the rest, and cc-data's are now about 31,000. A session that has only the MCP server (Claude Code without the skill, and later the other hosts the backlog's Desktop stories will bring) sees the opening paragraphs and none of the view descriptions or traps, which is where every warning about CLUE data lives. This story makes the instructions a short index and moves the full text behind a tool Claude calls before it starts.

The CLUE corpus has more traps than most, and each cost Scott real time: two things called "history", two clocks, an `action` column that is the wrong unit, and program output that reads like student edits. REPORT-110 to REPORT-152 already wrote most of those warnings down. This story adds the missing pieces and the routing that ties them together, so a researcher asking "what did this student change?" gets an answer from the patch-level view with the clock caveat stated, and never a LARA history pull.

## Background

**Decided scope** is REPORT-115's Jira story as amended on 2026-10-23 (the cap first, then the CLUE prose), with the channel `stream/researcher-dashboard/clue-cc-data` from #958 on and `stream/researcher-dashboard/general` #978 (CLUE-742's `owner_type` contract). The implementation goes on RD-15's cc-data-cli branch `RD-15-raise-display-limit`, which is cut from `REPORT-152-recent-logs` (on `REPORT-111-clue-views`, on `REPORT-110-clue-fetch`) and rebased onto when it is built. RD-15 changes only `internal/packages/result.go`, its test and the package-run line of `docs/researcher-guide.md`, none of the guidance, so everything below, measurements included, describes the code on `REPORT-152-recent-logs`.

**How guidance is built today** (REPORT-104, REPORT-95). `internal/guidance/guidance.go` embeds four files from `internal/guidance/src/`:

- `Skill()` = `skill_header.md` + `core.md`, written by `cc-data init` as the Claude Code skill.
- `Instructions()` = `mcp_header.md` + `core.md` + `tools.md`, passed as `ServerOptions.Instructions` in `internal/mcpserver/server.go` and delivered in the `initialize` response.
- `Core()` and `Tools()` are what the guards parse.

The guards, all of which this story has to keep green:

- `internal/guidance/guard_test.go`: every static view is in `core.md`'s `## Views` catalog and the reverse; every registered tool is in `tools.md`'s `## Tools` and the reverse; every identity column is in `## Identity columns`; both `report_type` vocabularies match the code; every slug-shaped name in the core is a real slug; the core names no `cc-data` command; the researcher guide's view table matches the static views; both surfaces carry the auth remedy (`TestBothSurfacesCarryTheAuthRemedy`) and the re-pull mechanism (`TestBothSurfacesCarryTheRePullMechanism`, which looks for "passing `refresh`" in `Instructions()`).
- `cmd/guidance_commands_test.go`: every `` `cc-data ...` `` spelled out in `Skill()` or `Instructions()` resolves to a real command.
- `internal/mcpserver/server_test.go`: `TestMCPInstructionsArriveInInitialize` (the instructions carry `run_membership`, `NOT_AUTHENTICATED` and `auth_status`, no frontmatter and no `--help`); `TestMCPToolSurface` (the exact tool list and its count); `TestMCPQueryDescriptionCarriesTheQueryRules` (the `query` description names every static view, `TRY_CAST` and `UNION ALL BY NAME`); `TestToolDescriptionsCarryTheirPortalFacts` (each named tool's description keeps its pinned facts, including the three `get_clue_*` tools and `get_recent_logs`).

**The cap, measured.**

- In Claude Code 2.1.310 (the installed build), a session's own system prompt carried cc-data's instructions cut off with `… [truncated]`, partway through `json_extract_s`. Rebuilding `main`'s `Instructions()` from its four source files gives 22,141 bytes, 22,065 characters, and the cut falls at exactly character 4,096 of it, with `## Views` at character 4,195. The binary also logs `instructionsLength` and `instructionsTruncated` on every MCP connection. The limit is `CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH` when set, else 4,096 (the same limit applies to a tool description; a deferred tool's description, once loaded through tool search, gets 16,384). Bug reports from earlier 2.1 releases describe the same cap at 2,048 for instructions and descriptions, so the limit has moved between versions and a researcher on an older build sees less.
- On this branch `Instructions()` is 31,509 bytes (31,425 characters): `core.md` 27,338, `tools.md` 3,039, `mcp_header.md` 1,130. So 87% of it never reaches an MCP-only client.
- What does reach it today is the tool-flow paragraph, Auth, `## Datasets and portals`, and the start of `## Runs and their data`. The two sections a client needs before anything else, the auth remedy and dataset refs, end at about 2,300 characters (`mcp_header.md` plus `## Datasets and portals`, 1,130 + 1,170).
- REPORT-110 and REPORT-152 already worked around the cap by putting their must-know facts in tool descriptions, which arrive whole (`get_clue_history`: "not get_history's interactive-state history"; `get_recent_logs`: "recent_logs view, not logs"). That stays: a tool description is the one surface every client sees in full.
- REPORT-104's spec rejected an on-demand catalog as "a Desktop-era" optimization for REPORT-89. The cap makes it necessary now, for Claude Code too.

**Claude Code's tool-result limit.** The same binary caps an MCP tool's text result at 25,000 tokens by default (`MAX_MCP_OUTPUT_TOKENS`). The guide text (core plus tool catalog) is 30,378 bytes, roughly 8,000 tokens, so `guide` fits with room to grow; R5 keeps it that way.

**The CLUE guidance already on the branch.** REPORT-110, REPORT-111 and REPORT-152 added, and this story does not repeat:

- `core.md`: the `clue_documents`, `clue_history`, `clue_content` and `clue_patches` catalog entries, with `owner_type = 'user'` cohort matching; a "Working with the CLUE views" block covering the two clocks, entry order `(idx, entry_id)`, ordering `clue_patches` by its whole key, "`action` is not the edit", runtime output (`program/values/`, `demoOutput`, `tickEntries/`, `content/step`, `tickAndProcess`, matched with `LIKE`), `replace` carrying only the new value, `value IS NULL` against JSON null, unparseable payloads, and the absent per-run `clue_patches`; the `run_membership` join keys; `doc_id` and `entry_id` in `## Identity columns`; the cohort recipe's CLUE step; materializing a large CLUE dataset; `recent_logs` and its overlap with `logs`.
- `tools.md` and the tool descriptions: the three `get_clue_*` tools, documents before history, Student ID Mapping as the intended source, and CLUE history versus `get_history`.
- `skill_header.md`: the three `cc-data get clue-*` commands and `get recent-logs`.
- `docs/researcher-guide.md`: the CLUE commands in §4's complete session, recent logs in §4, the four CLUE views in §5's table and a "few things to know" list mirroring the core's, and the `get clue-*` commands in §5's Portal reports passage. The old "not pullable by cc-data" passage the Jira story asks to reverse is already gone (`grep` finds no "not pullable").
- `query --help` and the `query` tool description name every static view from `duck.StaticViewNames()`, so the CLUE views and `recent_logs` are already listed.

**What is missing**, checked against the Jira story's lists:

- **Routing.** Nothing tells Claude which fetch and view a CLUE question needs before it starts. The only "history" warnings are phrased from the CLUE side (`get_clue_history` is not `get_history`'s), which helps once Claude is already looking at the CLUE tool, not when a researcher says "pull the history for this class's CLUE documents" and `get_history` is the first match. There is also nothing asking Claude to state which clock a duration or ordering used.
- **`entry_uid`.** `clue_history` passes it through as VARCHAR (`internal/store/kind.go:128`) and no guidance mentions it. Scott's inventory (`cc-data-studies` `sources/clue/clue-data-inventory.md`, branch `RIGSE-365-analysis-packages`) measured it NULL on all 6,862,752 entries: only CLUE's concurrent history manager stamps it, and classroom documents use the non-concurrent one. A researcher who sees an all-NULL column reads it as a data gap.
- **CLUE UI state in log `extras`.** The `logs` entry shows `extras_json->>'selectedNavTab'` as a syntax example but says nothing about what CLUE puts there. CLUE's logger (`collaborative-learning` `src/lib/logger.ts:169-185`, read on its `master`) sets `navTabsOpen`, `selectedNavTab`, `problemPath` and `tzOffset` on every event, and `group` and `workspaceMode` on a student's; a key whose value is undefined drops out of the JSON, so on the 263,570 CLUE rows of the local `dataflow-cohort` dataset `selectedNavTab` is absent from 2,173 (2,172 of them sign-in `INTERNAL_AUTHENTICATED` events) and `group` from 3,480. `navTabsOpen` is `navTabContentShown`, which is `dividerPosition > kDividerMin` (`persistent-ui.ts:144-146`); the companion `workspaceShown` is not logged, so split view and full-screen curriculum both log `true` and only `false` (workspace full, curriculum hidden) is unambiguous. The inventory measured `extras` populated on every CLUE log row, which holds for the object, not for each key.
- **Researcher guide §6** ("What kinds of questions can I ask?") has subsections for answers, interactive-state history and logs, and none for CLUE documents. §3's example prompts have no CLUE one.

## Requirements

### The MCP instructions cap

- **R1. `Instructions()` is an index of at most 4,096 bytes.** Bytes, not characters: Claude Code counts JavaScript string length (UTF-16 units), and a Go byte count is never smaller, so a byte bound is the conservative test. It carries, in this order, most important first:
  - the opening paragraph of `mcp_header.md` (what cc-data is and the tool flow), with cc-data's `guide` tool added as the first call;
  - the `## Auth` section as it is (the auth remedy and the norm against driving the browser login);
  - a short `## Full guidance` section: call cc-data's `guide` tool once per session before the first fetch or query, because the views catalog, the traps and the tool catalog are there and not here; a client already holding the cc-data skill has the data model, views and traps (everything but the tool catalog, whose facts the tool descriptions carry) and need not call it;
  - `## Sensitive data`, the same text the core carries, since it governs what Claude does with the first summary it reads;
  - `## Datasets and portals`, the same text the core carries, last because `guide` repeats it.

  Everything but `## Datasets and portals` falls within the first 2,048 bytes, so a client on a build with the older, smaller cap still gets the auth remedy, the pointer to `guide` and the sensitive-data norm whole.
- **R2. One source for every sentence the index shares with the core.** `## Datasets and portals` and `## Sensitive data` are not copied into a second file: the index renders them from the same source the core does. `Skill()`'s rendered text is unchanged by this restructuring (only R8 to R11's additions change it).
- **R3. A `guide` MCP tool.** It takes no arguments, is annotated read-only, and returns `guidance.Core()` followed by `guidance.Tools()` as one text block, byte for byte. Its description opens with "cc-data's full guidance" (views, columns, traps, tools) and says to call it before the first fetch or query. The tool name alone is generic (Claude Docs, for one, also registers a `guide` tool), and Claude Code defers MCP tools behind a tool search, so the description and the index both name cc-data where they name `guide`. It is cataloged in `tools.md`, which is what keeps `TestGuidanceDocumentsEveryTool` passing.
- **R4. The guards move with the text.**
  - A new test fails when `len(guidance.Instructions()) > 4096`, and another when the `## Full guidance` and `## Sensitive data` sections do not both end within the first 2,048 bytes.
  - A new test calls `guide` over the in-memory transport and asserts its text equals `guidance.Core() + "\n" + guidance.Tools()` (or whatever separator R3's rendering uses, stated once), so it returns the whole of both.
  - The index keeps the auth remedy (`TestBothSurfacesCarryTheAuthRemedy` still runs against it) and names `guide`.
  - Checks that read the full text through `Instructions()` read it through the guide text instead: the MCP half of `TestBothSurfacesCarryTheRePullMechanism` ("passing `refresh`", which lives in `tools.md`). `TestMCPInstructionsArriveInInitialize`'s `run_membership` is dropped rather than moved, since the guide test's check that the text contains `Core()` already covers it. `TestMCPInstructionsArriveInInitialize` additionally asserts the instructions name `guide`. Its no-frontmatter and no-`--help` checks stay on the instructions and are also applied to the guide text, which is now where the core and the catalog reach an MCP client.
  - `cmd/guidance_commands_test.go` scans `Skill()`, `Instructions()` and the guide text, so a command spelled anywhere a client can read is checked.
  - `TestMCPToolSurface` lists `guide` and its count goes up by one.
- **R5. The guide fits a tool result.** A test fails when the guide text exceeds 75,000 bytes: Claude Code's default tool-result cap is 25,000 tokens, which is 75,000 bytes at a pessimistic three bytes per token (English prose runs nearer four). It is 30,378 bytes before this story and about 33,000 with its additions.
- **R6. The `query` tool description points at `guide`.** It gains one sentence saying the views' columns and traps are in cc-data's `guide` tool (named as R3 names it), and `TestMCPQueryDescriptionCarriesTheQueryRules` pins it. `query` is the tool every analysis reaches, and its description always arrives whole.
- **R7. Prose that described the old contract is updated.** `guidance.go`'s package comment ("renders it for each surface that carries it: the Claude Code skill file and the MCP instructions"), and the `Instructions()` and `Tools()` comments, which need to say the instructions are the index and the catalog is rendered by `guide`. Three more say the core reaches every MCP client through the instructions: `queryDescription()`'s comment in `tools.go` ("the guidance, which every client of this tool also receives"), `TestCoreNamesNoCommand`'s comment ("The core renders into the MCP instructions"), and the `instructions:` label on `TestBothSurfacesCarryTheRePullMechanism`'s MCP-half error. No researcher-guide or README line claims the MCP server hands Claude the whole guidance (checked by `grep -i instructions`), so R12's new sentence is the only doc change.

### The CLUE guidance

All of it goes in `internal/guidance/src/` (the core unless named otherwise), worded for both surfaces, so the core still names no command and no Jira story.

- **R8. Routing a CLUE question.** A short subsection after the cohort recipe (`### Answering a question about CLUE documents`), which the recipe's CLUE step does not already say:
  - A question about what students did in CLUE documents (built, changed, revised, how long they spent) is answered from the CLUE stores, fetched by a Student ID Mapping run's id, documents first. "History" of a CLUE document is CLUE document history, never `history`, which is LARA interactive-state history from report-service, a different store written by a different application.
  - Which view answers what: `clue_patches` for what a student changed and in what order; `clue_content` for what a document holds now; `clue_documents` for which documents exist and whose they are; `clue_history` for entry-level timing and counts. `logs` (or `recent_logs`) adds where the student was in the CLUE interface between edits.
  - Leave runtime output out of a student question (the "Working with the CLUE views" entry has the patterns). Order edits within a document by the entry key, never by a clock, and when reporting a time, a duration or a cross-document ordering, say which clock it used and why (`created` within one document, `server_created` across documents).
  - On a dataset not yet materialized, `clue_patches` unpacks every history entry the query does not exclude by comparing an entry-level column (`doc_id`, `entry_id`, `idx`, `created`, `server_created`, `action`, `is_revert`) with literal values, such as `doc_id IN ('...', ...)`, a time range or the runtime-output `action` patterns. A join, an `IN (SELECT ...)` or `IN (VALUES ...)`, `= ANY([...])`, the per-run scoping the core already gives and a filter on a patch-level column (`path`, `op`, `value`, `tile_id`) narrow the result but not the work. So on a large dataset, materialize first, or find the document ids and then filter by them as literals.
- **R9. `entry_uid`.** The `clue_history` entry says `entry_uid` is NULL by construction on documents written without concurrent editing, which is every classroom document so far, and is not a data gap.
- **R10. CLUE UI state in `extras`.** The `logs` entry says CLUE events carry the user's interface state in `extras` (`navTabsOpen`, `selectedNavTab`, `workspaceMode`, among others, `workspaceMode` on student events only, and `selectedNavTab` and `group` not on every event), that this holds for `recent_logs` too, and that `navTabsOpen` is true both for split view and for full-screen curriculum, so only `false` (the workspace full screen) is unambiguous.
- **R11. Tool descriptions.** `get_clue_history`'s description gains "per-edit questions read clue_patches, not the action column", pinned in `TestToolDescriptionsCarryTheirPortalFacts`. `get_history`'s description gains that it is not CLUE document history and that a CLUE document's history is `get_clue_history`, also pinned, since `get_history` is the tool a "history" request matches first.
- **R12. The researcher guide.**
  - §6 gains `### About how students built CLUE documents`: the kinds of question the CLUE views answer (what changed and in what order, how long within a document, what a document holds now, what students had on screen while editing), one SQL example over `clue_patches` that orders by the whole key and leaves runtime paths out, and the clock note in plain words.
  - §3 gains one CLUE example prompt.
  - The guide describes the CLUE pull end to end in one place: §6's new subsection names the order (a Student ID Mapping run, then documents, history and content, materialize when large, then query) and points at §4's commands and §5's views rather than restating them.
  - A sentence where the guide describes the MCP server (§2 or §3) says Claude reads the full guidance through `guide` when the skill is not installed.

### Acceptance

- **R13. Guards.** `gofmt`, `go vet ./...` and `go test -count=1 ./...` pass, with every guidance guard above covering the CLUE views, tools and commands and the new `guide` tool, not the view-and-tool drift guard alone.
- **R14. Walk-throughs.** A Claude session asked a CLUE editing question ("what did student 1234 change in their Dataflow document, and in what order?") pulls `clue-history` (after `clue-documents`), queries `clue_patches` rather than `clue_history.action`, orders by the entry key rather than `created`, and states the client-versus-server clock caveat unprompted for any time it reports. A session asked for "the history" of a CLUE document does not call `get_history`. Each runs:
  - in Claude Code with the skill installed;
  - in Claude Code over MCP alone (no skill), where it calls `guide` first;
  The data is the local stack (#958, #983), which needs the local-stack terminal to seed a document whose history entries carry real patches (today's seeds have `records: [{}]`, so `clue_patches` is empty there). A redacted transcript of each walk-through goes in the PR description.

## Technical Notes

- **Files.** `internal/guidance/guidance.go` and `src/*.md`; `internal/mcpserver/tools.go` (the `guide` registration, `queryDescription`, `get_history` and `get_clue_history` descriptions) and `server.go` (unchanged call to `guidance.Instructions()`); `internal/mcpserver/server_test.go`; `internal/guidance/guard_test.go`; `cmd/guidance_commands_test.go`; `docs/researcher-guide.md`.
- **R2's mechanism is the implementation spec's choice.** Two shapes satisfy it: split `core.md`'s two shared sections into their own embedded files that `Core()` concatenates, or render the index by extracting sections from `core.md` by heading. Either way `Core()`'s text and every guard reading it are unchanged.
- **Size budget.** As planned, `mcp_header.md` with its `guide` sentence is 1,255 bytes, the `## Full guidance` pointer 432, `## Sensitive data` 262 and `## Datasets and portals` 1,169: 3,121 bytes joined, leaving 975 under R1, and the first four end at byte 1,951, leaving 97 under R1's 2,048. Anything else that wants to be in the index has to argue its way past R4's tests.
- **Verified with throwaway code against `REPORT-152-recent-logs` (deleted).** A `guide` tool registered with go-sdk v1.6.1's `mcp.AddTool`, an empty input struct and `any` as the output type, lists with `inputSchema` `{"type":"object","additionalProperties":false}`, no output schema and `readOnlyHint`. Called with no arguments and with `{}`, it returns one `TextContent` block, no structured content, whose text equals `Core() + "\n" + Tools()` byte for byte (30,378 bytes).
- **Tool descriptions arrive whole and are not guidance.** They are registered with the server and are not part of `Instructions()` or the guide text, so a fact in a description is not seen by the drift guards; `TestToolDescriptionsCarryTheirPortalFacts` is what pins one.
- **The core stays command-free** (`TestCoreNamesNoCommand`). R8 and R10 name tools and views, never `cc-data ...` spellings; the skill already carries the commands.
- **Existing wording conventions.** `tools.md` and older `core.md` entries use an em-dash after the leading name; new entries follow REPORT-111 and REPORT-152's colon style (`- \`clue_history\`: ...`), which `ParseCatalog` reads the same way.

## Out of Scope

- A `cc-data guide` CLI command. The skill already carries the full text on the CLI side.
- Splitting the guide by section (a `section` argument). The whole text is about 8,000 tokens and well under the tool-result cap; R5 is the trip wire if that changes.
- Rewriting existing CLUE guidance that REPORT-110 to REPORT-152 added, beyond R9 and R10's additions to entries.
- Claude Desktop, including whether it applies the same cap and the Desktop half of the Jira walk-through criterion. cc-data does not run under Desktop yet; REPORT-89 (Desktop readiness) and REPORT-90 (the `.mcpb` bundle) in the backlog cover it, and REPORT-89's acceptance already has a Desktop cold walk-through (Doug, 2026-10-24). Jira's acceptance says the same. An index plus `guide` works under any cap, so nothing here depends on Desktop's answer.
- REPORT-192's one-row-per-event view over `logs` and `recent_logs`.
- Any change to the CLUE stores, views or fetch.

## Decisions

- **The index carries Sensitive data, not a partial views catalog.** Sensitive data governs the first dataset summary Claude reads, possibly before it calls `guide`; a partial catalog would invite queries written against it without calling `guide`.
- **A byte bound on the guide text (R5).** Claude Code's tool-result cap is as silent as the instructions cap was; one test turns the next truncation red.
- **The CLUE routing is its own subsection** after the cohort recipe, not a recipe step or more Working-with-the-CLUE-views bullets: it is the step before both, and a heading makes it findable.
- **Claude Desktop is out of scope** (Doug, 2026-10-24): cc-data does not run under it yet; REPORT-89 and REPORT-90 cover it.
- **Walk-through transcripts go in the PR description,** not the repo: a committed transcript looks authoritative while nothing checks it against the guidance.
- **Edits within a document are ordered by the entry key, never by `created`.** The core's entry order is `(idx, entry_id)`, and Scott's inventory has 52 documents whose `created` steps back more than 60 s.
- **`get_history`'s description says it is not CLUE document history,** not that it "holds no CLUE data", which nothing verifies.
