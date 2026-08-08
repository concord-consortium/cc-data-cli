# CLUE data inventory: design

**Date**: 2026-08-07
**Branch**: `clue-data-inventory`
**Status**: approved, not yet started

## Why

`cc-data` gives researchers report-server data — Athena report CSVs, plus the
report-service's answer records, interactive-state history, and attachments. It
reaches none of CLUE's own datastores. There is no CLUE-specific code in the
repo at all.

Scott, as lead developer, has admin access to every datastore a CLUE researcher
would want. That access is the thing `cc-data` exists to make unnecessary. So
before building anything, he will do real research using that admin access, and
we will record what he needed and how he got it. The record becomes the
requirements for whatever CLUE support `cc-data` eventually grows.

The point is to capture a researcher's real needs without paying the cost of
building the tool first, and without slowing the research down to build it.

## What we are not doing

No changes to `cc-data`'s Go code during this work. No manifest, no
identity-keyed merge, no atomic writes, no dataset abstraction. Fetched data is
raw output in a local scratch folder, queried in place. If a re-fetch clobbers a
previous dump, that is fine — we note that a real implementation would have to
handle it, and move on.

## Roles

Claude drives the fetches. Scott brings the research question and the
credentials; Claude runs the CLIs, lands the data locally, and queries it. This
makes the record a by-product of the work rather than separate bookkeeping —
every command that ran is already in the session.

## The artifact

One file: `docs/clue-data-inventory.md`, in two parts.

### Part 1: inventory table

One row per data source.

| Column | Holds |
|---|---|
| Data | What it is in researcher terms, not schema terms |
| Where it lives | Datastore, plus collection or path |
| Access needed | The admin rights actually used to reach it |
| Recipe | Link to its section in part 2 |
| cc-data today | `covered` / `partial` / `absent` |

The last column is what converts into requirements later.

### Part 2: recipes

One `###` section per source, containing:

- The exact commands that worked.
- A sample record with values redacted, showing the shape.
- The identity fields — what makes a record unique. This is what `cc-data`
  would key merges on, and it is rarely obvious from the data alone.
- **Notes**: whatever bit us. Pagination limits, rate limits, absent fields,
  joins that needed a key that does not exist in either side.

The Notes are the highest-value part. A recipe that works is easy to
reconstruct; the reason a flag is there is not.

## The working loop

1. Scott brings a research question.
2. Claude fetches what it needs. Raw output lands in a per-source folder under
   `local-data/`, which is gitignored — in the repo for convenience, never
   committed.
3. Claude queries it with DuckDB reading the files directly.
4. Before moving on, Claude adds or updates that source's row and recipe, and
   commits.

One commit per source-fetch, with a message naming what was fetched and why.
`git log docs/clue-data-inventory.md` is then the chronology, so no hand-written
journal is needed. Dead ends survive in the history even when the final recipe
does not mention them.

Writing the recipe immediately after it works costs a few minutes mid-research.
That is deliberate: reconstructing it later from shell history reliably loses
the "why this flag" detail, which is the part worth having.

## Guardrails

This repo is public.

- No credentials, tokens, or presigned URLs in the doc.
- No real student records. Sample shapes carry redacted values.
- Project IDs and collection paths are included — operationally necessary and
  not secret. Anything borderline gets flagged for Scott rather than decided
  unilaterally.
- Fetched data lives in `local-data/`, which is gitignored so it can sit
  alongside the recipes without any risk of being committed. Nothing in it is
  ever added to git, and no sample drawn from it enters a doc unredacted.

## Known naming exception

The branch is `clue-data-inventory`, which breaks the repo's `REPORT-XX-`
convention because no ticket covers this work. Rename if one is filed.

## Success criteria

- Every CLUE data source Scott actually needed has a row and a working recipe.
- Each row states plainly whether `cc-data` reaches that data today.
- Someone building CLUE support in `cc-data` can read the file and know what to
  fetch, how records are identified, and what will go wrong.
