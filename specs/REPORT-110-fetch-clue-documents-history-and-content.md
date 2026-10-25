# cc-data: fetch CLUE documents, history and content into a dataset

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-110

**Status**: **Closed**

## Overview

Three new `cc-data get` commands, and the matching MCP tools, pull a report run's CLUE documents, document edit history and current document content into a dataset, duplicate-free and resumable, from report-server's CLUE export routes (REPORT-109). Each lands in its own identity-keyed store with a plain view of the same name, so a CLUE corpus becomes one `cc-data` pull instead of a set of scripts run from another repository.

The story also sets the contract REPORT-111 (derived CLUE view columns and `clue_patches`) and REPORT-152 (`recent_logs`) build on: a registry of store types with per-type identity. And it reworks the merge so landing Scott's history (6.38M entries, about 15 GB) needs about half a gigabyte of memory rather than the 5 to 7 GB the old engine measured.

## Requirements

### Commands and MCP tools

- **R1. Three `get` verbs.** `cc-data get clue-documents|clue-history|clue-content <run-id> --dataset <ref>`, each with `--refresh`, the same argument checks and output as `get answers` (a result line with `type`, `run_id`, `complete`, `merge_counts`, `coverage`; progress on stderr). No alias to bare `history`, and `get history` gains no CLUE behavior.
- **R2. Help keeps the two histories apart.** `get clue-history`'s help says it is CLUE document edit history and names `get history`; `get history`'s help says it is interactive-state history and names `get clue-history`. `get`'s short help mentions the CLUE data.
- **R3. Three MCP tools.** `get_clue_documents`, `get_clue_history`, `get_clue_content`, taking `dataset`, `run_id` and `refresh` as `get_answers` does and returning the CLI's payload. Their descriptions say a Student ID Mapping run is the intended source and that `get_clue_history` is not `get_history`'s history; `get_clue_history`'s says to fetch `get_clue_documents` first or its owner coverage is not counted. The descriptions carry this because Claude Code keeps only the first 4,096 characters of a server's instructions, and `## Views` falls past them.

### Fetch

- **R4. Per-type route, no decode.** Each type fetches its own route (`/clue/documents`, `/clue/history`, `/clue/content`) through the existing paged path: segment append per page, cursor persisted after the segment fsync, resume from the saved token, one restart on `EXPIRED_CURSOR`, merge, coverage. CLUE items are stored as received apart from the `_fetched_at` and `_run_id` stamps, so `entry_json` and `content_json` keep the server's strings byte for byte.
- **R5. A record without its identity is refused.** A CLUE item whose `doc_id` (and, for history, `entry_id`) is absent, null, not a string or empty fails the fetch with `MALFORMED_ITEM` (exit `ExitContract`), naming the run, type, item and page, before the page is appended. Answers and history keep their behavior.
- **R6. Re-pull and resume semantics** are the existing ones: a re-pull replaces the run's identity set and updates records in place with no duplicates; an interrupted pull resumes and ends with the same record set; a token idle past the server's hour restarts the run's fetch.

### Stores and per-type identity

- **R7. Store types.**

  | Store type | Identity | Store file | Membership file |
  |---|---|---|---|
  | `clue_documents` | `doc_id` | `clue_documents.v<N>.jsonl` | `members_clue_documents_<run>.v<N>.jsonl` |
  | `clue_history` | `doc_id`, `entry_id` | `clue_history.v<N>.jsonl` | `members_clue_history_<run>.v<N>.jsonl` |
  | `clue_content` | `doc_id` | `clue_content.v<N>.jsonl` | `members_clue_content_<run>.v<N>.jsonl` |

  A membership line holds the type's identity columns only.
