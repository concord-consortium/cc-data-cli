# Dataset materialization (Parquet query surface)

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-113

**Status**: **Closed**

## Overview

Add `cc-data dataset materialize`, which writes each of a dataset's file-backed views to a ZSTD Parquet file at a stable path inside the dataset folder. Queries read a view's Parquet while it is fresh and fall back to the raw artifacts otherwise, so materializing never changes an answer. Because the output is a plain Parquet file at a predictable path, an external tool can open it directly without going through cc-data at all.

cc-data answers every query by re-reading the raw JSONL and CSV a fetch produced. That is right for a few thousand answers and wrong at the scale a CLUE corpus reaches, where a dataset is measured in gigabytes and every question pays the full parse again. It is also the last piece of storage machinery the study code owns: the Dataflow study maintained a hand-written Parquet build, with its own column-type declarations and its own guard against a partial build replacing a complete one, purely because the tool did not offer one.

## Requirements

- `cc-data dataset materialize <portal>/<name>` writes one ZSTD Parquet per materializable view to `<dataset>/materialized/<view>.parquet`.
- A view is materializable when it is one of the static views **and** declares the files it reads. Both halves are derived from the view set rather than listed by hand. The static half excludes the per-run views (`report_<run>`, `answers_<run>`), which declare files but multiply with the run count. Together they select `reports`, `report_prompts`, `logs`, `answers`, `history`, `run_membership`, `student_id_mapping` and `student_metadata`, and exclude the manifest-derived VALUES tables (`downloads`, `attachment_files`) and the `read_text` attachment views (`attachment_states`, `attachment_content`).
- The Parquet carries the view's own output, derived columns included, so `materialized/logs.parquet` holds REPORT-112's `parameters_json`, `extras_json`, `event_time` and `received_time`, not the raw CSV's columns.
- Each file is written to a temporary name and renamed into place, so a partial file is never mistaken for a complete one.
- Materialization refuses a view that declares an input the manifest names but that is not on disk, unless `--allow-partial` is passed, and names the missing files. When overridden, the run reports how many of the declared inputs it built from.
- A run reports one outcome per materializable view, each being written, already fresh, discarded or refused, with a reason on the ones that need explaining. One list rather than a list per outcome, so a view cannot be reported twice or not at all, and so the MCP tool returns the outcomes as they stand rather than a hand-assembled projection. A discarded view must not be reported as already fresh: it is the one case where the view is not materialized and running again would fix it.
- The refusal is per view, not per command. Clean views are materialized, refused views are named with the files they are missing, and the run exits non-zero so a `set -euo pipefail` recipe stops.
- A download marked `!Complete` is a caveat on the view's outcome naming the runs, not a refusal, and `--allow-partial` does not gate it. A copy built with `--allow-partial` carries the same kind of caveat, saying how many of its declared inputs it used. Both ride on the outcome rather than on progress output because the MCP tool returns the outcome and may have nowhere to send progress.
- The view-to-download mapping the caveat needs is three rules, because no single rule covers it. Report-backed views map by file intersection. `answers` and `history` map by download `Type`, since the store is one merged file across every run. `run_membership` maps by `Type` too, since an incomplete run has no membership file for a per-run mapping to find.
- The refusal names both remedies for a missing input: re-fetch the run, or run `dataset reindex` to drop the download entry so the file stops being expected.
- Materialization refuses outright, with no flag to override it, for a view that registered from its typed-empty fallback rather than its primary statement. That is total loss rather than a shortfall.
- After copying a store-backed view, the run asserts the Parquet's row count against the manifest's recorded store count and fails the view if they disagree. The union views get no such check: pseudo-header filtering makes the arithmetic inexact, and an approximate assertion is worse than none.
- `--force` re-materializes a view whose recorded inputs are unchanged. It does not override the missing-input refusal; that is `--allow-partial`'s job alone.
- Freshness is one rule for every view: the manifest records each materialized view's input files with a fingerprint and the signature of the view definition it was built from, and the view's Parquet is used only while every input still matches and the definition still agrees. Otherwise the view is registered from its raw artifacts, with no error and no user action required.
- The signature covers what the input fingerprints cannot see. A cc-data release that changes a view's projection, derived columns or ordering leaves every input byte untouched, as does a reindex that re-stamps a zero `FetchedAt` and so reorders a dimension view's dedupe. It is taken over the view's statement with the dataset directory and the schema prefix neutralized, because a materialized view has to survive a rename and has to resolve the same alone as in a multi-dataset session.
- A recorded view is only skipped as fresh when its Parquet is still readable. The folder is documented as safe to delete at any time, so without that check a deleted or truncated copy reads as fresh forever while queries quietly fall back to the raw artifacts, repairable only through `--force`.
- An input that cannot be stat-ed records a reserved sentinel fingerprint, which no real fingerprint can collide with because a real one is always `<size>-<mtime>`. The sentinel compares like any other fingerprint, so an input absent at build time and absent still reads as fresh, while one that has since appeared or gone missing reads as stale.
- A Parquet the manifest names but that is absent on disk falls back to the raw artifacts, with a one-line warning per view; only a Parquet that is present but unreadable carries the engine's error. `dataset show` names the views whose recorded Parquet is missing.
- A session that outlives a fetch re-checks each Parquet-backed view's freshness on every query and puts a stale one back on its raw artifacts, so a repl answers the same as a session opened after the fetch would.
- Materialization does its copies outside the dataset's mutation locks, holding them only to read the manifest at the start and repoint it at the end, and discards a result whose inputs moved in between. A materialize run must not make a concurrent `get` fail as busy. The repoint waits for a `get` that is still running rather than failing busy and deleting every finished copy; the wait ends only with the caller's context, which the CLI cancels on interrupt. It is the only waiting acquisition, since rename, delete and purge take the materialize guard while holding the mutation locks and would deadlock against it if they waited too.
- A copy whose rename into place fails is refused on its own; the views renamed before it still reach the manifest.
- A run holds a separate, dataset-scoped materialize guard for its whole duration, so two runs never work the same dataset at once and a second fails as busy. `get` never takes this guard. `dataset rename`, `dataset delete` and `dataset purge` do take it, because the guard's lock file and the run's open copies live inside the directory they move or remove and Windows cannot rename or remove a directory with an open handle inside it; without that coupling a rename would fail after having already rewritten the manifest name. Because the kernel drops an `flock` when its holder dies however abruptly, any temp file present when a run acquires the guard is provably the work of a run that is no longer alive.
- A run sweeps those temp files unconditionally before it starts copying, which is the whole answer to an interrupted run.
- `dataset show` reports a view whose Parquet is stale, naming the command that refreshes it, and reports the materialized bytes separately from the dataset's total size. `dataset list` reports the same separate figure.
- `dataset purge` and `dataset delete` remove `materialized/`, and `Purge` clears `m.Materialized` alongside the four maps it already resets.
- `dataset reindex` drops the manifest's materialization entries rather than carrying them forward, and never deletes `materialized/`. Nothing except purge and delete removes it. The derived-subfolder contract is "never adopted, never reported as orphans, removed wholesale by purge and delete".
- `dataset show` names any Parquet in `materialized/` that no manifest entry references, which is the state a reindex leaves. The message states what cc-data can and cannot vouch for rather than telling the researcher to clear it.
- Materialization reads only raw artifacts, never an existing Parquet. Copying a view that was itself reading a stale Parquet would write that stale data back out with fresh fingerprints attached.
- Each Parquet carries its own provenance in the file footer: the view it holds and when it was built. Nothing in cc-data reads it back.
- A materialized view survives `dataset rename`, and resolves correctly in a multi-dataset session under its schema prefix. Both asserted by tests.
- The derived-subfolder rule is a named predicate rather than a special case for one folder name, so REPORT-89 can register `exports/` against it.
- `dataset materialize` is exposed as an MCP tool, documented in the shared guidance catalog, and listed in the pinned MCP tool inventory.
- Guidance says when to materialize, that `materialized/` is safe for the researcher to delete at any time, and that cc-data itself removes a finished Parquet only through `dataset purge` and `dataset delete`. The two sentences sit together, because the first alone reads like a warning that the tool might delete it. *(Amended during implementation: the sweep clears a half-written file from an interrupted run, so the promise is stated for a finished Parquet rather than for everything in the folder.)*
- The researcher guide documents the `materialized/<view>.parquet` layout as a stable path external tools may read directly.
- `materialize` has no view-selection flag. `materialized/` is a deterministic mirror of the materializable set, which is what lets an external script treat a path as a contract.
- The researcher guide states the two measured duplication costs, so the folder size is expected rather than discovered.
- The identical-results test iterates the materializable views derived from the view set, not a hand-written list, so a view added later cannot go uncompared.
- Always-on tests, on a small fixture: identical results and identical column types for every materialized view; staleness detected after a `get`; fallback when the Parquet is deleted; per-view refusal on a missing input with the other views still materialized and a non-zero exit, and the same run proceeding under `--allow-partial`; an incomplete download warning without blocking anything; the un-overridable refusal for a typed-empty view; the store row-count assertion; `Fresh` returning false once a recorded input is deleted; `--force` rebuilding an unchanged view; reindex dropping the entries with the files left on disk; and a file planted in a registered derived folder staying invisible to reindex and the orphan warning.
- The timing assertion needs a 1M-row fixture, roughly 540MB of generated JSONL, so it is opt-in through an environment variable. It asserts a column-pruned aggregate at 10x against a measured 60x, and separately that a high-cardinality aggregate is not slower. Its fixture generator must emit varied payloads.

