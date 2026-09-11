# Implementation Plan: Dataset materialization (Parquet query surface)

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-113
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Shape of the change

Three facts, all verified against the running engine, set the shape and remove most of the design freedom:

- **`COPY TO` works under the fully locked sandbox** and is refused for a path outside the allowlist. So materialization reuses the ordinary `duck.Open` path rather than needing a privileged writer, and the sandbox keeps constraining where it can write. Nothing about the trust boundary changes.
- **`COPY (SELECT * FROM "<view>")` preserves the view's exact column set and types**, JSON included, for both store-backed and CSV-union views. So materialization needs no access to how a view's SQL was built. It creates the views the normal way and copies them out by name, which means no refactor of `views.go`'s statement construction.
- **`internal/duck` imports `internal/dataset`, never the reverse.** Materialization therefore lives in `duck`, the manifest types live in `dataset`, and `reindex` cannot read a Parquet.

The engine's registration loop gains one level. Today it tries `primary`, then `fallback`. It becomes: try the materialized statement if there is one, then `primary`, then `fallback`. Every existing view keeps its exact behavior when nothing is materialized, which is what keeps this change small.

---

## Register derived subfolders and make purge remove them

**Summary**: Adds the named predicate the requirements ask for and fixes the verified purge gap. Independent of everything else, so it lands first and alone.

**Files affected**:
- `internal/dataset/dataset.go`: the predicate, the folder name, `deleteArtifacts`
- `internal/dataset/dataset_test.go`: purge coverage
- `internal/dataset/reindex_test.go`: the invisibility contract

**Estimated diff size**: ~130 lines

```go
// MaterializedDir is the derived subfolder holding a dataset's Parquet query surface.
const MaterializedDir = "materialized"

// derivedSubdirs are subfolders holding data cc-data generated from the dataset's own
// artifacts. They are never adopted as artifacts, never reported as orphans, and are
// removed wholesale by purge and delete. REPORT-89 registers its exports folder here.
var derivedSubdirs = map[string]bool{MaterializedDir: true}

// IsDerivedSubdir reports whether a directory name inside a dataset holds derived data.
func IsDerivedSubdir(name string) bool { return derivedSubdirs[name] }
```

In `deleteArtifacts`, the directory arm currently removes `segments` and `attachments` by literal name; it gains `|| IsDerivedSubdir(name)`. `Purge`'s other half, clearing `m.Materialized`, waits for the step that adds that field; putting it here would not compile.

Reindex is deliberately **not** added to the predicate's contract. It drops the manifest entries but leaves the files, because `materialized/<view>.parquet` is this story's deliverable to tools outside cc-data and stays correct for them; cc-data losing confidence in a file is not grounds for cc-data to remove it. The same reasoning covers REPORT-89's `exports/`, which is even more clearly a product than a cache.

Tests are the point of this step, because the invisibility properties currently hold by accident:

- Purge removes a populated `materialized/`. This fails before the change (verified: all three planted files survived a purge).
- Delete removes it too. This needs **no code**: `Delete` renames the whole dataset directory to a tombstone and `RemoveAll`s it (`internal/dataset/dataset.go:179`), so a subfolder goes with it. The test exists because the requirement names delete, and because it can fail: it goes red the moment someone rewrites `Delete` to remove known children selectively, which is how `Purge` came to have this gap in the first place.
- A file planted in `materialized/` that *would* be adopted at the top level (`answers.v99.jsonl`, `report_4242.csv`, `members_answers_5.v9.jsonl`) is not adopted by `Reindex` and raises no `ORPHAN_FILE` warning. This passes before the change; it is written to hold the contract against a future change that makes either walk subdirectories.

---

## Record materialization in the manifest

**Summary**: The manifest types and the fingerprint, with no producer or consumer yet. Splitting it out keeps the engine and writer steps reviewable on their own.

**Files affected**:
- `internal/dataset/manifest.go`: the `Materialized` entry and its map
- `internal/dataset/dataset.go`: `Purge` clearing `m.Materialized`
- `internal/dataset/fingerprint.go` (new): fingerprint computation
- `internal/dataset/fingerprint_test.go` (new), `internal/dataset/dataset_test.go`

**Estimated diff size**: ~160 lines

```go
// Materialized records one view's Parquet and the inputs it was built from. Freshness is
// per input rather than per store version because report CSVs have no version: report_<run>.csv
// is a fixed name overwritten on re-fetch.
type Materialized struct {
	File    string            `json:"file"`             // dataset-relative, e.g. materialized/logs.parquet
	Inputs  map[string]string `json:"inputs"`           // dataset-relative input path -> fingerprint
	BuiltAt time.Time         `json:"built_at"`
}
```

`Manifest` gains `Materialized map[string]Materialized \`json:"materialized,omitempty"\`` keyed by view name, and `ensureMaps` initializes it. `Purge` clears it alongside the four maps it already resets, in the same step because that is where the field appears: committing a manifest that still names files the same call is about to delete is what `Purge`'s own commit-ordering comment forbids. No manifest version bump: `decodeManifest` already tolerates an absent field, and an older binary reading a newer manifest ignores the key rather than failing.

