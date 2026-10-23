# CLUE Document Views: `clue_documents`, `clue_history`, `clue_content`, `clue_patches`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-111
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

cc-data gains four SQL views over the CLUE document data REPORT-110 fetches: the three plain views it registers are redefined with derived columns, and a new `clue_patches` view has one row per JSON patch under a unique key that orders them deterministically. A researcher asks "what did the student change, and when" in SQL over stable columns instead of rewriting a JSON-exploding script per study, and Scott's existing recipes port with renames only, except one offering lookup (see R10).

## Project Owner Overview

Scott's CLUE corpus work showed that the history `action` column cannot be the unit of analysis: most documents record their edits as one catch-all `setProgram` action, and the real edits sit one level down, in each entry's JSON patches. Getting the patches out correctly has a trap (many patches tie on time and entry, and DuckDB orders ties arbitrarily) that made his results change between runs until he fixed it. This story puts that fix in one shared view so no researcher meets the trap again.

It also carries the columns Scott's two Parquet files have and REPORT-109's rows lack: the document's class, unit and problem on every history entry and content row, the normalized action and tile on every entry, and tile summaries per document. With those, the Dataflow behavior study (REPORT-189) and the retired recipes (REPORT-122) read cc-data views with column renames rather than rewritten joins.

## Background

REPORT-110 fetches three CLUE stores into a dataset over REPORT-109's routes (report-service #445) and registers three plain views named after them, `clue_documents`, `clue_history` and `clue_content`, each `SELECT *` over its store file as `storeView` does for `answers` and `history` today (`internal/duck/views.go:324-338`). This story redefines those three in place, under the same names and reading the same files, and adds `clue_patches` (split agreed 2026-10-22, channel `stream/researcher-dashboard/clue-cc-data` #904 and #906).

Because the redefined views keep their names and declare their store files, `materializableFrom` (`views.go:674-687`) keeps them eligible for `dataset materialize`, so the Parquet carries the derived columns. A view that declares no files is never materialized: `run_answers` is the existing case (`views.go:364-375`).

Scott's code is the reference for every derived column (cc-data-studies, branch `RIGSE-365-analysis-packages`):

- `sources/clue/recipes/clue-documents/download-history.ts` `normalizeAction`: `/tileMap/{id}` becomes `/tileMap/{tile}` capturing `tile_id`, `/sharedModelMap/{id}` becomes `/sharedModelMap/{sharedModel}` capturing `shared_model_id`, `/rowMap/{id}` becomes `/rowMap/{row}`, each the first occurrence only (JavaScript `replace` with a non-global regex).
- `sources/clue/recipes/clue-documents/download-content.ts` `summarise`: `n_tiles` counts every `tileMap` key, `tile_types` is the sorted distinct `content.type` strings, `tile_type_counts` an object of type to count, `n_dataflow_tiles` the `Dataflow` count, `n_rows`, `n_shared_models` and `n_annotations` the key counts of `rowMap`, `sharedModelMap` and `annotations`, each 0 when the map is absent.
- `studies/clue-dataflow-behavior/build_edits.py`: the patch explode, two positional `unnest`s per level zipped with `range(json_array_length(...))`, giving `rec_idx` and `patch_idx`; a patch's tile is the one in its own path, else the entry's.
- `sources/clue/clue-data-inventory.md:102-118`: the column lists of `content.parquet` and `history.parquet`.

A CLUE history entry (collaborative-learning `src/models/history/history.ts:4-60`) holds `records[]`, each a `TreePatchRecord` with `tree`, `action`, `patches[]` and `inversePatches[]`; `patches` are JSON Patch operations (`op`, `path`, `value`). The history framework allows several trees per document, but CLUE registers only one: the only `putTree` call outside tests is the main document under its own key (`tree-manager.ts:384`), and `git log -S "putTree("` shows no other caller in any version, so every record's `tree` is the document key.

## Requirements

### `clue_documents`

- **R1.** One row per row of REPORT-110's documents store, every store column passed through, plus `tools` as a `VARCHAR[]` list on R21's rule when the stored value is a JSON array (NULL when the store's `tools` is NULL, since absent is not empty, REPORT-109 R13) and `n_tools`, its length (NULL when `tools` is NULL). `created_at` is a `TIMESTAMP` from its epoch milliseconds, on R21's rule.

### `clue_history`

- **R2.** One row per row of the history store, every store column passed through except as R3 to R5 say. `undoable` is a `BOOLEAN`, on R21's rule.
- **R3.** `action`, `tile_id` and `shared_model_id`, derived in SQL from `action_raw` exactly as Scott's `normalizeAction`: each of the three substitutions applies to the first match only, `tile_id` and `shared_model_id` are NULL when the action has no such segment, and all three are NULL when `action_raw` is NULL or empty (`normalizeAction` returns nulls for any falsy `raw`).
- **R4.** The document's columns on every row, joined from the documents store by `doc_id`: `doc_uid` (the document's `uid`) and the shared document column list R10 defines. A history row whose document is not in the documents store keeps its row with those columns NULL; the join never adds or drops a history row.
- **R5.** `created` and `server_created` pass through as REPORT-110's `TIMESTAMP`s, UTC by convention as `logs`' clocks are. `entry_json` as `JSON`, byte-identical to the stored string wherever DuckDB parses it, NULL where it does not; and `entry_json_unparsed`, the stored string where `entry_json` is NULL and the store's is not, NULL otherwise, so no payload is lost and `->>` on `entry_json` never raises.