## Technical Notes

### Verified: the DuckDB mechanics

Checked against the bundled engine (`github.com/duckdb/duckdb-go/v2 v2.10504.0`, reporting `v1.5.4`), since the sandbox's `enable_external_access = false` made it an open question whether any of this was reachable:

- `COPY (<select>) TO '<path>' (FORMAT parquet, COMPRESSION zstd)` succeeds.
- `read_parquet` over a file inside an allowed directory succeeds **after** `lock_configuration = true`, exactly as `read_csv` and `read_json` already do.
- `COPY ... (KV_METADATA {...})` writes custom key/value pairs into the Parquet footer and `parquet_kv_metadata()` reads them back, including under the locked sandbox.
- A store's typed column map round-trips exactly: JSONL copied to Parquet and read back yields identical column types and an empty `EXCEPT` in both directions.

### Verified: a view's declared types survive the round trip

Copying a view shaped like `logs` to Parquet and reading it back preserves `TIMESTAMP`, `JSON` and `VARCHAR[]` exactly. `json_extract_string` still works against the materialized `parameters_json`, and a `TRY_CAST` that produced NULL for invalid JSON is preserved as NULL, so no column needs re-declaring on read.

### Verified: every view is identical with its source swapped to Parquet

A dataset was built with two merged runs and a report CSV, its `answers` store copied to Parquet, and a second engine opened whose `answers` view reads `read_parquet` while every other statement is byte-for-byte as `viewSet.statements()` produces it. All views were compared on column names, types and full row content: **13 compared, 0 differing**, including the per-run views that join the swapped store to a membership file still read as JSONL. The comparison needs `ORDER BY ALL` to be deterministic; that does sort a `JSON` column under the bundled engine, so re-measure if the engine is upgraded.

