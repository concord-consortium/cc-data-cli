# Implementation Plan: Single-Source Claude/MCP Guidance and Drift Guard

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-104
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

> The guidance package, the catalog parser, the derived view inventory and the whole guard suite
> were written and run against the real repo before this plan, then deleted. The guard was observed
> **failing** on the researcher guide's genuine drift and then passing once the missing row was
> added, so it is known to be capable of both. See "Verification behind this plan".

## Implementation Plan

### Add the guidance package and its shared source

**Summary**: Create the package that owns the prose, with the core and the per-surface wrappers as
separate embedded files. Nothing consumes it yet, so this step is purely additive and reviewable on
its own.

**Files affected**:
- `internal/guidance/guidance.go` — new
- `internal/guidance/src/core.md` — new: the data-model prose, lifted from `SKILL.md`
- `internal/guidance/src/tools.md` — new: the tool catalog, which has never existed
- `internal/guidance/src/skill_header.md` — new: the Claude Code frontmatter and CLI framing
- `internal/guidance/src/mcp_header.md` — new: the MCP framing and the auth remedy

**Estimated diff size**: ~250 lines, nearly all prose moved rather than written

The package is deliberately dependency-free, so both `internal/claude` and `internal/mcpserver` can
import it without a cycle and neither has to import the other:

```go
// Package guidance owns the single source of the cc-data data-model prose and renders it
// for each surface that carries it: the Claude Code skill file and the MCP instructions.
package guidance

import _ "embed"

//go:embed src/core.md
var core string

//go:embed src/tools.md
var tools string

//go:embed src/skill_header.md
var skillHeader string

//go:embed src/mcp_header.md
var mcpHeader string

// Core is the shared data-model prose both surfaces render, and the only prose the drift
// guard reads.
func Core() string { return core }

// Tools is the tool catalog the MCP surface renders.
func Tools() string { return tools }

// Skill is the body cc-data init writes to ~/.claude/skills/cc-data/SKILL.md, before stamping.
func Skill() string { return skillHeader + "\n" + core }

// Instructions is the MCP server's instructions block. It carries no frontmatter and never
// directs the model to run a shell command or read --help, neither of which an MCP-only client
// can do; relaying a terminal instruction to the user (the auth remedy) is a different act.
func Instructions() string { return mcpHeader + "\n" + core + "\n" + tools }
```

`core.md` is the prose that rots and the prose the guard checks. The two headers are short and
surface-specific, which is what makes retiring the skill later a deletion of `skill_header.md` and
one consumer rather than a migration.

**Content moved, not rewritten**, and every one of `SKILL.md`'s six sections has to be placed
deliberately. Two of them do not belong wholly to either surface, so the partition is by paragraph,
not by section:

| `SKILL.md` section | Goes to | Note |
| --- | --- | --- |
| frontmatter | `skill_header.md` | Claude Code requires it; it is meaningless in `instructions` |
| Orientation | `skill_header.md` | `dataset show`, `dataset list`, ref syntax: all CLI |
| Auth | **split three ways** | the `NOT_AUTHENTICATED` remedy goes to *both* headers, worded per surface, because it is the line REPORT-89 flips to `login(portal)`; the status check goes to both headers in each one's own spelling (`cc-data auth status --check`, `auth_status` with `check=true`); only the per-portal and environment-alias facts go to `core.md`, and not under an Auth heading |
| Fetching data | **split** | the `cc-data get …` commands to `skill_header.md`; the `get_attachments` precondition to that tool's description; the dedup property, the log-run `report_type` values and the epoch seconds-versus-milliseconds guidance to `core.md` |
| Views | `core.md` | verbatim, including the `res_<N>` trap, `TRY_CAST` and the join recipe |
| Multi-dataset | `core.md` | `UNION ALL BY NAME` is data-model guidance; reword `--dataset` for the MCP surface |
| Sensitive data | `core.md` | the no-raw-PII norm applies to both |

The two splits are the point of the table. Assigning "Fetching data" wholesale to the skill would
drop the epoch-units guidance from the MCP surface, which is what stops Claude passing `timestamp`
to `to_timestamp` and getting year 57814.