### `clue_content`

- **R6.** One row per row of the content store, every store column passed through except as R7 to R9 say. `change_count` is a `BIGINT`, on R21's rule.
- **R7.** `content_json` as `JSON` and `content_json_unparsed`, on R5's rule. `parse_ok`: NULL when the stored content is NULL, otherwise whether `content_json` parsed.
- **R8.** Tile summaries derived in SQL from `content_json` exactly as Scott's `summarise`: `n_tiles`, `tile_types` (`VARCHAR[]`, sorted, distinct), `tile_type_counts` (`JSON` object of type to count, `{}` for a document with no typed tiles), `n_dataflow_tiles`, `n_rows`, `n_shared_models`, `n_annotations`. All are NULL when `content_json` is NULL, and 0 (or empty) when it parses and the map is absent. `self_uid`, `self_doc_key` and `self_class_hash` from the `self` object's `uid`, `documentKey` and `classHash` (REPORT-109 R15).
- **R9.** The shared document column list R10 defines, joined from the documents store by `doc_id` on R4's rule.

### The shared document columns

- **R10.** One list of documents-store columns, defined once in code, is joined onto both `clue_history` and `clue_content`: `portal_class_id`, `unit`, `investigation`, `problem`, `offering_id`. Their types are the documents store's own, so the joined columns cannot disagree with `clue_documents`; where there is no documents store to read them from (R14), they come from REPORT-110's pinned type map (A7), not a second list. `offering_id` is the export's merged offering (RTDB, then Firestore, then a group owner's offering, REPORT-109 R7), so Scott's `coalesce(rtdb_offering_id, offering_id, ...)` in `build_candidates.py` becomes `coalesce(offering_id, ...)`; which source supplied it is read from `clue_documents`' `rtdb_metadata ->> 'offeringId'` and `firestore_metadata ->> 'offeringId'`, the one place that port is more than a rename.

### `clue_patches`

- **R11.** One row per patch of every history entry whose `entry_json` parses: `doc_id`, `entry_id`, `idx`, `created`, `server_created`, `action`, `tile_id`, `is_revert`, `rec_idx`, `patch_idx`, `op`, `path`, `value`. `rec_idx` and `patch_idx` are the patch's 0-based positions in `records[]` and in that record's `patches[]`. `action`, `created` and `server_created` are the entry's, as `clue_history` derives them. `tile_id` is the tile in the patch's own `path`, else the entry's `tile_id`. `value` is the patch's `value` as `JSON`: NULL when the patch has none (`remove`), the JSON literal `null` when it sets null.
- **R12.** An entry that does not parse, has no `records`, or whose records have no `patches`, contributes no rows; `records` or `patches` that are not arrays contribute none either, without an error.
- **R13.** `(doc_id, idx, entry_id, rec_idx, patch_idx)` is unique, so a query or window ordered by it returns the same rows in the same order on every run and every engine instantiation, and a window ordered by it has no ties. The view itself applies no `ORDER BY`: a sort over every patch with its value exceeds the sandbox's memory and temp caps at Scott's scale, and a view's order does not survive a filter, join or aggregate anyway. Rows come out in store order, which is fixed but not the key's order.

### Typed scalars REPORT-110 stores as JSON

