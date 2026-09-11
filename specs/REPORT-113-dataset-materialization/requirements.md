# Dataset materialization (Parquet query surface)

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-113
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Add `cc-data dataset materialize`, which writes each of a dataset's file-backed views to a ZSTD Parquet file at a stable path inside the dataset folder. Queries read a view's Parquet while it is fresh and fall back to the raw artifacts otherwise, so materializing never changes an answer. Because the output is a plain Parquet file at a predictable path, an external tool can open it directly without going through cc-data at all.

## Project Owner Overview

cc-data answers every query by re-reading the raw JSONL and CSV a fetch produced. That is right for a few thousand answers and wrong at the scale a CLUE corpus reaches, where a dataset is measured in gigabytes and every question pays the full parse again. It is also the last piece of storage machinery our study code still owns: the Dataflow study maintains a hand-written Parquet build, with its own column-type declarations and its own guard against a partial build replacing a complete one, purely because the tool does not offer one.

This story takes over both jobs. A researcher runs one command and gets a fast, typed, columnar copy of their dataset that their existing analysis scripts can read by path, and the hand-written build script is deleted rather than reimplemented.

## Background

The query engine opens an ephemeral in-memory DuckDB per invocation, registers a view per store, per report CSV and per join dimension from the manifest, then locks a sandbox confining file access to the dataset folders (`internal/duck/engine.go:31`, `internal/duck/views.go:56`, `internal/duck/engine.go:116`). Store views read JSONL through `read_json` and report views read CSV through `read_csv`, both with explicit typed column maps, so every query re-parses everything it touches.

**The consuming story is what sets this story's scope.** REPORT-120 exists to retire `sources/clue/recipes/log-events/build-parquet.sh` onto `cc-data dataset materialize`, and its acceptance criteria are about the *materialized query surface*: that the study's queries return the same rows against the materialized dataset as against the hand-built Parquet, and that id columns land as VARCHAR so a join needs no cast.

The dataset REPORT-120 materializes contains **no stores at all**. REPORT-119 puts the log events in as a Student Actions report run downloaded with `get report`, which writes `report_<run>.csv` (`internal/fetch/report.go:332`); REPORT-118 puts the cohort in as a Student ID Mapping run, also a report CSV; and REPORT-112's `logs` view is a union over log-type report CSVs with derived columns. The CLUE stores do not arrive until REPORT-110, which is 0.3.0. So materialization scoped to stores would run on REPORT-119's dataset and produce nothing, and REPORT-120 could not land.

That also inverts the usual reading of this story. At 0.2.0 the data is 258,916 log rows, which is small, and the need is ownership of a typed surface rather than speed. At 0.3.0 REPORT-110 lands a 6.38M-entry, 15 GB `clue_history` store and names this story as its scale answer. Materializing views rather than artifacts serves both.

**External readability is a first-class requirement, not a side effect.** `sources/researcher-access.md:609` records that what this story is depended on for is the Parquet landing on disk where an external DuckDB can read it, not only being queryable through the CLI: every analysis script in `cc-data-studies` is plain Python invoking `duckdb` as a subprocess with no dependency on cc-data, and that independence is what makes the scripts outlive the tool. `studies/clue-dataflow-behavior/lib.py:22-24` names three Parquet paths as its entire data source.

## Requirements

