# Implementation Plan: the MCP instructions cap, and CLUE document data in the Claude guidance

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-115
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

Three commits and one verification pass, on `REPORT-115-clue-guidance` over RD-15's cc-data-cli branch `RD-15-raise-display-limit` (cut from `REPORT-152-recent-logs`; RD-15 touches none of the guidance). The first commit lands the cap fix on its own, as the Jira story asks. Every commit leaves `gofmt -l` empty and `go vet ./...` and `go test -count=1 ./...` passing. Run the tests with an isolated `HOME` (`HOME=$(mktemp -d)`): `cmd`'s tests run the root command, whose freshness check rewrites `~/.claude/skills/cc-data/SKILL.md` under the real one.

### The instructions become an index, and a `guide` tool returns the full text

**Summary**: R1 to R7. Splits the two sections the index shares with the core into their own files, renders `Instructions()` as the index, adds `Guide()` and the `guide` tool, and moves every guard that read the full text through `Instructions()` onto `Guide()`. The skill's rendered text does not change.

**Files affected**:
- `internal/guidance/src/datasets.md` (new): `core.md`'s `## Datasets and portals` section, moved verbatim.
- `internal/guidance/src/sensitive.md` (new): `core.md`'s `## Sensitive data` section, moved verbatim.
- `internal/guidance/src/core.md`: loses both sections; now opens at `## Runs and their data` and ends after `## Materializing a dataset`.
- `internal/guidance/src/mcp_header.md`: one sentence added to the opening paragraph.
- `internal/guidance/src/mcp_guide.md` (new): the `## Full guidance` pointer.
- `internal/guidance/src/tools.md`: a `guide` entry.
- `internal/guidance/guidance.go`: the new embeds, `Core()`, `Guide()`, `Instructions()`, doc comments.
- `internal/mcpserver/tools.go`: the `guide` registration (and the `guidance` import); one sentence in `queryDescription()`, and its doc comment, which says every client receives the guidance and should say the per-run view shapes are in the guide text.
- `internal/guidance/guard_test.go`, `internal/mcpserver/server_test.go`, `cmd/guidance_commands_test.go`: R4 and R5, and R7's two test-file comments.

**Estimated diff size**: ~170 lines, most of it the two moved sections.

**The split.** Cut `core.md` at `## Runs and their data` and at `## Sensitive data`. `datasets.md` is the text before the first cut without its final blank line, `sensitive.md` the text from the second cut to the end, and `core.md` what lies between without its final blank line. Each file then ends with exactly one newline, and

```go
func Core() string { return datasets + "\n" + core + "\n" + sensitive }
```

reproduces today's `core.md` byte for byte (checked with throwaway code against `REPORT-152-recent-logs`). Before committing, render `Skill()` and `Core()` on the parent commit and on this one and `cmp` them: both must be identical. That check is throwaway and is not committed, since after this commit there is no old text to compare against.

**`guidance.go`**:

```go
// Package guidance owns the single source of the cc-data data-model prose and renders it
// for each surface that carries it: the Claude Code skill file, the MCP instructions, and
// the MCP guide tool. The instructions are an index short enough for a client to keep whole;
// the guide tool returns the full text.
package guidance

import _ "embed"

//go:embed src/datasets.md
var datasets string

//go:embed src/core.md
var core string

//go:embed src/sensitive.md
var sensitive string

//go:embed src/tools.md
var tools string

//go:embed src/skill_header.md
var skillHeader string

//go:embed src/mcp_header.md
var mcpHeader string

//go:embed src/mcp_guide.md
var mcpGuide string

// Core is the shared data-model prose both surfaces render, and the only prose the drift
// guard reads.
func Core() string { return datasets + "\n" + core + "\n" + sensitive }

// Tools is the tool catalog the guide tool renders after the core.
func Tools() string { return tools }

// Skill is the body cc-data init writes to ~/.claude/skills/cc-data/SKILL.md, before stamping.
func Skill() string { return skillHeader + "\n" + Core() }

// Guide is the full guidance the MCP guide tool returns: the core and the tool catalog.
func Guide() string { return Core() + "\n" + tools }

// Instructions is the MCP server's instructions block: an index of the guidance, short enough
// that a client keeping only the first 4,096 characters of a server's instructions (Claude Code
// does) holds all of it. Most important first: the auth remedy, the pointer to the guide tool
// and the sensitive-data norm sit within the first 2,048, a cap earlier Claude Code builds used,
// and the dataset rules, which the guide repeats, come last. It carries no frontmatter
// and never directs the model to run a shell command or read --help, neither of which an
// MCP-only client can do; relaying a terminal instruction to the user (the auth remedy) is a
// different act.
func Instructions() string {
	return mcpHeader + "\n" + mcpGuide + "\n" + sensitive + "\n" + datasets
}
```

