# CLUE Document Views: `clue_documents`, `clue_history`, `clue_content`, `clue_patches`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-111

**Status**: **Closed**

## Overview

cc-data gains four SQL views over the CLUE document data REPORT-110 fetches: the three plain views it registers are redefined with derived columns, and a new `clue_patches` view has one row per JSON patch under a unique key that orders them deterministically. A researcher asks "what did the student change, and when" in SQL over stable columns instead of rewriting a JSON-exploding script per study, and Scott's existing recipes port with renames only, except one offering lookup (see R10).

Scott's CLUE corpus work showed that the history `action` column cannot be the unit of analysis: most documents record their edits as one catch-all `setProgram` action, and the real edits sit one level down, in each entry's JSON patches. Getting the patches out has a trap (many patches tie on time and entry, and DuckDB orders ties arbitrarily) that made his results change between runs until he fixed it; this story puts that fix in one shared view. It also carries the columns Scott's two Parquet files have and REPORT-109's rows lack (the document's class, unit and problem on every history entry and content row, the normalized action and tile on every entry, tile summaries per document), so the Dataflow behavior study (REPORT-189) and the retired recipes (REPORT-122) read cc-data views with column renames rather than rewritten joins.

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

- **R10.** One list of documents-store columns, defined once in code, is joined onto both `clue_history` and `clue_content`: `portal_class_id`, `unit`, `investigation`, `problem`, `offering_id`. Their types are the documents store's own, so the joined columns cannot disagree with `clue_documents`; where there is no documents store to read them from (R14), they come from REPORT-110's pinned type map, not a second list. `offering_id` is the export's merged offering (RTDB, then Firestore, REPORT-109 R7), NULL when neither metadata names one, as for a group document with no offering (CLUE-742), so Scott's `coalesce(rtdb_offering_id, offering_id, ...)` in `build_candidates.py` becomes `coalesce(offering_id, ...)`; which source supplied it is read from `clue_documents`' `rtdb_metadata ->> 'offeringId'` and `firestore_metadata ->> 'offeringId'`, the one place that port is more than a rename.

### `clue_patches`

- **R11.** One row per patch of every history entry whose `entry_json` parses: `doc_id`, `entry_id`, `idx`, `created`, `server_created`, `action`, `tile_id`, `is_revert`, `rec_idx`, `patch_idx`, `op`, `path`, `value`. `rec_idx` and `patch_idx` are the patch's 0-based positions in `records[]` and in that record's `patches[]`. `action`, `created` and `server_created` are the entry's, as `clue_history` derives them. `tile_id` is the tile in the patch's own `path`, else the entry's `tile_id`. `value` is the patch's `value` as `JSON`: NULL when the patch has none (every `remove`, and an `add` or `replace` written without one), the JSON literal `null` when it sets null.
- **R12.** An entry that does not parse, has no `records`, or whose records have no `patches`, contributes no rows; `records` or `patches` that are not arrays contribute none either, without an error.
- **R13.** `(doc_id, idx, entry_id, rec_idx, patch_idx)` is unique, so a query or window ordered by it returns the same rows in the same order on every run and every engine instantiation, and a window ordered by it has no ties. The view itself applies no `ORDER BY`: a sort over every patch with its value exceeds the sandbox's memory and temp caps at Scott's scale, and a view's order does not survive a filter, join or aggregate anyway. Rows come out in store order, which is fixed but not the key's order.

### Typed scalars REPORT-110 stores as JSON

- **R21.** REPORT-110 stores `created_at`, `change_count` and `undoable` as raw `JSON`, since one bad value under a typed pin fails every query, and leaves the typed columns to this story. Each view replaces the column, under the same name, with its typed form, non-NULL only when the JSON value is of that kind (`created_at` a number, `change_count` an integer, `undoable` a boolean, `tools` an array), so a string `"12"` or a fraction is never silently converted; and adds `<name>_unparsed`, the raw `JSON` where the stored value is non-NULL and the typed one is NULL, NULL otherwise. `entry_json_unparsed` and `content_json_unparsed` (R5, R7) are the same rule for the two payloads.

### All four views

- **R14.** Each view registers with a typed-empty fallback declaring its full column list, so a dataset with no CLUE data, or with only some of the three stores, opens and binds every documented query. A documents store that is absent from the manifest or whose file is missing on disk costs the joined columns of `clue_history` and `clue_content` (NULL, with a warning naming the file when it is missing), never their rows. A documents file that is present but malformed fails every query that reads it, with DuckDB's error naming the file, as a malformed `answers` or `history` store file does.
- **R15.** Each view declares the store files it reads, so all four are materializable and a fetch into any store a view reads makes that view's Parquet stale. `clue_patches` declares the history store's file. A file the manifest records but that is missing on disk is still declared, as `answers`' and `history`'s are, so `dataset materialize` refuses each view that reads it unless partial copies are allowed, and then says how much of its declared input it used.
- **R16.** Materialization returns the same rows as the raw view, and completes for `clue_patches` at Scott's scale inside the sandbox's caps. *(The scale half was measured on a synthetic corpus during speccing, below; the suite checks the rows.)*

### Fixtures and tests

- **R17.** Synthetic fixtures (no real student text: real entries carry it, and the repo is public) covering a multi-record, multi-patch entry, a `setProgram` entry, a revert, an entry tied with a sibling on `created` and on `idx`, an entry whose `idx` and `created` are both NULL (CLUE-742's export nulls an `index` that is not a safe integer and a time outside years 1 to 9999), an entry that does not parse, a record with no `patches`, a `remove` and a set-to-null patch, a history row whose document is absent, content with untyped tiles, absent maps and unparseable JSON, an R21 value of the wrong kind in each of the four columns, an entry whose payload `JSON.parse` accepts and DuckDB does not (a lone surrogate escape, stored with `parse_ok` true), and on each store a column the store widened beyond REPORT-110's pins. Every fixture document has its own value in every R10 column and `uid`, so a join on the wrong key or a column taken from the wrong document shows. The history fixture's lines are stored out of R13's order, so the order test's result comes from its `ORDER BY` on the key and not from the file.
- **R18.** Tests:
  - the `clue_patches` row count equals the sum of patch array lengths over the parseable fixture entries, computed in Go from the fixture, not with the view's own SQL;
  - no duplicate `(doc_id, entry_id, rec_idx, patch_idx)`, and every `clue_patches` row joins to a `clue_history` row on `(doc_id, entry_id)`;
  - the key list ordered by R13's key equals the list sorted in Go from the fixture, on two engine instantiations; and a `lag(path)` window over `PARTITION BY doc_id ORDER BY` the key equals the result computed in Go, on a fixture with entries tied on `created` and on `idx`, so a key that left a tie would fail it; the Go sort puts a NULL `idx` after every non-NULL one in its document, as DuckDB's default `NULLS LAST` does (the sandbox's `lock_configuration` keeps a query from changing it);
  - `action`, `tile_id` and `shared_model_id` match hand-written expectations for `tileMap`, `sharedModelMap` and `rowMap` paths, a path with two `tileMap` segments, an empty action and a NULL action;
  - the lone-surrogate entry keeps its stored `parse_ok` true, has NULL `entry_json` with its bytes in `entry_json_unparsed`, and contributes no `clue_patches` rows;
  - a column the store widened beyond the pins passes through each of the three redefined views and appears in its fallback;
  - every `clue_history` and `clue_content` row carries its document's columns, the documents-absent row keeps NULLs, and the row counts equal the stores';
  - the tile summaries match hand-written expectations per fixture document;
  - each R21 column is typed for a well-formed value and NULL with its `_unparsed` set for a value of the wrong kind (a string number, a fraction, an object), and both are NULL for a NULL value;
  - each view declares the full column list on a dataset with no CLUE stores, and each view's registered fallback SQL, described directly since a JSONL file with explicit columns never makes the primary fail, declares the primary's columns;
  - a store column named like each column a view adds leaves the view's value under the documented name and passes the store's through as `<name>_source`;
  - `entry_json` and `content_json` keep a stored payload's whitespace and escapes byte for byte, a `created_at` outside the `TIMESTAMP` range is NULL with its value in `created_at_unparsed`, and each `clue_patches` `value` is the fixture's own value;
  - materializing all four and reading them back returns the raw views' rows.

### Guidance

- **R19.** In the same change that registers the views: the four views' entries in `internal/guidance/src/core.md`'s `## Views` (replacing REPORT-110's stubs for the three it registered) and four rows in `docs/researcher-guide.md`'s view table, so `TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` pass.
- **R20.** The guidance says:
  - `created` is the client's clock and `server_created` Firestore's: use `created` for durations within one document and `server_created` across documents (Scott measured the skew at -55 s to +53 s at the 1st and 99th percentiles);
  - entries order by `(idx, entry_id)`, not `idx` alone, because 73 documents in Scott's corpus have duplicate or gapped `idx` values that are distinct entries (REPORT-109 R10), and an entry whose `idx` is NULL sorts after the document's indexed entries; and window functions over `clue_patches` order by R13's key or they split one operation into two between runs;
  - the runtime-output trap: patch paths under `program/values/`, `demoOutput` and `tickEntries/` are written by a running program, not the student, and `content/step` and `tickAndProcess` actions are most history entries; recorded as a known trap, not filtered by the view;
  - `action` is not the edit primitive (279 documents record granular actions, 2,604 use the catch-all `setProgram`), so per-edit questions read `clue_patches`;
  - `replace` patches carry the new value only, so detecting a revisited parameter needs the sequence;
  - `value IS NULL` is a patch with no value (every `remove`, and an `add` or `replace` written without one), while a patch that sets null has a non-NULL `value` whose `json_type` is `'NULL'`; query output renders both as `null`, so the test belongs in the SQL, and removals are found by `op`;
  - `clue_content`'s `parse_ok` is DuckDB's verdict, where `clue_history`'s is the server's;
  - `entry_json_unparsed` and `content_json_unparsed` are where a payload DuckDB cannot parse lives;
  - `clue_history`'s stored `parse_ok` is the server's `JSON.parse` verdict, while `entry_json IS NOT NULL` is DuckDB's, and `clue_patches` explodes by DuckDB's: the two disagree on a lone surrogate escape (JavaScript accepts it, DuckDB does not) and on trailing commas, `NaN` and `Infinity` (DuckDB accepts them, JavaScript does not);
  - `clue_patches` has no per-run view; scope it to a run through that run's entries, `WHERE (doc_id, entry_id) IN (SELECT doc_id, entry_id FROM clue_history_<run>)`;
  - output that needs order orders by R13's key, since `clue_patches` promises none;
  - a large CLUE dataset is worth materializing, since `clue_patches` explodes every entry on each raw read; and a window over the whole patch table sorts it, so at scale it should read only the columns it needs, leaving `value` out unless the window uses it.

## Technical Notes

### Reference code

Scott's code is the reference for every derived column (cc-data-studies, branch `RIGSE-365-analysis-packages`):

- `sources/clue/recipes/clue-documents/download-history.ts` `normalizeAction`: `/tileMap/{id}` becomes `/tileMap/{tile}` capturing `tile_id`, `/sharedModelMap/{id}` becomes `/sharedModelMap/{sharedModel}` capturing `shared_model_id`, `/rowMap/{id}` becomes `/rowMap/{row}`, each the first occurrence only (JavaScript `replace` with a non-global regex).
- `sources/clue/recipes/clue-documents/download-content.ts` `summarise`: the R8 columns, each 0 when the map is absent.
- `studies/clue-dataflow-behavior/build_edits.py`: the patch explode, two positional `unnest`s per level zipped with `range(json_array_length(...))`, giving `rec_idx` and `patch_idx`; a patch's tile is the one in its own path, else the entry's.
- `sources/clue/clue-data-inventory.md`: the column lists of `content.parquet` and `history.parquet`.

A CLUE history entry (collaborative-learning `src/models/history/history.ts`) holds `records[]`, each a `TreePatchRecord` with `tree`, `action`, `patches[]` and `inversePatches[]`; `patches` are JSON Patch operations (`op`, `path`, `value`). CLUE registers one tree per document, the main document under its own key (`tree-manager.ts`, the only `putTree` call outside tests in any version), so every record's `tree` is the document key.

### What this builds on in REPORT-110

See `specs/REPORT-110-fetch-clue-documents-history-and-content.md`.

- Store types `clue_documents`, `clue_history`, `clue_content`, each one JSONL store with a recorded `Columns` map (pins plus every field observed). Identity is `doc_id` for documents and content and `(doc_id, entry_id)` for history, one current row per identity, which is why R4's join cannot fan out and R13's key is unique.
- Every REPORT-109 item field is a store column under its wire name, plus `_fetched_at` and `_run_id`; none collides with a derived column name. A widened field that did would take the documented name, since DuckDB renames the view's later column to `<name>_1` without an error, so each view passes a store column named like one it adds through as `<name>_source`, REPORT-112's rule for `logs` (`sourceSuffix`, repeated until the name is free).
- Pinned types: `entry_json` and `content_json` `VARCHAR` byte for byte; `tools`, `metadata_sources`, `self`, the two metadata objects, `created_at`, `change_count` and `undoable` `JSON`; `created` and `server_created` `TIMESTAMP`; `portal_class_id`, `offering_id`, `idx` and `n_records` `BIGINT`; `found`, `parse_ok` and `is_revert` `BOOLEAN`.
- The registry (`store.Kind`, `store.MustKind`) carries each kind's pins; `cluePins` reads them, so R10's stand-in types and every fallback's source columns come from one place.
- `statements()` builds every store view in one loop over the kinds. `derivedStoreViews` in `views.go` replaces a kind's plain `storeView` with its derived view under the same name; REPORT-152 registers `recent_logs` through the same map. The per-run `clue_*_<run>` views select `s.*` from the view named after the store, so they carry the derived columns with no change.

### DuckDB behavior the views rely on

Checked in the DuckDB 1.5.5 CLI and through cc-data's driver (DuckDB 1.5.4):

- `regexp_replace` without the `g` flag and `regexp_extract` match `normalizeAction`, first segment only.
- The positional explode gives each patch its array position, and an unparseable entry, an empty `records`, a record without `patches` and a `records` object all yield zero rows with no error.
- `json_extract(patch, '$.value')` is SQL NULL for an absent value and JSON `null` for a null one; the driver decodes both to Go `nil`, so rendered output cannot show the difference and R20 points researchers at `value IS NULL` and `json_type(value)`.
- `json_type` names an integer `BIGINT` or `UBIGINT`, a fraction or exponent `DOUBLE`, a boolean `BOOLEAN`, which is what R21 tests; a plain `TRY_CAST` from `JSON` converts `"12"`, `12.5` and `"yes"`, which is why R21 does not use one.
- `TRY_CAST(x AS JSON)` keeps the input bytes when it parses and is NULL when it does not; `->>` on an unparseable `VARCHAR` raises and fails the whole query, which is R5's reason for the JSON type. It is not `JSON.parse`: it accepts trailing commas, `NaN` and `Infinity` and rejects a lone surrogate escape, which `JSON.parse` and Go's `json.Valid` accept.
- `'$.tileMap.*.content.type'` skips tiles with no type, so `n_tiles` counts keys; `histogram` over an empty list is NULL, hence R8's `{}`; `json_keys` of an absent map is NULL, so the counts are coalesced to 0 only when `content_json` is non-NULL.
- With the explicit `columns` map every store view passes, `read_json` does not read the file at `CREATE VIEW`: a malformed record installs the view and fails each query that reads the file. A fallback therefore registers only when a primary's SQL fails to bind, and the tests describe each fallback's SQL directly.
- A view's `ORDER BY` survives even `count(*)`. With realistic patch values (28.8M synthetic patches, about 5.5 GB of values) the sort failed a raw `count(*)` on the memory cap and `Materialize` on the temp cap; without it the count took 23 s and the Parquet 62 s (2.6 GB). `Materialize` copies with `COPY (SELECT * FROM <view>)` and nothing turns off `preserve_insertion_order`, so the Parquet holds the raw view's store order.
- The lambda arrow `x -> ...` is deprecated in 1.5 and reads like the JSON `->` operator; the views use `lambda x: ...`.

### Checked on real patches

One Dataflow student document from Scott's corpus, redacted and seeded on the local stack, has 4,029 entries and 8,465 patches. 3,493 of the patches carry no `value` key: its 3,241 `remove`s and 252 `add` and `replace` patches. Its entries, patches and kinds by action matched the source's counts, and the key was unique and its ordered read the same on every run, raw and materialized.

### Code

- `internal/duck/clue_views.go` holds the four builders and their helpers: `clueSource` (a store's `read_json`, or its typed-empty stand-in), `clueView` (the fallback is the primary's SQL over stand-ins), `typedScalar` (R21's typed and `_unparsed` columns from one name), `clueRename` (a clashing store column's `_source` name), `clueActionExprs`, `clueDocJoin` and `cluePatchKey`.
- `clueSource` reads the store's recorded `Columns` plus the pins at their pinned types, so widened fields pass through and a manifest that lacks a pin the derived SQL names still opens. A store file missing on disk gives a typed-empty relation but is still declared (R15); only the store's own view warns, so the file is reported once.
- `clue_content` computes its parsed content and tile-type list in `CROSS JOIN LATERAL` relations referenced as `cj.j` and `ct.types`, so a store column of the same name cannot shadow them and make every view on the dataset fail to open.
- `clue_patches` reads the history store file with the same fragments `clue_history` uses, not the `clue_history` view: a view that declares no files is never materialized, and `viewSignature` hashes only a view's own SQL, so reading another view by name would keep a stale Parquet looking fresh.
- `clue_history` and `clue_content` declare the documents file only when their own store has a file (`clueDeclared`), so a dataset with only documents does not materialize them empty.
- `incompleteRuns` in `materialize.go` admits, for a store download, any view whose declared files hold that store's file (its own view among them), so an incomplete `clue_documents` run caveats `clue_history` and `clue_content` and an incomplete `clue_history` run caveats `clue_patches`.
- The lone-surrogate fixture entry is written as raw text: Go's `encoding/json` turns `\ud83d` into U+FFFD both ways, which DuckDB parses.
- `internal/guidance/guard_test.go` checks `StaticViewNames()` against `core.md`'s `## Views` and the researcher guide's view table in both directions.
- `cluePatchKey` is the key the tests order by, and `TestClueGuidanceNamesThePatchKey` checks that `core.md` and the researcher guide spell it the same way.

## Out of Scope

- The fetch, the stores, their identity and membership, the plain views and their first guidance stubs (REPORT-110). Recent logs and the pre-pull cleanup (REPORT-152).
- Dataflow-specific classification (`structure`, `parameter`, `runtime`, coalescing into operations): study code over `clue_patches`, as in `build_edits.py`.
- Filtering runtime-output paths or actions out of any view; the guidance records them as a trap.
- `inversePatches`, and reconstructing a document's content at a point in history.
- Scott's corpus-discovery columns (`discovery`, `dataflow_tile_deleted`), which describe how his corpus was found, and `doc_key` (identical to `doc_id`).
- Per-run views for the CLUE stores, which REPORT-110 registers and which inherit this story's columns unchanged; `clue_patches` gets none (R20).
- The full workflow prose in the Claude skill and MCP guidance (REPORT-115).

## Decisions

### Requirements

- **`clue_patches` has no `ORDER BY`; the unique key is the contract** (Doug, 2026-10-22). With realistic patch values the sort fails both raw reads and `Materialize` at Scott's scale. Raising the caps for `Materialize` would not fix raw reads, and a sorted-chunk Parquet layout would be unique to one view. The view's order only ever helped a bare `SELECT *`; what Scott's tie problem needed was a unique key. Jira's acceptance line reads "two consecutive `cc-data query` runs of the same `clue_patches` query ordered by `(doc_id, idx, entry_id, rec_idx, patch_idx)` return identical row order".
- **`entry_json` and `content_json` as JSON, with `*_unparsed` beside them.** VARCHAR lets one malformed row fail any `->>` query; a bare `TRY_CAST` loses the payload DuckDB cannot parse. The cast is byte-identical where it parses, so Scott's `json_extract` calls port unchanged, and the extra column is NULL on nearly every row.
- **`clue_patches` reads the history store, not the `clue_history` view**, so it declares a file, is materializable, and goes stale when its SQL changes.
- **A patch's `tile_id` is its own path's tile, else the entry's**, as `build_edits.py` has it; it is the only way an `/addTile` patch, whose action names no tile, gets one.
- **No `tree` column** (stage 8). CLUE registers one tree per document, so it would be the document key on every row.
- **One document-column list on both `clue_history` and `clue_content`.** Jira's parity sentence puts them on content too, and two lists would drift.
- **`offering_id` stays the merged value; no per-source columns** (Doug, 2026-10-23). Provenance is a document fact already on `clue_documents` raw; if it becomes a recurring need, it belongs there as one derived `offering_source` column, not copied onto every history and content row and into REPORT-189's contract.
- **An empty `action_raw` gives NULL `action`, `tile_id` and `shared_model_id`**, as `normalizeAction`'s falsy check does; `clueActionExprs` wraps its input in `nullif(col, '')`.
- **The view keeps DuckDB's parse verdict** rather than repairing bytes R5 keeps identical. The stored `parse_ok` and `entry_json IS NOT NULL` can disagree, rarely, and R20 says which is which.
- **`clue_patches` is scoped to a run through `clue_history_<run>`.** It has no membership of its own, so it gets no per-run view.
- **A NULL `idx` sorts last in its document.** DuckDB's default is `NULLS LAST` and the sandbox's locked configuration keeps a query from changing it, so the Go sort in the tests matches.
- **A malformed documents file fails queries rather than costing `clue_history` its rows.** Keeping the rows would need a third registration tier for a case `dataset reindex` already repairs.
- **A missing store file is still declared**, so `dataset materialize` refuses or caveats the views that read it, as it does for `answers` and `history`, rather than writing a short Parquet that reads as fresh.

### Implementation

- **Each fallback is its primary's SQL over typed-empty stand-ins**, so a fallback cannot declare a different column or type from its primary.
- **The CLUE builders live in `clue_views.go`; `derivedStoreViews` lives in `views.go`** beside the loop that consults it, where REPORT-152 adds `recent_logs`. The stacked branches then share only the loop and the map.
- **Four view commits and a prose commit**, each keeping the guidance guard green.
- **`clueSource` warns only from the store's own view**, so a missing documents file prints one line, not three.
- **`typedScalar` renders its typed, `REPLACE` and `_unparsed` columns from one name**, so a rename cannot leave them apart.
- **Each helper arrives with the view that first uses it.** `go vet`, CI's only static check, does not report an unused package-level variable.
- **`clueSource` reads the store's recorded `Columns`, not only the pins**, so a field the export adds later passes through every view, as `storeView` does.
- **`incompleteRuns` admits views by declared file, not only by type name**, so an incomplete download caveats every view that reads its store.
- **Lateral relations, not CTE columns, for `clue_content`'s parsed content.** A store column named `j` made the CTE form fail its primary and fallback alike, which failed `Open` for the whole dataset. The lateral form measured within noise of the CTE form on 30,000 documents.
