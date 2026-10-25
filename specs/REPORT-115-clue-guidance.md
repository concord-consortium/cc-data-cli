# cc-data: the MCP instructions cap, and CLUE document data in the Claude guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-115

**Status**: **Closed**

## Overview

The MCP server's instructions shrink to an index under Claude Code's 4,096-character cap, and a new `guide` tool returns the full guidance, so a client with only the MCP server sees the views catalog and the traps instead of a text cut off mid-sentence. The guidance then gains the CLUE pieces REPORT-110, REPORT-111 and REPORT-152 did not add: how to route a CLUE question to the right fetch and view, `entry_uid`, and the UI state in CLUE log events.

Researchers ask Claude questions and Claude answers them through cc-data, learning how the data is shaped from one body of guidance delivered two ways: the Claude Code skill, and the instructions an MCP server hands every client when it connects. Claude Code keeps only the first 4,096 characters of those instructions, and cc-data's had grown to about 31,500 bytes, so an MCP-only session saw the opening paragraphs and none of the view descriptions or traps, which is where every warning about CLUE data lives. The CLUE corpus has more traps than most (two things called "history", two clocks, an `action` column that is the wrong unit, program output that reads like student edits), so a researcher asking "what did this student change?" should get an answer from the patch-level view with the clock caveat stated, and never a LARA history pull.

## Requirements

### The MCP instructions cap

- **R1. `Instructions()` is an index of at most 4,096 bytes.** Bytes, not characters: Claude Code counts JavaScript string length, and a Go byte count is never smaller. In order, most important first: the opening paragraph of `mcp_header.md` with cc-data's `guide` tool named as the first call; `## Auth` as it is; a short `## Full guidance` pointer (call cc-data's `guide` once per session before the first fetch or query; a client holding the skill need not); `## Sensitive data`; `## Datasets and portals`, last because `guide` repeats it. Everything but the last section falls within the first 2,048 bytes, a cap earlier Claude Code builds used.
- **R2. One source for every sentence the index shares with the core.** `## Datasets and portals` and `## Sensitive data` are rendered from the same source by both; `Skill()`'s text is unchanged by the restructuring.
- **R3. A `guide` MCP tool.** No arguments, read-only, returning `guidance.Core()` followed by `guidance.Tools()` as one text block. Its description opens with "cc-data's full guidance" and says to call it before the first fetch or query; the description and the index name cc-data wherever they name `guide`, since the bare name is shared with other servers' tools. It is cataloged in `tools.md`.
- **R4. The guards move with the text.** Tests fail when the instructions exceed 4,096 bytes, when `## Full guidance` and `## Sensitive data` do not both end within the first 2,048 bytes, or when `guide` does not return `Core()` and `Tools()` whole. The auth-remedy check still runs on the instructions; the re-pull check reads the guide text; the no-frontmatter and no-`--help` checks apply to both; the command-name guard scans the skill, the instructions and the guide text; `TestMCPToolSurface` lists `guide`.
- **R5. The guide fits a tool result.** A test fails when the guide text exceeds 75,000 bytes: Claude Code's default tool-result cap is 25,000 tokens, 75,000 bytes at a pessimistic three bytes per token.
- **R6. The `query` description points at `guide`,** pinned by `TestMCPQueryDescriptionCarriesTheQueryRules`.
- **R7. Prose that described the old contract is updated:** `guidance.go`'s package, `Instructions()` and `Tools()` comments, `queryDescription()`'s comment, `TestCoreNamesNoCommand`'s comment and the re-pull check's error label.

### The CLUE guidance

All of it lives in `internal/guidance/src/`, worded for both surfaces, so the core still names no command and no Jira story.

- **R8. Routing a CLUE question** (`### Answering a question about CLUE documents`, after the cohort recipe): a question about what students did in CLUE documents is answered from the CLUE stores, fetched by a Student ID Mapping run's id, documents first, and a CLUE document's history is never `history` (LARA interactive-state history); which view answers what, with `logs` and `recent_logs` adding what the student had on screen; leave runtime output out, order a document's edits by the entry key and never by a clock, and say which clock a reported time used (`created` within a document, `server_created` across documents); before materializing, only literal comparisons on entry-level columns narrow the `clue_patches` unpacking, so materialize first on a large dataset or filter by document ids as literals.
- **R9. `entry_uid`** is NULL by construction on documents written without concurrent editing, every classroom document so far, and is not a data gap.
- **R10. CLUE UI state in `extras`:** CLUE events carry `navTabsOpen`, `problemPath`, `selectedNavTab` (a sign-in event can lack it), `workspaceMode` on a student's and `group` once the student is in one, `recent_logs` rows likewise; `navTabsOpen` is true for split view and for the curriculum full screen alike, so only `false` is unambiguous.
- **R11. Tool descriptions:** `get_clue_history` sends per-edit questions to `clue_patches`, not the `action` column; `get_history` says it is not CLUE document history and that a CLUE document's history is `get_clue_history`. Both are pinned in `TestToolDescriptionsCarryTheirPortalFacts`.
- **R12. The researcher guide:** §6 gains "About how students built CLUE documents" (the pull order, the questions the CLUE views answer, one `clue_patches` query ordered by the whole key with runtime paths left out, the two clocks in plain words); §3 gains a CLUE example prompt and a sentence that Claude reads the full guidance through `guide` when the skill is not installed.

### Acceptance

- **R13. Guards:** `gofmt`, `go vet ./...` and `go test -count=1 ./...` pass, with every guidance guard covering the CLUE views, tools and commands and the `guide` tool.
- **R14. Walk-throughs** in Claude Code, with the skill installed and over MCP alone, against the local stack's seeded CLUE document with real Dataflow patches: a CLUE editing question pulls `clue-history` after `clue-documents`, queries `clue_patches` rather than `clue_history.action`, orders by the entry key and states the clock caveat; a "history" question never calls `get_history`; the MCP-only session calls `guide` first. Redacted transcripts go in the PR description.