- `cc-data dataset materialize <portal>/<name>` writes one ZSTD Parquet per materializable view to `<dataset>/materialized/<view>.parquet`.
- A view is materializable when it is one of the static views **and** declares the files it reads. Both halves are derived from the view set rather than listed by hand. The static half is what excludes the per-run views (`report_<run>`, `answers_<run>`), which declare files but multiply with the run count. Together they select `reports`, `report_prompts`, `logs`, `answers`, `history`, `run_membership`, `student_id_mapping` and `student_metadata`, and excludes the manifest-derived VALUES tables (`downloads`, `attachment_files`), where materializing is pointless, and the `read_text` attachment views (`attachment_states`, `attachment_content`), which would copy every attachment's bytes.
- The Parquet carries the view's own output, derived columns included, so `materialized/logs.parquet` holds REPORT-112's `parameters_json`, `extras_json`, `event_time` and `received_time`, not the raw CSV's columns.
- Each file is written to a temporary name and renamed into place, so a partial file is never mistaken for a complete one.
- Materialization refuses a view that declares an input the manifest names but that is not on disk, unless `--allow-partial` is passed, and names the missing files. This is the whole of the guard, and the condition it exists for: `sources/clue/recipes/clue-documents/build-parquet.sh:34` states the invariant as "every document that should have history must have a .jsonl right now", and it is what a glob matching fewer files once turned into 6.27M entries replaced by 110k. The manifest is cc-data's source list, so the same invariant reads as: every file a view declares must be on disk right now. When overridden, the run reports how many of the declared inputs it built from, matching the source script's "building from N of M documents".
- A run reports one outcome per materializable view, each being written, already fresh, discarded or refused, with a reason on the ones that need explaining. One list rather than a list per outcome, so a view cannot be reported twice or not at all, and so the MCP tool returns the outcomes as they stand rather than a hand-assembled projection that stops mentioning an outcome added later. A discarded view in particular must not be reported as already fresh: it is the one case where the view is not materialized and running again would fix it.
- The refusal is per view, not per command, because the evidence is per view: this view declares a file that is not there. Clean views are materialized, refused views are named with the files they are missing, and the run exits non-zero so a `set -euo pipefail` recipe stops. That is the shape `sources/clue/recipes/clue-documents/download-history.ts:206` already uses for a multi-item operation: work every item, summarize per item, exit non-zero if any failed. A dataset with a subset of its views materialized is an ordinary state, since freshness is per view and it is the same state a newly downloaded run produces.
- A download marked `!Complete` is reported as a warning naming the runs, not a refusal, and `--allow-partial` does not gate it. Exactly one place in the tree writes `Complete: false` (`internal/fetch/paged.go:195`, the store fetch), because report, report_job and attachment downloads write to a `.tmp` and record their entry only after the rename succeeds. A paged fetch merges once at the end, so an incomplete store download leaves the store file untouched and the Parquet byte-identical to one built if the run had never been requested. There is no shortfall to refuse. `dataset show` already carries the same fact as `INCOMPLETE`.
- The view-to-download mapping the warning needs is stated rather than left to be inferred, because no single rule covers it. Report-backed views map to their downloads by file intersection. `answers` and `history` map by download `Type`, since the store is one merged file across every run and carries no per-run identity. `run_membership` maps by `Type` too: its filenames do encode `(type, run)`, but an incomplete run has no membership file at all, so a per-run mapping would never see the run the warning is about.
- The refusal names both remedies for a missing input, since the point of refusing is to surface them rather than to leave a flag to be typed forever: re-fetch the run, or run `dataset reindex` to drop the download entry so the file stops being expected.
- Materialization refuses outright, with no flag to override it, for a view that registered from its typed-empty fallback rather than its primary statement. That is total loss rather than a shortfall, always a broken dataset, and there is no legitimate reason to publish a knowingly empty Parquet. The engine already distinguishes the two cases; it is the branch that warns "degraded to empty".
- After copying a store-backed view, the run asserts the Parquet's row count against the manifest's recorded store count and fails the view if they disagree. Both hand-written build scripts this story retires end with exactly this check (`log-events/build-parquet.sh:97`, `clue-documents/build-content-parquet.sh:102`). The union views get no such check: the pseudo-header filtering makes the arithmetic inexact, and an approximate assertion is worse than none.
- `--force` re-materializes a view whose recorded inputs are unchanged, which is otherwise a no-op. It does not override the missing-input refusal; that is `--allow-partial`'s job alone.
- Freshness is one rule for every view: the manifest records each materialized view's input files with a fingerprint, and the view's Parquet is used only while every input still matches. Otherwise the view is registered from its raw artifacts, with no error and no user action required.
- An input that cannot be stat-ed records a reserved sentinel fingerprint, which no real fingerprint can collide with because a real one is always `<size>-<mtime>`. The sentinel then compares like any other fingerprint: an input that was absent when the Parquet was built and is absent still reads as fresh, while one that has since appeared, or has since gone missing, reads as stale. Telling those apart is the whole reason the sentinel is recorded rather than the input being skipped. This is the read side, and it is needed whichever way the write side is gated: a file can go missing after a successful materialization, and the Parquet must then fall back to the raw artifacts rather than be trusted.
- A Parquet the manifest names but that is absent on disk falls back to the raw artifacts, with a warning. The typed-empty fallback keeps the meaning it already has: nothing readable at all.
- Materialization does its copies outside the dataset's mutation locks, holding them only to read the manifest at the start and repoint it at the end, and discards a result whose inputs moved in between. A materialize run must not make a concurrent `get` fail as busy.
- A run holds a separate, dataset-scoped materialize guard for its whole duration, so two runs never work the same dataset at once and a second one fails as busy. `get` never takes this guard, so the rule above is unaffected. Two things follow. The duplicated work and the repoint race between overlapping runs are gone, and, because the kernel drops an `flock` when its holder dies however abruptly, any temp file in `materialized/` when a run acquires the guard is provably the work of a run that is no longer alive.
- A run sweeps those temp files unconditionally before it starts copying. This is the whole answer to an interrupted run: nothing in the tree installs a signal handler, so a killed run's deferred cleanup never executes and its temp Parquet would otherwise sit in a folder that no warning names, that reindex and the orphan check are contractually blind to, and that `materialized_bytes` counts forever. Making the next run clear it is preferred over reporting it and asking the researcher to act, and the guard is what makes the sweep safe rather than a heuristic: without it, a sweep would delete a concurrent run's in-flight copy and that run would then fail on the rename with a bare "no such file or directory".
- `dataset show` reports a view whose Parquet is stale, naming the command that refreshes it, and reports the materialized bytes separately from the dataset's total size. `dataset list` reports the same separate figure.
- `dataset purge` and `dataset delete` remove `materialized/` along with everything else they remove, and `Purge` clears `m.Materialized` alongside the four maps it already resets. Committing a manifest that still names files the same call is about to delete is what its own commit-ordering comment forbids (`internal/dataset/dataset.go:268`).
- `dataset reindex` drops the manifest's materialization entries rather than carrying them forward. Carrying them would need no Parquet reading and so no import cycle, and a reindex of an unchanged dataset leaves the materializable views byte-identical, so the files stay valid. It is still wrong: reindex is the command for recovering from a manifest cc-data cannot trust, and it has narrow paths that change a view's output while every declared input still fingerprints the same, notably a zero `FetchedAt` re-stamped from the clock, which silently reorders the dimension views' dedupe. Trading that against a rebuild measured in seconds is the wrong trade in the one command whose purpose is recovery.
- `dataset reindex` never deletes `materialized/`, and neither does anything else except `dataset purge` and `dataset delete`. The Parquet is this story's deliverable to tools outside cc-data, which need no manifest entry to read it, so cc-data losing confidence in a file is not grounds for cc-data to remove it. The derived-subfolder contract is therefore "never adopted, never reported as orphans, removed wholesale by purge and delete", and reindex is deliberately not on that list, which is also the right answer for REPORT-89's `exports/`.
- `dataset show` names any Parquet in `materialized/` that no manifest entry references, which is the state a reindex leaves and also the state a materialize run leaves if it discards a view after copying it. The message states what cc-data can and cannot vouch for rather than telling the researcher to clear it: the manifest no longer records these files, queries have gone back to the raw artifacts, `dataset materialize` restores them, and they stay readable by external tools meanwhile. Deciding this needs no view knowledge, only the folder listing against `Materialized`, so unlike the staleness warning it belongs in `driftWarnings` and reaches every caller of the show contract for free.
- Materialization reads only raw artifacts, never an existing Parquet. Copying a view that was itself reading a stale Parquet would write that stale data back out with fresh fingerprints attached, which is the one way this feature could silently corrupt an answer.
- Each Parquet carries its own provenance in the file footer: the view it holds and when it was built. Nothing in cc-data reads it back, since reindex deliberately does not try to revalidate a Parquet; it is there so a Parquet found outside the dataset, in a backup or a copied folder, can say what it is.
- A materialized view survives `dataset rename`. The manifest records dataset-relative paths resolved against the dataset directory at query time, so the folder moves with the rename. Verified, and asserted by a test, because unrelocatable stored paths are exactly why the persistent DuckDB was rejected.
- A materialized view resolves correctly in a multi-dataset session, under its schema prefix, and a dataset with no Parquet in the same session is unaffected. Verified, and asserted by a test.
- The derived-subfolder rule is a named predicate rather than a special case for one folder name, so REPORT-89 can register `exports/` against it. `materialized` and `exports` must not collide, and a test holds that a file planted inside a registered derived folder stays invisible to reindex and to the orphan-file warning.
- `dataset materialize` is exposed as an MCP tool, documented in the shared guidance catalog, and listed in the pinned MCP tool inventory.
- Guidance says when to materialize, that `materialized/` is safe for the researcher to delete at any time, and that cc-data itself removes it only through `dataset purge` and `dataset delete`. The two sentences sit together: read alone, the first one reads like a warning that the tool might delete it, which is exactly what an external script hardcoding the path needs to know is untrue. The "when" goes in the shared core both surfaces render; the CLI spelling goes in the skill header, since the core may not name a command.
- The researcher guide documents the `materialized/<view>.parquet` layout as a stable path external tools may read directly, since that is what the study scripts depend on.
- `materialize` has no view-selection flag. `materialized/` is a deterministic mirror of the materializable set, so what it holds is a function of what the dataset holds, which is what lets an external script treat a path as a contract. A flag would make the contents a function of which flags someone last ran, and an outside reader could not tell an absent file from an unselected one.
- The researcher guide states the two measured duplication costs, so the folder size is expected rather than discovered. `reports` unions the log CSVs that `logs` also reads, so both carry the same rows: on a 200,000-row log fixture, `reports.parquet` was 5.4 MB beside a 9.5 MB `logs.parquet`, 36% of a folder whose largest member that study never opens. At REPORT-110's scale the same overlap is roughly 7%, because `reports` unions report CSVs only and the 313 MB `history` Parquet dominates.
- The identical-results test iterates the materializable views derived from the view set, not a hand-written list, so a view added later cannot go uncompared.
- Always-on tests, on a small fixture: identical results and identical column types for every materialized view; staleness detected after a `get`; fallback when the Parquet is deleted; per-view refusal on an input missing from disk, with the other views still materialized and a non-zero exit, and the same run proceeding under `--allow-partial`; an incomplete download warning without blocking anything; the un-overridable refusal for a view that fell back to typed-empty; the store row-count assertion failing a view whose Parquet is short; `Fresh` returning false once a recorded input is deleted; `--force` rebuilding an unchanged view; reindex dropping the entries and `dataset show` then naming the unreferenced Parquet, with the files themselves left on disk; and a file planted in a registered derived folder staying invisible to reindex and the orphan warning.
- The timing assertion needs a 1M-row fixture, roughly 540MB of generated JSONL, so it is opt-in through an environment variable rather than run on every CI job. It asserts a column-pruned aggregate at 10x against a measured 60x, and separately that a high-cardinality aggregate is not slower. Its fixture generator must emit varied payloads.

