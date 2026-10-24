# cc-data: fetch CLUE documents, history and content into a dataset

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-110
**Repo**: https://github.com/concord-consortium/cc-data-cli
**Implementation Spec**: [implementation.md](implementation.md)
**Status**: **In Development**

## Overview

Three new `cc-data get` commands, and the matching MCP tools, pull a report run's CLUE documents, document edit history and current document content into a dataset, duplicate-free and resumable, from report-server's CLUE export routes (REPORT-109). Each lands in its own identity-keyed store with a plain view of the same name, so a CLUE corpus becomes one `cc-data` pull instead of a set of scripts run from another repository.

## Project Owner Overview

Scott's CLUE Dataflow analysis starts from a corpus of 4,674 documents and about 6.38 million history entries that today comes from shell scripts holding a borrowed service-account key. REPORT-109 gave report-server routes that export the same data for a run the researcher owns. This story is the client half: a researcher creates a Student ID Mapping run for the cohort and runs `cc-data get clue-documents`, `clue-history` and `clue-content` on it, and the data lands in their dataset next to the run's answers and logs, queryable at once, safe to re-pull, and resumable after an interruption.

It also sets the contract the next two stories build on. REPORT-111 redefines the three plain views with derived columns (Scott's parquet parity) and adds `clue_patches`; REPORT-152 reuses the per-type identity this story introduces for its `recent_logs` store. And it makes the merge that lands a pull hold up at Scott's scale: the store engine as it stands would need about 3.6 GB of memory to land his history, which this story brings down to about half a gigabyte.

## Background

**The wire contract (REPORT-109, closed spec `specs/REPORT-109-clue-document-export-api.md` in report-service, PR #445, in review; CLUE half CLUE-742, collaborative-learning PR #3052).** Three routes, `GET /api/v1/reports/:id/clue/documents`, `/clue/history` and `/clue/content`, with the bulk endpoints' authorization, envelope `{items, next_page_token, total_endpoints}`, `limit` (max 500) and opaque `page_token`; termination is `next_page_token == null`; 410 `EXPIRED_CURSOR` after an hour idle; paging is at-least-once, every item carrying a stable identity (`doc_id`, or `(doc_id, entry_id)`); an item is never partial (R9 to R12). `total_endpoints` is the number of **scope owners** (users, groups and class owners), not `remote_endpoint`s. Item shapes are R13 to R15; `entry_json` and `content_json` are the stored strings byte for byte, and history items carry no owner (D7). Inner cursors, including the mid-document history cursor, are server-side (R10): the client only echoes `page_token`.

**What cc-data has today (verified against `main`).**
- *Fetch.* `fetch.FetchPaged` (`internal/fetch/paged.go`) is the one path for `answers` and `history`: per-download lock, segment append per page with `_fetched_at` and `_run_id` stamps and an fsync before the cursor write, resume from the saved token, a single restart on `EXPIRED_CURSOR`, merge, coverage. It builds the route as `/api/v1/reports/<run>/<type>` and double-decodes `report_state` on every item.
- *Identity.* `store.Identity` (`internal/store/identity.go`) is a fixed four-field struct (`source_key`, `remote_endpoint`, `question_id`, `history_id`), and `Key(typ)` branches on `TypeHistory`. Membership files (`members_<type>_<run>.v<N>.jsonl`), the `run_membership` view (fixed four identity columns), `perStoreView` (hard-coded `USING` list), and the guidance guard's `IdentityColumnNames()` (from `membershipColumns`) all assume those four fields.
- *Type lists.* The store types `answers|history` are spelled independently in `storeVersionRe` (`dataset/merge.go`), `reindexStoreRe` and `reindexMemberRe` (`dataset/reindex.go`), `finalArtifactRe` and `manifestTotals` (`dataset/summary.go`), `isStoreType` (`duck/materialize.go`) and `statements()` (`duck/views.go`).
- *Pinned columns.* `store.pinnedColumns` is one global map applied to every store type.
- *Coverage.* `computeCoverage` counts distinct `remote_endpoint`s in the run's membership. CLUE items have none, so reused as is it reports every owner empty (REPORT-109 Technical Notes).
- *Merge memory.* `mergeUnderLock` builds a `SegmentIndex` (a map of key to a struct holding a copy of the whole `Identity`), copies the identities again for membership, and builds `covered` as a map holding every identity of every run of the type. Measured with a throwaway test on 1,000,000 history-shaped records (two 20-character id fields, the CLUE identity's size): **570 bytes of heap per identity** (index 337 MB, identity copies 61 MB, `covered` 145 MB), so about **3.6 GB** for Scott's 6.38M entries before Go's GC headroom, which can double it. A sorted slice of `(key, offset, length)` measured **80 bytes per identity** on the same records, about 510 MB for 6.38M. Through the real `MergeCompact` path (1M history-shaped entries, peak heap in use sampled every 20 ms): a first pull peaked at **783 MB**, and a second run's pull of the same identities, which also reads the first run's membership into `covered`, at **1,082 MB**. Scaled to Scott's 6.38M entries that is about **5.0 GB** and **6.9 GB** of live heap, before GC headroom.
- *Whole-store rewrite.* Every merge streams the old store and writes a complete new version. For a 15 GB history store that is 15 GB written per landed pull, with peak disk of roughly the segment plus the old and new store versions.

**Byte-for-byte strings (verified).** A throwaway test appended records through `Segment.AppendPage` (which unmarshals each record into a map and re-marshals it, escaping `<`, `>` and `&`) and read them back with `read_json(..., columns={'content_json':'VARCHAR'})` in the module's DuckDB (go-duckdb `v2.10504.0`): the MD5 of the value matched the original for HTML characters, non-ASCII, an embedded NUL, control characters and a 15 MB value whose record was 27 MB. No `maximum_object_size` is needed for a 27 MB newline-delimited record. A lone UTF-16 surrogate escape (`"\ud800"`) fails `read_json` for the whole file, but Firestore and the RTDB store UTF-8 and cannot hold one.

**How a pinned column behaves on a value of another type (verified, DuckDB 1.5.5 CLI).** With `read_json(..., columns=...)`: `VARCHAR` accepts any value (a number becomes its text, an object its JSON text); `JSON` accepts any value with its JSON type kept; `TIMESTAMP` parses `2025-09-01T12:00:00.000Z` and `...00Z`; `BIGINT` turns the string `"12"` into 12, **silently truncates** `12.5` to 12, and **fails the whole query** on `"abc"`. So a strict type is safe only where the export builds the value by construction.

**The two `history`s.** `get history` and the `history` store are LARA interactive-state history from report-service's Firestore. CLUE document history is a different thing from a different source. The CLI keeps them apart by name everywhere (`clue-history`, `clue_history`), and each one's help names the other.

**Scope split (Doug, 2026-10-22, `stream/researcher-dashboard/clue-cc-data` #906).** REPORT-110 owns the whole CLUE fetch: the three commands and MCP tools, per-type fetch and coverage, the three stores with per-type identity, the plain views, and their guidance. REPORT-111 redefines the plain views in place (same names, same `views.go`) and adds `clue_patches`. REPORT-152 owns recent logs and the `clue_prepull`/`clue_source` removal. Implementation stacks 110, then 111, then 152.

## Requirements

### Commands and MCP tools

- **R1. Three `get` verbs.** `cc-data get clue-documents <run-id> --dataset <ref>`, `get clue-history <run-id> --dataset <ref>` and `get clue-content <run-id> --dataset <ref>`, each with `--refresh` (delete the segment and re-fetch from the start), the same argument checks and output as `get answers` (a result line with `type`, `run_id`, `complete`, `merge_counts`, `coverage`; progress on stderr). There is no alias to bare `history`, and `get history` gains no CLUE behavior.
- **R2. Help keeps the two histories apart.** `get clue-history`'s help says it is CLUE document edit history and names `get history`; `get history`'s help says it is interactive-state history and names `get clue-history`. `get`'s own short help mentions the CLUE data.
- **R3. Three MCP tools.** `get_clue_documents`, `get_clue_history` and `get_clue_content`, taking `dataset`, `run_id` and `refresh` exactly as `get_answers` does, returning the same result payload as the CLI. Their descriptions state that a Student ID Mapping run is the intended source and that `get_clue_history` is CLUE document history, not `get_history`'s; `get_clue_history`'s also says to fetch `get_clue_documents` for the run first, or its owner coverage is not counted (R12). The descriptions carry these because an MCP client may never see the catalog: Claude Code keeps only the first 4,096 characters of a server's instructions, and `## Views` falls past them (REPORT-115).

### Fetch

- **R4. Per-type route, no decode.** Each type fetches its own route (`/clue/documents`, `/clue/history`, `/clue/content`) through the existing paged path: segment append per page, cursor persisted after the segment fsync, resume from the saved token, one restart on `EXPIRED_CURSOR`, merge, coverage. CLUE items are stored as received apart from the `_fetched_at` and `_run_id` stamps: no double-decode and no other rewriting of any field, so `entry_json` and `content_json` keep the server's strings byte for byte (Background).
- **R5. A record without its identity is refused.** A CLUE item whose identity field (`doc_id`; and `entry_id` for history) is absent, null, not a string, or empty fails the fetch with a contract error naming the run and type, before the page is appended. (Answers and history keep their current behavior.)
- **R6. Re-pull and resume semantics** are the existing ones: re-pulling a run replaces its identity set and updates records in place with no duplicates; an interrupted pull resumes from the saved token and ends with the same record set as an uninterrupted one; a token idle past the server's hour restarts the run's fetch from the beginning.

### Stores and per-type identity (the interface REPORT-111 and REPORT-152 build on)

- **R7. Store types.** Three store types, each with an ordered identity:

  | Store type | Identity | Store file | Membership file |
  |---|---|---|---|
  | `clue_documents` | `doc_id` | `clue_documents.v<N>.jsonl` | `members_clue_documents_<run>.v<N>.jsonl` |
  | `clue_history` | `doc_id`, `entry_id` | `clue_history.v<N>.jsonl` | `members_clue_history_<run>.v<N>.jsonl` |
  | `clue_content` | `doc_id` | `clue_content.v<N>.jsonl` | `members_clue_content_<run>.v<N>.jsonl` |

  A membership line holds the type's identity columns only (`{"doc_id":"..."}`, `{"doc_id":"...","entry_id":"..."}`).
- **R8. One registry.** Every store type (the existing `answers` and `history` and the three above) is declared once, with its identity columns, its pinned columns and its route segment, and every place that today spells `answers|history` derives from that declaration: the merge's version cleanup, reindex's store and membership recognition, the summary's orphan-file check and totals, the artifact match `dataset purge` and `dataset delete` remove files by, materialize's store-type test, the view statements and per-run views, and the guard's identity-column inventory. Attachment scanning, which reads answers and history records only, keeps its explicit two types. Adding a store type is one declaration plus its own fetch and guidance, which is the shape REPORT-152's `recent_logs` (keyed by `id`) takes. Identity values are JSON strings.
- **R9. Existing datasets are untouched.** `answers` and `history` keep their identity, key encoding, store and membership file formats byte for byte; a dataset written by 0.2.x opens, queries, merges and reindexes unchanged with no migration. The one visible difference is materialization: R14 widens `run_membership`'s definition, so a `run_membership` Parquet written by 0.2.x reads as stale (`STALE_MATERIALIZED` in `dataset show`, queries reading the JSONL meanwhile) until the dataset is materialized again, while the `answers` and `history` copies stay fresh.
- **R10. Pinned columns per type.** Pinning becomes per store type (a global pin would retype an `answers` field that happens to share a name such as `type`). Every field of R13 to R15 is pinned, so a view's columns and types do not depend on which documents a pull happened to see:
  - **By construction, strict types:** `portal_class_id`, `offering_id`, `idx`, `n_records` BIGINT; `found`, `parse_ok`, `is_revert` BOOLEAN; `server_created`, `created` TIMESTAMP (both ISO 8601 UTC from `toISOString`); `idx` and `created` hold by construction because CLUE-742's `toHistoryRow` emits `idx` only for a safe integer `index` (`Number.isSafeInteger`, since an integer such as `1e300` overflows BIGINT) and `created` only within years 1 to 9999, else null (Doug, 2026-10-22; REPORT-109 R14), and a document's `offering_id` comes from its metadata's `offeringId` only, so a group document whose metadata names none arrives with `offering_id: null` and is kept like any other row (CLUE-742, Doug, 2026-10-24); `doc_id`, `entry_id`, `uid`, `context_id`, `owner_type`, `entry_uid`, `entry_json`, `content_json` VARCHAR.
  - **Passed through from CLUE, string in CLUE's types:** `type`, `visibility`, `title`, `unit`, `investigation`, `problem`, `group_id`, `network`, `prev_entry_id`, `model`, `action_raw`, `state`, `version`, `last_history_entry_id` VARCHAR.
  - **Passed through, number or boolean in CLUE's types:** `created_at`, `change_count`, `undoable` JSON, so a stray value in old data can never fail every query on the view; REPORT-111 derives typed columns from them.
  - **Objects and lists:** `tools`, `metadata_sources`, `firestore_metadata`, `rtdb_metadata`, `self` JSON.
  - `_fetched_at` TIMESTAMP and `_run_id` BIGINT as for every store. A field the export adds later is not pinned and widens as today.
- **R11. Merge memory at Scott's scale.** Landing a segment holds in memory only a compact sorted index of the segments being merged, each identity as its encoded key plus its offset and length (a benchmark over 1M `clue_history`-shaped identities stays at or under 120 bytes per identity; the measured shape was 80), and does not grow with the old store or with other runs' membership: membership is read and written as a stream, in key order, and a membership file found out of key order fails the merge closed rather than dropping records. This applies to every store type, `answers` and `history` included.

### Coverage

- **R12. Coverage counts owners.** For the three CLUE types, `coverage.queried` is the response's `total_endpoints` (scope owners) and `with_data` is the number of distinct owners, `(context_id, uid)`, holding at least one record in the run's membership, with `empty = queried - with_data`:
  - `clue_documents` and `clue_content` read the owner from their own records.
  - `clue_history` items carry no owner, so its owners come from the `clue_documents` store, by `doc_id`. When any of the run's history documents is absent from that store (the run's documents were never fetched, or a document appeared between the two pulls), the history download has no `coverage` and its result says to fetch `clue-documents` for the run before `clue-history`. Coverage is computed only when the history lands, so a documents pull made afterwards does not add it; only a fresh history pull does.
  - Coverage is computed by streaming the run's membership against the store in key order, never by loading either whole, under the per-dataset lock, so another run's merge of the same type cannot remove the store version being read.

### Views

- **R13. Three plain views.** `clue_documents`, `clue_history` and `clue_content` are static views over their stores, built exactly like `answers` and `history` (every stored column at its pinned type; with no store yet, a typed-empty stand-in carrying every pinned column of that type, not the answers contract columns `storeView` falls back to today), and materializable like them. They live in `internal/duck/views.go` and are the views REPORT-111 redefines in place with the same names.
- **R14. Membership and per-run views.** `run_membership` always carries every identity column of every registered kind that has per-run membership, whichever types the dataset holds (a kind without it, such as REPORT-152's `recent_logs`, adds no column): it gains `doc_id` and `entry_id` (NULL on answers and history rows, and the four existing identity columns NULL on CLUE rows), so a CLUE store joins it type-qualified with `USING (doc_id)` or `USING (doc_id, entry_id)`; a `clue_history` join on `doc_id` alone fans out, one row per entry pair, and the catalog says so. Per-run views `clue_documents_<run>`, `clue_history_<run>` and `clue_content_<run>` follow from membership as `answers_<run>` does.

### Dataset summary

- **R15. `dataset show` and `--json`** report `clue_documents`, `clue_history` and `clue_content` totals from the manifest and each CLUE download's completeness, merge counts and coverage, as for answers and history; the orphan-file, missing-file and incomplete-download warnings cover the new files; `dataset reindex` adopts and rebuilds them.
- **R16. A large-store hint.** `dataset show` adds a `LARGE_UNMATERIALIZED` warning when a store file is larger than 4 GiB and that store's view has no fresh materialized copy, naming the view, its size and `cc-data dataset materialize <ref>`.

### Guidance and documentation

- **R17. Catalog rows, guarded.** In the same change: `core.md`'s Views section gains `clue_documents`, `clue_history`, `clue_content` (one-line purpose each, the CLUE-versus-LARA history distinction, joining by `doc_id`, that a CLUE row has no `remote_endpoint`, and how CLUE rows meet the cohort: `uid` is VARCHAR and is a portal `user_id` only for `owner_type = 'user'`, so `TRY_CAST(d.uid AS BIGINT) = m.user_id` against `student_id_mapping`, and `portal_class_id = class_id`; `student_id_mapping` has one row per learner, which is one per assignment, so that join repeats a document once per assignment its student has in the class: test cohort membership with `SEMI JOIN` or `EXISTS`, or add `d.offering_id = m.offering_id`, which leaves out documents with no offering), its per-run views line adds the three, and the `run_membership` entry gives the CLUE `USING` lists; the Identity columns section becomes per store and adds `doc_id` and `entry_id`; `tools.md` gains the three tools; the researcher guide's view table gains the three views. REPORT-104's guard passes in both directions. Full workflow prose stays REPORT-115's.
- **R18. Prose that names the stores.** README's command list and dataset layout, the researcher guide's command lists, the skill header's "Fetching data" list, and any comment header describing the store types or the identity contract are updated to the per-type contract.

### Tests and acceptance

- **R19. Tests** run against a fake report-server serving items shaped exactly as REPORT-109's R13 to R15 (fixtures taken from CLUE-742's `toHistoryRow`, `toDocumentRow` and `toContentRow` output), and cover: each command end to end; a history fetch interrupted mid-document and resumed, ending with the same record set and no duplicate; a resumed page that re-delivers items (at-least-once); a re-pull that updates a record in place and removes one no longer in scope; two runs sharing documents; `EXPIRED_CURSOR` restart; refusal of an identity-less item (R5); byte-for-byte `entry_json` and `content_json` through the store and the view, including `<`, `&`, non-ASCII and a multi-megabyte value; pinned types holding when the fixture's values would sniff differently; coverage for all three types including history's absent-coverage case; reindex adoption of the new files; an 0.2.x dataset fixture opening and merging unchanged (R9); the merge-memory benchmark (R11); and the guidance guard.
- **R20. Acceptance on staging**, once REPORT-109 and CLUE-742 are deployed there: against a run covering staging class `7be899cf…`, each command completes and reports counts; the history pull, interrupted and resumed, matches an uninterrupted one; a re-pull adds no duplicates; `dataset show` reports the three types; and the counts match REPORT-109's own staging check of that class (document, entry and content counts).

## Technical Notes

- **Files.** `internal/store/{identity,membership,merge,mergesource,segment,columns}.go`; `internal/dataset/{dataset,merge,reindex,summary,manifest}.go`; `internal/fetch/paged.go`; `internal/api/pagination.go` (route only; the envelope is `BulkPage` as is); `cmd/get.go`, `cmd/get_answers.go` (`newPagedGetCmd`), `cmd/get_history.go`, new `cmd/get_clue.go`; `internal/mcpserver/{tools,types}.go`; `internal/duck/{views,materialize}.go`; `internal/guidance/src/{core,tools}.md`; `docs/researcher-guide.md`; `README.md`.
- **Key order.** The identity key stays the length-prefixed field encoding (`encodeFields`), so stores and membership files of every type are sorted by that byte order, which is what lets the merge and coverage walk them as streams (R11, R12).
- **Run time and disk at Scott's scale.** About 15 GB of history in roughly 12,800 pages of 500 entries (about 1.2 MB each at his average entry size); peak disk during a merge is the segment plus the old and new store versions. A materialized `clue_history` is about a tenth of the JSONL (REPORT-113 measured 2.92 GB to 313 MB on a 6.38M-row history-shaped fixture).
- **Verified before planning (throwaway code, deleted).** The key encoding sorts each document's history keys contiguously and in the same document order as the `clue_documents` keys (7,488 random entries over 2,813 documents with ids of 1 to 25 characters), so R12's history-to-documents walk is a single merge pass. Every membership file is written from `SegmentIndex.Identities()`, which is in key order, and has been since REPORT-77 introduced the merge engine; `WriteMembership` has no other caller. The `run_membership` shape R14 needs (each member casting the identity columns it lacks to NULL, `UNION ALL BY NAME`) binds in DuckDB 1.5.5, and the type-qualified `USING (doc_id, entry_id)` join returns exactly the run's entries.
- **Checked against a local stack (2026-10-23).** report-server built from REPORT-109's and REPORT-150's branches merged locally, with CLUE-742's `clueExport` in the Firebase emulator (12 documents, 5 owners of all three kinds, one document of 1,200 entries, one with duplicate and gapped indexes, one entry carrying both of the export's nulls), read with plain HTTP rather than cc-data:
  - Each route's items carry exactly the fields the implementation plan pins for its store (19, 15 and 10) and no others, with JSON types R10 expects; responses carry `Cache-Control: no-store` and `Pragma: no-cache`.
  - `total_endpoints` is on every page (5, the owner count). History pages at `limit` 500 and 97 (3 and 13 pages) both deliver all 1,209 entries once each, with a page boundary inside a document; replaying a token re-serves the same items; a history token on `/clue/documents` is 404 `NOT_FOUND`.
  - Written as stored lines (sorted keys, `_fetched_at` and `_run_id` added) and read with the per-store pinned columns, all three load in DuckDB 1.5.5. The null `idx` and `created` entry reads as NULL, the distinct `(context_id, uid)` owners over the documents equal `total_endpoints`, and every history document resolves to a document row, so R12's coverage for this run is 5 of 5 owners.
- **Errors.** A run the caller cannot export is 404 `NOT_FOUND`; a report without per-learner data is 422 `UNPROCESSABLE`; a portal report-server has no CLUE function for is 503 `SERVICE_UNAVAILABLE`, which the client retries as transient (report-server also uses that code for transient conditions), so it surfaces after the retry budget with the server's message.
- **Mixed versions.** Downgrading is unsupported, as for REPORT-152's `recent_logs`. A cc-data built before this story (0.2.x, and the `v0.3.0-pre.1` pre-release, which has the same per-run view loop) cannot open a query session on a dataset holding CLUE membership: its per-run view loop builds `clue_history_<run>` over a `clue_history` view it lacks, the view and its fallback both fail to register, and every query fails, `answers` included. Its `dataset reindex` rebuilds the manifest without the CLUE stores, memberships and downloads, leaving their files on disk for this release's reindex to adopt again, and its `dataset purge` removes the CLUE membership files but keeps the CLUE store files.
- **The `idx` and `created` nulls are upstream.** R10's pins rest on `toHistoryRow` (collaborative-learning #3052 on `CLUE-742-clue-export-function`, `functions-v2/src/clue-export-walk.ts`) nulling an `index` that is not a safe integer and a `created` outside years 1 to 9999, which REPORT-109's R14 states as part of the contract (report-service #445 on `REPORT-109-clue-document-export-api`). An entry whose stored `index` is not a non-negative integer still stops the export at that entry, since the history cursor carries it raw; CLUE's writers never store one (Doug).
- **`clue_prepull`.** `internal/packages/run.go:79` refuses a `clue_prepull` package because "cc-data cannot fetch CLUE data yet"; that stops being true when this story merges. Its removal is REPORT-152's (Doug, #906), in the same 0.3.0 release.

## Out of Scope

- Derived columns on the CLUE views, `clue_patches`, and Scott's parquet parity (REPORT-111).
- Recent logs, `clue_prepull` and `clue_source` (REPORT-152).
- Workflow guidance beyond the catalog rows (REPORT-115); retiring the cc-data-studies recipes (REPORT-122).
- Partitioned or append-only stores that avoid rewriting the whole store per merge.
- New columns in `dataset list`'s text table (its `--json` totals gain the keys).
- Teacher documents, comments, and anything else REPORT-109 leaves out.

## Open Questions

### RESOLVED: Merge memory: rework the store engine in this story, or accept today's?
**Context**: Measured 570 bytes per identity today, about 3.6 GB of heap for Scott's 6.38M history entries before GC headroom; a compact sorted index with streamed membership measured 80 bytes per identity (about 510 MB). The rework touches the merge every store type uses, so it also changes the answers and history path. REPORT-152's acceptance has a package pull its own CLUE corpus on a laptop and a VM, and the dashboard's VM baseline is 4 GB / 2 vCPU (`final-design.md`, cost guard), so B cannot land Scott's history there. A widens a 3-point story into the merge every store type uses.
**Options considered**:
- A) Rework now (R11): compact sorted index, `covered` walked from the sorted membership files, membership written as a stream.
- B) Keep the engine; document the memory need and file a follow-up.
- C) Only stream membership (drops `covered`, about a quarter of the cost), keep the map index.