- **R21.** REPORT-110 stores `created_at`, `change_count` and `undoable` as raw `JSON`, since one bad value under a typed pin fails every query (#910), and leaves the typed columns to this story. Each view replaces the column, under the same name, with its typed form, non-NULL only when the JSON value is of that kind (`created_at` a number, `change_count` an integer, `undoable` a boolean, `tools` an array), so a string `"12"` or a fraction is never silently converted; and adds `<name>_unparsed`, the raw `JSON` where the stored value is non-NULL and the typed one is NULL, NULL otherwise. `entry_json_unparsed` and `content_json_unparsed` (R5, R7) are the same rule for the two payloads.

### All four views

- **R14.** Each view registers with a typed-empty fallback declaring its full column list, so a dataset with no CLUE data, or with only some of the three stores, opens and binds every documented query. A documents store that is absent from the manifest or whose file is missing on disk costs the joined columns of `clue_history` and `clue_content` (NULL, with a warning naming the file when it is missing), never their rows. A documents file that is present but malformed fails every query that reads it, with DuckDB's error naming the file, as a malformed `answers` or `history` store file does.
- **R15.** Each view declares the store files it reads, so all four are materializable and a fetch into any store a view reads makes that view's Parquet stale. `clue_patches` declares the history store's file. A file the manifest records but that is missing on disk is still declared, as `answers`' and `history`'s are, so `dataset materialize` refuses each view that reads it unless partial copies are allowed, and then says how much of its declared input it used.
- **R16.** Materialization returns the same rows as the raw view, and completes for `clue_patches` at Scott's scale inside the sandbox's caps.

### Fixtures and tests

- **R17.** Synthetic fixtures (no real student text: real entries carry it, and the repo is public) covering a multi-record, multi-patch entry, a `setProgram` entry, a revert, an entry tied with a sibling on `(created, entry_id)`'s `created` and on `idx`, an entry whose `idx` and `created` are both NULL (CLUE-742's export nulls an `index` that is not a safe integer and a time outside years 1 to 9999), an entry that does not parse, a record with no `patches`, a `remove` and a set-to-null patch, a history row whose document is absent, and content with untyped tiles, absent maps and unparseable JSON, an R21 value of the wrong kind in each of the four columns, an entry whose payload `JSON.parse` accepts and DuckDB does not (a lone surrogate escape, stored with `parse_ok` true), and on each store a column the store widened beyond REPORT-110's pins. Every fixture document has its own value in every R10 column and `uid`, so a join on the wrong key or a column taken from the wrong document shows. The history fixture's lines are stored out of R13's order, so the order test's result comes from its `ORDER BY` on the key and not from the file.
- **R18.** Tests:
  - the `clue_patches` row count equals the sum of patch array lengths over the parseable fixture entries, computed in Go from the fixture, not with the view's own SQL;
  - no duplicate `(doc_id, entry_id, rec_idx, patch_idx)`, and every `clue_patches` row joins to a `clue_history` row on `(doc_id, entry_id)`;
  - the key list `SELECT doc_id, entry_id, rec_idx, patch_idx FROM clue_patches ORDER BY` R13's key equals the list sorted in Go from the fixture, on two engine instantiations; and a `lag(path)` window over `PARTITION BY doc_id ORDER BY` the key equals the result computed in Go, on a fixture with entries tied on `created` and on `idx`, so a key that left a tie would fail it; the Go sort puts a NULL `idx` after every non-NULL one in its document, as DuckDB's `ORDER BY` does (its default `NULLS LAST`, which the sandbox's `lock_configuration` keeps a query from changing);
  - `action`, `tile_id` and `shared_model_id` match hand-written expectations for `tileMap`, `sharedModelMap` and `rowMap` paths, a path with two `tileMap` segments, an empty action and a NULL action;
  - the lone-surrogate entry keeps its stored `parse_ok` true, has NULL `entry_json` with its bytes in `entry_json_unparsed`, and contributes no `clue_patches` rows;
  - a column the store widened beyond the pins passes through each of the three redefined views and appears in its fallback;
  - every `clue_history` and `clue_content` row carries its document's columns, the documents-absent row keeps NULLs, and the row counts equal the stores';
  - the tile summaries match hand-written expectations per fixture document;
  - each R21 column is typed for a well-formed value and NULL with its `_unparsed` set for a value of the wrong kind (a string number, a fraction, an object), and both are NULL for a NULL value;
  - each view's fallback installs with the full column list on a dataset without the stores;
  - materializing all four and reading them back returns the raw views' rows.

### Guidance

- **R19.** In the same change that registers the views: the four views' entries in `internal/guidance/src/core.md`'s `## Views` (REPORT-104's catalog structure, replacing REPORT-110's stubs for the three it registered) and four rows in `docs/researcher-guide.md`'s view table, so `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` pass.
- **R20.** The guidance says:
  - `created` is the client's clock and `server_created` Firestore's: use `created` for durations within one document and `server_created` across documents (Scott measured the skew at -55 s to +53 s at the 1st and 99th percentiles);
  - entries order by `(idx, entry_id)`, not `idx` alone, because 73 documents in Scott's corpus have duplicate or gapped `idx` values that are distinct entries (REPORT-109 R10), and an entry whose `idx` is NULL (an index the export could not carry as an integer) sorts after the document's indexed entries; and window functions over `clue_patches` order by R13's key or they split one operation into two between runs;
  - the runtime-output trap: patch paths under `program/values/`, `demoOutput` and `tickEntries/` are written by a running program, not the student, and `content/step` and `tickAndProcess` actions are most history entries; recorded as a known trap, not filtered by the view;
  - `action` is not the edit primitive (279 documents record granular actions, 2,604 use the catch-all `setProgram`), so per-edit questions read `clue_patches`;
  - `replace` patches carry the new value only, so detecting a revisited parameter needs the sequence;
  - `value IS NULL` is a patch with no value (a `remove`), while a patch that sets null has a non-NULL `value` whose `json_type` is `'NULL'`; query output renders both as `null`, so the test belongs in the SQL;
  - `entry_json_unparsed` and `content_json_unparsed` are where a payload DuckDB cannot parse lives;
  - `clue_history`'s stored `parse_ok` is the server's `JSON.parse` verdict, while `entry_json IS NOT NULL` is DuckDB's, and `clue_patches` explodes by DuckDB's: the two disagree on a lone surrogate escape (JavaScript accepts it, DuckDB does not) and on trailing commas, `NaN` and `Infinity` (DuckDB accepts them, JavaScript does not);
  - `clue_patches` has no per-run view; scope it to a run through that run's entries, `WHERE (doc_id, entry_id) IN (SELECT doc_id, entry_id FROM clue_history_<run>)`;
  - output that needs order orders by R13's key, since `clue_patches` promises none;
  - a large CLUE dataset is worth materializing, since `clue_patches` explodes every entry on each raw read; and a window over the whole patch table sorts it, so at scale it should read only the columns it needs, leaving `value` out unless the window uses it.

## Technical Notes

### What this story assumes of REPORT-110

Taken first from REPORT-109's closed spec (R13 to R15), the scoping check (#904) and REPORT-110's Jira, then checked against REPORT-110's interface post (#910) and its finished spec on `REPORT-110-clue-fetch` (`specs/REPORT-110-fetch-clue-documents-history-and-content/`, assumed at its head); every assumption below holds there.

- **A1.** Store types and manifest keys `clue_documents`, `clue_history`, `clue_content`, each one `dataset.Store` with one JSONL `File` and a `Columns` map, files named `clue_documents.v1.jsonl` and so on.
- **A2.** Identity: `doc_id` for documents and content, `(doc_id, entry_id)` for history, one current row per identity. R4's join cannot fan out and R13's key is unique only because of this.
- **A3.** Every REPORT-109 item field is a store column under its wire name, plus the `_fetched_at` and `_run_id` bookkeeping the existing stores carry. None is named `action`, `tile_id`, `shared_model_id`, `doc_uid`, any R8 or R10 column on content, or any `*_unparsed` column, so no derived column shadows a stored one.
- **A4.** Pinned types (#910 confirms): `entry_json` and `content_json` `VARCHAR` byte for byte; `tools`, `metadata_sources`, `self`, `firestore_metadata`, `rtdb_metadata`, `created_at`, `change_count` and `undoable` `JSON`; `created` and `server_created` `TIMESTAMP`; `portal_class_id`, `offering_id`, `idx` and `n_records` `BIGINT`; `found`, `parse_ok` and `is_revert` `BOOLEAN`; the CLUE strings `VARCHAR`. `json_type` names an integer `BIGINT` or `UBIGINT`, a fraction or exponent `DOUBLE`, a boolean `BOOLEAN` (checked), which is what R21 tests; a plain `TRY_CAST` from `JSON` converts `"12"`, `12.5` and `"yes"` (checked), which is why R21 does not use one.
- **A5.** REPORT-110 registers its plain views in `internal/duck/views.go` from one store registry, `store.Kind` (its R8), in a single loop over the kinds in `statements()` (its implementation spec), so this story overrides the three CLUE kinds' entries in that loop; its guidance stubs live in `core.md`'s `## Views` and the guide's table. Its per-run `clue_*_<run>` views select `s.*` from the view named after the store (`perStoreView`, `views.go:501-513`), registered after the static views (`statements()`, `views.go:66-82`), so they carry this story's derived columns with no change.
- **A6.** The documents store's `uid` is the owner key REPORT-109 R13 names, and `doc_uid` on history is that value.
- **A7.** REPORT-110 pins every CLUE column's type in its registry, in code, and its stand-ins already carry every pinned column (#910), so R10's types and every fallback's source columns read from that registry rather than a second list.

### Verified in DuckDB (throwaway, 2026-10-22)

Run in the DuckDB 1.5.5 CLI against synthetic fixtures, then the load-bearing ones again through cc-data's own driver (`duckdb-go-bindings v0.10504.0`, `SELECT version()` is `v1.5.4`) in a throwaway test in `internal/duck`, with the same results: the normalization, the explode, `TRY_CAST` of ISO strings to `TIMESTAMP`, `JSON` to `VARCHAR[]`, the byte-identical JSON cast and `lambda x:` syntax.

- Normalization with `regexp_replace` (no `g` flag) and `regexp_extract` matches `normalizeAction` on all three path kinds, a double `tileMap` path (first segment only) and NULL.
- The positional explode: over 359,488 synthetic patches every `patch_idx` equalled the patch's array position, and `(doc_id, entry_id, rec_idx, patch_idx)` was unique. An unparseable entry, an empty `records`, a record without `patches` and a `records` object all yield zero rows with no error.
- `json_extract(patch, '$.value')` gives SQL NULL for an absent value and JSON `null` for a null one.
- `'$.tileMap.*.content.type'` skips tiles with no type, which is why `n_tiles` counts keys rather than types. `histogram` over an empty list is NULL, hence R8's `{}`. `json_keys` of an absent map is NULL, so content `{}` gives NULL counts unless the SQL makes them 0 for parsed content, as R8 requires; and the coalescing must be conditional on `content_json` being non-NULL, or a missing document would get 0s and `{}` too.
- The driver decodes both a JSON `null` and SQL NULL to Go `nil`, so cc-data's rendered output cannot show R11's distinction; `value IS NULL` and `json_type(value)` can.
- `dataset materialize` copies with `COPY (SELECT * FROM <view>) TO ...` (`internal/duck/materialize.go:384`) and nothing in `internal/` turns off `preserve_insertion_order`, so the Parquet holds the raw view's store order.
- `TRY_CAST(x AS JSON)` keeps the input bytes when it parses (spacing, escapes and duplicate keys all survive) and is NULL when it does not; `->>` on an unparseable `VARCHAR` raises and fails the whole query, which is R5's reason for the JSON type.
- DuckDB keeps a view's `ORDER BY` even under `count(*)`. With realistic patch values (28.8M patches, about 5.5 GB of values) the sort fails both a raw `count(*)` (memory cap) and `Materialize` (temp cap); without it the count takes 23 s and the Parquet 62 s (see the Self-Review's sort finding).
- `TRY_CAST(x AS JSON)` is not `JSON.parse`: it accepts `{"a":1,}`, `[1,2,]`, `NaN` and `Infinity`, and rejects a lone surrogate escape such as `"\ud83d"`, which `JSON.parse` and Go's `json.Valid` both accept (checked in the 1.5.5 CLI and through the driver).
- The lambda arrow `x -> ...` is deprecated in 1.5 and collides visually with the JSON `->` operator; the views use `lambda x: ...` or avoid lambdas.

### Code

- `internal/duck/views.go`: `statements()` order matters only for views that read other views; these four read store files, so they need no ordering beyond REPORT-110's placement. `typedEmpty` and `columnsClause` sort keys, so a fallback's column order is alphabetical unless the builder supplies an order.
- `fileMissing` at build time is how `reportUnionView` tells a missing input from a present one (`views.go:222`). With an explicit `columns` map, as every store view passes, DuckDB does not read a `read_json` input when the view is created: a malformed record anywhere in the file installs the view and fails each query that reads the file, with an error naming it (checked through the driver in the implementation). A fallback therefore registers only when a primary's SQL itself fails to bind, so the tests describe each fallback's SQL directly.
- `materializableFrom` and `viewSignature` (`views.go:674-705`): the signature hashes the view's own SQL only, so a view that reads another view by name would keep a fresh-looking Parquet after the other view's definition changed. R15's `clue_patches` therefore reads the history store file itself.
- Guidance: `internal/guidance/guard_test.go` checks `StaticViewNames()` against `core.md`'s `## Views` and `docs/researcher-guide.md`'s `How datasets are organized` section in both directions.

## Out of Scope

- The fetch, the stores, their identity and membership, the plain views and their first guidance stubs (REPORT-110). Recent logs and the pre-pull cleanup (REPORT-152).
- Dataflow-specific classification (`structure`, `parameter`, `runtime`, coalescing into operations): study code over `clue_patches`, as in `build_edits.py`.
- Filtering runtime-output paths or actions out of any view; the guidance records them as a trap.
- `inversePatches`, and reconstructing a document's content at a point in history.
- Scott's corpus-discovery columns (`discovery`, `dataflow_tile_deleted`), which describe how his corpus was found, and `doc_key` (identical to `doc_id`).
- Per-run views for the CLUE stores (`clue_documents_<run>`, `clue_history_<run>`, `clue_content_<run>`), which REPORT-110's R14 registers and which inherit this story's columns unchanged (A5); `clue_patches` gets none (R20).
- The full workflow prose in the Claude skill and MCP guidance (REPORT-115).

## Open Questions

### RESOLVED: Judgment call: `entry_json` and `content_json` as JSON, with the unparseable payloads kept beside them
**Context**: Jira says `entry_json` and `content_json` are "kept as JSON"; REPORT-109 D7 keeps the raw payload; REPORT-110 stores them as VARCHAR.
**Options considered**:
- A) Keep them VARCHAR. No loss, but `->>` raises on one malformed row and fails the whole query.
- B) Cast to JSON with `TRY_CAST`. Always safe, but a payload DuckDB cannot parse is gone from the view.
- C) Cast to JSON and add `*_unparsed`, non-NULL only where the cast failed.