```go
// Fingerprint identifies a file's content cheaply, as size and modification time. This is what
// Go's own build cache uses. It cannot distinguish a rewrite that preserved both, which a real
// re-fetch does not do; a content hash would be exact and would cost a full read of every input,
// which at 15 GB defeats the purpose of materializing.
// FingerprintAbsent is what a declared input that is not on disk records. It cannot collide
// with a real fingerprint, which is always <size>-<mtime>. A union view keeps declaring a CSV
// that has gone missing (internal/duck/views.go:218 appends dl.Files whether or not the file is
// there), so this is a normal state rather than an error, and Fresh must be able to tell "absent
// then and absent now" from "absent then, present now".
const FingerprintAbsent = "absent"

func Fingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return FingerprintAbsent
	}
	return fmt.Sprintf("%d-%d", fi.Size(), fi.ModTime().UnixNano())
}

// FingerprintInputs fingerprints a view's declared files, deduplicated, in the form
// Materialized.Inputs records and Fresh compares against. The writer records through it rather
// than building the map itself, so "how an input is recorded" has one definition.
func FingerprintInputs(dir string, files []string) map[string]string

// Fresh reports whether the Parquet may be read: the view's current inputs are exactly the
// recorded ones, and each still fingerprints the same. The caller supplies the current list
// because only the engine knows which files a view reads.
//
// Comparing the set, not just the recorded entries, is load-bearing. A run downloaded since the
// materialization adds a file to a union view, and a Parquet built without it is stale even
// though every recorded input still matches perfectly.
func (m Materialized) Fresh(dir string, current []string) bool
```

Tests: a fingerprint changes when content is rewritten and when only the mtime moves; `Fresh` is true unchanged, false after a rewrite, false when an input is deleted, false when an input is added, and false when the view stops declaring one of the recorded inputs. That last one is what the set comparison is for, and it is the only assertion that fails if the comparison is dropped: an added input is already caught by the lookup miss. And the absent round trip, verified ahead of implementation against the real view set: materialize a `reports` view one of whose CSVs is missing, and `Fresh` is true immediately afterwards and false the moment the CSV is restored from a backup.

---

## Teach the engine to prefer a fresh materialized view

**Summary**: The read path. After this step a hand-placed Parquet is used, which is what makes the step testable before a writer exists.

**Files affected**:
- `internal/duck/views.go`: `viewStmt.materialized`, the resolver, `MaterializableViews(m)`
- `internal/duck/engine.go`: three-level registration
- `internal/duck/materialized_test.go` (new)

**Estimated diff size**: ~210 lines

`viewStmt` gains one field, and `viewSet` gains the resolved map:

```go
type viewStmt struct {
	name         string
	materialized string // optional read_parquet form, tried before primary
	primary      string
	fallback     string
	files        []string
}
```

`statements()` is unchanged. A single post-pass applies materialization, which keeps every builder untouched:

```go
// applyMaterialized points a view at its Parquet when the manifest records one that is fresh.
// Membership of the materializable set is decided by materializableFrom over these same
// statements, so the writer and the reader can never disagree about which views have a Parquet.
//
// There is deliberately no presence check. A Parquet that is absent, corrupt or truncated all
// fail at CREATE VIEW rather than at query time, so the registration loop's warning covers every
// shape a bad file takes, and a stat per view per Open would buy only a differently worded
// message. A stale entry is silent by contract; an unreadable one is not.
func (vs viewSet) applyMaterialized(stmts []viewStmt) []viewStmt {
	if len(vs.m.Materialized) == 0 {
		return stmts // the common case, and it skips building the eligible set
	}
	eligible := map[string]bool{}
	for _, n := range materializableFrom(stmts, vs.prefix) {
		eligible[n] = true
	}
	for i, st := range stmts {
		bare := bareViewName(st.name, vs.prefix)
		if !eligible[bare] {
			continue
		}
		mat, ok := vs.m.Materialized[bare]
		if !ok || !mat.Fresh(vs.canonDir, st.files) {
			continue
		}
		stmts[i].materialized = fmt.Sprintf("CREATE VIEW %s AS SELECT * FROM read_parquet(%s)",
			st.name, vs.file(mat.File))
	}
	return stmts
}
```

`materializableFrom` takes the statements rather than the manifest because `applyMaterialized`
already holds them; the exported `MaterializableViews(m)` builds them once and delegates, so
`Open` never builds the view set twice. It takes the prefix with them, because a statement built
for a named dataset carries one (`"alpha"."answers"`) while the set is keyed on the bare name
`StaticViewNames` produces. Both callers go through `bareViewName` so there is one answer to what
a statement's view is called, rather than two that can disagree about the prefix.