## Technical Notes

### Verified: the DuckDB mechanics

Checked against the bundled engine (`github.com/duckdb/duckdb-go/v2 v2.10504.0`, `go.mod:6`, reporting `v1.5.4`), since the sandbox's `enable_external_access = false` made it an open question whether any of this was reachable:

- `COPY (<select>) TO '<path>' (FORMAT parquet, COMPRESSION zstd)` succeeds.
- `read_parquet` over a file inside an allowed directory succeeds **after** `lock_configuration = true`, exactly as `read_csv` and `read_json` already do.
- `COPY ... (KV_METADATA {...})` writes custom key/value pairs into the Parquet footer and `parquet_kv_metadata()` reads them back, including under the locked sandbox. Not load-bearing here (see the reindex requirement), but available as human-readable provenance.
- A store's typed column map round-trips exactly: JSONL copied to Parquet and read back yields identical column types and an empty `EXCEPT` in both directions.

### Verified: a view's declared types survive the round trip

The premise of materializing views rather than artifacts is that the Parquet carries the view's types, which is what REPORT-120's "id columns are VARCHAR" criterion turns on and what REPORT-112's JSON columns depend on. Copying a view shaped like `logs` to Parquet and reading it back:

```
before: event_time:TIMESTAMP, extras_json:JSON, id:VARCHAR, n_tiles:BIGINT, parameters_json:JSON, tile_types:VARCHAR[]
after : event_time:TIMESTAMP, extras_json:JSON, id:VARCHAR, n_tiles:BIGINT, parameters_json:JSON, tile_types:VARCHAR[]
```

`json_extract_string` still works against the materialized `parameters_json`, and a `TRY_CAST` that produced NULL for invalid JSON is preserved as NULL. JSON, TIMESTAMP and LIST all survive, so no column needs re-declaring on read.

### Verified: every view is identical with its source swapped to Parquet

A dataset was built with two merged runs and a report CSV, its `answers` store copied to Parquet as materialization would, and a second engine opened whose `answers` view reads `read_parquet` while every other statement is left byte-for-byte as `viewSet.statements()` produces it. All views the fixture produced were compared on column names, column types and full row content: **13 compared, 0 differing**, including the per-run views that join the swapped store to a membership file still read as JSONL, and the dimension views whose `QUALIFY ROW_NUMBER()` wrapper binds against union column names.

