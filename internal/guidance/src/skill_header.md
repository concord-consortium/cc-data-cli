---
name: cc-data
description: Download and query Concord Consortium researcher data (report CSVs, student answers, interactive state history, file attachments) into local datasets and analyze them with SQL via cc-data. Use whenever a researcher asks to pull, join, or analyze student report/answer/history data, or asks a plain-English question that maps to that data.
---

# cc-data

`cc-data` downloads a researcher's report data into local, duplicate-free datasets
and queries across them with SQL (embedded DuckDB). Command detail lives in
`cc-data <cmd> --help` — treat help as the source of truth and read it before
guessing flags.

## Orientation

- Orient on a dataset with `cc-data dataset show <ref> --json` — never read
  `manifest.json` directly. It reports per-type totals, the download table, and
  warnings.
- List datasets with `cc-data dataset list --json`.
- Make one with `cc-data dataset create <ref>` before fetching into it; a fetch into
  a dataset that does not exist fails.
- Delete a dataset's data with `cc-data dataset purge <ref>`.
- `--dataset` is repeatable on `query`/`repl`, which is how a query spans more
  than one dataset.
- Speed up a large dataset's queries with `cc-data dataset materialize <ref>`,
  which writes each view to `materialized/<view>.parquet` inside the dataset
  folder. `--force` rebuilds an unchanged view; `--allow-partial` builds a view
  whose declared file is missing from disk. It exits non-zero if any view was
  refused, and `cc-data dataset show <ref>` names a view whose copy has gone
  stale.

## Auth

- If a command fails with `{"error":"NOT_AUTHENTICATED",...}`, relay to the user:
  run `cc-data login --portal <portal>`, or `cc-data login <environment>` for one
  of the environments (`prod`, `staging`, `dev`), which sets the portal and its
  paired report server together. Never drive the browser login yourself.
- `cc-data auth status --check` shows validity and metadata.
- The environment names also work wherever a `portal` is passed: the `--portal`
  flag on `logout` and on every `reports` subcommand.

## Making a run

- `cc-data reports list --portal <portal|env>` — the runs you already have.
- `cc-data reports filter-options --dimension <name> --portal <portal|env>` — the
  values a filter dimension offers, narrowed by `--report-filter` as selections
  are made, and by `--report-slug` to a report that offers the dimension.
- `cc-data reports create --report-slug <slug> --report-filter '<json>' --portal
  <portal|env>` — a run from a slug and a filter. Use `--report-filter-file` when
  the filter is too long to quote.
- `cc-data reports duplicate <run-id> --portal <portal|env>` — a fresh snapshot of
  an Athena run.

## Fetching data

- `cc-data get report <run-id> --dataset <ref>` — the report CSV. A Portal report
  is computed per request, so re-read it with `--refresh` rather than duplicating
  the run.
- `cc-data get answers <run-id> --dataset <ref>` — student answers.
- `cc-data get history <run-id> --dataset <ref>` — full interactive state history.
- `cc-data get attachments <run-id> --dataset <ref>` — file attachments (requires
  answers or history fetched first). Prefer downloading into the dataset over
  `--url`; a presigned URL is a credential-free capability to a student's file.