**Decision**: C. The cast is byte-identical where it parses (verified), so Scott's `json_extract(entry_json, ...)` ports unchanged; the extra column is NULL on nearly every row, so it costs next to nothing in the Parquet, unlike a second full copy of the payload.

### RESOLVED: Judgment call: `clue_patches` reads the history store, not the `clue_history` view
**Context**: The scoping check (#904) had `clue_patches` declare no files and read `clue_history`.
**Options considered**:
- A) Read `clue_history` and declare no files, like `run_answers`.
- B) Read the history store file with the same SQL fragments `clue_history` uses, and declare that file.

**Decision**: B. A view with no files is never materialized (`materializableFrom`), and `clue_patches` is the view most worth materializing at Scott's scale; and a view reading another by name would keep a fresh-looking Parquet after the other's definition changed, since `viewSignature` hashes only its own SQL. Sharing the fragments keeps one definition of the normalization.

### RESOLVED: Judgment call: a patch's `tile_id` is its own path's tile, falling back to the entry's
**Context**: Jira lists `tile_id` on `clue_patches` without saying which tile.
**Options considered**:
- A) The entry's `tile_id` (from `action_raw`) on every patch.
- B) The tile in the patch's `path`, else the entry's (`build_edits.py`).

**Decision**: B. It is what Scott's study uses, and it is the only way an `/addTile` or `/content/userAddTile` patch, whose action names no tile, gets one.