One mechanical note for the test: the comparison needs `ORDER BY ALL` to be deterministic. `ORDER BY ALL` does sort a `JSON` column under the bundled engine, measured over the real `logs` view carrying `parameters_json` and `extras_json` with four rows of distinct JSON, so the comparison needs no per-view column list. Ordering by a `JSON` column by name works too. Re-measure if the engine is upgraded rather than assuming it, since JSON comparison semantics are the kind of thing a DuckDB release changes.

### Verified: the speedup is real but strongly query-shaped

On a generated 6.38M-row history-shaped fixture (2.92 GB JSONL), materialized to 313 MB of ZSTD Parquet (10.7%) in 5.0s, under the sandbox's own caps (`memory_limit = 2GB`, `threads = 4`, `internal/duck/engine.go:111`):

| Query shape | JSONL | Parquet | Speedup |
| --- | --- | --- | --- |
| `count(*)` | 785ms | 2ms | 327x |
| `GROUP BY question_id` | 992ms | 11ms | 87x |
| `GROUP BY question_id` + `count(DISTINCT remote_endpoint)` | 1.272s | 238ms | 5.3x |
| per-learner row count | 1.11s | 262ms | 4.2x |
| `json_extract_string(state, ...)` over the blob column | 1.591s | 1.138s | 1.4x |
| `SELECT * ... LIMIT 100` | 5ms | 33ms | 0.15x |

Column pruning is where the order of magnitude comes from. A query that reads the wide state blob is bounded by JSON parsing rather than scanning and gains about 1.4x. An unfiltered `LIMIT` is slower materialized, by 28ms, because JSONL streams and stops while Parquet decompresses a row group.

`run_membership` on its own, over 20 membership files totaling 741 MB, materializes to 3.7 MB (0.49%) and gains 36x on `count(*)`, 34x on a single-learner lookup, 11.6x on rows-per-run and 5.4x on distinct-learners-per-run.

### Measured: a view's derived columns are free until they are materialized

`logs` appends `parameters_json`, `extras_json`, `event_time` and `received_time` beside the source columns they derive from, and `csvScan` keeps the sources because no name collides. In a view that costs nothing, since the derived columns are computed per query. Materialized, they are disk. Per column chunk on a 200,000-row fixture with realistic CLUE parameters, `logs.parquet` came to 11.69 MB: `parameters` 2.62 MB beside `parameters_json` 2.62 MB, `extras` 0.26 beside `extras_json` 0.26, `time` 0.69 beside `event_time` 1.17, `timestamp` 0.96 beside `received_time` 1.17. The derived columns are 5.22 MB of 11.69 MB, so 45% of the file REPORT-120 actually reads is a second encoding of data already in it.

This story does not act on it. Materialization must carry the view's exact column set, or a query stops returning the same answer from the Parquet as from the raw artifacts, which is the feature's central promise; trimming the superseded sources is not available here. Whether `logs` should carry both forms at all is REPORT-112's contract and would change results for every consumer, materialized or not. Recorded so REPORT-120 can answer the one question that decides it: whether the study ever reads the raw `parameters` and `time` columns, or only the derived ones.

### The fingerprint is the one new mechanism

Freshness cannot key on a version, because report CSVs do not have one: `report_<run>.csv` is a fixed name overwritten on re-fetch. Stores and membership files do carry versions in their names, but a single rule covering all three is worth more than three rules. So the manifest records, per materialized view, each input file with a size-and-mtime fingerprint, and the view is fresh only while every input still matches.

Size and mtime is what Go's own build cache uses. It cannot distinguish a rewrite that preserved both, which a real re-fetch does not do. A content hash would be exact and would cost a full read of every input, which at 15 GB defeats the purpose.

### Lifecycle: three specific changes, all verified by running them

Verified by building a real dataset, planting files inside a derived subfolder that would be adopted or flagged at the top level (`answers.v99.jsonl`, `report_4242.csv`, `members_answers_5.v9.jsonl`), and running the real code paths:

- **Purge misses it.** `Purge` left the derived folder and all three files completely intact, because `deleteArtifacts` removes only `segments` and `attachments` by literal name and otherwise skips directories (`internal/dataset/dataset.go:291`).
- **`dataset show` size includes it.** `dirSize` counted every planted file (`internal/dataset/summary.go:262`).
- **Reindex and the orphan check already ignore it.** `Reindex` adopted none of them, and `BuildShowJSON` raised no `ORPHAN_FILE` warning for any. That property currently holds by accident: `Reindex` skips *all* directories (`internal/dataset/reindex.go:45`) and the orphan scan is files-only (`internal/dataset/summary.go:232`). The named predicate is what turns the accident into a contract.

### Constraints inherited from elsewhere

- Everything stays inside the dataset folder, so the sandbox allowlist is unchanged and the "Dataset-folder trust boundary" note (`README.md:85`) holds as written. What changes is the shape a planted file can take, from JSONL or CSV to a Parquet container with its own compression codecs: same boundary, wider parser behind it.
- The drift guard requires every registered view and every MCP tool to appear in the guidance catalog, in both directions (`internal/guidance/guard_test.go:39`, `:56`), and separately checks the researcher guide's view table. Materialization adds no view but does add a tool, which also needs adding to `internal/mcpserver/server_test.go:109`. `TestCoreNamesNoCommand` forbids naming a `cc-data` command in the core, so the "when" and the CLI spelling live in different files.
- `internal/duck` imports `internal/dataset`, never the reverse, which is why reindex cannot read a Parquet footer.
- Manifest decoding tolerates absent fields and carries a forward-migration switch (`internal/dataset/manifest.go:136`); `CurrentManifestVersion` is 1.

## Out of Scope