**`mcp_header.md`**: the opening paragraph ends with one more sentence, so the first thing a client reads names the tool:

```markdown
... orient with `dataset_show`; and analyze with `query`. Call cc-data's `guide`
tool before the first fetch or query: these instructions are only an index (see
Full guidance below).
```

**`mcp_guide.md`**:

```markdown
## Full guidance

These instructions are only an index. Before the first fetch or query, call
cc-data's `guide` tool once: it returns the data model, every view with its
columns and traps, the report slugs, the cohort recipes and the tool catalog,
none of which is repeated here. A client that has loaded the cc-data skill
already holds all of it but the tool catalog, whose facts the tool descriptions
carry, and need not call it.
```

**`tools.md`**, a first entry, in the em-dash style of its neighbors:

```markdown
- `guide` — cc-data's full guidance: this text, the data model, every view with
  its columns and traps, the report slugs, the recipes and this tool catalog. The
  server's instructions are an index of it.
```

**`tools.go`**, registered at the top of `registerTools`, beside `version` (go-sdk lists tools sorted by name, so registration order does not change the listing):

```go
addTool(s, &mcp.Tool{Name: "guide", Description: "cc-data's full guidance: the data model, every view with its columns and traps, the report slugs, the cohort recipes, and the tool catalog. The server's instructions are only an index of it, so call this once before the first fetch or query.", Annotations: readOnly},
	func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: guidance.Guide()}}}, nil, nil
	})
```

The inline `struct{}` input matches `version`'s registration. With it and `any` as the output type, go-sdk v1.6.1 lists `inputSchema` `{"type":"object","additionalProperties":false}`, no output schema, and returns one text block, called with no arguments or with `{}` (throwaway check). `queryDescription()` gains, after the views sentence: `"Each view's columns and traps are in cc-data's guide tool: call it before the first query. "`.

**Tests**:

- `guard_test.go`:
  - `TestInstructionsFitTheMCPCap`: `len(guidance.Instructions()) <= instructionsCap`, with `const instructionsCap = 4096` and a comment saying Claude Code keeps only that many characters of a server's instructions and that a byte count is never below the character count. It fails today (31,509) and passes after.
  - `TestInstructionsPutTheEssentialsFirst`: `## Full guidance` and `## Sensitive data` both end within `instructions[:2048]` (each section's last sentence is found there). It fails if the order reverts to datasets first (measured: Sensitive data then starts at byte 2,413 and the pointer at 2,676).
  - `TestGuideFitsAToolResult`: `len(guidance.Guide()) <= 75000`, commented with the 25,000-token default tool-result cap at three bytes per token.
  - `TestInstructionsIndexTheGuide`: the instructions contain `## Datasets and portals`, that section's last sentence as wrapped (`"check the dataset's portal is a\n  hostname"`), `## Sensitive data`, its last sentence as wrapped (`"Suggest purging a\ndataset"`) and "cc-data's `guide` tool", and do not contain `## Views`. It stays in the external `guidance_test` package with the other guards, which is why it matches text rather than the unexported section variables. The last clause is what catches the core being concatenated back in under a raised cap.
  - `TestBothSurfacesCarryTheRePullMechanism`: the MCP half reads `guidance.Guide()` and its error label becomes `guide:`. Its comment's "both surfaces" stays true: the MCP surface is now instructions plus guide.
  - `TestCoreNamesNoCommand`: its comment says the core renders into the guide tool's text rather than the MCP instructions.
  - `TestBothSurfacesCarryTheAuthRemedy` is unchanged and keeps running against `Instructions()`.
- `server_test.go`:
  - `TestMCPInstructionsArriveInInitialize`: the wanted strings become `NOT_AUTHENTICATED`, `auth_status` and `` `guide` ``; `run_membership` is dropped, since the next test's `Contains(Core())` covers it.
  - `TestMCPGuideReturnsTheFullGuidance` (new): call `guide` with no arguments; one `TextContent` block; its text equals `guidance.Guide()` and contains `guidance.Core()` and `guidance.Tools()`, and, as the instructions test checks of the instructions, neither opens with `---` nor contains `--help`. The `Contains` checks are what fail if the handler returns `Instructions()` or the core alone; the `--help` check is what fails if `--help` enters `core.md` or `tools.md`, which no test catches once they leave the instructions.
  - `TestMCPToolSurface`: `guide` joins `want` (so the count rises by one) and the read-only annotation loop.
  - `TestMCPQueryDescriptionCarriesTheQueryRules`: `"cc-data's guide tool: call it before the first query"` joins the wanted strings.
- `cmd/guidance_commands_test.go`: `both` becomes `guidance.Skill() + "\n" + guidance.Instructions() + "\n" + guidance.Guide()` and its comment names the guide.

---

### CLUE routing, `entry_uid`, CLUE UI state, and the two tool descriptions

**Summary**: R8 to R11. Prose only in the core, two tool descriptions, and their pins.

**Files affected**:
- `internal/guidance/src/core.md`: a new subsection, and one addition each to the `logs` and `clue_history` entries.
- `internal/mcpserver/tools.go`: `get_history` and `get_clue_history` descriptions.
- `internal/mcpserver/server_test.go`: `TestToolDescriptionsCarryTheirPortalFacts`.

**Estimated diff size**: ~45 lines.

**R8**, after the cohort recipe's closing line ("Re-read any Portal run later ..."), before `## Identity columns`:

```markdown
### Answering a question about CLUE documents

- A question about what students did in CLUE documents (what they built or
  changed, in what order, how long it took) is answered from the CLUE stores:
  fetch a Student ID Mapping run's CLUE documents, then their history and
  content. A CLUE document's history is its edit history, `clue_history`. The
  `history` store is LARA interactive-state history, a different store written by
  a different application, so never fetch it for a CLUE question.
- `clue_patches` answers what a student changed and in what order,
  `clue_content` what a document holds now, `clue_documents` which documents
  exist and whose they are, and `clue_history` entry-level timing and counts.
  `logs` and `recent_logs` add what the student had on screen between edits (the
  `logs` entry describes CLUE's `extras`).
- Leave runtime output out when the question is about the student (the Working
  with the CLUE views entry lists the patterns). Order a document's edits by the
  entry key, never by a clock, and say which clock a reported time, duration or
  cross-document ordering used: `created` within one document, `server_created`
  across documents.
- Before a dataset is materialized, `clue_patches` unpacks every history entry in
  it that the query does not exclude by comparing an entry-level column
  (`doc_id`, `entry_id`, `idx`, `created`, `server_created`, `action`,
  `is_revert`) with literal values: `doc_id IN ('...', ...)`, a time range, or
  the runtime-output `action` patterns. A join, an `IN (SELECT ...)` or
  `IN (VALUES ...)`, `= ANY([...])`, the per-run scoping in Working with the
  CLUE views, and a filter on `path`, `op`, `value` or `tile_id` narrow the
  result, not the work, so on a large dataset materialize first, or look up the
  document ids and filter by them as literals.
```

It names no command and no hyphenated slug, so `TestCoreNamesNoCommand` and `TestGuidanceDocumentsOnlyRealSlugs` are unaffected; it opens with prose, so no catalog parses it.

**R9**, appended to the `clue_history` entry after "the join never adds or drops an entry.":

```markdown
  `entry_uid` is NULL by construction on a document written without concurrent
  editing, which is every classroom document so far, and is not a data gap.
```

**R10**, appended to the `logs` entry after the `username` paragraph (the entry's last line, "which the `reports` entry above explains and which is not the same as false."):

```markdown
  CLUE events (`application = 'CLUE'`) carry the user's interface state in
  `extras`: `navTabsOpen` and `problemPath` on every event, `selectedNavTab` on
  nearly every one (a sign-in event can lack it), `workspaceMode` on a student's,
  and `group` once the student is in one; `recent_logs` rows carry the same.
  `navTabsOpen` is true for split view and for the curriculum full screen alike,
  so only `false` (the workspace full screen) is unambiguous.
```

Checked on the local `learn.concord.org/dataflow-cohort` dataset (read-only, no network): all 263,570 rows with `application = 'CLUE'` (all student events) have `extras`, `navTabsOpen`, `problemPath`, `tzOffset` and `workspaceMode`; `selectedNavTab` is absent from 2,173 (2,172 of them `INTERNAL_AUTHENTICATED`) and `group` from 3,480; the other 14,860 rows have no `extras`.

**R11**, `tools.go`:

- `get_history` gains: `"It is not CLUE document history: a CLUE document's history is get_clue_history."`
- `get_clue_history` gains, after its existing sentences: `"Per-edit questions read the clue_patches view, not the action column."`

`TestToolDescriptionsCarryTheirPortalFacts` adds `"a CLUE document's history is get_clue_history"` to `get_history`'s facts and `"clue_patches view, not the action column"` to `get_clue_history`'s.

---

### The researcher guide

**Summary**: R12. A CLUE subsection in §6, a CLUE prompt in §3, and one sentence on the MCP server. No guard reads §3 or §6; `TestResearcherGuideDocumentsEveryStaticView` reads §5's table, which is unchanged.

**Files affected**:
- `docs/researcher-guide.md`.

**Estimated diff size**: ~50 lines.

- **§3**, a fourth example prompt after the student-actions one:

  ```markdown
  > "Pull the CLUE documents and their history for Student ID Mapping run 612 into
  > `dataflow`, and show me what each student changed in their Dataflow program,
  > in order."
  ```

- **§3**, at the end of the paragraph that opens "This guide covers **Claude Code**", one sentence: when the skill is not installed, Claude reads the same guidance through the MCP server's `guide` tool, so a session with only the MCP server knows the same views and caveats.

- **§6**, a new subsection after "Across time (longitudinal)":

  ````markdown
  ### About how students built CLUE documents

  These come from a run's CLUE documents, pulled by a Student ID Mapping run's id:
  the documents first, then their history and content (§4 has the commands), then
  `dataset materialize` once the dataset is large. §5 describes the four views.

  - **What changed, and in what order**: every edit a student made to a document,
    one row per change, from `clue_patches`.
  - **How long things took inside a document**: the time between a student's edits.
  - **What a document holds now**: its tiles and their kinds, from `clue_content`.
  - **What the student had on screen while editing**: the CLUE events in the logs
    carry which tab was open and whether the curriculum was showing.

  ```sql
  -- One student's edits, in order, without the program's own output
  SELECT p.doc_id, p.created, p.op, p.path
  FROM clue_patches p JOIN clue_documents d USING (doc_id)
  WHERE d.owner_type = 'user' AND d.uid = '1234'
    AND p.path NOT LIKE '%program/values/%'
    AND p.path NOT LIKE '%demoOutput%'
    AND p.path NOT LIKE '%tickEntries/%'
    AND coalesce(p.action, '') NOT LIKE '%content/step'
    AND coalesce(p.action, '') NOT LIKE '%tickAndProcess'
  ORDER BY p.doc_id, p.idx, p.entry_id, p.rec_idx, p.patch_idx;
  ```

  > Each edit has two times. `created` is the student's device clock, right for
  > how long something took inside one document; `server_created` is the server's,
  > right for comparing documents, since device clocks can be off by about a minute.
  > Ask Claude which one an answer used.
  ````

  The guide's §6 also adds, after the code block's clock note: "On a big dataset that hasn't been materialized, a query like this one unpacks every document's edits before the join narrows them, so materialize first, or ask Claude to filter by the document ids directly."

  The `coalesce` matters: `action` is NULL for an entry with an empty or NULL `action_raw`, and `NULL NOT LIKE ...` is NULL, which would silently drop those patches.

---

### Walk-throughs (no commit)

**Summary**: R13 and R14. Verification on the finished branch; the transcripts go in the PR description (see Decisions). Nothing is committed.

1. **Fixture.** Ask the local-stack terminal on `clue-cc-data` for a seeded document whose history entries carry real patches: a granular edit, a `setProgram` catch-all, `content/step` ticks with `program/values/` paths, and a `remove`. Today's seeds have `records: [{}]`.
2. **Claude Code with the skill.** Build the branch and put a `cc-data` wrapper first on the session's `PATH` that sources `~/tmp/local-stack/cc-data-env.sh` and execs this build, so both the MCP server and the skill's Bash calls get the stack's `HOME`, `CC_DATA_ROOT`, keyring and dead proxy while the `claude` process keeps the real `HOME` (its login and skills) and network. Exporting the stack's environment into `claude` itself would cut it off from both. Install this branch's skill first, and check it is still there when each run ends: every `cc-data` invocation runs `MaybeRefresh`, so the globally registered server (`~/.local/bin/cc-data mcp`, a 0.1.x build that every other Claude Code session starts) and any bare `cc-data` call rewrite a skill whose stamp differs from their own version with that build's older text. Back it up, run this build's `cc-data init --no-mcp` without the stack's environment (it writes only the skill and the `CLAUDE.md` pointer), confirm before and after each `claude -p` that `SKILL.md` still ends with this build's `dev` stamp and names `clue_patches` (rerun the walk-through if not), and restore the backup after step 3. Then run `claude -p "<question>" --verbose --output-format stream-json --strict-mcp-config --mcp-config <file>` once per R14 question, where the file launches the wrapper's `cc-data mcp`: `-p` refuses `stream-json` without `--verbose`, and `--mcp-config` takes several values, so a prompt placed after it is read as a second config path. `--strict-mcp-config` keeps the globally registered cc-data server, which reads the real `~/.config/cc-data`, out of the session. The transcript must show `get_clue_documents` then `get_clue_history`, a `query` on `clue_patches` (not `clue_history.action`), the clock caveat, and no `get_history`.
3. **Claude Code over MCP alone.** The same with `--disable-slash-commands`, which leaves every skill out of the session (confirm in the stream's init event that no cc-data skill is listed), and with `~/.claude/skills/cc-data/` moved aside: `~/.claude/CLAUDE.md` names that file's path, and disabling skills does not stop a `Read` of it. The transcript must also show a `guide` call before the first fetch and no read of a `SKILL.md`.
4. **Cleanup.** Delete every dataset created; leave the servers running.

## Decisions

- **Split files rather than cutting sections out of `core.md` by heading.** Concatenation has no failure mode to test; a renamed heading would silently drop a section from the index.
- **The guide text is `Core()` plus `Tools()`, without the MCP header,** as the Jira acceptance reads: every client that can call `guide` already holds the header and the auth remedy.
- **One commit for the cap fix, guards included.** About 170 lines, mostly moved text; intermediate states would each need their own guard changes.
- **The guidance sources check out with LF on every platform** (departure, Doug, 2026-10-25): `.gitattributes` gains `internal/guidance/src/*.md text eol=lf`. A Windows checkout converts them to CRLF and `go:embed` keeps the carriage returns, which fails `TestInstructionsIndexTheGuide`'s wrapped-sentence matches and adds a byte per line to the sizes the cap tests bound.

## Verification notes

Throwaway builds of all three commits against `REPORT-152-recent-logs` (reverted) were clean under `gofmt -l`, `go vet ./...` and `go test -count=1 ./...`.

- **Sizes:** `Instructions()` 3,121 bytes (from 31,509), with header, Auth, pointer and Sensitive data ending at byte 1,951; `Guide()` 33,041; `Skill()` 34,388; the longest tool description, `query`, 953. `Skill()` and `Core()` render byte-identical after the first commit.
- **Guards:** with the code changes and no test changes, exactly the three predicted tests fail. Pointing the `guide` handler at `Instructions()`, putting the datasets section first, or dropping the `query` sentence each fails its test. A `--help` added to `core.md` is caught only once the guide test checks for it.
- **go-sdk v1.6.1** sorts `tools/list` by name, so registration order does not set the listing.
- **§6's SQL** over REPORT-111's `clueFixture` returns the student's patches in key order with `program/values/x` left out; its `coalesce` keeps patches whose `action` is NULL.
- **`clue_patches` filter pushdown,** by `EXPLAIN ANALYZE` on raw stores (10,000 entries): literal comparisons on entry-level columns narrow the unpacking (`=`, `<>`, `<`, `LIKE` with a leading wildcard, `BETWEEN`, time bounds including `now()`-relative ones, `IN` lists up to 5,000 values, a literal `(doc_id, entry_id)` tuple, `list_contains([...], doc_id)`); `= ANY([...])`, `IN (VALUES ...)`, a join, `IN (SELECT ...)`, the per-run `(doc_id, entry_id) IN (SELECT ...)` and any patch-level filter unpack every entry. Materialized, the Parquet already holds the patches and a join reads only its rows. §6's sample unpacked only its non-runtime entries, because its `action` filters push down.
- **Walk-through flags:** `claude -p` refuses `stream-json` without `--verbose`, and a prompt after `--mcp-config <file>` is read as a second config path (checked offline). Every `cc-data` invocation, the global MCP server's included, rewrites a skill stamped with another version, hence step 2's stamp checks.