### Verified: the speedup is real but strongly query-shaped

On a generated 6.38M-row history-shaped fixture (2.92 GB JSONL), materialized to 313 MB of ZSTD Parquet (10.7%) in 5.0s, under the sandbox's own caps (`memory_limit = 2GB`, `threads = 4`):

| Query shape | JSONL | Parquet | Speedup |
| --- | --- | --- | --- |
| `count(*)` | 785ms | 2ms | 327x |
| `GROUP BY question_id` | 992ms | 11ms | 87x |
| `GROUP BY question_id` + `count(DISTINCT remote_endpoint)` | 1.272s | 238ms | 5.3x |
| per-learner row count | 1.11s | 262ms | 4.2x |
| `json_extract_string(state, ...)` over the blob column | 1.591s | 1.138s | 1.4x |
| `SELECT * ... LIMIT 100` | 5ms | 33ms | 0.15x |

Column pruning is where the order of magnitude comes from. A query that reads the wide state blob is bounded by JSON parsing rather than scanning. An unfiltered `LIMIT` is slower materialized, because JSONL streams and stops while Parquet decompresses a row group.

`run_membership` on its own, over 20 membership files totaling 741 MB, materializes to 3.7 MB (0.49%) and gains 36x on `count(*)`.

### Measured: a view's derived columns are free until they are materialized

`logs` appends `parameters_json`, `extras_json`, `event_time` and `received_time` beside the source columns they derive from. In a view that costs nothing, since they are computed per query. Materialized, they are disk: on a 200,000-row fixture the derived columns are 5.22 MB of an 11.69 MB `logs.parquet`, so 45% of the file is a second encoding of data already in it. This story does not act on it (see Not Yet Implemented).

### The fingerprint is the one new mechanism

Freshness cannot key on a version, because report CSVs do not have one: `report_<run>.csv` is a fixed name overwritten on re-fetch. Stores and membership files do carry versions in their names, but a single rule covering all three is worth more than three rules. Size and mtime is what Go's own build cache uses; it cannot distinguish a rewrite that preserved both, which a real re-fetch does not do, and a content hash would cost a full read of every input.

### Lifecycle: verified by running the real code paths

- **Purge missed derived folders.** `Purge` left a planted derived folder and its files intact, because `deleteArtifacts` removed only `segments` and `attachments` by literal name.
- **`dataset show` size includes it.** `dirSize` counted every planted file.
- **Reindex and the orphan check already ignore it**, but by accident: `Reindex` skips all directories and the orphan scan is files-only. The named predicate turns the accident into a contract.

### Constraints inherited from elsewhere

- Everything stays inside the dataset folder, so the sandbox allowlist is unchanged. What changes is the shape a planted file can take, from JSONL or CSV to a Parquet container with its own compression codecs: same boundary, wider parser behind it.
- The drift guard requires every registered view and every MCP tool to appear in the guidance catalog, in both directions, and separately checks the researcher guide's view table. `TestCoreNamesNoCommand` forbids naming a `cc-data` command in the core, so the "when" and the CLI spelling live in different files.
- `internal/duck` imports `internal/dataset`, never the reverse, which is why reindex cannot read a Parquet footer.

