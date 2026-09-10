# `logs` view with parsed `parameters` and `extras`

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-112

**Source Spec**: [specs/REPORT-112-logs-view/](specs/REPORT-112-logs-view/)

**Status**: **Closed**

## Overview

Add a `logs` view over the log-type report CSVs that exposes `parameters` and `extras` as queryable JSON alongside the original strings, and both of the log's UNIX-integer clocks as real UTC timestamps. The existing `reports` view is unchanged.

## Requirements

- A `logs` view over the log-type report CSVs, carrying every column each CSV recorded plus `run_id`, as `reports` does.
- `parameters_json` and `extras_json` are `TRY_CAST(... AS JSON)`, null on unparsable input and never an error.
- The original `parameters`, `extras`, `time` and `timestamp` columns are retained unchanged under their own names.
- `event_time` derives from `time` and `received_time` from `timestamp`. Each is correct whether its source was detected `BIGINT`, `DOUBLE` or `VARCHAR`, and null rather than an error for a value that is not a number.
- `event_time` treats `time` as UNIX **seconds**, `received_time` treats `timestamp` as UNIX **milliseconds**, and a test pins one epoch value per column so neither reading can be introduced silently.
- Both are declared and emitted as `TIMESTAMP` holding UTC, so the column's type does not depend on whether a CSV is present on disk, and both compare directly to the stores' `_fetched_at`.
- A NULL in a parsed column is disambiguated by its retained source column: `parameters IS NOT NULL AND parameters_json IS NULL` is a value that was present and did not parse. The catalog entry states this and a test pins it.
- A log CSV recovered by `reindex` without a slug is admitted on the same terms as one with a slug.
- A missing CSV contributes zero rows and keeps its columns; a corrupt one costs its rows rather than the view.
- On a dataset with no log runs, `logs` still declares `run_id` and the four derived columns, so a documented query binds before any log data has been downloaded. It declares no source columns, which depend on what was downloaded.
- The `reports` view behaves exactly as before, asserted rather than assumed.
- A catalog entry and the researcher-guide table row land in the same change as the view, per REPORT-94's atomic rule and REPORT-104's entry structure.
- The entry names all three log reports as members, states what `username` can mean across them, and gives the `downloads` join on `run_id` selecting both `slug` and `hide_names`.

## Technical Notes

`logs` follows `reportUnionView` rather than REPORT-94's `dimensionViewStmt`, because the fixed-known-schema premise the dimension pattern rests on does not hold for logs: `student-actions-with-metadata` carries columns the other two do not. Derived columns are appended to each member's `SELECT` rather than wrapped around the union, so a member whose CSV lacks a source column still contributes.

The shipped expressions:

```sql
TRY_CAST(parameters AS JSON) AS parameters_json,
TRY_CAST(extras AS JSON)     AS extras_json,
to_timestamp(TRY_CAST(time AS DOUBLE))             AT TIME ZONE 'UTC' AS event_time,
to_timestamp(TRY_CAST(timestamp AS DOUBLE) / 1000) AT TIME ZONE 'UTC' AS received_time
```

**A log row carries two clocks, not one instant at two resolutions.** The Kinesis ingester (`cloud-formation/log-ingester.json`) sets `time` from the client device's own clock, rounded to seconds, falling back to the server clock when the client sends nothing usable, and sets `timestamp` to server receipt in milliseconds. Measured on staging run 187, ordering within a session by `time` leaves 29.4% of adjacent event pairs tied against 0.3% on `timestamp`, and observed client-to-server skew reaches 872 ms.

**The Go driver returns a JSON column as `map[string]interface{}`.** Scanning a bare `TRY_CAST(... AS JSON)` into a `*string` fails with "unsupported Scan", so tests assert through `->>`, which returns `VARCHAR`.

**No CSV line-size option is set.** DuckDB's `read_csv` caps the whole line at 2,000,000 bytes, not a field at Python's 128 KB, which is what the story was originally written around.

**Verification belongs in Go against the embedded engine.** The local `duckdb` CLI is v1.3.0 while the binary embeds v1.5.4, so anything checked against the CLI is checked against a different engine.

## Out of Scope