### RESOLVED: Low confidence: the sort `clue_patches` carries at Scott's scale
**Context**: R13's `ORDER BY` meets Jira's "two consecutive runs return identical row order" for any query, but DuckDB sorts on every query that reads the raw view, aggregates included, and Scott's corpus could hold tens of millions of patches under the sandbox's 2 GB memory and 4 GB temp caps.
**Options considered**:
- A) Keep the `ORDER BY`, and point the guidance at materializing large CLUE datasets.
- B) Drop it: the view guarantees only that the key is unique, and the acceptance criterion becomes "the same query ordered by the key returns identical order".
- C) Measure at a realistic scale and decide from the number.

**Decision**: A, from C's measurement. A synthetic 7M-entry history (Scott's is 6.86M) exploding to 24.5M patches, read under `threads = 4`, `memory_limit = '2GB'` and `max_temp_directory_size = '4GB'`: `count(*)` took 7.4 s unordered and 8.7 s ordered (18%), a `GROUP BY op` 12.6 s, one document 0.05 s (the filter pushes below the sort), and writing the ordered view to Parquet 23 s (151 MB). Nothing spilled past the caps. The cost is modest, and the materialized Parquet has none, since it is written in order. Superseded: with realistic patch values the sort fails at that scale, and the view has no `ORDER BY` (the Self-Review's sort finding, decided A by Doug, 2026-10-22; R13).

### RESOLVED: Low confidence: a `tree` column on `clue_patches`
**Context**: A patch's `path` is relative to its record's `tree`, and the history framework allows several trees per document. Jira's column list has no `tree`, and Scott's code never reads it.
**Options considered**:
- A) Add `tree`, the record's tree id.
- B) Leave it out; it stays reachable through `entry_json`.