- **R8. One registry.** Every store type, `answers` and `history` included, is declared once with its identity columns, pins and route, and every place that spelled `answers|history` derives from it: the merge's version cleanup, reindex's recognition, the summary's orphan check and totals, the artifact match `dataset purge` and `dataset delete` use, materialize's store-type test, the view statements and per-run views, and the guard's identity-column inventory. Attachment scanning keeps its explicit two types. Identity values are JSON strings.
- **R9. Existing datasets are untouched.** `answers` and `history` keep their identity, key encoding, store and membership formats byte for byte; a 0.2.x dataset opens, queries, merges and reindexes with no migration. The one visible difference: R14 widens `run_membership`, so a `run_membership` Parquet written by 0.2.x reads as `STALE_MATERIALIZED` until the dataset is materialized again, while the `answers` and `history` copies stay fresh.
- **R10. Pinned columns per type.** Every export field is pinned, per kind, so a view's columns and types don't depend on which documents a pull saw:
  - **Strict, by construction:** `portal_class_id`, `offering_id`, `idx`, `n_records` BIGINT; `found`, `parse_ok`, `is_revert` BOOLEAN; `server_created`, `created` TIMESTAMP; `doc_id`, `entry_id`, `uid`, `context_id`, `owner_type`, `entry_uid`, `entry_json`, `content_json` VARCHAR. `idx` and `created` hold because CLUE-742's `toHistoryRow` emits `idx` only for a safe integer and `created` only within years 1 to 9999, else null. A document's `offering_id` comes from its metadata's `offeringId` only, so a group document whose metadata names none arrives with `offering_id: null` and is kept.
  - **Passed through, strings in CLUE's types:** `type`, `visibility`, `title`, `unit`, `investigation`, `problem`, `group_id`, `network`, `prev_entry_id`, `model`, `action_raw`, `state`, `version`, `last_history_entry_id` VARCHAR.
  - **Passed through, number or boolean in CLUE's types:** `created_at`, `change_count`, `undoable` JSON; REPORT-111 derives typed columns.
  - **Objects and lists:** `tools`, `metadata_sources`, `firestore_metadata`, `rtdb_metadata`, `self` JSON.
  - `_fetched_at` TIMESTAMP and `_run_id` BIGINT as for every store. A field the export adds later is not pinned and widens as today.
- **R11. Merge memory at Scott's scale.** Landing a segment holds only a compact sorted index of the segments being merged (encoded key, offset, length; at most 120 bytes per identity over 1M `clue_history`-shaped identities) and does not grow with the old store or other runs' membership: membership is read and written as a stream in key order, and a membership file out of key order fails the merge closed. This applies to every store type.

### Coverage

- **R12. Coverage counts owners.** For the CLUE types, `queried` is `total_endpoints` (scope owners: users, groups and class owners), `with_data` the distinct `(context_id, uid)` owners holding a record in the run's membership, `empty = queried - with_data`.
  - `clue_documents` and `clue_content` read the owner from their own records.
  - `clue_history` resolves owners through the `clue_documents` store by `doc_id`. When any of the run's history documents is absent there, the download has no `coverage` and the result carries `coverage_note` "owners not counted: fetch clue-documents for run N before clue-history". Coverage is computed only when history lands, so a later documents pull doesn't add it.
  - Coverage streams membership against the store in key order under the per-dataset lock, so another run's merge cannot remove the store version being read.

### Views

- **R13. Three plain views.** `clue_documents`, `clue_history`, `clue_content` are static views over their stores, built as `answers` and `history` are; with no store yet, a typed-empty stand-in carrying that kind's own pinned columns. Materializable. These are the views REPORT-111 redefines in place.
- **R14. Membership and per-run views.** `run_membership` always carries every identity column of every registered kind that has per-run membership: it gains `doc_id` and `entry_id` (NULL on answers and history rows; the four LARA identity columns NULL on CLUE rows), so CLUE stores join it type-qualified with `USING (doc_id)` or `USING (doc_id, entry_id)`. A `clue_history` join on `doc_id` alone fans out, and the catalog says so. Per-run views `clue_documents_<run>`, `clue_history_<run>`, `clue_content_<run>` follow from membership.

### Dataset summary

- **R15.** `dataset show` and `--json` report the three types' totals and each CLUE download's completeness, merge counts and coverage; the orphan, missing-file and incomplete-download warnings cover the new files; `dataset reindex` adopts and rebuilds them.
- **R16. Large-store hint.** `LARGE_UNMATERIALIZED` when a store file is over 4 GiB and its view has no fresh materialized copy, naming the view, its size and `cc-data dataset materialize <ref>`.

### Guidance and documentation

- **R17. Catalog rows, guarded.** `core.md`'s Views gain the three views: purpose, the CLUE-versus-LARA history distinction, joining by `doc_id`, no `remote_endpoint`, and the cohort join. `uid` is a portal `user_id` only for `owner_type = 'user'`, so the join is `TRY_CAST(d.uid AS BIGINT) = m.user_id` plus `portal_class_id = class_id`. `student_id_mapping` has one row per learner (per assignment), so that join repeats a document per assignment: use `SEMI JOIN`/`EXISTS`, or add `d.offering_id = m.offering_id`, which leaves out documents with no offering.

  The per-run views line and the `run_membership` entry gain the CLUE `USING` lists, and the Identity columns section becomes per store with `doc_id` and `entry_id`. `tools.md` gains the three tools, and the researcher guide's view table the three views. REPORT-104's guard passes both ways.