Auth splits for two reasons that both follow from the render order above, where each surface's
wrapper precedes the core. A remedy divided between the two would state the fix before naming the
condition that triggers it, since the invocation would sit in the wrapper and `NOT_AUTHENTICATED`
in the core; and `cc-data auth status --check` placed in the core would render into the MCP
instructions as a shell command the model cannot run. So every auth *instruction* is wrapper prose,
whole and self-contained, and each rendered surface has exactly one `## Auth` section.

What stays in the core is not auth prose at all: that auth, datasets and data are per portal, that a
portal is always a full hostname, and that an environment alias is accepted where a `portal` selects
a server to read from (`reports_list`, `reports_jobs`) but refused by `dataset_create`. Those are
facts about portals, so they sit with the dataset-ref warning the last step rewrites rather than
under an Auth heading of their own. The cost of the split is one duplicated sentence, the norm
against driving the browser login, which the guard step pins on both surfaces.

What is genuinely new is `tools.md`, because the tool catalog has never existed anywhere.

---

### Derive the static view inventory

**Summary**: Give the guard a code-derived list of view names instead of a hand-written one. Small
and self-contained, and it belongs to `duck` rather than to the guard.

**Files affected**:
- `internal/duck/views.go` — add `StaticViewNames` and `IdentityColumnNames`
- `internal/duck/views_test.go` — new: assert each inventory is non-empty and that the view list
  excludes per-run views

**Estimated diff size**: ~55 lines