**Decision**: A (Doug, 2026-10-22). Measured through the real merge, today's engine needs about 5.0 GB of live heap for a first pull of Scott's history and 6.9 GB for an overlapping one, over the 4 GB VM; C still leaves roughly 4 to 5 GB.

### RESOLVED: Judgment call: what history coverage reports when owners cannot be resolved
**Options considered**:
- A) Resolve owners through the `clue_documents` store; omit coverage when any document is unresolved.
- B) Count distinct documents as `with_data` (a different unit from `queried`).
- C) Make `with_data` nullable in the `dataset show --json` contract.

**Decision**: A. B mixes units in one field, and C changes a stable JSON contract for every type. Omitting `coverage` is already legal (the field is `omitempty`).

### RESOLVED: Judgment call: JSON rather than BIGINT for `created_at`, `change_count` and `undoable`
**Options considered**:
- A) JSON, keeping the raw value and its type.
- B) BIGINT/BOOLEAN as CLUE declares them.

**Decision**: A. They are passed through from stored CLUE data without coercion, and a single non-numeric value would fail every query on the view (verified), while `"12.5"` would truncate silently. REPORT-111 owns typed derived columns.

### RESOLVED: Low confidence: the size at which `dataset show` suggests materializing (R16)
**Context**: REPORT-110's Jira asks for a hint past "a size threshold" and names none. Measured with the DuckDB 1.5.5 CLI under the query sandbox's caps (`memory_limit` 2GB, 4 threads), warm cache: a `count(*)` and a `GROUP BY` over a 1.04 GiB history-shaped JSONL (460,000 entries of about 2.3 KB, Scott's average) each took 0.43 s. At Scott's 15 GB that is several seconds per query, which is where REPORT-113's 87x column-pruned speedup pays.
**Options considered**:
- A) 1 GiB: fires on datasets whose queries take under half a second.
- B) 4 GiB: about 2 s per scan warm, more on a slower disk.
- C) No hint; the guidance already says when to materialize.