**Decision**: B (stage 8). CLUE registers one tree per document, the main document under its own key (`tree-manager.ts:384`, the only `putTree` call outside tests in any version), so `tree` would be the document key on every row and `path` is never ambiguous. Jira's column list stands.

### RESOLVED: Low confidence: the document columns on `clue_content`, and `offering_id` on both
**Context**: Jira asks for the document columns on `clue_history` and tile summaries on `clue_content`, and also for "the columns of Scott's `history.parquet` and `content.parquet` that REPORT-109's rows lack, so his recipes port with renames only". `content.parquet` has `portal_class_id`, `unit`, `problem` and `offering_id`, and Scott's study reads them from it (`build_population.py`, `build_candidates.py`).
**Options considered**:
- A) R10 as written: one list on both views.
- B) Document columns on `clue_history` only.
- C) Per-view lists matching Scott's files exactly.

**Decision**: A. Jira's parity sentence already puts those columns on `clue_content`, so B misses it; C would keep two lists that differ by `investigation` and `offering_id` for no reader's benefit, where one list keeps the two views from drifting.

## Self-Review

Roles: Senior Engineer, QA Engineer, Education Researcher, Performance Engineer. Each finding below was checked against the code or run in DuckDB before it was written; each had one defensible correction, made in place.

### QA Engineer

#### RESOLVED: The ordering test could not fail
R18 compared `clue_patches`' output across two engine instantiations. Without an `ORDER BY`, DuckDB returns a small JSONL file's rows in file order (checked: a three-line file came back in file order on three runs with `threads = 4`), so deleting R13's ordering would leave that test green. R17 now stores the history fixture out of key order and R18 compares the output with the order computed in Go. (Once the view's `ORDER BY` was dropped, the test orders by the key itself and adds a window check; see the Performance Engineer's sort finding.)

#### RESOLVED: The join tests could pass with a wrong join
A fixture whose documents shared their `unit` or `portal_class_id` could not tell a join on `doc_id` from one on `uid`, or a column read from the wrong document. R17 now gives every fixture document its own value in every joined column.

### Senior Engineer

#### RESOLVED: A corrupt documents file costs `clue_history` its rows
The Technical Notes said a present but unreadable file fails only at query time; in DuckDB 1.5.5 a malformed first record fails `CREATE VIEW`, so `clue_history`'s primary would fail and the view would degrade to empty because of the documents file. Keeping the rows would need a third registration tier that `register` (`internal/duck/engine.go:144-163`) does not have, for a case `dataset reindex` already repairs. R14 now promises the rows only when the documents store is absent or its file missing; the note is corrected. (The implementation found that the CLI check does not carry over: with the explicit `columns` map every store view passes, a malformed file installs the view and fails each query that reads it, so R14 and the note now say that.)

#### RESOLVED: The stand-in has no source for the joined columns' types
With no documents store in the manifest, R10's columns have no recorded types to cast NULLs to. R10 now takes them from REPORT-110's pinned types, recorded as assumption A7 for stage 8.