- **R18. Prose that names the stores.** README's command list and dataset layout, the researcher guide's command lists, the skill header's "Fetching data" list (`clue-documents` first), and comment headers stating the identity contract are updated to the per-type contract.

### Tests and acceptance

- **R19. Tests** against a fake report-server serving CLUE-742-shaped items cover:
  - each command end to end;
  - a history pull interrupted mid-document and resumed;
  - a resumed page re-delivered (at-least-once);
  - a re-pull updating in place and removing an out-of-scope record;
  - two runs sharing documents;
  - an `EXPIRED_CURSOR` restart;
  - R5's refusal;
  - byte-for-byte `entry_json` and `content_json`, including `<`, `&`, non-ASCII and a multi-megabyte value;
  - pins holding over sniffable values;
  - coverage for all three types, including history's absent case;
  - reindex adoption;
  - the 0.2.x fixture (R9);
  - the merge-memory benchmark (R11);
  - the guidance guard.
- **R20. Acceptance on staging** *(not yet run: waits on REPORT-109 and CLUE-742 reaching staging)*. Against a run covering staging class `7be899cf…` (a CLUE class hash, REPORT-109 R24's staging class):
  - each command completes and reports counts;
  - the history pull, interrupted and resumed, matches an uninterrupted one;
  - a re-pull adds no duplicates;
  - `dataset show` reports the three types;
  - the counts match REPORT-109's own staging check of that class.

## Technical Notes

- **Wire contract** (REPORT-109, report-service; CLUE half CLUE-742, collaborative-learning).
  - Routes and envelope: `GET /api/v1/reports/:id/clue/{documents,history,content}` with the bulk endpoints' authorization and envelope `{items, next_page_token, total_endpoints}`, `limit` max 500 and an opaque `page_token`.
  - Paging: termination is `next_page_token == null`, and a token idle past an hour is 410 `EXPIRED_CURSOR`. Paging is at-least-once, and an item is never partial.
  - Item content: history items carry no owner. Inner cursors, including the mid-document history cursor, are server-side.
  - Errors: 404 `NOT_FOUND` for a run the caller cannot export, and 422 `UNPROCESSABLE` for a report without per-learner data. 503 `SERVICE_UNAVAILABLE` is retried as transient and surfaces after the retry budget.
- **The store registry** (`internal/store/kind.go`).
  - **The `Kind` fields:**
    - `Name`;
    - `Identity` (columns in key order);
    - `Optional` (identity columns written to membership only when non-empty, `history_id`);
    - `Strict` (refuse absent, null, non-string or empty identity values);
    - `Pinned`;
    - `Route`;
    - `DoubleDecode` (`report_state`, answers and history only);
    - `OwnerStore` (the store whose records carry each record's owner, for R12).
  - **Lookups and keys:** `Kinds()`, `KindOf`, `MustKind`, `Names()`. `Identity` is `[]string` aligned with `Kind.Identity`. `Kind.Key` is the length-prefixed `encodeFields` encoding, with `Decode` and `Field` beside it.
  - **Derived file regexes:** these quote each name and are anchored, so `history` never matches inside `clue_history`. The file-name regexes live in `internal/dataset/storefiles.go`.
- **Identity extraction.** `IdentityFromRecord` scans tokens with `json.Decoder`, top-level keys only, skipping nested values whole. A nested object can precede the identity and hold a key of the same name: `report_state`'s interactive state in `history`, `firestore_metadata`'s `uid`/`context_id` in `clue_documents`. It costs about 1.6 µs per 2.3 KB record, against 7.7 µs for a struct unmarshal. It is not early on `clue_content`, where `content_json` sorts before `doc_id`, but that store has one row per document.
- **Torn lines.** `BuildIndex` checks each segment line with `json.Valid` before the scan. A crash mid-append glues a record's head onto the resumed copy, and the early-stopping scan would otherwise accept that line and land it in the store, where every DuckDB read then fails.
- **Segment index and merge.** `SegmentIndex` is a slice of `(key, offset, length)` sorted with `SliceStable`, where the last occurrence of a key wins. `MergeSource` is an iterator. `MultiSource` k-way merges runs and reads the `(_fetched_at, _run_id)` stamps only for keys several runs share. Fetch stamps a page with `StampLines`, checks strict identity, then writes it with `AppendLines`.
- **Streamed membership.**
  - **Writing:** `(*SegmentIndex).WriteMembership` writes through `fsutil.WriteAtomic0600(path, func(io.Writer) error)`.
  - **Reading:** `MembershipReader` fails on out-of-order keys with `ErrMembershipOrder`.
  - **The covered walk:** `store.Covered` (`Has`, `Drain`) walks other runs' membership in the old store's key order, and `dataset.openCovered` builds it. `Drain` runs after the old store is exhausted, so a disorder past the last old key still fails the merge.
  - **Measured:** about 83 bytes per identity on 1M `clue_history` keys. A 1-record merge over a 1M-record store held by another run peaks at a few MB, against 363 MB before.
- **Key order.** The identity key stays `encodeFields`. Each document's history keys sort contiguously and in the same document order as the `clue_documents` keys, so R12's history-to-documents walk is a single merge pass.
- **Coverage walk** (`internal/fetch/clue_coverage.go`). `ownerCoverage` runs under `DS.Lock()`, taken after `MergeCompact` returns because that lock is not reentrant. Outside the lock, another run's merge of the same kind can delete the store version the manifest names. A read error leaves coverage nil, and an empty run counts 0.
- **Views** (`internal/duck/views.go`).
  - **Stand-ins:** `storeView(k)` falls back to a typed-empty stand-in from `k.Pinned` for every kind. The `answers` and `history` stand-ins gained five pins they lacked, with every existing column keeping its type.
  - **`run_membership`:** `membershipSelect` selects every registered identity column on every member, casting the ones a type lacks to NULL, under `UNION ALL BY NAME`.
  - **Per-kind pins:** pins are per kind on purpose. `ColumnDetector.Map` returns every pinned column whether or not a record carried it, so a shared map would give each CLUE view the other routes' columns as always-NULL. REPORT-111's `h.*` join would then rename them `<name>_1`.
- **Pinned-type behavior** (DuckDB 1.5.5).
  - VARCHAR and JSON accept any value.
  - TIMESTAMP parses `toISOString` output, but refuses the six-digit years past 9999.
  - BIGINT truncates `12.5` silently and fails the whole query on `"abc"`.

  So a strict type is safe only where the export builds the value by construction.
- **Byte-for-byte strings.** Strings survive `AppendPage`'s re-marshal and `read_json` unchanged. That was checked by MD5 for HTML characters, non-ASCII, NUL, control characters and a 15 MB value.
- **The 0.2.x fixture** (`internal/dataset/testdata/v0.2`). It was generated by `main`'s real fetch path. Its copy helper restores each recorded input's mtime with `os.Chtimes`, because materialized freshness compares `<size>-<mtime>` fingerprints.
- **Scale.** Scott's history is about 15 GB in roughly 12,800 pages of 500 entries. Peak disk during a merge is the segment plus the old and new store versions. A materialized `clue_history` is about a tenth of the JSONL.
- **Mixed versions.** Downgrading is unsupported.
  - **Queries:** a cc-data built before this story (0.2.x or `v0.3.0-pre.1`) cannot open a query session on a dataset holding CLUE membership, because its per-run view loop builds `clue_history_<run>` over a view it lacks.
  - **Reindex:** its `dataset reindex` drops the CLUE entries from the manifest and keeps their files.
  - **Purge:** its `dataset purge` removes the CLUE membership files and keeps the store files.
- **`clue_prepull`.** `internal/packages/run.go` still refuses a `clue_prepull` package because "cc-data cannot fetch CLUE data yet". REPORT-152 removes it in the same 0.3.0 release.

## Out of Scope

- Derived columns on the CLUE views, `clue_patches`, and Scott's parquet parity (REPORT-111).
- Recent logs, `clue_prepull` and `clue_source` (REPORT-152).
- Workflow guidance beyond the catalog rows (REPORT-115); retiring the cc-data-studies recipes (REPORT-122).
- Partitioned or append-only stores that avoid rewriting the whole store per merge.
- New columns in `dataset list`'s text table (its `--json` totals gain the keys).
- Teacher documents, comments, and anything else REPORT-109 leaves out.

## Not Yet Implemented

- R20's staging acceptance. It runs once REPORT-109 (report-service #445) and CLUE-742 (collaborative-learning #3052) are deployed to staging, comparing counts with REPORT-109's own staging check of class `7be899cf…`.

## Decisions

### Requirements

- **Merge memory: rework the store engine in this story** (Doug, 2026-10-22). The old engine measured 570 bytes per identity, about 5.0 GB of live heap for a first pull of Scott's history and 6.9 GB for an overlapping one, over the dashboard VM's 4 GB. Streaming membership alone still left 4 to 5 GB.
- **Unresolved history owners omit coverage.** The alternatives were to count documents as `with_data`, which mixes units with `queried`, or to make `with_data` nullable, which changes a stable JSON contract for every type. Omitting `coverage` was already legal.
- **`created_at`, `change_count`, `undoable` pinned JSON, not BIGINT/BOOLEAN.** They pass through from stored CLUE data. One non-numeric value would fail every query on the view, and `12.5` would truncate silently.
- **`LARGE_UNMATERIALIZED` at 4 GiB.** A scan of a 1.04 GiB history-shaped JSONL takes 0.43 s warm, so 1 GiB would fire where queries are fast, and 4 GiB is about 2 s per scan. No hint at all would drop a Jira requirement.
- **`clue_history` membership per entry, not per document.** With streamed membership the per-entry set costs disk (about 0.4 GB at Scott's scale), not memory. Per document would keep an entry upstream removed from a still-covered document forever.
- **No workflow prose in the researcher guide here.** That is REPORT-115's. This story carries the guarded view-table rows and the command lists.
- **REPORT-152 adds its own registry fields** (`ClassKeyed`, `Accumulate`). Unused fields declared here would be untestable. R14 is worded so a kind without per-run membership adds no `run_membership` column.
- **One identity rule across stories.** A CLUE item without its id stops the pull with `MALFORMED_ITEM`, since REPORT-109 guarantees both ids by construction. REPORT-152 fails a log row without its `id` the same way.
- **`idx` and `created` stay strict, guaranteed at the source** (Doug, 2026-10-22). CLUE-742's `toHistoryRow` emits null for a non-safe-integer `index` and for a `created` outside years 1 to 9999. The rejected alternatives were to accept a stray value failing every query, or to pin JSON and derive the typed columns.
- **The cohort join names its grain.** `student_id_mapping` has one row per learner (per assignment), so R17 gives `SEMI JOIN`/`EXISTS` or the `offering_id` match rather than a join that repeats documents.
- **`owner_type` with `user`/`group`/`class`** (CLUE-742, Doug, 2026-10-24). This matches `ownerType` in CLUE's document-axes docs, where `kind` already means a document's preset. A group document's offering comes from metadata only and may be null.
- **R9 states the one stale materialized copy.** R14 changes `run_membership`'s SQL and so its view signature. Only that Parquet goes stale on upgrade, and a test asserts no other view does.

### Implementation

- **Reshape the merge engine before adding any CLUE type.** The registry, then the index, then the covered walk, each behavior-neutral under the existing merge, sweep, crash, fault-injection and concurrency suites, which only see answers and history.
- **`MultiSource` reads stamps only on colliding keys.** Reading every key's stamp would be a second 15 GB read at Scott's scale.
- **Keep the token scan** over a struct unmarshal. Stored records are key-sorted, so the identity comes early in every store but `clue_content`, which holds one row per document.
- **`json.Valid` on segment lines only.** It keeps torn lines out of the store at about 6 µs per record. Old-store lines keep the fast scan, since only validated segment lines reach a store.
- **The memory test also covers the walk.** It asserts the peak heap of a 1-record merge over a 1M-record store held by another run, because an index-only test would pass a map-backed `Covered`.
- **One stand-in rule.** Every kind's typed-empty stand-in is its pins, and `contractStoreColumns` is deleted. A full test run with the widened `answers`/`history` stand-ins showed nothing depends on the narrower shape.
- **Per-kind pins listed field by field** (19, 15 and 10, R10's 39 in all), checked against CLUE-742's `toDocumentRow`, `toHistoryRow` and `toContentRow`. Each view's empty-dataset `DESCRIBE` is compared with its own list.
- **Coverage under the dataset lock.** A test hook inside the walk checks the lock is held. Without it, another run's merge deletes the store version the walk opened.
- **The 0.2.x fixture is committed and its mtimes restored on copy.** The existing suites build datasets with the code under change, so only a fixture written by `main` can show old files still read.
- **`dataset show`'s totals column is widened.** Three of the new type names are 12 to 14 characters, past the old `%-12s`.
- **`(*SegmentIndex).WriteMembership`** replaces the planned separate membership writer, since the index already holds the keys in order.
- **The multi-megabyte `entry_json` is built by the test** rather than committed as a fixture.