Registration in `Open` becomes an ordered attempt. The distinct warning matters: a materialized copy that fails is a different event from a view degrading to empty, and conflating them would hide a corrupt Parquet behind a message about missing artifacts.

```go
for _, stmt := range vs.statements() {
	if stmt.materialized != "" {
		if _, err := conn.ExecContext(ctx, stmt.materialized); err == nil {
			continue
		} else {
			fmt.Fprintf(warnOut, "warning: materialized %s is unreadable (%v); reading the raw artifacts instead\n", stmt.name, err)
		}
	}
	// existing primary-then-fallback, unchanged
}
```

```go
// MaterializableViews returns the views of this dataset that can be materialized: those that
// scan files, minus the per-run views. Both halves are code-derived, so a view added later
// joins or stays out of the set by its own construction rather than by a list someone updates.
//
// It takes the manifest because "declares files" is a property of a populated dataset: over an
// empty manifest every builder returns its stand-in and no view declares anything.
func MaterializableViews(m *dataset.Manifest) []string {
	vs := viewSet{m: m}
	return materializableFrom(vs.statements(), vs.prefix)
}

// materializableFrom takes statements the caller has already built, so Open does not build the
// full view set twice: applyMaterialized has the slice in hand and passes it straight through.
// Those statements carry the schema prefix of whichever dataset they were built for, so the
// caller passes it too and the set stays keyed on bare names.
func materializableFrom(stmts []viewStmt, prefix string) []string {
	static := map[string]bool{}
	for _, n := range StaticViewNames() {
		static[n] = true
	}
	var out []string
	for _, st := range stmts {
		bare := bareViewName(st.name, prefix)
		if len(st.files) > 0 && static[bare] {
			out = append(out, bare)
		}
	}
	return out
}

// bareViewName is a statement's view name with its schema prefix and quoting removed. That is
// the form StaticViewNames produces and the form the manifest's Materialized map is keyed on,
// so every lookup against either has to go through it.
func bareViewName(name, prefix string) string {
	return strings.Trim(strings.TrimPrefix(name, prefix), `"`)
}
```

The writer and the tests both consume the exported form rather than either hand-listing views. Verified against a populated fixture: the set comes out as `answers`, `logs`, `report_prompts`, `reports`, `run_membership`, `student_id_mapping`, `student_metadata`, plus `history` once that store exists, and the four VALUES- and `read_text`-backed views are excluded by declaring no files, exactly as the requirements state.

Tests, all driven by materializing a Parquet that carries one sentinel row the raw artifacts do not, so "the Parquet is the source" is proved rather than assumed:

- The sentinel is returned when the entry is fresh, and not returned when it is stale, when the Parquet is deleted, or when it is corrupt. Going stale is silent; a deleted, corrupt or truncated Parquet must warn and read the raw artifacts, never degrade to empty. All three unreadable shapes fail at `CREATE VIEW`, verified ahead of implementation, which is what lets one warning cover them.
- **The sentinel survives `dataset rename`.** Verified ahead of implementation: materialize, rename the dataset, re-open, sentinel still returned. This is the property whose absence disqualified the persistent DuckDB, so it is pinned rather than assumed.
- **Multi-dataset resolution.** Two datasets opened together, one materialized: `"alpha"."answers"` returns the sentinel and `"beta"."answers"` does not. Verified ahead of implementation. This is the test that covers `bareViewName`, and it has to be a *named*-dataset test to cover it at all: the prefix is empty in every single-dataset session, so a mistake in trimming it off is invisible to every other test in this plan, and it costs the whole feature rather than one view, since `materializableFrom` returning an empty set means no view is ever pointed at its Parquet.
- `MaterializableViews(m)` over a populated fixture matches the expected names, so a view added later that declares files is noticed here.

---

## Add the materialize writer

**Summary**: The producer. Opens the dataset with materialization suppressed, copies each materializable view out, and repoints the manifest.

**Files affected**:
- `internal/duck/materialize.go` (new)
- `internal/duck/engine.go`: `OpenRaw`, the unexported `exec`, and the degraded-view set `OpenRaw` reports
- `internal/dataset/dataset.go`: `lockMutation` exported as `LockMutation`
- `internal/duck/materialize_test.go` (new)

**Estimated diff size**: ~380 lines

```go
type MaterializeOptions struct {
	Force        bool
	AllowPartial bool
	Progress     func(view string, i, n int)
}

type MaterializeResult struct {
	Written []string          // views materialized
	Skipped []string          // views already fresh (no --force)
	Refused map[string]string // view -> why it was not materialized
}