- Materializing `attachment_states` and `attachment_content`. They `read_text` every attachment, so materializing them copies the attachment corpus a second time.
- Materializing per-run views (`answers_<run>`, `report_<run>`). They multiply with the run count and are cheap joins over views that are themselves materialized.
- Pre-parsing the state blob into a typed structure. That is what would move the JSON-decoding shape past 1.4x, and it is a data-model change rather than a storage-format change.
- Any change to the sandbox allowlist, the trust boundary, or the resource caps.
- REPORT-89's `exports/` subfolder. This story owns the mechanism; 89 registers against it.
- Automatic materialization on `get`, or any background refresh.

## Decisions

Recorded compressed; the reasoning that produced them is in the git history and in REPORT-120's own description.

**Union fact views carry no provenance columns, and `downloads` is not materialized.** REPORT-112 deferred onto this story whether `reports`, `logs` and REPORT-111's CLUE views should denormalize `slug` and `hide_names`, because materialization writes a column set into Parquet and an external reader has no `downloads` to join against. They should not. The hazard is real in the abstract, since `username` carries five meanings across the log slugs and the `hide_names` split and only `slug` plus `hide_names` separate them, but it belongs to CLI and MCP consumers, who have `downloads` and are taught the join by the catalog entries. The bare-Parquet reader this story exists for does not have it: across `studies/clue-dataflow-behavior/`, `username`, `student_name`, `student_id`, `primary_user_id`, `learner_id`, `application`, `slug` and `hide_names` appear zero times in production queries, and `user_id` only in test fixtures. What the study selects from the log Parquet is `doc_key`, `offering_id`, `class_id`, `session`, `event`, `event_time` and `extras`; its identity axis is the document and the class or offering, never the person. Denormalizing would add two columns the only 0.2.0 consumer would not read.

Materializing `downloads` was the alternative way to close the same gap and is rejected with it. It scans no files, being an inlined VALUES table over the manifest (`internal/duck/views.go:365`), so it satisfies neither half of the materializable predicate. Admitting it means either declaring `manifest.json` an input it does not read, which leaves the Parquet stale after every mutation that rewrites the manifest, or hand-listing an exception, which forfeits the code-derived predicate that keeps the writer and the reader from disagreeing. If a future consumer does need provenance beside the fact tables, write `materialized/downloads.parquet` unconditionally on every run, outside the `Materialized` map and with no freshness entry, rather than making `downloads` a materializable view.

**The study's hottest join key is in the query shape materialization helps least.** `sources/clue/recipes/log-events/build-parquet.sh` lifts `doc_key`, `doc_type`, `doc_uid` and `tile_id` out of the `parameters` JSON into real columns, because they are the join to the content and history Parquets and leaving them buried makes every downstream query re-parse the JSON; `build_candidates.py:262` and `build_cycles.py:249` both depend on that. This spec's own measurements put a query over the wide blob at 1.4x against 87x for a column-pruned aggregate, so `parameters_json` being JSON-typed does not recover it. Lifting CLUE-specific log parameters into a view spanning every portal and app is the wrong side of the line `porting-plan.md` draws between a generic transform and study code, so the extraction belongs in the study's own first derived step, over cc-data's `logs`. REPORT-120 is where this shows up, since that story re-points the study's queries.

**No persistent DuckDB, contrary to the ticket's title.** Scott's pipeline, which this story is modeled on, uses Parquet only: no `.duckdb` file, no `ATTACH`, no `duckdb.connect(path)` anywhere in `cc-data-studies`. Every invocation is `duckdb -c` against a fresh in-memory database (`studies/clue-dataflow-behavior/lib.py:35`), and `lib.py:22-24` names three Parquet files as the whole data source. Measured, a native DuckDB table beat Parquet by ~1.4x for a second full copy on disk, and views stored inside a DuckDB file freeze the absolute Parquet path, so `dataset rename` breaks them with a misleading sandbox permission error. The ticket's persistent DuckDB has no counterpart in the code it was specified from.

**Views, not artifacts.** See Background: artifact-scoped materialization cannot serve REPORT-120, and an artifact's Parquet would give external readers the raw CSV columns rather than the derived ones the study needs.

**Fixed paths, not version-stamped filenames.** External scripts hardcode paths (`lib.py:22-24`), and a name that changes on every fetch breaks them. The cost, verified: replacing a fixed-name Parquet under a live session changes that session's answers mid-query rather than pinning a snapshot, since DuckDB re-opens by path. That is acceptable for a user-initiated command, and it is better than what JSONL does today, where `cleanupOldVersions` deletes the file a live session is reading immediately after a merge repoints. On Windows a rename over an open Parquet can fail, which `fsutil.RenameAtomic` retries for 100ms; it fails cleanly and is re-run, the same exposure `manifest.json` already carries.

**`materialized/`, not `.materialized/`.** Once an external script is meant to hardcode the path, a hidden directory is the wrong choice, and it sits alongside the existing non-hidden `segments/` and `attachments/`.

**`--force` and `--allow-partial` stay separate flags.** Collapsing them would make one flag mean "I accept incomplete data" in one situation and "spend the time again" in another, which is the shape of flag that gets passed reflexively until it stops protecting anything.

**`--allow-partial` means one thing: a missing input.** Read back from the code being retired, the missing input is the flag's primary meaning rather than an extension of it: `clue-documents/build-parquet.sh` offers `--allow-partial` for exactly the source-list-versus-disk mismatch, and treats an in-progress download as a separate branch of the same message ("If a download is still running, wait for it"). An earlier draft of this requirement keyed the guard on `Download.Complete` alone while citing the glob incident as its justification, which meant the acceptance test REPORT-120 was to run against "the case that motivated it" would have exercised a condition the guard did not cover. That draft in fact protected nothing at all: `Complete: false` is written in exactly one place, the store fetch, whose merge happens once at the end and so leaves the materialized bytes identical either way, and store downloads carry no `Files` for a file-based mapping to find. The sibling repo has no completion flag of any kind, because tmp-and-rename makes presence on disk the signal; cc-data's report path uses the same discipline, which is why only the store fetch can carry the flag and why the flag cannot indicate a short surface.