```go
// StaticViewNames returns the names of the views every dataset gets, derived by building
// the statement set over an empty manifest so the per-run and per-job views (which come
// from manifest entries) are excluded. Deriving it keeps the drift guard's inventory side
// from becoming a second hand-maintained list.
func StaticViewNames() []string {
	vs := viewSet{m: &dataset.Manifest{}}
	var names []string
	for _, st := range vs.statements() {
		names = append(names, strings.Trim(st.name, `"`))
	}
	return names
}
```

The guard's third inventory, the identity columns, comes from the membership key rather than the
store contract:

```go
// IdentityColumnNames returns the columns that identify a record across the stores, derived
// from the membership key so the drift guard's inventory side stays code-derived. Deliberately
// not contractStoreColumns, which also carries the internal _fetched_at/_run_id bookkeeping.
func IdentityColumnNames() []string {
	names := make([]string, 0, len(membershipColumns))
	for name := range membershipColumns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
```

Verified it returns exactly `history_id`, `question_id`, `remote_endpoint`, `source_key`, with none
of the `_`-prefixed bookkeeping that `contractStoreColumns` carries and that has no business being
required in prose.

Building the statement set over an **empty** manifest is what separates the static views from the
generated ones: `report_<run>`, `answers_<run>`, `history_<run>` and `report_<N>_job_<M>` all come
from manifest entries, so an empty manifest yields the nine static names and nothing else. The names
come back SQL-quoted, hence the `strings.Trim`.

The exclusion assertion needs a fixture that would genuinely produce a per-run view, or it passes
without proving anything. `perDownloadViews` requires `len(dl.Files) > 0 && dl.Columns != nil` for a
report download, so a `Download` carrying `Files` but no `Columns` yields the same nine names as an
empty manifest. Build the comparison fixture with both fields set (it then yields ten, the tenth
being `report_<run>`) so the test is comparing against a manifest the code actually expands.

---

### Add the catalog parser

**Summary**: The half of the guard that reads documentation. Separated from the guard's assertions
because it is the piece with the subtle requirements, and it is worth reviewing on its own.

**Files affected**:
- `internal/guidance/catalog.go` — new
- `internal/guidance/catalog_test.go` — new

**Estimated diff size**: ~215 lines

```go
package guidance

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// leadingNames matches the backticked identifiers that open a catalog entry, in either
// markup the guarded files use: a bulleted list item ("- `x`, `y` — ...") or a markdown
// table row ("| `x`, `y` | ...").
var leadingNames = regexp.MustCompile("^(?:- |\\| )((?:`[a-z0-9_]+`(?:, )?)+)")

// ParseCatalog returns every name documented in the first section whose heading
// contains heading. The section runs to the next heading of any level, and a later
// heading containing heading does not reopen it. A section that is not found, or is
// found empty, is an error: the "every documented name still exists" check would
// otherwise pass against nothing.
func ParseCatalog(body, heading string) ([]string, error) {
	var names []string
	found, in, closed := false, false, false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			closed = closed || in
			in = !closed && strings.Contains(line, heading)
			found = found || in
			continue
		}
		if !in {
			continue
		}
		if m := leadingNames.FindStringSubmatch(line); m != nil {
			for _, tok := range strings.Split(m[1], ", ") {
				names = append(names, strings.Trim(tok, "`"))
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no section heading containing %q", heading)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("section %q documents no names", heading)
	}
	return names, nil
}

// Missing returns the names in want that are absent from have, sorted, so a guard
// failure names the entries to fix rather than reporting that two sets differ. It
// ships here rather than in the guard's test file so the negative-control test
// exercises the same comparison the guard does.
func Missing(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if !set[w] {
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}
```

Four properties are load-bearing and each has a test:

- **It reads catalog sections, not the whole document.** A substring search over `SKILL.md` reports
  the `query` tool as documented because the word appears in prose about `cc-data query`, and would
  do the same for the `reports`, `answers`, `history` and `downloads` views.
- **One regex covers both markup shapes.** A bulleted entry and a markdown table row both put the
  names first behind a fixed prefix, so `^(?:- |\| )` is the whole difference.
- **A missing or empty section is an error.** The "every documented name still exists" direction
  passes vacuously against zero names, so a renamed heading would otherwise switch half the guard
  off silently. The researcher guide is the live risk, since its table sits under a numbered prose
  heading.
- **The first matching section wins.** Matching a heading by substring means a later heading that
  happens to contain the same word would otherwise reopen the section and fold unrelated entries
  into the result. Closing the span for good at the first heading after it keeps the parse pointed
  at one catalog.

`catalog_test.go` asserts each of them, plus the two properties the guarded files exercise but no
unit test would otherwise pin: both markup shapes, and an entry documenting more than one name. Both
are live in the files today (`- \`answers\`, \`history\``, and the guide's
`| \`run_membership\`, \`downloads\``), so a first-identifier-only parse would fail the guard on
introduction, and the test says so rather than leaving the reason to the guard's output.

```go
package guidance

import (
	"reflect"
	"testing"
)

func TestParseCatalogReadsBothMarkupShapes(t *testing.T) {
	bullet := "## Views\n\n- `reports` — report CSV rows\n"
	table := "## Views\n\n| View | What it holds |\n|---|---|\n| `reports` | Report CSV rows |\n"
	for _, body := range []string{bullet, table} {
		got, err := ParseCatalog(body, "Views")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []string{"reports"}) {
			t.Fatalf("got %v", got)
		}
	}
}

func TestParseCatalogReadsEveryNameInAMultiNameEntry(t *testing.T) {
	bullet := "## Views\n\n- `answers`, `history` — the identity-keyed stores\n"
	row := "## Views\n\n| V | W |\n|---|---|\n| `run_membership`, `downloads` | Provenance |\n"
	if got, err := ParseCatalog(bullet, "Views"); err != nil || !reflect.DeepEqual(got, []string{"answers", "history"}) {
		t.Fatalf("bulleted multi-name entry: got %v, %v", got, err)
	}
	if got, err := ParseCatalog(row, "Views"); err != nil || !reflect.DeepEqual(got, []string{"run_membership", "downloads"}) {
		t.Fatalf("multi-name table row: got %v, %v", got, err)
	}
}

// An entry that opens with prose rather than a name is documentation, not a catalog
// entry. This is what keeps the per-run shapes out of the inventory the guard compares.
func TestParseCatalogIgnoresAnEntryThatDoesNotOpenWithAName(t *testing.T) {
	body := "## Views\n\n- `reports` — x\n- Per-run views: `report_<run>`, `answers_<run>`.\n"
	got, err := ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reports"}) {
		t.Fatalf("per-run shapes leaked into the catalog: %v", got)
	}
}

func TestParseCatalogTakesOnlyTheFirstMatchingSection(t *testing.T) {
	body := "## Views\n\n- `reports` — x\n\n## Per-run Views\n\n- `answers_584` — x\n"
	got, err := ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"reports"}) {
		t.Fatalf("a later heading reopened the section: %v", got)
	}
}

func TestParseCatalogFailsOnAMissingSection(t *testing.T) {
	if _, err := ParseCatalog("## Something Else\n\n- `reports` — x\n", "Views"); err == nil {
		t.Fatal("a missing catalog section must be an error, not an empty result")
	}
}

func TestParseCatalogFailsOnAnEmptySection(t *testing.T) {
	if _, err := ParseCatalog("## Views\n\nprose, but no entries\n", "Views"); err == nil {
		t.Fatal("a section documenting no names must be an error")
	}
}

func TestMissingNamesTheAbsentEntries(t *testing.T) {
	if got := Missing([]string{"reports", "answers"}, []string{"reports"}); !reflect.DeepEqual(got, []string{"answers"}) {
		t.Fatalf("got %v", got)
	}
	if got := Missing([]string{"reports"}, []string{"reports"}); got != nil {
		t.Fatalf("nothing missing should report nothing, got %v", got)
	}
}
```

---

### Wire the guidance into both surfaces

**Summary**: Make the skill writer and the MCP server render from the package. This is the step that
makes the single source real; it is deliberately after the package exists and before the guard turns
on, so each commit stands alone.

**Files affected**:
- `internal/claude/skill.go` — render `guidance.Skill()` instead of the embedded `SKILL.md`
- `internal/claude/skill/SKILL.md` — deleted, its content now living in `internal/guidance/src/`
- `internal/mcpserver/server.go` — pass `Instructions`
- `internal/mcpserver/server_test.go` — assert the instructions arrive

**Estimated diff size**: ~60 lines

```go
// internal/mcpserver/server.go
func NewServer(opts Options) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "cc-data", Version: opts.Version},
		&mcp.ServerOptions{Instructions: guidance.Instructions()},
	)
	registerTools(s, opts)
	return s
}
```

`skill.go` keeps its version stamp and its `MaybeRefresh` freshness check untouched; only the source
of `skillBody` changes, so `stampedContent/1` and the installed-file behavior are unaffected and the
existing skill tests keep passing.

The acceptance test reads the value back over the transport the repo's tests already use:

```go
func TestMCPInstructionsArriveInInitialize(t *testing.T) {
	setupEnv(t)
	cs := connect(t)
	got := cs.InitializeResult().Instructions
	if got == "" {
		t.Fatal("no instructions in the initialize response")
	}
	if !strings.Contains(got, "run_membership") {
		t.Fatal("instructions do not carry the data model")
	}
}
```

---

### Turn on the drift guard

**Summary**: The guard itself, plus the one-row backfill it needs to pass on introduction.

**Files affected**:
- `internal/guidance/guard_test.go` — new
- `docs/researcher-guide.md` — add the missing `attachment_states` row

**Estimated diff size**: ~125 lines

```go
package guidance_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/guidance"
	"github.com/concord-consortium/cc-data-cli/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registeredTools(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	srv := mcpserver.NewServer(mcpserver.Options{Version: "guard"})
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "guard", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestGuidanceDocumentsEveryStaticView(t *testing.T) {
	views := duck.StaticViewNames()
	if len(views) == 0 {
		t.Fatal("no static views enumerated; the inventory side of the guard is broken")
	}
	documented, err := guidance.ParseCatalog(guidance.Core(), "Views")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(views, documented); len(m) > 0 {
		t.Fatalf("views registered but not documented: %v", m)
	}
	if m := guidance.Missing(documented, views); len(m) > 0 {
		t.Fatalf("views documented but no longer registered: %v", m)
	}
}

func TestGuidanceDocumentsEveryTool(t *testing.T) {
	tools := registeredTools(t)
	if len(tools) == 0 {
		t.Fatal("no tools enumerated; the inventory side of the guard is broken")
	}
	documented, err := guidance.ParseCatalog(guidance.Tools(), "Tools")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(tools, documented); len(m) > 0 {
		t.Fatalf("tools registered but not documented: %v", m)
	}
	if m := guidance.Missing(documented, tools); len(m) > 0 {
		t.Fatalf("tools documented but no longer registered: %v", m)
	}
}

func TestGuidanceDocumentsEveryIdentityColumn(t *testing.T) {
	cols := duck.IdentityColumnNames()
	if len(cols) == 0 {
		t.Fatal("no identity columns enumerated; the inventory side of the guard is broken")
	}
	// Identity columns are described in sentences, not catalog entries, so this half matches a
	// backticked name anywhere in the core prose. That is safe here and nowhere else: unlike
	// `query` or `reports`, none of these four is an ordinary English word.
	body := guidance.Core()
	var undocumented []string
	for _, col := range cols {
		if !strings.Contains(body, "`"+col+"`") {
			undocumented = append(undocumented, col)
		}
	}
	if len(undocumented) > 0 {
		t.Fatalf("identity columns registered but not documented: %v", undocumented)
	}
}

// The auth remedy is the one piece of guidance that exists on both surfaces, worded
// differently on each, so no inventory comparison can notice a surface losing it.
// Matched without its leading capital, so rewording around the rule does not fail.
func TestBothSurfacesCarryTheAuthRemedy(t *testing.T) {
	for surface, body := range map[string]string{"skill": guidance.Skill(), "instructions": guidance.Instructions()} {
		if !strings.Contains(body, "NOT_AUTHENTICATED") {
			t.Errorf("%s: no NOT_AUTHENTICATED remedy", surface)
		}
		if !strings.Contains(body, "drive the browser login") {
			t.Errorf("%s: the browser-login norm is missing", surface)
		}
	}
}

// The core renders into the MCP instructions, where the model has tools and no shell, so
// a command written there would be an instruction it cannot carry out. Command spellings
// belong in the per-surface wrappers.
func TestCoreNamesNoCommand(t *testing.T) {
	if strings.Contains(guidance.Core(), "`cc-data ") {
		t.Error("the core names a cc-data command; move it to skill_header.md")
	}
}