func Materialize(ctx context.Context, d *dataset.Dataset, opts MaterializeOptions, warnOut io.Writer) (MaterializeResult, error)
```

The order is load-bearing and mirrors `mergeUnderLock`'s discipline, for the reason the requirements give: the copy must not hold the mutation locks, or a concurrent `get` fails as busy for its whole duration.

1. Read the manifest under the per-dataset lock, then release. Warn here, without refusing, for every `!Complete` download feeding a materializable view, naming the runs: only the store fetch can set that flag and it leaves the materialized bytes unchanged, so there is nothing to gate. The view-to-download mapping is three rules, per the requirements: file intersection for report-backed views, download `Type` for `answers` and `history`, and `Type` again for `run_membership`, since an incomplete run has no membership file for a per-run mapping to find.
2. Open an engine over the dataset with materialization suppressed, so a view is always copied from its raw artifacts and a stale Parquet can never be copied forward into a fresh-looking one. `Open` has three non-test callers (`cmd/query.go:60`, `cmd/repl.go:34`, `internal/mcpserver/tools.go:375`), so rather than changing its signature at all four sites this is a sibling `OpenRaw` sharing one unexported constructor with `Open`.
3. Per view, in order: refuse it when `OpenRaw` registered it from its `fallback` statement, which is total loss and has no override; refuse it when it declares an input that is not on disk and `--allow-partial` was not passed, naming the files; skip it when the manifest's entry is fresh and `--force` was not passed. Otherwise `COPY (SELECT * FROM "<view>") TO '<tmp>' (FORMAT parquet, COMPRESSION zstd, KV_METADATA {...})` into a temp name in `materialized/`, and stop there. The temp name comes from `os.CreateTemp` with a `<view>.parquet.tmp-*` pattern rather than from the pid: the copies deliberately run outside the mutation locks, so one process can have two runs in flight over the same dataset, and a pid-derived name would have them writing the same file. For a store-backed view, assert the Parquet's row count against `m.Stores[typ].Count` and refuse the view on a mismatch. The KV metadata carries the view name and build time as human-readable provenance; nothing reads it back.
4. Re-take the lock (`dataset.LockMutation`, exported for this, so the lock ordering has one implementation), re-read the manifest, repoint only the views whose inputs still fingerprint as they did when copied and that the view still declares, `fsutil.RenameAtomic` those temp files into place, delete the rest, and write the manifest last. The fingerprints to compare against are taken just before each copy starts, not rebuilt at the repoint, or the comparison is against itself. Renaming after the re-check rather than before is what keeps the manifest write the commit point, as it is in `mergeUnderLock` and `Purge`; renaming in step 3 would leave a complete, unreferenced Parquet inside a folder that reindex and the orphan check are contractually blind to. A view whose inputs moved is discarded and reported as skipped: the Parquet describes a state that is already gone.

The refusals in step 3 are per view rather than per command, because the evidence is per view. Clean views are still materialized, `Refused` carries a reason per view for the caller to render, and the command exits non-zero if the map is non-empty.

Two message-content requirements ride on `Refused` and on the progress writer, and neither is decoration:

- A missing-input refusal names the files **and both remedies**, because the point of refusing is to surface them rather than to leave a flag to be typed forever: re-fetch the run, or run `dataset reindex` to drop the download entry so the file stops being expected. Without the remedies the researcher's only discoverable move is `--allow-partial` on every run afterwards, which is the erosion the flag was kept separate from `--force` to avoid.

```
view reports declares 2 inputs and 1 is missing: report_200.csv
  re-fetch run 200, or run cc-data dataset reindex <ref> to drop the entry so the file stops being expected
  to build from what is on disk anyway, pass --allow-partial
```

- Under `--allow-partial` the run reports how many of the declared inputs it built from, per view, mirroring `clue-documents/build-parquet.sh:60`'s "--allow-partial given; building from N of M documents". A count is what makes a shortfall land: "1 of 2" is read, "proceeding anyway" is not.

```
--allow-partial given; building reports from 1 of 2 declared inputs
```

Both go through `output.Progressf` to stderr, like every other `dataset` subcommand's prose.

The suppression flag in step 2 is the subtle part. Without it, `Materialize` would open the dataset, the engine would helpfully point `logs` at yesterday's Parquet, and the copy would write that Parquet back out with today's fingerprints attached, laundering stale data into a fresh-looking entry.

Tests: materializing then querying returns identical rows and column types for every view in `MaterializableViews()`; a second run skips everything; `--force` rewrites; a view whose declared input is missing is refused while every other view is still written, and proceeds under `--allow-partial`; a view that fell back to typed-empty is refused with `--allow-partial` given; a store-backed Parquet short of `Stores[typ].Count` is refused; an incomplete download warns and blocks nothing; a `get` between the copy and the repoint leaves the view unmaterialized rather than wrongly fresh (driven through a `testHookBeforeRepoint` seam in `duck`, the same shape as `merge.go`'s hook, which also proves the mutation locks are free in that window, since the `get` would otherwise fail as busy); and no `.tmp-` file survives any of those paths.

---

## Report materialization in show and list

**Summary**: The user-visible reporting, separated from the mechanics so its contract change is reviewable on its own.

**Files affected**:
- `internal/dataset/summary.go`: `materialized_bytes`, the unreferenced-Parquet warning
- `internal/duck/materialize.go`: `StaleMaterializedViews`, `AnnotateShowJSON`
- `cmd/dataset_show.go`, `internal/mcpserver/tools.go`: both callers annotate
- `internal/dataset/summary_test.go`

**Estimated diff size**: ~190 lines

`ShowJSON` and `ListRowJSON` each gain `MaterializedBytes int64 \`json:"materialized_bytes"\``. Both are additive: `size_bytes` keeps its meaning, since it is a documented stable contract that `dataset list` also publishes.