**Decision**: B. A would teach researchers to ignore the warning; C drops a Jira requirement that costs one size check against data `dataset show` already reads.

### RESOLVED: Judgment call: membership for `clue_history` stays per entry, not per document
**Options considered**:
- A) Per entry, `(doc_id, entry_id)`, like every other store.
- B) Per document, so `covered` is thousands of ids instead of millions.

**Decision**: A. With R11's streamed membership the per-entry set costs disk (about 0.4 GB at Scott's scale), not memory, and B would keep an entry upstream removed from a still-covered document forever.

### RESOLVED: Stage 8: workflow prose in the researcher guide
**Context**: The prose step planned an "About CLUE documents" paragraph in the researcher guide's section 6 that no requirement asked for.
**Decision**: Dropped. Workflow prose is REPORT-115's; this story carries the guarded view-table rows and the command lists only.

### RESOLVED: Stage 8: REPORT-152's registry fields
**Context**: REPORT-152 (its spec on `REPORT-152-recent-logs`) adds two fields to the store declaration for `recent_logs`: `ClassKeyed` (the number in its segment and membership names is a class id, it is stamped `_class_id` rather than `_run_id`, and it has no `run_membership` rows, identity columns or per-run views) and `Accumulate` (a merge adds to a key's membership instead of replacing it).
**Decision**: REPORT-152 adds both fields in its own steps; R14 is worded so a kind without per-run membership adds no `run_membership` column. Declaring unused fields here would be code no REPORT-110 test can exercise.

### RESOLVED: Stage 8: an item without its identity
**Context**: R5 fails a CLUE pull on an item with no `doc_id` or `entry_id`. REPORT-152's draft skipped and counted a log row with a null `id`.
**Decision**: One rule. REPORT-109 guarantees both CLUE ids by construction (R11, `toDocumentRow`, `toHistoryRow`), so a missing one is a broken server contract that should stop the pull. REPORT-152 (its R6) now fails a log row without its `id` the same way, with the same `MALFORMED_ITEM` contract error, which supersedes the earlier decision to keep the two policies different.

## Self-Review

Roles: Senior Engineer, QA Engineer, Education Researcher, Performance Engineer, and a downstream consumer (REPORT-111 and REPORT-152). Each finding was checked against the code before it was written down; the ones below had one defensible correction and are fixed in place.

### Senior Engineer

#### RESOLVED: `run_membership` would lose the CLUE columns on a dataset without CLUE membership
`runMembershipView` selects a fixed column list per member and `UNION ALL BY NAME`s them, so a column exists only when some member supplies it; a dataset with answers only would have no `doc_id`, and a type-qualified CLUE join would fail to bind rather than return nothing. R14 now requires every registered identity column on every dataset.

#### RESOLVED: an absent CLUE store would stand in with the answers columns
`storeView` falls back to `contractStoreColumns` (`source_key`, `remote_endpoint`, ...) when a type has no store. A `clue_history` view on a dataset with no CLUE pull would then have no `entry_json`, and REPORT-111's redefinitions would fail to bind there. R13 now requires the type's own pinned columns in the stand-in.

#### RESOLVED: R8 would have swept attachment scanning into the registry
`internal/dataset/attachments.go:170` and `:200` loop over `answers` and `history` because those are the records that reference attachments. Deriving them from the registry would scan CLUE records for attachment references. R8 now leaves them explicit.

### QA Engineer

#### RESOLVED: R11's bound was not testable as worded
"About 100 bytes" with no stated identity shape cannot fail. R11 now names the shape (1M `clue_history` identities) and a ceiling (120 bytes) above the measured 80.

### Education Researcher

#### RESOLVED: no stated way to put CLUE rows in the cohort
`student_id_mapping.user_id` and `class_id` are BIGINT (`internal/duck/views.go:750-753`); CLUE's `uid` is a string and is a synthetic `group_...` or `class_...` id for group and class owners (REPORT-109 R5). Without a catalog line the natural join (`uid = user_id`) fails to bind or silently drops group work. R17 now carries the cast and the `owner_type` caveat. Document rows name the owner's type `owner_type`, with values `user`, `group` and `class`, matching `ownerType` in CLUE's document-axes docs (CLUE-742, Doug, 2026-10-24).

### Second pass

Roles: Senior Engineer, Data Integrity Reviewer (the CLUE export's values against the pins), Operator, and the downstream stories. Each finding was checked against `main`, CLUE-742's `clue-export-walk.ts`, or throwaway code (deleted).

#### RESOLVED: R8 missed the artifact match purge and delete use
`internal/dataset/dataset.go:400` spells `answers\.v\d+\.jsonl|history\.v\d+\.jsonl` in `artifactFileRe`, which `deleteArtifacts` uses for `dataset purge` and `dataset delete`. A throwaway test on `main` purged a dataset holding a `clue_history` store: the membership file went (`members_.*` is generic) and the store file stayed, where no reindex can adopt it again, since adoption needs a membership file at the same version. R8 and the registry step now derive it from the registry, which is also what REPORT-152 assumes.

#### RESOLVED: two strict pins rest on values CLUE clients wrote
R10 lists `idx` and `created` as built by construction, but `toHistoryRow` sets `idx` to the stored `index` as is (`stored.index ?? null`), and `created` to `isoFromMillis(entry.created)` of a client-written number. An `index` that is a string or a fraction is served unless it ends a page (REPORT-109 checks only the next cursor); under BIGINT a string fails every query on `clue_history` and a fraction truncates silently (Background). `toISOString` writes years past 9999 in the six-digit form, and DuckDB 1.5.5 refuses `+010000-01-01T00:00:00.000Z` as a TIMESTAMP (checked with the CLI), so one `created` of 253,402,300,800,000 ms or more, such as a microsecond value, fails every query on the view. `server_created` is safe: Firestore timestamps stop at 9999. REPORT-111 consumes both pins (its A4 and R5) and orders patches by `idx`.
**Options considered**:
- A) Keep both pins and correct R10: they rest on CLUE clients writing integer indexes and millisecond times, and a bad value fails every query until it is fixed upstream.
- B) Pin `idx` and `created` as JSON and VARCHAR, and have REPORT-111 derive the typed columns with its `_unparsed` rule.
- C) Keep both pins and make the export guarantee them: CLUE-742's `toHistoryRow` emits `idx` only for an integer and `created` only for a year from 1 to 9999, else null, and REPORT-109's R15 states it.