**`materialized_bytes` is additive, not a redefinition of `size_bytes`.** `ShowJSON` is documented as the stable `dataset show --json` contract (`internal/dataset/summary.go:14`) and `dataset list` publishes the same figure, so redefining it would break both. The same field is added in both places or neither.

**Materialize is exposed over MCP.** The "when to materialize" prose lands in the shared core both surfaces render, so an MCP client is told when to materialize whether or not it can; without the tool its only recourse is to send the user to a terminal, which is the shape the MCP header exists to avoid.

## Open Questions

None. Both questions this spec opened were decided: no persistent DuckDB, and views rather than artifacts (which also settled membership coverage, since `run_membership` is a view like any other). The provenance question REPORT-112 deferred onto this story was decided too; it is under Decisions.

## Self-Review

Roles: Senior Engineer, QA Engineer, DevOps/Operator, Security Engineer. Findings that did not survive a check against the code are not recorded. Findings marked superseded were raised against the artifact-scoped design and no longer apply.

### Senior Engineer

#### RESOLVED: A missing Parquet must fall back to the raw artifacts, not to an empty view

The requirement originally said a missing materialized copy degrades as a missing store file does, which silently returns no data over a dataset that is completely intact. Verified that both `read_json` and `read_parquet` over a missing file fail at view *creation*, not query time, which is what makes the engine's primary-then-fallback registration work (`internal/duck/engine.go:88`). So the engine must stat the Parquet and choose the raw statement when it is absent, as `reportUnionView` already does for CSVs (`internal/duck/views.go:200`, the stat at `:211`). Fixed in the requirements.

#### SUPERSEDED: Membership files are not versioned in lockstep with the store

Raised against a design where membership would have been materialized as an artifact keyed on the store version. Verified true: `mergeUnderLock` repoints only the runs in the merge, and `membershipUnionMulti` reads every other run's membership at its own recorded version (`internal/dataset/merge.go:27`). Under the view-scoped design the point is moot, and it is part of why a per-input fingerprint beats a per-artifact version rule.

### DevOps/Operator

#### RESOLVED: Holding the mutation locks for the whole copy makes `get` fail while materialize runs

`lockMutation` takes both locks non-blocking and returns `ErrBusy` rather than waiting (`internal/dataset/dataset.go:106`). Materializing 6.38M records took 5s locally and would be far longer at 15 GB, and a concurrent `get` would fail outright for that whole window. `mergeUnderLock` already has the pattern that avoids it: do the expensive work against what was read, re-check before committing, restart if it moved (`internal/dataset/merge.go:146`). Added as a requirement.

### QA Engineer

#### RESOLVED: "Every view returns identical results" must iterate the views, not a hand-written list

Otherwise the test passes forever while a newly added view goes uncompared. The view set is already derived from the statement set rather than hand-maintained (`internal/duck/views.go:585`), which is what the guidance drift guard is built on; the identical-results test should iterate the same source. Fixed in the requirements.

#### RESOLVED: The timing fixture would cost every CI run 455MB and several seconds

CI runs plain `go test ./...` (`.github/workflows/ci.yml:39`) with no `-short` convention: the only `t.Skip` calls in the tree are Windows permission-bit guards. Gated behind an environment variable, with the correctness half kept always-on at small scale. Fixed in the requirements.

#### RESOLVED: `viewStmt.files` is not a complete dependency inventory

Asserted while choosing the view-scoped design that `files` already answers "which artifacts does this view read". It is set by only 5 of the 9 static view builders: the VALUES-backed views (`downloads`, `attachment_files`) and the `read_text` views (`attachment_states`, `attachment_content`) declare none. That turns out to define the right set rather than break it, since a view declares `files` precisely when it scans files, so the materializable set is exactly the set worth materializing. The requirement now states the predicate rather than assuming the field is universal.

### Security Engineer

#### RESOLVED: The trust boundary is unchanged, but the parsing surface is not

The README already states that a dataset folder inherits the trust of whoever can write to it (`README.md:85`), so that statement needs no revision. What changes is that a hostile file in a dataset folder can now be a Parquet container rather than JSONL or CSV. Not a new boundary crossing and not a design change, but recorded so that "the trust boundary is unchanged" is not read as "nothing about the exposure changed".

---

## Self-Review (round 2)

Roles: Senior Engineer, QA Engineer, DevOps/Operator, Data Steward. Every finding below was checked by running the real view set and the real manifest code; the ones that did not survive that check are not recorded. In particular, "materializing a degraded view launders its warning away" was raised and dropped: `reportUnionView` warns during statement *construction*, so the warning still fires even when the view is read from Parquet, and a corrupt store JSONL fails at query time rather than at `CREATE VIEW`, so `COPY` fails too rather than writing an empty Parquet.

### Senior Engineer

#### RESOLVED: A missing input file is a normal state, and the fingerprint has no defined behavior for it

`reportUnionView` appends `dl.Files...` to the view's declared files whether or not the CSV is on disk (`internal/duck/views.go:218`, the stat at `:210`); `dimensionViewStmt` does the same (`:695`). Verified by deleting one of two report CSVs and reading the view set back: `reports` still declares `[report_100.csv report_200.csv]`, `os.Stat` fails on the second, the view registers and returns the surviving run's rows, and the session warns "report CSV report_200.csv is missing on disk; contributing zero rows to reports". This is a supported, documented, warned-about state, not corruption.

`Fingerprint(path)` as specified returns `("", err)` for that file, and neither `Materialize` nor `Fresh` says what to do with the error. The three obvious implementations diverge sharply: aborting the run makes a dataset with one missing CSV unmaterializable even though every other view is healthy; skipping the view silently drops it from the set with no diagnostic; recording an empty fingerprint works only if `Fresh` also treats a stat error as a non-match, which is what makes a restored file correctly read as stale.