`dirSize` currently walks the tree once. It becomes a single walk returning both totals, so `dataset list` does not pay a second walk per dataset:

```go
// dirSizes returns the dataset's total bytes and the subset under registered derived
// subfolders, in one walk. Derived bytes are the ones a researcher can reclaim for free.
func dirSizes(dir string) (total, derived int64)
```

One warning is added, matching the existing `MISSING_FILE` / `INCOMPLETE` / `ORPHAN_FILE` style:

Two warnings are added, matching the existing `MISSING_FILE` / `INCOMPLETE` / `ORPHAN_FILE` style, and they live in different places for a reason worth stating.

```
UNREFERENCED_MATERIALIZED: materialized/logs.parquet is on disk but the manifest no longer records it; queries are reading the raw artifacts, run cc-data dataset materialize <ref> to restore it (external tools can still read the file)
STALE_MATERIALIZED: view logs was materialized from inputs that have since changed; run cc-data dataset materialize <ref> to refresh it
```

The first needs no view knowledge, only the folder listing against `m.Materialized`, so it goes straight into `driftWarnings`, lands inside the existing `sort.Strings`, and reaches every caller of the show contract without any of them changing. It covers both the state a reindex leaves and the orphan a discarded materialize run leaves. Its wording deliberately does not tell the researcher to delete anything: an external script may be reading that file, and it is still correct for that reader.

**The second cannot be produced by `driftWarnings`.** Deciding staleness needs the view's current input list, which only `internal/duck` knows, and `internal/dataset` cannot import it. So `duck` exports the check plus one decorator, and both callers of `BuildShowJSON` use it:

```go
// StaleMaterializedViews names the views whose recorded Parquet no longer matches the inputs.
// It lives here, not in dataset, because a view's input list is a property of the view set.
func StaleMaterializedViews(d *dataset.Dataset) ([]string, error)

// AnnotateShowJSON appends the warnings only the view set can decide, and re-sorts. Both
// surfaces call it: the CLI at cmd/dataset_show.go:32 and the MCP dataset_show tool at
// internal/mcpserver/tools.go:193, which publish the same documented ShowJSON contract.
func AnnotateShowJSON(d *dataset.Dataset, s *dataset.ShowJSON) error
```

Neither file imports `duck` today, so both gain the import. Routing them through one decorator rather than letting each remember is what keeps the two surfaces from publishing different warning sets, and keeps the re-sort inside the function that owns the ordering invariant.

That also keeps exactly one implementation of freshness, shared by the reader, the writer and the warning. A second copy in `dataset` would be the "one source of truth" problem in its most literal form: two answers to "is this Parquet usable" that can disagree.

`renderShow` prints the derived figure beside the total when non-zero. `reindex` needs no code to drop the entries: `reindexIdentity` constructs a fresh `Manifest` without a `Materialized` map, so they are already gone by construction. Three tests rather than one change: the entries are gone, `dataset show` then raises `UNREFERENCED_MATERIALIZED`, and the Parquet files are still on disk. The last is the one that catches a later "tidy-up" making reindex delete the folder, which would break the external readers this story exists for.

---

## Wire the CLI command and the MCP tool

**Summary**: The two surfaces, together, because the drift guard checks the MCP tool inventory in both directions and would go red on a split.

**Files affected**:
- `cmd/dataset_materialize.go` (new), `cmd/dataset.go`
- `cmd/dataset_materialize_test.go` (new)
- `internal/mcpserver/tools.go`, `internal/mcpserver/server_test.go`

**Estimated diff size**: ~250 lines

The command follows the shape of the existing `dataset` subcommands exactly: `resolveExistingRef`, `echoRef`, `notFound`, `mutationErr`, and progress through `output.Progressf` so prose goes to stderr.

```
cc-data dataset materialize <ref> [--force] [--allow-partial]
```

There is no view-selection flag, deliberately: `materialized/` stays a deterministic mirror of the materializable set, so an external script hardcoding a path can tell an absent file from absent data. The command renders `MaterializeResult` as written, skipped and refused counts, names each refused view with its reason, and returns a non-zero exit when any view was refused, so a `set -euo pipefail` recipe stops rather than consuming a surface missing a view.

The MCP tool is annotated neither read-only nor destructive: it writes, but only derived data that is regenerable and documented as safe to delete. `server_test.go:109`'s pinned inventory gains `dataset_materialize`, which is what makes forgetting the catalog entry a CI failure rather than a discovery.