func TestResearcherGuideDocumentsEveryStaticView(t *testing.T) {
	body, err := os.ReadFile("../../docs/researcher-guide.md")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := guidance.ParseCatalog(string(body), "How datasets are organized")
	if err != nil {
		t.Fatal(err)
	}
	if m := guidance.Missing(duck.StaticViewNames(), documented); len(m) > 0 {
		t.Fatalf("views missing from the researcher guide's table: %v", m)
	}
	if m := guidance.Missing(documented, duck.StaticViewNames()); len(m) > 0 {
		t.Fatalf("researcher guide's table lists views that no longer exist: %v", m)
	}
}

// The guard must be able to fail, not merely observed passing. Both halves it drives
// are shipped code: gut either ParseCatalog or Missing and this goes red.
func TestGuardDetectsAnUndocumentedName(t *testing.T) {
	body := "## Views\n\n- `reports` — only this one\n"
	documented, err := guidance.ParseCatalog(body, "Views")
	if err != nil {
		t.Fatal(err)
	}
	m := guidance.Missing([]string{"reports", "answers"}, documented)
	if len(m) != 1 || m[0] != "answers" {
		t.Fatalf("guard did not report the missing name, got %v", m)
	}
}
```

The parser's own properties are asserted in `catalog_test.go`, next to the parser. What lives here is
the guard: three inventory comparisons and the negative control for the comparison itself.

The backfill is one row, because the view side was already complete and the tool side is supplied by
this story's own `tools.md`:

```markdown
| `attachment_states` | The saved CODAP/SageModeler state the current answer points at. |
```

**The guard is proved able to fail**, not merely observed passing.
`TestGuardDetectsAnUndocumentedName` drives the comparison against a body with a known gap and
asserts it names the entry that is missing, rather than asserting a boolean; the parser's own
failure modes are covered next door in `catalog_test.go`. Both halves it drives are shipped code:
stub `ParseCatalog` to `return nil, nil` or `Missing` to `return nil` and it goes red. That is why
`Missing` is exported from the package instead of living beside the assertions, where a deleted
guard would have taken its own negative control with it.

`TestMCPToolSurface`'s hardcoded `want` list is left alone. It is the specification of the intended
surface checked against the registration, and its exact-count assertion catches a tool exposed by
accident; the guard is the opposite direction and the two coexist.

---

### Rewrite the tool descriptions

**Summary**: The description edits, kept last because they are the step most likely to attract
wording review and should not hold up the substrate.

**Files affected**:
- `internal/mcpserver/tools.go` — descriptions, and `dataset_create`'s arguments
- `internal/mcpserver/types.go` — `datasetCreateIn` gains `Portal` and `Name`
- `internal/guidance/src/core.md` — the dataset-ref warning, rewritten for the new argument shape
- `internal/mcpserver/server_test.go` — cover the argument change, including the explicit portal,
  the omitted-portal fallback, an environment alias as `portal`, and a slash in `name`

**Estimated diff size**: ~105 lines

`dataset_create` takes `portal` and `name` separately, with `portal` optional:

```go
type datasetCreateIn struct {
	Portal      string `json:"portal,omitempty" jsonschema:"the dataset's portal as a hostname; environment aliases are not accepted here. Omit to use the configured default portal."`
	Name        string `json:"name" jsonschema:"the dataset name"`
	Description string `json:"description,omitempty"`
}
```

and the handler joins them before the existing parser, so the default-portal fallback and every
existing validation stay exactly where they are:

```go
if strings.Contains(in.Name, "/") {
	return nil, nil, fmt.Errorf("dataset name %q must not contain a slash; pass the portal in the portal argument", in.Name)
}
ref := in.Name
if in.Portal != "" {
	ref = in.Portal + "/" + in.Name
}
parsed, err := dataset.ParseRefForConfig(cfg, ref)
```

The slash check is about which of two spellings a call means, not about safety. `splitRef` splits at
the first slash, so a slashed `name` under an explicit `portal` already fails `ValidateName`, but it
fails reporting `wildfire/x` as a bad name rather than telling the model which argument the portal
belongs in. With `portal` omitted it does not fail at all: the name is re-split as a ref and the call
quietly succeeds, which leaves `<portal>/<name>` alive as a second spelling inside the schema the
model reads every session. That is the deprecated-argument rot the requirements' decision A rejected,
so the check refuses both cases with one message that names the argument to use.

The hostname-only note in the description is not decoration: `portal` on `reports_list` and
`reports_jobs` accepts an environment alias and this one refuses it, so the same argument name means
two different things across the surface. `core.md`'s existing warning about this is rewritten too,
since it currently explains the rule in terms of a `staging/wildfire` ref spelling that no longer
exists.

The other descriptions: `get_attachments` states that answers or history must be fetched first;
`query` echoes `TRY_CAST` on VARCHAR answer columns and `UNION ALL BY NAME`, and generates its view
list from `duck.StaticViewNames()` rather than restating it; `dataset_delete` and `dataset_purge`
say what is destroyed and that it is not recoverable.

The view list is generated from the registration rather than from `guidance.Core()`'s catalog, which
the requirement's "the same catalog the guidance renders" would also allow. Both avoid the third
hand-written copy, which is what the requirement is protecting; the registration wins on two counts.
It names the views a query can actually run against rather than the ones the prose claims, and it
has no error path, where parsing the catalog at registration time would need a silent fallback that
could ship a description with its view list missing. The guard already pins the two sets equal, so
the choice costs nothing in drift.

## Open Questions

None. The requirements resolved every design question, and the two that carried implementation risk
(whether one parser really covers both markup shapes, and whether the guard can be made to fail)
were run rather than assumed.

## Verification behind this plan

All of the following was written into the real repo, run, and then reverted:

- **The guard passes and fails for the right reasons.** With `core.md` and `tools.md` in place, the
  view guard and the tool guard both pass, and the researcher-guide guard **failed** with
  `views missing from the researcher guide's table: [attachment_states]` — the genuine drift, found
  by the guard rather than by a human. Adding the one row turned it green. A guard that has only
  ever been seen passing has not been shown to work; this one has been seen doing both.