Second pass. Roles: Performance Engineer, Education Researcher (porting Scott's recipes). Each finding was checked against Scott's scripts and CLUE-742's builders, or run through the implementation plan's Go built as throwaway code with cc-data's driver (DuckDB 1.5.4), `Open` and `Materialize`, and the 1.5.5 CLI.

### Performance Engineer

#### RESOLVED: `clue_patches`' `ORDER BY` fails at Scott's scale once patch values are realistic
The resolved sort question measured 24.5M patches whose whole Parquet was 151 MB, so its patch values were a few bytes each. Rerun with values the size real entries carry: a synthetic 15.2 GB history (9.6M entries, about 1.6 KB each, Scott's being 15 GB at about 2.3 KB) exploding to 28.8M patches with about 5.5 GB of values. Through the plan's own view, `Open` and `Materialize` under the sandbox (`threads = 4`, `memory_limit = '2GB'`, `max_temp_directory_size = '4GB'`): `SELECT count(*) FROM clue_patches` fails with `Out of Memory Error ... (1.8 GiB/1.8 GiB used)`, and `Materialize` refuses `clue_patches` with `failed to offload data block ... (3.7 GiB/3.7 GiB used)` from the temp cap, so the guidance's advice to materialize a large CLUE dataset cannot be followed. The same view without the `ORDER BY` counts the 28.8M rows in 23 s and writes its Parquet in 62 s (2.6 GB), and two runs of `COPY (SELECT entry_id, rec_idx, patch_idx FROM ...) TO csv` over it were byte-identical, starting in file order, since nothing in cc-data turns off `preserve_insertion_order` and unnest and projection keep it. This revisits the resolved decision and R13's contract, which REPORT-189 reads.
**Options considered**:
- A) Drop the view's `ORDER BY`. R13 keeps the key and its uniqueness; the rows come out in store order (REPORT-110's identity-key order, each document's entries contiguous, then array positions), the same on every run; output and window order use the key, as the guidance already says for windows. R16 becomes "the Parquet returns the raw view's rows in the raw view's order", and R18's order test compares with the fixture's store order computed in Go.
- B) Keep the `ORDER BY` and raise the sandbox caps for `Materialize` only. A raw read still fails (the `count(*)` failure is the memory cap, not the temp cap), and the caps are the sandbox's denial-of-service bound.
- C) Keep the `ORDER BY` and write `clue_patches`' Parquet as sorted per-document chunks, a materialized layout no other view has, while raw reads still fail as in B.

**Decision**: A (Doug, 2026-10-22). Long term it is the contract that holds: the view's order helped only a bare `SELECT *`, since a filter, join or aggregate drops it; what Scott's tie problem needed was a unique key, which `rec_idx` and `patch_idx` give; it costs nothing as corpora grow, where B and C keep paying; and Scott's windows already order explicitly. R13, R16, R18 and R20 are updated. The store order is fixed but is REPORT-110's encoded identity-key order, not R13's key, so no test or guidance relies on it. Jira's acceptance line now reads "two consecutive `cc-data query` runs of the same `clue_patches` query ordered by `(doc_id, idx, entry_id, rec_idx, patch_idx)` return identical row order".

### Education Researcher

#### RESOLVED: An empty `action_raw` normalized to `''`, where Scott's gives NULL
`normalizeAction` opens with `if (!raw) return { action: null, ... }`, so an empty action is NULL in `history.parquet`, and CLUE-742's `toHistoryRow` passes an empty `entry.action` through (`extracted(entry.action)`). The plan's `regexp_replace` returned `''` for it (checked through the driver). R3 now makes all three columns NULL for an empty action too, the plan's `clueActionExprs` wraps its input in `nullif(col, '')` (rechecked: NULL), and R18 tests it.

#### RESOLVED: Two parse verdicts on one `clue_history` row
The stored `parse_ok` is the server's `JSON.parse`; `entry_json` is DuckDB's cast, and they differ. Through the driver, an entry carrying a lone surrogate escape (which `JSON.stringify` writes for a split emoji) kept `parse_ok` true, had NULL `entry_json`, and contributed no `clue_patches` rows, while an entry with `NaN` and a trailing comma, stored `parse_ok` false, exploded into patches. Scott's `build_edits.py` called `json_extract` on the raw string of every entry whose action parsed, which raises on such a payload, so his corpus held none outside the filtered step and tick actions; the difference is rare, which is why the view keeps DuckDB's verdict rather than repairing bytes R5 keeps identical. R20 now tells researchers which column is which, R17 adds the lone-surrogate entry, and R18 tests it.

#### RESOLVED: No way stated to scope `clue_patches` to a run
Per-run views come only from membership keys (`perDownloadViews`, `views.go:490-499`), and `clue_patches` has no membership of its own, so there is no `clue_patches_<run>`, and a dataset holding two runs' CLUE data would mix them in every patch query. R20 now gives the scoping through `clue_history_<run>`.

### Third pass