Tests, following `cmd/get_report_test.go`'s shape: a clean run exits zero and names the views written; a run where one view's input is missing exits **non-zero** while the other views are still written, and the message carries both remedies; the same run under `--allow-partial` exits zero and reports "N of M" for the affected view. The exit code is the assertion that matters, since it is what stops a `set -euo pipefail` recipe from consuming a surface missing a view, and it is the one always-on test in the requirements with no home anywhere else in this plan.

---

## Single-source the guidance and the researcher guide

**Summary**: The prose the drift guard requires, plus the external-path documentation the study scripts depend on.

**Files affected**:
- `internal/guidance/src/core.md`: when to materialize, and that cc-data never removes the folder on its own
- `internal/guidance/src/skill_header.md`: the CLI spelling
- `internal/guidance/src/tools.md`: the tool catalog entry
- `docs/researcher-guide.md`: the `materialized/` layout

**Estimated diff size**: ~140 lines

The split between core and skill header is forced by `TestCoreNamesNoCommand`, which rejects any "`cc-data `" spelling in the core. So the core says *when* materializing is worth it, and carries both halves of the deletion sentence: the folder is safe for the researcher to delete at any time, and cc-data itself removes it only through purge and delete. The two sit together because the first alone reads like a warning that the tool might remove it. The skill header carries the command spelling. The tool catalog entry is what `TestGuidanceDocumentsEveryTool` requires.

The researcher guide gains the layout as a documented, stable contract, since REPORT-120 depends on reading it directly:

```
<data-root>/<portal>/datasets/<name>/materialized/<view>.parquet
```

with three notes. These are ordinary Parquet files carrying each view's own columns, readable by any DuckDB, pandas or Polars without cc-data. They are safe for the researcher to delete at any time, **and** cc-data itself removes them only through `dataset purge` and `dataset delete`; the two sentences sit together, because read alone the first one reads like a warning that the tool might delete them, which is exactly what a script hardcoding a path needs to know is untrue. And the folder is larger than the view a study reads, for two measured reasons: `reports` unions the same log CSVs `logs` reads, so both carry those rows (5.4 MB beside 9.5 MB on a 200,000-row fixture, roughly 7% overlap once REPORT-110's 313 MB `history` dominates), and a view's derived columns become disk when materialized rather than being computed per query (`parameters` 2.62 MB beside `parameters_json` 2.62 MB, 45% of `logs.parquet` in total).

---

## Add the opt-in timing test

**Summary**: The measurement, gated so it does not run in CI. Last because it is the only step nothing else depends on.

**Files affected**:
- `internal/duck/materialize_bench_test.go` (new)

**Estimated diff size**: ~150 lines

Skipped unless an environment variable is set, because CI runs plain `go test ./...` with no `-short` convention and the fixture is roughly 455MB of generated JSONL.

Two assertions, and the fixture generator carries a comment saying why its payloads vary: a first attempt used an identical blob per row, ZSTD dictionary encoding took the Parquet to 0.9% of the JSONL, and the measured speedup collapsed from 54x to 2.6x because the cost had moved entirely into the aggregation. A generator emitting a constant blob measures compression, not materialization.

- A column-pruned aggregate (`GROUP BY question_id`) is at least 10x faster materialized. Measured 54x at 1M rows, so the threshold carries five times the headroom rather than being tuned until it passed. The test names the shape and says column pruning is the mechanism under test, so nobody later "fixes" a regression by swapping the query.
- A high-cardinality aggregate (`count(DISTINCT remote_endpoint)`, measured 3.7x) is **not slower**. That is a not-worse check rather than a speedup check, and it is what would catch a change that made materialization a pessimization for the shapes it does not help.

---

## Open Questions

None.

## Self-Review

Roles chosen for what this document has to survive: the engineer reviewing the resulting commits (are the steps really independent, does step N compile without N+1), the engineer writing the named tests (can each be written against the harness this plan provides), and the operator. Every finding was checked by running code against the real view set; the ones that did not survive are not recorded.

### Commit reviewer

#### RESOLVED: The materializable predicate, as written, selected nothing

The plan derived `MaterializableViews()` "from the statement set over an empty manifest filtered to those declaring files", copying how `StaticViewNames()` works. Ran it: over an empty manifest, **0 of 12** views declare files. Every builder returns its stand-in when there is nothing to read, and a stand-in scans no files, so the function would have returned an empty set and materialization would have silently done nothing on every dataset.

"Declares files" is a property of a *populated* manifest, unlike "is a static view", which is exactly why `StaticViewNames` can use an empty one. `MaterializableViews` now takes the manifest.

#### RESOLVED: The same predicate wrongly included the per-run report views

Over a populated manifest the views declaring files came out as `reports`, `report_prompts`, `answers`, `run_membership` and **`report_7`**. `perDownloadViews` sets `files: dl.Files` on both `report_<run>` and `report_<run>_job_<id>` (`internal/duck/views.go:451`, `:456`), so the bare predicate contradicts the requirements' Out of Scope, which excludes per-run views because they multiply with the run count.

