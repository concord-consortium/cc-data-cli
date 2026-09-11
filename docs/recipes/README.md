# Recipe scripts

The working scripts behind [`../clue-data-inventory.md`](../clue-data-inventory.md).
They are here, tracked, rather than in `local-data/`, which is gitignored — the
*data* must never be committed, but the *code that fetches it* is the reusable
part and was useless to anyone else while it sat next to the data.

They are not polished tools. They are what was actually run, kept because the
next person to ask a similar question should not have to rediscover which
Firestore path holds document content or why an Athena query times out.

## Layout

| | |
|---|---|
| `portal/` | Portal MySQL, via `rails runner` on a production ECS box |
| `clue-documents/` | Firestore metadata and history, RTDB content |
| `log-events/` | Report server, and the direct-Athena route that replaced it |
| `behavior/` | derived datasets for behaviour detection, built from the three Parquet files |

## Environment

| Variable | Used by | Meaning |
|---|---|---|
| `CC_DATA_LOCAL` | the `.ts` scripts | absolute path to your `local-data/`. Defaults to `../../../local-data`, correct only when the script runs from this directory. |
| `CC_ATHENA_WORKGROUP` | `athena_logs.py` | your Athena workgroup — report-service names it `<portal_server> <user_id> <email>` with every non-`[a-z0-9]` character replaced by `-`. Find it with `aws athena list-work-groups`. |

The TypeScript scripts need CLUE's `firebase-admin` and a service account key, so
they are **run from the `collaborative-learning` repo's `scripts/` directory**,
not from here:

```
cp clue-documents/download-content.ts ~/Development/collaborative-learning/scripts/
cd ~/Development/collaborative-learning/scripts
CC_DATA_LOCAL=/path/to/cc-data-cli/local-data npx tsx download-content.ts
```

Each script's header comment states how to run it and what it assumes.

## Order

Later steps consume earlier outputs:

1. `portal/find_dataflow_classes.rb` — which classes ran a Dataflow assignment.
2. `clue-documents/` — find documents, then `download-history.ts` and
   `download-content.ts`, then the `build-*-parquet.sh` pair.
3. `log-events/learner_census.rb` → `learner_keys.rb` → `athena_logs.py` →
   `build-parquet.sh`.
4. `clue-documents/build-augmented-lists.ts` — adds documents that only the log
   events reveal, then re-run the content and history downloads.
5. `behavior/` — derived analysis over the Parquet files above, in the order
   its own [README](behavior/README.md) gives. Reads `local-data/` only; fetches
   nothing.

`fetch-log-reports.sh` drives the report server instead of Athena. It is kept
because it documents a route that mostly does not work; see
[report-service-log-query-issues.md](../report-service-log-query-issues.md).

## Two things that will bite

- **Do not delete `local-data/clue-documents/history/*.jsonl`.** Doing so once
  let a later rebuild silently overwrite 6.27M history entries with 110k, because
  a glob matching fewer files is not an error. `build-parquet.sh` now refuses
  unless every document in the source list has its JSONL present.
- **Everything here reads production data.** No script writes to Firestore, the
  RTDB, or the portal, and none should start.