- **One regex handles both markup shapes**, confirmed against the real files: nine views from
  `SKILL.md`'s bulleted Views section and, after the backfill, nine from the researcher guide's
  markdown table, including the entries that document two names at once.
- **`StaticViewNames` returns exactly the nine static views** over an empty manifest, with no per-run
  or per-job names, so the guard's inventory side is genuinely derived.
- **The seventeen registered tools come from `ListTools`**, the real registration, so the tool guard
  cannot pass by agreeing with a stale list.
- **The package composes without a cycle**: `internal/guidance` imports nothing from the repo, and
  `internal/duck` and `internal/mcpserver` are imported only by the guard's external test package.
  `go build ./...` and the full suite stay green.

The corrections made after that run were re-probed the same way, again written into the repo, run,
and deleted:

- **The parser as it now stands**, including first-match-wins and `Missing`, against the real files:
  nine views from `SKILL.md`, eight from the researcher guide with `attachment_states` the one
  absent, an error for the Tools section that does not exist yet, and both multi-name shapes.
- **`catalog_test.go` was extracted verbatim from this document** along with `catalog.go` and run.
  All seven tests pass, so the two code blocks above are compilable and self-consistent.
- **`StaticViewNames` over an empty manifest returns the nine**, and over a manifest carrying a
  report download with both `Files` and `Columns` returns ten, the tenth being `report_584`. The
  exclusion is therefore observed rather than assumed, which is what the fixture note above is for.