Fixed by intersecting with `StaticViewNames()`. Both halves stay code-derived, so a view added later joins or stays out of the set by its own construction rather than by a list someone remembers to update. The requirements' wording is corrected to match, since it stated the bare predicate too.

#### RESOLVED: `Fresh` was declared with one signature and called with another

Declared as `Fresh(dir string) bool` in the manifest step and called as `mat.Fresh(vs.canonDir, st.files)` in the engine step, one step later. The prose underneath already said the caller passes the view's current file list, so the declaration was simply stale. Step 2 would not have compiled against step 3.

### Test author

#### RESOLVED: The staleness warning cannot be written where the plan put it

The plan put `STALE_MATERIALIZED` in `driftWarnings`, which lives in `internal/dataset`. Deciding staleness needs the view's current input list, and that is a property of the view set in `internal/duck`, which `dataset` cannot import: the dependency runs `duck` to `dataset` and never back (`internal/duck/engine.go:13`). Writing it as planned would have forced a second view-to-files mapping inside `dataset`, which is two answers to "is this Parquet usable" that can drift apart.

Moved to a `duck.StaleMaterializedViews` helper called from `cmd/dataset_show.go`, which already imports both, so one implementation of freshness serves the reader, the writer and the warning.

### Operator

#### RESOLVED: Changing `Open`'s signature touches every caller for one internal need

Materialization needs an engine that ignores existing Parquet, which the plan added as a flag on `Open`. `Open` has three non-test callers (`cmd/query.go:60`, `cmd/repl.go:34`, `internal/mcpserver/tools.go:375`), none of which has any interest in the flag. Changed to a sibling `OpenRaw` over a shared unexported constructor, leaving all three untouched.

#### Checked and not a problem: materialize inherits the sandbox's 2GB memory cap

Because materialization reuses the sandboxed engine, its `COPY` runs under `memory_limit = 2GB` (`internal/duck/engine.go:111`), which looked like a risk against a 15 GB corpus. Already measured: the 6.38M-row, 2.92 GB fixture materialized in 5.0s under exactly that cap, because `COPY` streams rather than buffering. Recorded rather than actioned, so the next person does not re-open it.

---

## Self-Review (round 2)

Roles: the engineer reviewing the resulting commits, the engineer writing the named tests, and the operator. Each finding was checked against the running code; two candidates that did not survive are noted at the end.

### Commit reviewer

#### RESOLVED: `cmd/dataset_show.go` does not import `duck`, and it is not the only caller of `BuildShowJSON`

The plan places `STALE_MATERIALIZED` in `cmd/dataset_show.go` "which already imports both". It does not: its imports are `fmt`, `sort`, `text/tabwriter`, `internal/dataset`, `internal/output` and `cobra`. Adding the import is trivial, so the step still works, but the reason given for choosing that file is not the reason that holds.

The consequence that does matter is the second caller. `BuildShowJSON` has exactly two non-test callers: `cmd/dataset_show.go:32` and `internal/mcpserver/tools.go:193`, where the `dataset_show` tool returns the summary struct unchanged. Under the plan the CLI shows the staleness warning and the MCP tool does not, so the same documented `ShowJSON` contract carries different warning sets on the two surfaces. Since materialize is itself exposed over MCP, the surface that can act on the warning is the one that never sees it.

A smaller version of the same problem: `driftWarnings` ends with `sort.Strings(warnings)` (`internal/dataset/summary.go:236`), so warnings appended by a caller land after the sort and break the ordering the struct has always had.

Suggested resolution: have both callers go through one decorator rather than each remembering, for instance a `duck.AnnotateShowJSON(d, s) error` that appends the warnings and re-sorts, called from `cmd/dataset_show.go` and from the MCP tool. One implementation, two call sites, and the sort stays inside the function that owns the invariant.

**Resolved as suggested, and the Q2 resolution shrinks it.** The unreferenced-Parquet warning needs no view knowledge, so it goes straight into `driftWarnings` and reaches both surfaces for free. Only `STALE_MATERIALIZED` still needs `internal/duck`, and it goes through one `duck.AnnotateShowJSON(d, s) error` that appends and re-sorts, called from `cmd/dataset_show.go:32` and `internal/mcpserver/tools.go:193`. Both call sites gain the import; the file list for this step gains `cmd/dataset_show.go` and `internal/mcpserver/tools.go`.

#### RESOLVED: Step 3 renames into place, but step 4 discards by removing "its temp file"

The two steps disagree about when the rename happens. Step 3 copies to a temp name and renames to `materialized/<view>.parquet`; step 4 then says a view whose inputs moved "is discarded, its temp file removed". After step 3 there is no temp file: what exists is a complete, final-named Parquet that the manifest does not reference.