- Changing the `reports` view, its membership, or its column set.
- Parsing `parameters`/`extras` for any other report type. Only log-type CSVs have them.
- A typed or per-key projection of `extras`, which would make this a CLUE-specific view rather than a log view.
- Materialization, which is REPORT-113.
- Retiring the recipes that do this by hand, which is REPORT-119.
- An inline provenance column on `logs`. Considered and deferred; see Not Yet Implemented.

## Not Yet Implemented

- **Whether union fact views should carry provenance columns inline.** Deferred out of this story deliberately, not missed. `logs` unions three reports whose `username` means up to five different things, and the fix is a `downloads` join on both `slug` and `hide_names` rather than copying either attribute into the view. The general question spans `reports`, `logs` and the views REPORT-111 adds, and belongs to whoever specs **REPORT-113**: decide it there for every union view at once, before a materialized column set makes the answer expensive to change.

## Decisions

### Does `logs` admit a log CSV that has no slug?

The Jira said both "log-type CSVs only" and "slug-recognized via provenance", which pick different sets. **Admit on `dl.ReportType == ReportTypeLog`, not on slug.** All three log slugs map to that one type; `recoverReportType` returns `recovered = false` for a log CSV, so the codebase already treats that identification as confident; and excluding those rows would drop from `logs` data the user can still see in `reports`, with nothing to explain the difference.

---

### Does this story set a CSV line-size option?

The Jira's acceptance criterion ("a row over 128 KB loads") rests on Python's `csv` limit, not DuckDB's, so it passed before any change was made. **Set no option.** A criterion that passes before the change is worth nothing. What replaces it is a test at the real boundary: a 130 KB field loads and is queryable *without* any option set, which is the fact that makes the option unnecessary. If a row ever exceeds 2 MB, DuckDB fails loudly with an error naming `max_line_size`.

---

### The derived columns cannot assume the source columns exist

`recoverReportType` types a CSV as `log` on `event` and `time` alone, so a CSV with no `parameters` can be a member, and referencing it would fail the view's creation for every other run in the dataset. **Each member substitutes a typed NULL where its own recorded schema lacks the source column**, driven off the descriptor's `src` field so the declared type and the substituted NULL cannot disagree.

---

### A derived name colliding with a source column needs a guard, and the failure differs by member count

If a log CSV ever carried a column named `event_time`, `received_time`, `parameters_json` or `extras_json`, an unskipped duplicate fails two different ways. With one member there is no `UNION` and `CREATE VIEW` quietly renames the second to `event_time_1`, so `SELECT event_time` resolves to the CSV's own column and the derived value hides under a name nothing documents. With two or more, `UNION ALL BY NAME` rejects it outright, and because the fallback unions the same members it fails too, so `Open` refuses the **whole dataset** rather than one view. **A member skips a derived column whose name its own schema already contains, in both the populated member and the typed-empty stand-in.**

Nothing in cc-data can detect the server-side change that would cause this: it holds no copy of the log column list and derives none, so a test asserting the four names against a transcript would fail only when a person edited the transcript. The runtime skip is the whole defense, tested by behavior at both member counts. What the skip cannot prevent is accepted rather than solved: a member keeping its own `event_time` puts a `VARCHAR` and a `TIMESTAMP` under one name and the union resolves it to `VARCHAR` silently, which is the better half of the trade against a dataset that will not open.

---

### The CSV carries both `time` and `timestamp`, and the view parses both

An earlier draft derived `event_time` from `time` alone, on the grounds that `timestamp` was uncharacterized. The ingester lambda characterizes it: they are **different clocks**, not one instant at two resolutions. **Derive both.** `event_time` stays on `time`, which keeps Scott's meaning and agrees with the filter that selected the rows (the server prunes a run with `log.time >= <unix seconds>`, so an `event_time` built on `timestamp` could fall outside the requested date range). `received_time` carries `timestamp` at the resolution the data actually has. Both source columns are retained as `BIGINT`; the earlier draft's claim that `timestamp` is a string came from Scott's build forcing `timestamp: 'VARCHAR'` in its own `columns=` map.

---

### What does a NULL in a parsed column mean, and where is that recorded?

An earlier catalog entry read "a NULL means unparsable, not absent". A NULL arises three ways: the string did not parse, the field was empty, or the run's CSV never carried the column. Since `core.md` renders into the MCP instructions, that sentence would have turned a run with no `parameters` column into a report of malformed payloads. **State the invariant and pin it with a test**: every parsed column keeps its source string beside it, so `parameters IS NOT NULL AND parameters_json IS NULL` isolates a value that was present and did not parse.