Suggested resolution: state the semantics in the requirements. A missing input records a reserved sentinel fingerprint distinct from any real one, `Fresh` returns false whenever a current input cannot be stat-ed, and materialization proceeds. Add it to the always-on test list, since the behavior is invisible otherwise.

**Resolved by reading the code this story retires.** The first proposal here was to proceed and record the sentinel, on the reasoning that materialization is a cache and a cache must reproduce its source, and that routing a persistent condition through `--allow-partial` would leave the flag typed reflexively until it stopped protecting anything. The sibling repo contradicts that. `sources/clue/recipes/clue-documents/build-parquet.sh:26-60` offers `--allow-partial` for precisely the source-list-versus-disk mismatch and handles an in-progress download as a separate branch of the same message, so the missing input is the flag's primary meaning rather than an extension of it. It also answers the flag-economics objection directly, by naming the remedy in the refusal rather than expecting the researcher to live with the override. Neither of the other two build scripts lets a missing input yield a short Parquet either: `log-events/build-parquet.sh:26` refuses on any missing input, and `clue-documents/build-content-parquet.sh:24` proceeds for exactly one input it documents as optional, announcing it on stderr.

Two further facts, both verified by running the code. A missing store file degrades at `CREATE VIEW` rather than at query time, and `COPY` of the degraded view succeeds, writing a 404-byte zero-row Parquet without complaint, which is why total loss needs a refusal with no override rather than the same flag. And the manifest's store count is an exact invariant for a store view's rows (8 records across two merged runs, 8 rows from the view), which makes the row-count assertion both build scripts end with directly portable to the store-backed views.

So: refuse on either shortfall shape, `--allow-partial` overrides both and reports N of M, an un-overridable refusal for a view that fell back to typed-empty, the sentinel fingerprint on the read side regardless, and the store row-count assertion after each store-backed copy. Written into the Requirements and the Decisions sections.

Noted and deliberately left out of scope: `driftWarnings` raises `MISSING_FILE` for stores and membership only (`internal/dataset/summary.go:197-206`), never for a download's report CSVs, so `dataset show` stays silent about the condition that will now make `materialize` refuse. That is a pre-existing hole rather than one this story opens, and closing it changes a documented stable contract, so it belongs in its own ticket.

#### RESOLVED: `dataset show` cannot report "needs re-materializing" after a reindex, because reindex destroys the only record

The requirement says reindex drops the materialization entries **and** that `dataset show` then reports the dataset as needing re-materializing. Verified that `reindexIdentity` returns a fresh `Manifest` carrying only Version, Name, Description and CreatedAt (`internal/dataset/reindex.go:179`), so `Materialized` is gone by construction, exactly as the implementation plan says. But `StaleMaterializedViews` reads `m.Materialized`, so after a reindex it finds nothing and warns about nothing.

The Parquet files themselves survive: `Reindex` skips directories (`:46`), and the derived-subfolder contract makes them invisible to the orphan check. So the end state is a `materialized/` folder full of files that nothing references, that `materialized_bytes` still counts, and that no warning mentions. The second half of the requirement has no mechanism behind it.

Suggested resolution: either drop the second half, or base the warning on the folder rather than the map, which covers both this case and any other unreferenced Parquet: if `materialized/` holds Parquet files that no manifest entry names, warn once naming the refresh command. That is also the only thing that would flag an orphan left by a discarded materialize run.

**Resolved: warn on the folder, keep the files.** Two options were tested before settling.

Carrying the entries forward through a reindex was the first, since keeping a record needs no Parquet reading and therefore no import cycle, which means the plan's stated reason for dropping them is not the real one. Measured: a reindex of an unchanged dataset with a prior manifest left 14 of 15 view statements byte-identical (the one difference, `downloads`, is not materializable) and every artifact fingerprint unchanged, so the files really do stay valid. The set comparison in the freshness rule also catches the loud failure case, a manifest-less reindex losing a slug and collapsing `student_id_mapping` to its stand-in while the CSV fingerprint is untouched, because losing the slug removes the file from the view's declared inputs. Rejected anyway: the residual paths that change a view's output while every input still matches, chiefly a zero `FetchedAt` re-stamped from the clock reordering the dimension dedupe, are silent, and the payoff is a rebuild measured in seconds inside the one command whose whole purpose is recovering from a manifest that cannot be trusted.

Deleting `materialized/` inside reindex was the second, and it is the option the "make the bad state impossible rather than recording it and asking the user to clear it" rule points at directly. Rejected for a reason that outranks that rule here: `materialized/<view>.parquet` is this story's deliverable to tools outside cc-data (`sources/researcher-access.md:609`, and `studies/clue-dataflow-behavior/lib.py:22-24` hardcodes three such paths as its entire data source). An external reader needs no manifest entry, and the files stay correct, so deleting them would break a running study script over cc-data's internal bookkeeping. cc-data losing confidence in a file is not grounds for cc-data to remove it.

So: drop the entries, leave the files, and warn from the folder listing rather than from the map. That lands the check in `driftWarnings` with no `internal/duck` dependency, so it sorts with the other warnings and reaches the MCP `dataset_show` tool as well as the CLI without either changing, and it covers the orphan a discarded materialize run leaves too. The message states what cc-data can and cannot vouch for rather than inviting deletion of something an external script may be reading.

#### RESOLVED: `Purge` clears four manifest maps by name and would leave `Materialized` behind

`Purge` explicitly resets `Stores`, `Membership`, `Downloads` and `Attachments` before committing the manifest, on the stated discipline that "the manifest write is the commit point ... so a failed write can never leave a surviving manifest that references already-deleted artifacts" (`internal/dataset/dataset.go:268`). The implementation plan changes only `deleteArtifacts`, so after a purge the manifest still names materialized views whose Parquet files were just deleted, which is precisely the state that comment exists to forbid.