That orphan is unusually well hidden. It sits inside a registered derived subfolder, so by this spec's own contract `Reindex` will not adopt it and the orphan-file check will not flag it, and it is counted in `materialized_bytes` forever. It also inverts the commit discipline the rest of the package follows, where the manifest write is the commit point and files land after it (`internal/dataset/dataset.go:268`, `internal/dataset/merge.go:146`).

Suggested resolution: hold the temp name until after the repoint. Copy every view to `materialized/<view>.parquet.tmp-<pid>`, re-take the lock, re-check the fingerprints, then rename the survivors and delete the rest, and write the manifest last. Add a test asserting no `.tmp-` file survives a discarded run, which the plan already lists but which cannot pass under the current ordering.

**Resolved as suggested.** Step 3 copies to `materialized/<view>.parquet.tmp-<pid>` and stops there; step 4 re-takes the lock, re-checks the fingerprints, renames the survivors with `fsutil.RenameAtomic`, deletes the rest, and writes the manifest last, so the manifest write stays the commit point as it is in `merge.go` and `Purge`. The named test for no surviving `.tmp-` file can then actually fail, which under the old ordering it could not.

#### RESOLVED: The writer needs a statement-executing path that `Engine` does not expose

`Engine`'s only exported method is `Query`, which returns `*sql.Rows`. Running `COPY ... TO` through it and dropping the result leaks the rows and deadlocks `Close`: reproduced here, and the test hung for the full ten-minute timeout in `sql.(*Conn).close` waiting on `closemu` while five Parquet files sat correctly written on disk. `Materialize` lives in `package duck` so it can reach `e.conn` directly, but the writer step's file list names only `materialize.go` and its test, and the choice is worth making deliberately rather than discovering.

Suggested resolution: add an unexported `func (e *Engine) exec(ctx, sql string) error` in `engine.go`, list `engine.go` in the writer step's files, and use it for both the `COPY` and any `PRAGMA` the writer needs.

**Resolved as suggested.** `engine.go` joins the writer step's file list.

### Test author

#### RESOLVED: The reindex test as described cannot fail for the right reason

The plan says reindex "needs no code" because `reindexIdentity` builds a fresh manifest, and gives it a test rather than a change. That test is a legitimate regression guard against a future refactor that starts carrying the manifest forward, and it should stay. But it is the only test the plan names for the reindex requirement, and the requirement has two halves: entries are dropped, and `dataset show` then reports the dataset as needing re-materializing. The second half is untested because, as the requirements-side finding records, it has no mechanism. A reader of the plan sees one requirement with one test and no gap.

Suggested resolution: whichever way the requirements-side question is settled, name the test for it here. If the warning moves to a folder-based check, the test is "reindex, then `dataset show` names the leftover Parquet"; if the second half is dropped, say so in this step so the missing test is a decision rather than an omission.

**Resolved.** The requirements-side question settled on the folder-based check, so this step keeps the existing regression guard (`reindexIdentity` drops the map by construction, and the test goes red if a refactor starts carrying it forward) and adds two more: after a reindex, `dataset show` names the unreferenced Parquet, and the Parquet files are still on disk afterward. The second is the one that would catch someone later "tidying up" by making reindex delete the folder, which would break the external readers this story exists for. Because the new warning needs no view knowledge, it goes in `driftWarnings` rather than through the caller-side decorator, so its test lives in `internal/dataset/summary_test.go` alongside the other warning codes.

### Operator

#### RESOLVED: `Open` now builds the full statement set twice per dataset

`applyMaterialized` calls `MaterializableViews(vs.m)`, which constructs a second `viewSet` and calls `statements()` on it, while the caller has already called `statements()` to get the slice being annotated. Every view's SQL, including one `report_<run>` view per report download and one `answers_<run>` per membership entry, is built twice on every `Open`, `query`, `repl` and MCP `query` call, materialized or not.

This is string building rather than I/O and it will not be visible at 20 runs. It is worth one line of thought at REPORT-110's scale, and it is trivially avoidable.

Suggested resolution: give `MaterializableViews` an unexported sibling taking the already-built `[]viewStmt`, and let the exported form build the statements once and delegate. The writer and the tests keep the exported signature.

**Resolved as suggested.** No behavior change, so no new test; the existing `MaterializableViews` test covers the exported form and `applyMaterialized`'s tests cover the sibling.

### Checked and not a problem

- **Materializing a degraded view does not launder its warning.** `reportUnionView` calls `vs.warnf` while *constructing* the statement, so the "report CSV is missing on disk" warning fires on every `Open` whether the view is later read from Parquet or not. Verified.
- **A corrupt store JSONL cannot be materialized into an empty Parquet.** `read_json` with an explicit `columns=` map does not validate content at `CREATE VIEW`; it fails at query time. Verified: the view registers with no warning and the query fails outright, so the `COPY` fails too rather than writing zero rows. The typed-empty degradation applies to files that are missing or unopenable, which is the case the fingerprint already covers.