## Out of Scope

- Materializing `attachment_states` and `attachment_content`. They `read_text` every attachment, so materializing them copies the attachment corpus a second time.
- Materializing per-run views (`answers_<run>`, `report_<run>`). They multiply with the run count and are cheap joins over views that are themselves materialized.
- Pre-parsing the state blob into a typed structure. That is what would move the JSON-decoding shape past 1.4x, and it is a data-model change rather than a storage-format change.
- Any change to the sandbox allowlist, the trust boundary, or the resource caps.
- REPORT-89's `exports/` subfolder. This story owns the mechanism; 89 registers against it.
- Automatic materialization on `get`, or any background refresh.

## Not Yet Implemented

- **Trimming the superseded source columns from `logs`.** The derived columns are 45% of `logs.parquet`, a second encoding of `parameters` and `time`. Materialization must carry the view's exact column set or a query stops returning the same answer from the Parquet as from the raw artifacts, and whether `logs` should carry both forms at all is REPORT-112's contract, which would change results for every consumer. Deferred so REPORT-120 can answer the question that decides it: whether the study ever reads the raw `parameters` and `time` columns, or only the derived ones.
- **Lifting `doc_key`, `doc_type`, `doc_uid` and `tile_id` out of the `parameters` JSON.** These are the study's hottest join keys and the retired `build-parquet.sh` lifted them into real columns. Deferred to the study's own first derived step in REPORT-120, because a CLUE-specific extraction in a view spanning every portal and app is the wrong side of the generic-transform line, and `parameters_json` being JSON-typed does not recover the cost (1.4x on a wide-blob query against 87x on a column-pruned one).
- **`driftWarnings` raises `MISSING_FILE` for stores and membership only, never for a download's report CSVs.** So `dataset show` stays silent about the condition that now makes `materialize` refuse. This is a pre-existing hole rather than one this story opened, and closing it changes a documented stable contract, so it belongs in its own ticket: REPORT-129.

## Decisions

### Should materialization be scoped to stores (as the ticket said) or to views?

**Context**: The ticket specified writing each *store* to Parquet. REPORT-120 is the consuming story and its dataset contains no stores at all.

**Options considered**:
- A) Stores, as specified: one Parquet per `answers`/`history` store file.
- B) Views: one Parquet per materializable view, carrying the view's own derived columns.

**Decision**: B. REPORT-119 puts the log events in as a report CSV, REPORT-118's cohort is another report CSV, and REPORT-112's `logs` is a union over log-type CSVs; the CLUE stores do not arrive until REPORT-110 at 0.3.0. Store-scoped materialization would have run on REPORT-119's dataset and produced nothing, so REPORT-120 could not have deleted `build-parquet.sh`. Views also give external readers REPORT-112's derived columns rather than the raw CSV's, which is what the study actually consumes.

---

### Should the dataset keep a persistent DuckDB file alongside the Parquet?

**Context**: The ticket's title specified "Parquet + persistent DuckDB".

**Options considered**:
- A) A `.duckdb` file whose views point at the Parquet, as specified.
- B) Parquet only, queried through the existing ephemeral in-memory engine.

**Decision**: B. It has no counterpart in the pipeline it was modeled on: no `.duckdb` file, no `ATTACH`, no `duckdb.connect(path)` anywhere in `cc-data-studies`, where every invocation is `duckdb -c` against a fresh in-memory database and `lib.py:22-24` names three Parquet files as the whole data source. Measured, a native DuckDB table beat Parquet by about 1.4x for a second full copy on disk, and views stored inside a DuckDB file freeze the absolute Parquet path, so `dataset rename` breaks them with a misleading sandbox permission error.

---

### Fixed filenames or version-stamped ones?

**Context**: Stores are versioned files swapped atomically on merge, so a version-stamped Parquet would follow the existing convention.

**Options considered**:
- A) `materialized/<view>.v<N>.parquet`, following the store convention.
- B) `materialized/<view>.parquet`, fixed.

**Decision**: B. External scripts hardcode paths (`lib.py:22-24`), and a name that changes on every fetch breaks them. The cost, verified: replacing a fixed-name Parquet under a live session changes that session's answers mid-query rather than pinning a snapshot, since DuckDB re-opens by path. That is acceptable for a user-initiated command, and better than what JSONL does today, where `cleanupOldVersions` deletes the file a live session is reading. On Windows a rename over an open Parquet can fail, which `fsutil.RenameAtomic` retries for 100ms.

---

### `materialized/` or `.materialized/`?

**Context**: The folder holds derived data, which would normally argue for hiding it.

**Options considered**:
- A) Hidden, `.materialized/`.
- B) Visible, `materialized/`.

