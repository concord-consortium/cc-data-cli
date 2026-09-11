# CLUE Dataflow history dataset: design

**Date**: 2026-08-10
**Branch**: `clue-data-inventory`
**Status**: approved, not yet built

## Why

The research question is whether student behaviours are visible in how a
Dataflow document was built over time: **trial and error**, **systematicity**,
**decomposition**, and **reusing**. Those are questions about a sequence of
actions, not about a final document, so they need the edit history.

4,602 CLUE documents contain a Dataflow tile. 2,782 of them have history, across
6.25 million entries and roughly 10 GB. This spec covers pulling those entries
into a local, queryable store.

The entries table is a **base layer**, not the deliverable. Other
representations — sessions, per-tile timelines, derived behaviour measures —
will be built on top of it. That is why the raw entry payload is retained rather
than discarded after parsing: every later representation is derived from it, and
a wrong parsing decision must cost a re-parse rather than a re-download.

## Scope

**In:** history entries for the 2,782 CLUE documents that contain a Dataflow
tile and have history.

**Out:**

- The 861 documents from the standalone Dataflow app and the `/branch/dataflow/`
  build. All have zero entries — verified across the whole population, not
  sampled.
- Document content in the realtime database. Wanted for the full dataset, not
  part of this step.
- Log events. These come from the report server's Athena `student-actions`
  report, which requires a report run created in the report server web UI first.
  A separate thread that starts with a human, not with this download.

## Why the schema looks like this

The four behaviours drive one non-obvious decision. An entry's action arrives as
a path with ids embedded in it:

```
/content/tileMap/<tileId>/content/setSlate
/content/sharedModelMap/<id>/sharedModel/variables/2/setValue
```

Left as-is, every tile produces a distinct action string, so actions cannot be
grouped and per-tile work cannot be isolated. **Decomposition** (did they work
across several tiles?) and **reusing** (`userCopyTiles`, `handleDragCopyTiles`)
both depend on separating the tile id from the action. So the id is extracted
into its own column and the action is normalised to a groupable form.

**Systematicity** may span documents by one student, so `doc_uid` matters as much
as `doc_id`. **Trial and error** needs program execution interleaved with edits,
so tick entries stay in the same table as edits rather than being split out —
"was the program running while they edited" must be answerable without a join.

## Schema

### `history_entries` — one row per entry

| Column | Source |
|---|---|
| `doc_id` | Firestore document id |
| `doc_uid` | document owner, from document metadata |
| `portal_class_id` | mapped via `Portal::Clazz.class_hash` |
| `unit`, `investigation`, `problem` | document metadata; null for personal documents |
| `entry_id` | Firestore id of the history entry |
| `idx` | the entry's `index` field |
| `prev_entry_id` | `previousEntryId` |
| `created` | timestamp from the parsed payload |
| `model` | parsed `entry.model` |
| `action_raw` | parsed `entry.action`, untouched |
| `action` | `action_raw` with ids replaced by `{tile}` / `{sharedModel}` |
| `tile_id` | extracted from the action path, null when absent |
| `shared_model_id` | extracted from the action path, null when absent |
| `n_records` | length of the payload's `records` array |
| `undoable`, `is_revert`, `entry_uid`, `state` | parsed payload |
| `entry_json` | the untouched payload string |

`entry_json` is the contract that makes every other column safe to get wrong.

### `history_documents` — one row per document

Integrity facts, stored so they can be queried later rather than printed once:

| Column | Meaning |
|---|---|
| `doc_id` | |
| `expected_max_idx` | max index recorded during the earlier survey |
| `entries_fetched` | rows actually written |
| `min_idx`, `max_idx` | observed range |
| `distinct_idx` | distinct index values seen |
| `fetched_at` | when this document was downloaded |

## Index anomalies are data, not errors

Index sequences are expected to disagree with entry counts — gaps, duplicates,
and ranges that do not start at zero. CLUE's own history framework documentation
describes ordering flakiness around `numHistoryEntriesApplied` and around
concurrent entry writes, so these may carry signal about how a document was
edited.

Therefore:

- **No entry is ever dropped.** Everything fetched is stored, whatever its index.
- Discrepancies are recorded in `history_documents` and remain queryable.
- No anomaly flag is computed at ingest. Gaps are `LAG(idx)` and duplicates are
  `GROUP BY idx` at query time, so no interpretation is baked in.

A download is not considered failed because indices disagree. It fails only when
a fetch errors or a file is incomplete.

## Download

- One JSONL file per document under `local-data/clue-documents/history/`,
  written to a temp name and renamed on completion. A partial file therefore
  cannot be mistaken for a complete one, and a restart skips finished documents
  by checking which files exist.
- About 20 documents fetched concurrently. Measured throughput is ~1,585
  entries/sec serially, so the full pull is ~1.1 hours serial and well under that
  concurrently.
- Read-only. No writes to Firestore.
- Cost: ~6.25M Firestore document reads, roughly $4 at current pricing. Recorded
  so it is a known cost rather than a surprise; not worth optimising around.

## Query surface

DuckDB converts the JSONL files into a single Parquet file, which is what
queries run against. JSONL is kept until Parquet is verified, then it is
disposable.

Replay is `WHERE doc_id = ? ORDER BY idx`. Ticks are excluded with a predicate on
`action` rather than by being stored elsewhere.

## Verification

1. Every one of the 2,782 documents has a completed file.
2. Per document, `entries_fetched` compared against **`expected_max_idx + 1`** —
   the recorded value is a 0-based max index, so a clean document has one more
   entry than its max index. Comparing against the bare value marks every
   document anomalous and hides the real ones. Anomalies are also flagged for
   duplicate indices (`distinct_idx < entries_fetched`) and gaps
   (`max_idx - min_idx + 1 != distinct_idx`). All are listed, none are failures.
3. Parquet row count equals the sum of `entries_fetched`.
4. Spot-check one document by replaying its entries in `idx` order and
   confirming the sequence parses.

## Known wart

The download script must run from the CLUE repo's `scripts/` directory, because
that is where `serviceAccountKey.json` and `firebase-admin` live per that repo's
README. The canonical copy lives in `local-data/clue-documents/` and is copied in
to run, so no files are left in the CLUE checkout.

## Success criteria

- All 2,782 documents downloaded, with any index discrepancies recorded rather
  than hidden.
- A single Parquet file where replaying one document is one ordered query, and
  filtering program ticks is one predicate.
- Raw payloads intact, so later representations can be derived without
  re-downloading.