## Technical Notes

- **The split.** `core.md`'s two shared sections live in `src/datasets.md` and `src/sensitive.md`. `Core()` is `datasets + "\n" + core + "\n" + sensitive`, which reproduced the old `core.md` byte for byte, so `Skill()` and every guard reading `Core()` were unchanged by the cap fix. `Instructions()` is `mcp_header.md`, `mcp_guide.md`, `sensitive.md` and `datasets.md`; `Guide()` is `Core()` plus `tools.md`.
- **Sizes when this closed:** `Instructions()` 3,121 bytes with the essentials ending at byte 1,950, leaving 975 under R1's cap and 97 under its 2,048 mark; the guide text about 33,300 bytes. Anything else that wants to be in the index has to argue its way past R4's tests.
- **The cap, as measured.** Claude Code 2.1.310 cut cc-data's instructions at exactly character 4,096 and appended `… [truncated]`. The limit is `CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH` when set, else 4,096; earlier 2.1 releases are reported at 2,048. A tool description arrives whole, which is why the must-know facts also live in descriptions.
- **The `guide` tool.** Registered with go-sdk v1.6.1's `mcp.AddTool`, an inline `struct{}` input and `any` output: it lists `inputSchema` `{"type":"object","additionalProperties":false}`, no output schema and `readOnlyHint`, and returns one `TextContent` block. go-sdk sorts `tools/list` by name, so registration order does not set the listing.
- **Line endings.** `.gitattributes` keeps `internal/guidance/src/*.md` at LF on every platform. A Windows checkout otherwise converts them to CRLF and `go:embed` keeps the carriage returns, which breaks the index test's wrapped-sentence matches and adds a byte per line to the sizes R1 and R5 bound.
- **The `clue_patches` filter rule,** by `EXPLAIN ANALYZE` on raw stores: literal comparisons on entry-level columns narrow the unpacking (`=`, `<>`, `<`, `LIKE`, `BETWEEN`, time bounds, `IN` lists, a literal `(doc_id, entry_id)` tuple, `list_contains([...], doc_id)`); `= ANY([...])`, `IN (VALUES ...)`, a join, `IN (SELECT ...)`, the per-run scoping and any patch-level filter unpack every entry. Materialized, a join reads only its rows.
- **CLUE `extras`,** checked on a real production CLUE dataset (263,570 CLUE rows, all student events): `navTabsOpen`, `problemPath`, `tzOffset` and `workspaceMode` on every row; `selectedNavTab` absent from 2,173 (almost all sign-in events) and `group` from 3,480. CLUE's logger sets these per event (`collaborative-learning` `src/lib/logger.ts`); `navTabsOpen` is `dividerPosition > kDividerMin`, and the companion `workspaceShown` is not logged.
- **§6's example query.** The `coalesce(p.action, '')` matters: `action` is NULL for an entry with an empty or NULL `action_raw`, and `NULL NOT LIKE ...` would drop those patches. On the seeded document it returned 1,590 of 8,465 patches in key order with no runtime paths left.
- **Running the tests.** `cmd`'s tests run the root command, whose freshness check rewrites `~/.claude/skills/cc-data/SKILL.md`, so run them with `HOME=$(mktemp -d)`; so do the walk-throughs' `cc-data` calls. `claude -p` refuses `stream-json` without `--verbose`, a prompt after `--mcp-config <file>` is read as a second config path, and `--strict-mcp-config` keeps a globally registered cc-data server out of the session.

## Out of Scope

- A `cc-data guide` CLI command: the skill already carries the full text on the CLI side.
- Splitting the guide by section: the whole text is well under the tool-result cap, and R5 is the trip wire.
- Rewriting the CLUE guidance REPORT-110 to REPORT-152 added, beyond R9 and R10's additions.
- Claude Desktop, including whether it applies the same cap: cc-data does not run under it yet; REPORT-89 and REPORT-90 cover it.
- REPORT-192's one-row-per-event view over `logs` and `recent_logs`.
- Any change to the CLUE stores, views or fetch.

## Decisions

- **The index carries Sensitive data, not a partial views catalog:** Sensitive data governs the first summary Claude reads, possibly before it calls `guide`, and a partial catalog would invite queries written without calling `guide`.
- **A byte bound on the guide text (R5):** the tool-result cap is as silent as the instructions cap was; one test turns the next truncation red.
- **The CLUE routing is its own subsection** after the cohort recipe: it is the step before both the recipe and the Working-with-the-CLUE-views notes, and a heading makes it findable.
- **Claude Desktop is out of scope** (Doug, 2026-10-24): cc-data does not run under it yet.
- **Walk-through transcripts go in the PR description,** not the repo: a committed transcript looks authoritative while nothing checks it.
- **Edits within a document are ordered by the entry key, never by `created`:** the core's entry order is `(idx, entry_id)`, and Scott's inventory has 52 documents whose `created` steps back more than 60 s.
- **`get_history`'s description says it is not CLUE document history,** not that it holds no CLUE data, which nothing verifies.
- **Split files rather than cutting sections out of `core.md` by heading:** concatenation has no failure mode to test; a renamed heading would silently drop a section from the index.
- **The guide text is `Core()` plus `Tools()`, without the MCP header:** every client that can call `guide` already holds the header and the auth remedy.
- **One commit for the cap fix, guards included:** intermediate states would each need their own guard changes.
- **The guidance sources check out with LF everywhere** (Doug, 2026-10-25, a departure from the implementation plan): found when PR #20's Windows run failed on CRLF; the embedded guidance is then the same bytes on every OS.