**Decision**: B. Once an external script is meant to hardcode the path, a hidden directory is the wrong choice, and it sits alongside the existing non-hidden `segments/` and `attachments/`.

---

### Should `materialize` take a view-selection flag?

**Context**: `reports` unions the same log CSVs `logs` reads, so a log-heavy dataset writes those rows twice; `--view` would let REPORT-120 materialize only what it reads.

**Options considered**:
- A) `--view <name>`, repeatable, defaulting to the full set.
- B) No flag; the folder is always the full materializable set.

**Decision**: B, and document the cost. The magnitude did not survive measurement: `reports.parquet` was 5.4 MB beside a 9.5 MB `logs.parquet` on a 200,000-row fixture, and roughly 7% overlap at REPORT-110's scale where the 313 MB `history` Parquet dominates. Against single-digit percentages, a flag would cost the property the external consumer depends on: without it `materialized/` is a deterministic mirror of the dataset, and with it the contents become a function of which flags someone last ran, so an outside reader cannot distinguish an absent file from an unselected one. It is also the kind of flag that breeds: `--view` invites `--exclude`, then its interaction with `--force`.

---

### Should the union fact views denormalize `slug` and `hide_names`?

**Context**: REPORT-112 deferred this onto this story, because materialization writes a column set into Parquet and an external reader has no `downloads` table to join against. `username` carries five meanings across the log slugs and the `hide_names` split.

**Options considered**:
- A) Denormalize `slug` and `hide_names` onto `reports`, `logs` and REPORT-111's CLUE views.
- B) Materialize `downloads` so the dimension travels with the fact tables.
- C) Neither; the join stays a CLI and MCP concern.

**Decision**: C. The hazard belongs to CLI and MCP consumers, who have `downloads` and are taught the join by the catalog entries. The bare-Parquet reader this story exists for does not hit it: across `studies/clue-dataflow-behavior/`, `username`, `student_name`, `student_id`, `primary_user_id`, `learner_id`, `application`, `slug` and `hide_names` appear zero times in production queries, and what the study selects is `doc_key`, `offering_id`, `class_id`, `session`, `event`, `event_time` and `extras`. B was rejected with it: `downloads` scans no files, so it satisfies neither half of the materializable predicate, and admitting it would need either a false `manifest.json` input or a hand-listed exception. If a future consumer needs provenance beside the fact tables, write `materialized/downloads.parquet` unconditionally on every run, outside the `Materialized` map.

---

### What exactly does `--allow-partial` override?

**Context**: The guard exists because a glob matching fewer files once replaced 6.27M history entries with 110k. An early draft keyed it on `Download.Complete`.

**Options considered**:
- A) An incomplete download (`Complete: false`).
- B) A declared input missing from disk.
- C) Both.

**Decision**: B, with the incomplete download demoted to an ungated warning. Grepping every assignment of `Complete` finds exactly one that writes `false`: the store fetch, which merges once at the end, so an incomplete store download leaves the Parquet byte-identical to one built if the run had never been requested. Under a file-based mapping the A guard could never fire at all, since store downloads carry no `Files`. So the draft protected nothing in either direction, and the incident it cited is a missing-files incident. The sibling repo has no completion flag of any kind, because tmp-and-rename makes presence on disk the signal.

---

### Should `--force` and `--allow-partial` be one flag?

**Context**: Both are overrides on the same command.

**Options considered**:
- A) One flag covering both.
- B) Two flags.

**Decision**: B. Collapsing them would make one flag mean "I accept incomplete data" in one situation and "spend the time again" in another, which is the shape of flag that gets passed reflexively until it stops protecting anything.

---

### Is `materialized_bytes` a new field or a redefinition of `size_bytes`?

**Context**: `ShowJSON` is documented as the stable `dataset show --json` contract and `dataset list` publishes the same figure.

**Options considered**:
- A) Redefine `size_bytes` to exclude derived data.
- B) Add `materialized_bytes` beside it.

**Decision**: B. Redefining would break both published surfaces. The same field is added in both places or neither.

---

### Is materialize exposed over MCP?

**Context**: An MCP client may not be able to run a terminal command.

**Options considered**:
- A) CLI only, with the guidance telling an agent to send the user to a terminal.
- B) A `dataset_materialize` MCP tool alongside the CLI command.

**Decision**: B. The "when to materialize" prose lands in the shared core both surfaces render, so an MCP client is told when to materialize whether or not it can; without the tool its only recourse is to send the user to a terminal, which is the shape the MCP header exists to avoid. The tool carries no hints, so a client applies the MCP default and treats it as destructive, which is right: an `allow_partial` run replaces a complete Parquet with a shorter one at a path scripts outside cc-data read.

---

### What should a missing Parquet do at query time?

**Context**: The first draft said a missing materialized copy degrades as a missing store file does.