- **`IdentityColumnNames` returns the four**, `ListTools` returns the seventeen, and
  `ServerOptions.Instructions` round-trips byte-identical through `InitializeResult()`.
- **The `dataset_create` prologue across seven cases**: explicit portal, the default-portal fallback,
  no default configured, an environment alias, a slash in `name` with and without a portal, and an
  invalid name. Each produced the message this plan says it does.

Two things remain unexecuted because they do not exist until the prose is written: the wrapper split
of the auth section, and the guard assertion that pins the remedy on both surfaces.

## Self-Review

Multi-role review of this plan, run after it was written. Roles: Commit Reviewer, Test Engineer,
Technical Writer, Senior Go Engineer. Every issue was checked against the current *and* the proposed
code, which here meant assembling the proposed package in the real repo and running the existing
suite against it. All three below survived and are fixed above.

### Commit Reviewer

#### RESOLVED: step one defined `Instructions()` twice and referenced undeclared embeds
The step showed the package, then said "the real renderers add the surface wrappers" and showed a
second block redefining `Instructions()` with a different body. Taken literally the step does not
compile: two definitions of one function, and the second block's `skillHeader` and `mcpHeader` are
never declared, because the first block embeds only `core.md` and `tools.md`.

The first block was a simplification carried over from the throwaway, which had no wrappers; the
second is what the plan actually intends. Presenting both as the code leaves the implementer to
guess which is real. **Resolution**: one block, with all four embeds and the final bodies.