Roles: Upstream Contract Reviewer (CLUE-742 and REPORT-110 as they now stand), Data Steward (where each ported column's value comes from), Security Engineer. Each finding was checked against the upstream commits, Scott's scripts, or the implementation plan's Go built as throwaway code (stand-ins for REPORT-110's names, run through `Open` and `Materialize` with cc-data's driver, DuckDB 1.5.4). The Security Engineer's review (identifiers through `sqlIdent`, literals and paths through `sqlStr`, store column names reaching SQL only as quoted keys, the sandbox's `lock_configuration`) found nothing. The implementation spec's third pass records the plan's own findings.

### Upstream Contract Reviewer

#### RESOLVED: `idx` can be NULL, and nothing fixed where such an entry sorts
CLUE-742's `toHistoryRow` (collaborative-learning #3052, branch `CLUE-742-clue-export-function`) now emits `idx` only for a safe-integer `index` and `created` only for a time in years 1 to 9999, and REPORT-110's fixtures gained an entry with both null. R13's key stays unique, since `entry_id` is the identity, but R18 compares the view under `ORDER BY` with a sort computed in Go, and no fixture had a NULL `idx`, so a Go sort that put NULLs first would pass and then disagree with real data. Through the driver, `ORDER BY` R13's key put the NULL-`idx` entry after its document's indexed entries (`default_null_order` is `NULLS_LAST`), and `SET default_null_order` from a query fails with "the configuration has been locked", so that placement holds for every caller. R17 adds the entry, R18 says where the Go sort puts it, and R20 tells researchers where such entries land.

### Data Steward

#### RESOLVED: `build_candidates.py` reads an offering column no view carries
Scott's `build_candidates.py` (`PORTAL_IDS_SQL`) reads `c.rtdb_offering_id` and `c.offering_id` from `content.parquet`, coalesces them with the logs' `offering_id`, and records which source won as `offering_source`. His `offering_id` is the Firestore metadata's (177 documents) and `rtdb_offering_id` the RTDB node's (3,784). CLUE-742's `documentOffering` already merges them, RTDB first, then Firestore, then a group uid's offering, so R10's `offering_id` is the merged value and no view has `rtdb_offering_id`: that query does not port by renaming, and its `offering_source` can be rebuilt only from `clue_documents`' `rtdb_metadata ->> 'offeringId'` and `firestore_metadata ->> 'offeringId'`. The other qualified column references in `studies/clue-dataflow-behavior/*.py` map to view columns or to the out-of-scope `doc_key` and `dataflow_tile_deleted`. Changing R10's list would revisit its resolved decision and REPORT-189's columns, so it went to Doug.
**Options considered**:
- A) Keep R10. Background says `offering_id` is the export's merged value, so the coalesce collapses to `offering_id` and per-source provenance reads `clue_documents`' metadata columns; the Overview's "renames only" names this one exception.
- B) Add `rtdb_offering_id` and a Firestore counterpart to R10's list, extracted from `clue_documents`' metadata, so the query ports by rename.

**Decision**: A (Doug, 2026-10-23). The merged offering is the fact the analysis uses; provenance is a document fact, already on `clue_documents` raw, and if it becomes a recurring need the place for it is one derived `offering_source` column there, not a copy on every history and content row. B would add two columns to both views and to REPORT-189's contract for one diagnostic. R10 and the Overview name the exception.

### Fourth pass

Roles: a whole-document consistency read (Overview, Background, Requirements, Technical Notes, Out of Scope, Open Questions and Self-Review against each other and against the implementation plan, after three rounds of in-place edits), and the Dataset Lifecycle Reviewer whose materialize finding is recorded in the implementation spec's fourth pass, with R15 updated here. Each finding was checked against the text, REPORT-110's spec on `REPORT-110-clue-fetch`, or `main`; each had one defensible correction, made in place. The rest of the read held: the dropped `tree` column, the decision A on `offering_id` (R10 and the Overview), R11's column list against `cluePatchesView`, and the no-commit-hash rule all agree across both files.

### Consistency read

#### RESOLVED: The sort question's decision still read "keep the `ORDER BY`"
"Low confidence: the sort `clue_patches` carries at Scott's scale" was resolved A, keep the sort, and its decision ended "the materialized Parquet has none, since it is written in order", while R13, the plan's `cluePatchKey` comment and the Technical Notes say the view applies no `ORDER BY`, from the second pass's sort finding. A reader landing on the Open Questions first met the opposite of the contract. The entry now ends with a pointer to the later decision rather than being rewritten, so the measurement it records stays.

#### RESOLVED: Out of Scope said REPORT-110 had yet to decide the per-run CLUE views
"Per-run views for the CLUE stores (`clue_history_<run>`), which REPORT-110's membership work decides" predates REPORT-110's R14, which now registers `clue_documents_<run>`, `clue_history_<run>` and `clue_content_<run>`, and this spec relies on them twice: A5 (they inherit the derived columns) and R20 (scoping `clue_patches` to a run through `clue_history_<run>`). The bullet now names all three as REPORT-110's, inheriting this story's columns, with `clue_patches` getting none.

#### RESOLVED: R15 did not say what declaring a store file means when the file is gone
The implementation spec's fourth pass found that the planned `clueSource` dropped a missing file from the view's declared inputs, so `dataset materialize` wrote `clue_history` and `clue_content` short of the documents store with no refusal and no caveat, against the refusal R14's neighbors (`answers`, `history`, the report unions) give and the researcher guide promises. R15 now says a recorded file missing on disk is still declared, so materializing refuses each view that reads it unless partial copies are allowed. R14's query-time behavior (rows kept, joined columns NULL, one warning) is unchanged.