**Options considered**:
- A) Degrade to the typed-empty view, as a missing store does.
- B) Fall back to the raw artifacts, with a warning.

**Decision**: B. A silently empty result over a completely intact dataset is the worst failure this feature could have. Verified that both `read_json` and `read_parquet` over a missing file fail at view *creation* rather than at query time, which is what makes primary-then-fallback registration work, so the engine can try the Parquet statement and fall through. Absent, corrupt and truncated Parquets all fail the same way, so one warning covers every shape.

---

### How long should a materialize run hold the dataset's mutation locks?

**Context**: `lockMutation` takes both locks non-blocking and returns `ErrBusy` rather than waiting. Materializing 6.38M records took 5s locally and would be far longer at 15 GB.

**Options considered**:
- A) Hold them for the whole run, as every other mutating command does.
- B) Hold them only to read the manifest and to repoint it, re-checking before committing.

**Decision**: B, following `mergeUnderLock`'s existing discipline: do the expensive work against what was read, re-check before committing, discard if it moved. Under A a concurrent `get` would fail outright for the whole copy window. The repoint is the one acquisition that waits rather than fails, since failing there would delete every finished copy because someone started a `get`.

---

### What happens to a view whose inputs move while its copy is running?

**Context**: Because the copies run outside the mutation locks, a `get` can merge mid-run.

**Options considered**:
- A) Record it anyway; the Parquet is internally consistent.
- B) Discard it and report it.
- C) Retry the copy against the new inputs.

**Decision**: B. The Parquet describes a state that is already gone, and recording it would attach fresh fingerprints to stale data. C was rejected during implementation: a retry inside a command already doing gigabyte copies turns a predictable run into an unpredictable one, and a fetch loop could keep invalidating it. The input fingerprints are taken just before each copy starts and compared again at the repoint, since rebuilding them at the repoint would compare a value against itself.

---

### Does a missing input abort the run, skip the view, or record something?

**Context**: A union view keeps declaring a CSV that has gone missing; this is a supported, warned-about state, not corruption. `Fingerprint` returning an error left three implementations that diverge sharply.

**Options considered**:
- A) Abort the run, so one missing CSV makes the whole dataset unmaterializable.
- B) Skip the view silently.
- C) Refuse the view with a named override, and record a reserved sentinel on the read side.

**Decision**: C. Read back from the code being retired: `clue-documents/build-parquet.sh` offers `--allow-partial` for precisely the source-list-versus-disk mismatch, `log-events/build-parquet.sh` refuses on any missing input, and neither lets a missing input yield a short Parquet. The sentinel is needed on the read side regardless, because a file can go missing after a successful materialization. Two further facts, both verified: a missing store file degrades at `CREATE VIEW` and `COPY` of the degraded view succeeds, writing a 404-byte zero-row Parquet, which is why total loss needs a refusal with no override; and the manifest's store count is an exact invariant for a store view's rows, which makes the row-count assertion both build scripts end with directly portable.

---

### Is the refusal per view or per command?

**Context**: A dataset can have one broken view and seven healthy ones.

**Options considered**:
- A) Per command: refuse the run if any view is short.
- B) Per view: materialize the clean ones, name the refused ones, exit non-zero.

**Decision**: B, following `download-history.ts:206`, which works every item, summarizes per item, and exits non-zero if any failed. A dataset with a subset of its views materialized is an ordinary state, since freshness is per view and it is the same state a newly downloaded run produces. Extended during implementation to cover a copy that fails outright (a full disk, or a concurrent `get` removing a store version mid-copy): that is per-view evidence too, so it refuses the view rather than aborting the run and discarding every completed copy. Only a cancelled context stops the run.

---

### Should reindex carry the materialization entries forward?

**Context**: Carrying them needs no Parquet reading and therefore no import cycle, so the plan's stated reason for dropping them was not the real one. Measured: a reindex of an unchanged dataset left 14 of 15 view statements byte-identical and every artifact fingerprint unchanged.

**Options considered**:
- A) Carry the entries forward.
- B) Drop the entries and delete `materialized/`.
- C) Drop the entries, keep the files, and warn from the folder listing.

**Decision**: C. A was rejected because reindex has narrow paths that change a view's output while every input still fingerprints the same, notably a zero `FetchedAt` re-stamped from the clock silently reordering the dimension dedupe; trading that against a rebuild measured in seconds is the wrong trade in the one command whose purpose is recovery. B is what the "make the bad state impossible" rule points at, and was rejected for a reason that outranks it here: `materialized/<view>.parquet` is this story's deliverable to tools outside cc-data, so deleting a still-correct file over cc-data's internal bookkeeping would break a running study script.

---

### What does freshness have to cover beyond the input files?

**Context**: The first design keyed freshness on the input fingerprints alone. Those see every byte a view reads and nothing about the view itself.