No wrong answers follow, because `MaterializableViews` over an emptied manifest returns nothing and the entries are inert. But the requirement is stated as "purge and delete remove `materialized/` along with everything else they remove", and the manifest entries are part of what they remove.

Suggested resolution: `Purge` clears `m.Materialized` alongside the other four, and the purge test asserts it.

**Resolved as suggested.** Requirement updated; no decision needed, since leaving entries that name files the same function just deleted is the exact state `Purge`'s own commit-ordering comment exists to forbid.

### DevOps/Operator

#### RESOLVED: The incomplete-download guard cannot reach the store views by any file-based mapping, and could not have protected anything if it did

The requirement is that materialization "refuses to run when any download the view reads is marked incomplete". A view knows its files; a download knows its `Complete` flag. For report-backed views the two join on `Download.Files`. For `answers`, `history` and `run_membership` there is no join: a store download entry is constructed as `Download{Type, RunID, Complete, FetchedAt}` with no `Files` at all (`internal/fetch/paged.go:195`), and reindex synthesizes the same shape (`internal/dataset/reindex.go:120`). An implementer taking the obvious file-intersection route gets a guard that silently exempts the three views the motivating incident actually concerned.

The practical risk for stores is lower than it looks, because a paged fetch merges once at the end (`internal/fetch/paged.go:71`), so an interrupted `get` leaves the store at its last complete state rather than short. But the requirement promises coverage it cannot deliver, and the gap is invisible in a test that only exercises report downloads.

Suggested resolution: state the mapping in the requirements. Report-backed views map by file intersection; `answers`, `history` and `run_membership` map by download `Type`. Note in the technical notes why the second rule is needed, so nobody later "simplifies" it back to one rule.

**Resolved, and the finding turned out to be larger than it was raised as.** Grepping every assignment of `Complete` in the tree finds exactly one that writes `false`: `internal/fetch/paged.go:195`, the store fetch. Report, report_job and attachment downloads are only ever recorded `Complete: true`, because `internal/fetch/report.go:74-118` streams to a `.tmp`, renames, and only then upserts the entry. So `!Complete` can describe nothing but an `answers` or `history` download, and a paged fetch merges once at the end (`internal/fetch/paged.go:71`), which means an incomplete store download leaves the store file untouched and the Parquet byte-identical to one built if the run had never been requested.

The guard as originally required therefore protected nothing in either direction. Under the obvious file-based mapping it could never fire, since store downloads carry no `Files`. Under a `Type` mapping it would fire on a condition that changes no byte of the output. The incident it was justified by is a missing-files incident, which the Q1 resolution now covers.

The sibling repo confirms the shape from the other side: it has no completion flag anywhere, because every downloader writes to a `.tmp` and renames so that presence on disk is the completion signal (`athena_logs.py:128`, `download-history.ts:17`, `download-content.ts:20`, `download-metadata.ts:37`). Its build guards can only compare the source list against disk, which is exactly the provable invariant Q1 adopted.

So `!Complete` is demoted from a refusal to a warning naming the runs, ungated by `--allow-partial` since there is nothing to allow, and the mapping is written into the requirements as three rules rather than one because no single rule covers it. The refusal that remains, for a missing input, is per view, following `download-history.ts:206-237`, which works every item, summarizes per item, and exits non-zero if any failed.

### Data Steward

#### RESOLVED: `reports` contains everything `logs` contains, so the log corpus is written to Parquet twice

`reportsView` admits every allowed report type (`internal/duck/views.go:202`), log runs included. Verified on a fixture holding one answers CSV and one log CSV: `reports` declares `[report_100.csv report_200.csv]` while `logs` declares `[report_200.csv]`. Materializing both writes the log rows once as `reports.parquet` and again, with the derived columns, as `logs.parquet`.

For REPORT-120's dataset that is the whole point of the exercise duplicated, and it is the largest single artifact. The spec's size figures are all per-view (313 MB for a 2.92 GB history fixture, 3.7 MB for membership), so nothing in the document tells a researcher that `materialized_bytes` on a log-heavy dataset is roughly double the view they wanted. There is also no way to ask for less: `materialize` is all-or-nothing over the derived set.

Suggested resolution: at minimum, say so in the requirements and in the researcher guide, so the disk figure is not a surprise. Better, add `--view <name>` (repeatable) selecting a subset of `MaterializableViews`, which costs little, keeps the derived set as the default, and lets REPORT-120 materialize `logs` and `student_id_mapping` without paying for `reports`.

**Resolved: document it, do not add the flag.** The finding's premise held but its magnitude did not. "Roughly double" was reasoned rather than measured; measured, `reports.parquet` was 5.4 MB beside a 9.5 MB `logs.parquet` on a 200,000-row fixture, 36% of the folder, because `logs` is the larger file thanks to its derived columns. Scaled to REPORT-110, the overlap is roughly 7%, since `reports` unions report CSVs only and the 313 MB `history` Parquet dominates a folder nobody would exclude it from. `--view` would save single-digit percentages in the regime that matters and a few megabytes in the other, with no time argument either, since 2.92 GB materialized in 5.0s.

Against that it would cost the property the external consumer depends on. Without a flag, `materialized/` is a deterministic mirror of the materializable set and its contents are a function of the dataset, which is what makes a hardcoded path a contract. With one, the contents become a function of which flags someone last ran, and an outside reader cannot distinguish an absent file from an unselected one. That is the same reasoning that settled fixed paths over version-stamped filenames and reindex-must-not-delete. It is also the kind of flag that breeds: `--view` invites `--exclude`, then its interaction with `--force`.

The measurement is recorded in the Requirements and the Technical Notes instead, along with a larger cost found while checking this one: 45% of `logs.parquet` is the derived columns duplicating their own sources, which is invisible in a view and only becomes disk when materialized. That one is REPORT-112's contract to change, not this story's, and is raised to REPORT-120 rather than acted on.