**Recommendation**: C while CLUE-742's PR is open, since it makes the bad value impossible at its source and changes nothing in cc-data or REPORT-111; A if it has merged.

**Decision**: C (Doug, 2026-10-22). CLUE-742 (collaborative-learning #3052) is still open, so the guarantee goes into `toHistoryRow` there, and R10 and the Technical Notes name it as a dependency.

#### RESOLVED: the identity-policy note described REPORT-152's draft
"Stage 8: an item without its identity" said REPORT-152 skips and counts a log row with a null `id`. REPORT-152's spec on `REPORT-152-recent-logs` (its R6 and implementation) now fails such a row with `MALFORMED_ITEM`, the code this story's implementation gives R5. The note now records one rule for both stories.

#### RESOLVED: nothing said what an older cc-data does with a CLUE dataset
R9 promises only the forward direction. A throwaway test on `main` (`duck.Open` over a dataset whose manifest holds a `clue_history` store and `clue_history/584` membership) failed with "registering view clue_history_584 ... fallback also failed", with or without an `answers` store beside it, so 0.2.x can run no query on such a dataset. Its reindex dropped the CLUE store and membership from the manifest and kept the files. Technical Notes now carry a "Mixed versions" line, worded as REPORT-152's for `recent_logs`.

### Third pass

Roles: Education Researcher, Operator, Senior Engineer. Each finding was checked against `main`, `v0.3.0-pre.1`, or throwaway code (deleted).

#### RESOLVED: the cohort join repeats a document per assignment
R17 told researchers to join CLUE rows to `student_id_mapping` on `uid` and class. That view has one row per `learner_id` (`core.md`'s own entry), and a learner is a student in one assignment, so a student with two assignments in a class has two rows there. A throwaway DuckDB check with that shape returned 4 rows for 2 documents through the R17 join and 2 through a `SEMI JOIN`. R17 now names the grain and gives the two joins that do not fan out.

#### RESOLVED: the history coverage note promised more than a later documents pull delivers
R12 said the history result tells the researcher to fetch `clue-documents` "first", and the planned note read "fetch clue-documents for run <id> to count owners". Coverage is computed once, when the history lands, and the manifest keeps no `total_endpoints` outside the coverage it stores. A throwaway test on `main` ran the same paged fetch twice: the second run requested the first page again, since a landed pull leaves no cursor to resume. So a documents pull made afterwards cannot add history coverage, and getting it means re-pulling the history (15 GB at Scott's scale). Filling it in from the later documents pull would need a new persisted field for `queried`. R12 now says the order matters and why, and the implementation's note and the skill header's command list put `clue-documents` first.

#### RESOLVED: R9 did not mention materialized copies
`viewSignature` hashes a view's SQL, and `Materialized.Fresh` compares that hash before anything else (`internal/dataset/fingerprint.go:57`). R14 adds the CLUE identity columns to every `run_membership` member, so a `run_membership` Parquet written by 0.2.x stops being fresh on upgrade, with no download in between; `dataset materialize` writes every declared view (it takes no view list), so every materialized dataset with membership holds one. The `answers` and `history` views keep their SQL, so their copies stay fresh. R9 now says so, and the CLUE step tests it.

#### RESOLVED: the mixed-versions note named only 0.2.x
`v0.3.0-pre.1` is an ancestor of `main` and has the same per-run view loop (`perDownloadViews` building `perStoreView` for every membership key), so it fails to open a CLUE dataset the same way. The note now covers every build before this story.

### Fourth pass

Roles: downstream consumer (REPORT-111 on `REPORT-111-clue-views`, REPORT-152 on `REPORT-152-recent-logs`), Senior Engineer. Each finding was checked against those specs, `main`, or throwaway code (deleted). The implementation spec's fourth pass records the plan's findings, including the one that added the lock to R12.

#### RESOLVED: the registry-fields note named fields REPORT-152 does not add
"Stage 8: REPORT-152's registry fields" said REPORT-152 needs `StampColumn`, `Accumulate` and `RunViews: false`. REPORT-152's spec (its A1, A5 and store step) adds `ClassKeyed` and `Accumulate`, with `ClassKeyed` covering the stamp column, the per-run views and the `run_membership` exclusion. The note now names those two and what each does; R14's wording already fits `ClassKeyed`.