A companion `parameters_parse_failed` column was rejected as redundant state derivable from two columns already in the row. Enumerating the three causes was rejected because lists rot, where the invariant covers `extras_json` and anything a later story adds. The predicate is pinned by a test rather than left in markdown, so it cannot keep being handed out after the behavior changes.

---

### Does `logs` carry a provenance column?

`logs` unions three reports whose shapes differ, so `username` can be absent, the student's, the student's salted hash, the teacher's, or the teacher's hash. **Add nothing; the catalog entry carries the explanation.** An inline `slug` was the initial recommendation and is a half-fix: it separates three of the five states, and the other two are the `hide_names` split, so `GROUP BY slug, username` still merges plain and hashed usernames from two runs of the same slug while looking like it worked. Disambiguation takes `slug` **and** `hide_names`, both already in `downloads` on the `run_id` key every row carries. Copying one attribute inline would put a value in a second place that must agree with `downloads` forever. See Not Yet Implemented for where the general question goes.

---

### Is a derived timestamp `TIMESTAMP` or `TIMESTAMP WITH TIME ZONE`?

`to_timestamp(DOUBLE)` returns `TIMESTAMPTZ`, so declaring the stand-in `TIMESTAMP` made the column's type depend on whether a CSV was present on disk, and silently: `to_timestamp(...) = TIMESTAMP '2025-09-10 00:00:00'` is false while the `TIMESTAMPTZ` literal comparison is true. It also crossed views, comparing an offset apart from the stores' `_fetched_at`, and bucketed `date_trunc('day', ...)` by the reader's machine timezone. **`AT TIME ZONE 'UTC'`, declared `TIMESTAMP`**, which is what this repo already does for `_fetched_at`: convert to UTC in Go, declare `TIMESTAMP`.

Declaring `TIMESTAMPTZ` instead was rejected for buying a truer instant type at the price of leaving the cross-view comparison and the day-bucketing wrong by default. `SET TimeZone='UTC'` on every connection was rejected on a verified fact: it fixes comparison and bucketing but not the type instability, and is a session-wide side effect for one column's benefit. Because the column is now timezone-naive, that it holds UTC is a convention the catalog entry has to state.

---

### What does `logs` look like on a dataset with no log runs?

`reportUnionView` short-circuits to a `run_id`-only stand-in when nothing matches, so the documented query would not bind until the first log run landed. **Declare `run_id` plus the four derived columns**, implemented in `reportUnionView` so `reports` and `report_prompts` are unaffected. Declaring the twelve base log columns as well was killed by a fact: `username` is not common to the log reports, so a stand-in declaring it would make `logs` *lose* a column once a student-actions run landed. That yields the rule the stand-in follows, that it may only declare a subset of every populated schema, and the four derived columns are the largest set satisfying it.

---

### Generalize `reportUnionView` rather than write a second union

No requirement asked for either shape. Generalizing is the smaller change and keeps the missing-file, corrupt-CSV and quarantine behaviors in one implementation, but it edits a function two shipped views depend on. That is why a byte-identical `reports` assertion is in the test list, with its golden captured by running the **pre-change** code: a golden regenerated from the new code records whatever the new code does and could not fail.

---

### The view and its documentation are one commit

`TestGuidanceDocumentsEveryStaticView` and `TestResearcherGuideDocumentsEveryStaticView` compare `duck.StaticViewNames()` against `core.md` and `docs/researcher-guide.md` in both directions, so registering `logs` without documenting it fails and documenting it without registering it fails too. Adding the view and documenting it therefore cannot be separate commits without a red one between them. They stayed separate steps because they are separate concerns to review, the SQL and the prose. Generalizing `reportUnionView` does stand alone as its own commit.

---

### `keepData` is inert for this view

`reportUnionView`'s `keepData` argument only does anything inside `csvScan`, and only under `dl.ReportType == dataset.ReportTypeAnswers`, which is unreachable for every `logs` member. It is passed as `true` to match `reportsView`. Recorded because the argument is not obviously inert at the call site, and a reader comparing `logs` to `report_prompts`, which passes `false` for a reason, would otherwise look for a reason here too.