**Options considered**:
- A) Input fingerprints only.
- B) Fingerprints plus a signature of the view definition the copy was built from.

**Decision**: B. A cc-data release that changes a view's projection, derived columns or ordering leaves every input byte untouched, so under A the older Parquet keeps reading as fresh and queries return the previous release's columns indefinitely, which breaks the feature's central promise that materializing never changes an answer. The same hole covers a manifest-only change racing a run: a dimension view embeds its download's fetch time as a SQL literal to break ties, so a reindex re-stamping a zero `FetchedAt` reorders the dedupe without touching a file. The signature is taken over the view's statement with the dataset directory and the schema prefix neutralized, since neither changes what the view returns and both would otherwise break rename survival and multi-dataset resolution. It is compared on the read path, on the skip-if-fresh path, at the repoint re-check, and by the staleness warning.

---

### Is a recorded entry enough to call a view fresh?

**Context**: The guidance tells researchers the folder is safe to delete at any time.

**Options considered**:
- A) Trust the manifest entry; `--force` repairs anything else.
- B) Require the recorded Parquet to be readable before skipping the view.

**Decision**: B. Under A, deleting a Parquet, which the documentation invites, leaves the entry fresh, `materialize` reporting "already fresh" and doing nothing, no warning anywhere, and queries silently falling back to the raw artifacts. The only recourse was a flag that nothing pointed the researcher at. The check is a stat plus a footer read through the engine already open, which is metadata-only and costs about 2ms even on a 313 MB Parquet.

---

### Where does each drift warning live?

**Context**: Deciding staleness needs the view's current input list, which only `internal/duck` knows, and `internal/dataset` cannot import it. `BuildShowJSON` has two non-test callers, the CLI and the MCP `dataset_show` tool.

**Options considered**:
- A) Both warnings in `driftWarnings`, duplicating a view-to-files mapping inside `dataset`.
- B) Both appended by each caller.
- C) The unreferenced-Parquet warning in `driftWarnings`, the staleness warning through one exported decorator both callers use.

**Decision**: C. The unreferenced warning needs only the folder listing against `Materialized`, so it goes straight into `driftWarnings`, lands inside the existing sort, and reaches both surfaces for free. Staleness goes through `duck.ShowJSON`, which builds the summary and the staleness warnings from one manifest read, appends and re-sorts, so the two surfaces cannot publish different warning sets, the holdings and the warnings cannot describe two versions of the manifest, and there is exactly one implementation of freshness shared by the reader, the writer and the warning. A would have been two answers to "is this Parquet usable" that can drift apart; B would have let the surface that can act on the warning be the one that never sees it.

---

### How is the materializable set derived?

**Context**: The first draft derived it "from the statement set over an empty manifest filtered to those declaring files", copying `StaticViewNames`. Run against an empty manifest, 0 of 12 views declare files, so the function returned nothing and materialization would have silently done nothing on every dataset. Over a *populated* manifest the bare predicate wrongly included `report_<run>`.

**Options considered**:
- A) A hand-maintained list of view names.
- B) Declares-files over an empty manifest.
- C) Declares-files over the real manifest, intersected with `StaticViewNames()`.

**Decision**: C. "Declares files" is a property of a populated dataset, unlike "is a static view", so `materializableViews` takes the manifest. Both halves stay code-derived, so a view added later joins or stays out of the set by its own construction rather than by a list someone remembers to update. The two attachment views are the one exception, excluded by name: they declare the files they read like any other view, and keeping them out by leaving their file lists empty would have made the degradation warning the only thing standing between them and a second copy of the attachment corpus. Because the statements carry a schema prefix in a multi-dataset session while the set is keyed on bare names, both the predicate and the reader go through one `bareViewName` helper; an earlier draft trimmed the prefix in only one of the two, which would have disabled the feature entirely whenever more than one dataset was registered.

---

### How does materialization get an engine that ignores existing Parquet?

**Context**: Copying a view that was itself reading a stale Parquet would launder stale data into a fresh-looking entry. `Open` has three non-test callers with no interest in the distinction.

**Options considered**:
- A) A flag on `Open`, updating all callers.
- B) A sibling `OpenRaw` over a shared unexported constructor.

**Decision**: B, leaving all three callers untouched. `OpenRaw` also returns the set of views that registered from their typed-empty fallback, which is what the un-overridable refusal keys on. Separately, the writer needs a statement-executing path that `Engine` does not expose: running `COPY ... TO` through `Query` and dropping the result leaks the rows and deadlocks `Close`, reproduced with a test that hung for the full ten-minute timeout while five Parquet files sat correctly written on disk. Hence an unexported `Engine.exec`.

---

### When are the temp files renamed into place?

**Context**: The first draft renamed at the end of each copy and then spoke of removing "its temp file" at the repoint, by which point no temp file exists.