### Technical Writer

#### RESOLVED: the content partition accounted for four of `SKILL.md`'s six sections
The plan said `core.md` takes the Views section and the sensitive-data norm, and `skill_header.md`
takes the frontmatter, Orientation and "the CLI command list". `SKILL.md` has six `##` sections, and
that sentence places four of them. Verified by listing them: Orientation, Auth, Fetching data, Views,
Multi-dataset, Sensitive data.

Worse than the omission is that one of the sections it *does* name cannot be assigned wholesale.
"Fetching data" contains the `cc-data get …` commands, which are CLI-only; the `get_attachments`
precondition, which the requirements say belongs in that tool's description; and the log-run
`report_type` values plus the epoch seconds-versus-milliseconds guidance, which is pure data model.
Sending the whole section to the skill would drop from the MCP surface the guidance that stops
Claude passing `timestamp` to `to_timestamp` and getting year 57814 — a silent, plausible-looking
wrong answer, on the surface that has no other source.

Auth is the other unplaced one, and it is unplaceable as a unit for a different reason: the
`NOT_AUTHENTICATED` remedy has to exist on both surfaces while being worded differently on each,
and it is the single line REPORT-89 flips to `login(portal)`.

**Resolution**: the partition is now a table covering all six sections, with Auth and Fetching data
marked as splits and the reason for each split stated.

### Test Engineer

#### RESOLVED: the researcher-guide guard checked only one direction
`requirements.md` says the guard fails "in both directions: a registered name missing from the
guidance, and a name the guidance references that no longer exists." The view and tool guards do
both. The researcher-guide guard asserted only that every registered view appears in the table, so a
row naming a view that had been removed would sit there indefinitely — the same rot in the same file
the guard was added to protect. **Resolution**: the reverse assertion is added, with its own message.

### Checked and cleared

- **The proposed wiring composes.** The guidance package was assembled in the real repo with all
  four embedded files, `internal/claude/skill.go` was switched from its `//go:embed skill/SKILL.md`
  to `guidance.Skill()`, and the existing `internal/claude` suite passed unchanged, including the
  assertions that the written file carries the version stamp and the `name: cc-data` frontmatter.
  The frontmatter stays at line 1 because `Skill()` puts the header first, and `stampedContent/1`
  only appends.
- **No cycle, and nothing else references the embedded file.** `internal/guidance` imports nothing
  from the repo. The only references to `SKILL.md` outside the spec are the embed directive itself
  (removed in the same step) and destination paths in `pointer.go` and `skill_test.go`, which are
  about where the file is written, not where its content comes from.
- **`StaticViewNames()` needs no `canonDir`.** Building the statement set over an empty manifest only
  produces SQL strings and never touches disk, so the zero-valued `viewSet` is safe.