**Options considered**:
- A) Rename as each copy finishes; delete the final-named file if the view is later discarded.
- B) Hold the temp name until after the re-check, then rename the survivors and delete the rest.

**Decision**: B. Under A a discarded view leaves a complete, unreferenced Parquet inside a folder that reindex and the orphan check are contractually blind to, counted in `materialized_bytes` forever, and it inverts the commit discipline the rest of the package follows, where the manifest write is the commit point.

---

### How is a temp file left by an interrupted run cleaned up?

**Context**: A kill, a crash or a power cut ends a run before its deferred cleanup can run (the CLI cancels on interrupt so that Ctrl-C does not, but a second Ctrl-C still kills). The leftover was invisible to every diagnostic: the unreferenced-Parquet warning matches only names ending `.parquet`, the derived-subfolder contract hides the folder from reindex and the orphan check, and `materialized_bytes` counts it forever.

**Options considered**:
- A) Report it in the unreferenced-Parquet warning and let the researcher delete it.
- B) Sweep files older than a cutoff.
- C) Hold a dedicated materialize guard for the whole run, then sweep unconditionally.

**Decision**: C. A records a broken state and asks the user to clear it, which is the shape to avoid when the state can be made impossible. B is a heuristic that a slow copy over 15 GB can outlast. A naive sweep without the guard was measured to be actively harmful: deleting a concurrent run's in-flight copy makes that run die on the rename with a bare "no such file or directory", writing nothing. The guard removes the possibility, because a live run still holds it and the kernel drops an `flock` when its holder dies however abruptly, both verified. It also settles something it was not added for: two overlapping runs used to copy every view twice and race at the repoint. `get` never takes this guard, so the rule that a materialize must not make a concurrent fetch fail as busy is unchanged.

---

### How does a run report what happened to each view?

**Context**: The result carried `Written`, `Skipped` and `Refused`, and the repoint put a discarded copy into `Skipped`, which the CLI rendered as "already fresh". That is backwards in the one case that matters: a discarded view is not materialized and running again picks it up.

**Options considered**:
- A) Reword the CLI label to "skipped", which is true of both outcomes.
- B) Add a fourth list, `Discarded`.
- C) One outcome per view, carrying a status and a reason.

**Decision**: C. A leaves the reader no reason to re-run and does not touch the MCP surface at all. B keeps a shape where nothing stops a view landing in two lists or none, and where the MCP tool assembles per-status keys by hand, so a status added later would be silently omitted: that is the same two-lists-that-must-agree failure this story already rejected for the materializable predicate and for the show warnings. Under C a view structurally has exactly one outcome, the MCP tool returns the list as it stands, and callers keep `Written()`, `Fresh()`, `Discarded()` and `Refused()` accessors so the CLI and the exit-code rule read as before.

---

### What exit class does a refusal use?

**Context**: The spec said only "non-zero". The help documents exit 5 as a *server* contract error.

**Options considered**:
- A) Exit 5, the contract class.
- B) Exit 1, the "internal/other" class.

**Decision**: B, as `MATERIALIZE_REFUSED`. A refusal is entirely local, and using the server-contract class would mislead anyone scripting on the code.

---

### Does a second materialize run on the same dataset wait or fail?

**Context**: The materialize guard is non-blocking, like every other lock in the package.

**Options considered**:
- A) Wait for the first run.
- B) Fail as busy.

**Decision**: B, matching every other mutating command, which returns `ErrBusy` rather than queuing. There is no point running two at once; they would do identical work. The generic busy message is kept rather than adding a materialize-specific sentinel, since the remedy is the same either way and `lockMutation` already collapses two different contended locks into one `ErrBusy`.

---

### Checked and recorded rather than actioned

- **The sandbox's 2GB memory cap does not bind.** `COPY` streams rather than buffering: the 6.38M-row, 2.92 GB fixture materialized in 5.0s under exactly that cap.
- **Materializing a degraded view does not launder its warning.** `reportUnionView` warns while *constructing* the statement, so the "report CSV is missing on disk" warning fires on every `Open` whether the view is later read from Parquet or not.
- **A corrupt store JSONL cannot be materialized into an empty Parquet.** `read_json` with an explicit `columns=` map does not validate content at `CREATE VIEW`; it fails at query time, so the `COPY` fails too rather than writing zero rows.
- **The trust boundary is unchanged, but the parsing surface is not.** A dataset folder already inherits the trust of whoever can write to it. What changes is that a hostile file there can now be a Parquet container rather than JSONL or CSV.
- **Membership files are not versioned in lockstep with the store.** `mergeUnderLock` repoints only the runs in the merge, so `membershipUnionMulti` reads every other run's membership at its own recorded version. Moot under the view-scoped design, and part of why a per-input fingerprint beats a per-artifact version rule.
