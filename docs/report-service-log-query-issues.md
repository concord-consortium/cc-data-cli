# report-service: log reports fail on large, long-running assignments

Written 2026-08-11 from a real attempt to pull CLUE log events for the Neural
Engineering Dataflow lessons. Everything here is evidence from production runs
2281–2298 on <https://report-server.concord.org>, report slug
`student-actions-with-metadata`.

Code references are to the `report-service` repository, paths relative to its
`server/` directory.

## Summary

A log report over an assignment with a few hundred learners spread across
several school years cannot be run. Not "is slow" — cannot be run, by any
combination of filters the form offers. Sixteen attempts produced two usable
CSVs, both from small, short-lived assignments.

The cause is that the generated Athena SQL constrains only the `secure_key`
partition, leaving `app`, `year` and `month` unbounded. Each learner then
multiplies out across every app/year/month combination the table projects, and
the query spends its life listing S3 prefixes.

Three separate defects compound:

1. **`app` is never constrained** — a 15× reduction nobody can ask for, because
   the form has no way to express it. Exposing it as an optional filter is the
   fix worth doing.
2. **Athena's failure reason is discarded** — the server is told exactly what
   went wrong and shows the user the word "Failed".
3. **A date range is effectively required but presented as optional** — without
   one, the ceiling is roughly 150 learners.

## Background: how the log table is partitioned

The Glue table `log_ingester_production.logs_by_app_and_secure_key` uses Athena
partition projection:

| Partition key | Projection | Cardinality |
|---|---|---|
| `app` | enum of 15 values (`Activity_Player`, `CLUE`, `CODAP`, …) | 15 |
| `year` | integer, range 2014–2050 | 37 |
| `month` | integer, range 1–12 | 12 |
| `secure_key` | **injected** — values must come from the WHERE clause | n |

`storage.location.template` is
`s3://log-ingester-production/logs_by_app_and_secure_key/${app}/${year}/${month}/${secure_key}/`,
so every combination is a distinct S3 prefix that Athena must probe.

`ReportQuery.get_athena_query/3` (`lib/report_server/reports/report_query.ex:91`)
emits exactly one partition predicate:

```elixir
where = [
  "log.secure_key IN #{ReportUtils.string_list_to_single_quoted_in(secure_keys)}"
]
```

With nothing bounding `app`, `year` or `month`, each secure key expands to
15 × 37 × 12 = **6,660** prefixes. Athena rejects any query that could read more
than 1,000,000 partitions, which puts the hard ceiling at about **150 learners**.

## Evidence

Runs were matched to Athena executions by submission time. The executions live in
the user's own workgroup, named `<portal_server> <user_id> <email>` with every
non-`[a-z0-9]` character replaced by `-`
(`lib/report_server/athena_db.ex:103`) — e.g.
`learn-concord-org-28-scytacki-concord-org`.

### First attempt — no date range

| Run | Learners | SQL | Outcome |
|---|---|---|---|
| 2285 | 78 | 4.2 KB | **succeeded**, 20 min |
| 2286 | 97 | 5.0 KB | **succeeded**, 26 min |
| 2284 | 176 | 8.1 KB | `HIVE_EXCEEDED_PARTITION_LIMIT`, 22 min |
| 2290 | 519 | 21.0 KB | `Query timeout`, 30 min |
| 2292 | 522 | 21.1 KB | `Query timeout`, 30 min |
| 2291 | 528 | 21.3 KB | `Query timeout`, 30 min |
| 2289 | 561 | 22.6 KB | `Query timeout`, 30 min |
| 2282 | 670 | 26.6 KB | `HIVE_EXCEEDED_PARTITION_LIMIT`, 21 min |
| 2288 | 694 | 27.7 KB | `HIVE_EXCEEDED_PARTITION_LIMIT`, 28 min |
| 2287 | 2,130 | 82.4 KB | `CONSTRAINT_VIOLATION`, 1 s |
| 2283 | 3,493 | 134.5 KB | `CONSTRAINT_VIOLATION`, 1 s |
| 2281 | 3,669 | 141.5 KB | `CONSTRAINT_VIOLATION`, 2 s |

The boundary between 97 (succeeded) and 176 (failed) is exactly where
1,000,000 / 6,660 ≈ 150 predicts it.

The `CONSTRAINT_VIOLATION` message is:

> For the injected projected partition column secure_key, the WHERE clause must
> contain only static equality conditions, and at least one such condition must
> be present. Predicates provided cannot be converted to a valid partition.

That is the same problem at a larger scale: the `IN` list grows past what Athena
will expand into injected partition values, and it is rejected during planning.

### Second attempt — with a date range

`apply_date_range/3` (`lib/report_server/reports/report_query.ex:156`) does emit
`log.year`/`log.month` predicates, so a date range prunes the projection. Six
runs were re-submitted with `2022-08-01 … 2026-09-01` (49 months), which cuts
6,660 combinations per key to 15 × 49 = 735 and lifts the partition ceiling to
about 1,360 learners.

`HIVE_EXCEEDED_PARTITION_LIMIT` did not recur. All six still failed:

| Run | Assignment(s) | Learners | Scanned | Outcome |
|---|---|---|---|---|
| 2293 | 2460 | 670 | 52 MB | `Query timeout`, 30.5 min |
| 2294 | 2462 | 561 | 43 MB | `HIVE_S3_THROTTLING` (S3 503), 6.1 min |
| 2295 | 2463 | 519 | 40 MB | `Query timeout`, 30.1 min |
| 2296 | 2464 | 528 | 32 MB | `Query timeout`, 30.4 min |
| 2297 | 2465 | 522 | 36 MB | `Query timeout`, 30.4 min |
| 2298 | 2467, 2468, 3052, 2841 | 694 | 72 MB | `Query timeout`, 30.1 min |

**The scanned column is the point.** Thirty minutes to read 40 MB. These queries
are not data-bound; they are spending the entire budget on S3 round trips.
520 learners × 15 apps × 49 months ≈ 382,000 prefixes to list.

The `HIVE_S3_THROTTLING` failure is the same cost showing up as a rate limit —
six of these in flight at once is enough to get 503s back from S3.

## Issue 1 — the generated SQL never constrains `app`

**This is the fix worth doing.** Fourteen of the fifteen projected `app` values
are irrelevant to any given report, and the report knows which one it wants: a
CLUE report will never match `CODAP` or `Activity_Player` rows. Yet
`get_athena_query/3` emits no `log.app` predicate, so all 15 are probed.

Adding it cuts prefix probing 15× — for the runs above, from ~382,000 to
~25,000. Combined with a date range that is a ~270× reduction against the
current behaviour, and it turns the failing queries into ones that should finish
in minutes.

The obstacle is that a report run does not currently carry an application. The
`app` value is a property of the log rows, not of the filter.

### Recommended: an optional `app` filter for advanced users

**Add `app` as an optional filter on the form.** Leave it blank and behaviour is
exactly as it is today; set it and the query gets the predicate that makes it
finish. This is the smallest change, it needs no mapping to maintain, and it
puts the decision with the person who knows which application they are asking
about.

It is not automatic, and that is the trade: someone who does not know to set it
still gets the slow path. But it is deliverable now, and the two automatic
options below both carry maintenance burdens that are worse than the problem.

### Rejected: derive the app from the resource URL

`LearnerData.fetch/3` already returns `runnable_url` per learner, so the
application could be inferred by pattern-matching the URL. This needs a mapping
from URL patterns to log `app` values, living somewhere, kept in sync as
applications are deployed, renamed, or moved to new domains. The CLUE work in
this repository ran into exactly that class of problem from the other direction —
a `url LIKE '%collaborative-learning%'` filter silently missed an entire
standalone Dataflow deployment on its own domain. A mapping like this is wrong
quietly, and being wrong here means silently dropping log rows.

### Rejected for now: use the portal's tool field

The principled version is to match on a field the portal already stores against
each resource, rather than inferring from a URL. That field exists —
`external_activities.tool_id`, joining to `tools` — but it does not currently
support this:

- **The vocabulary does not match.** The `tools` table has two rows,
  `ActivityPlayer` and `LARA`. The log `app` enum has fifteen values including
  `CLUE`, `CODAP` and `CEASAR`. These are not the same taxonomy, so a mapping
  layer is needed anyway.
- **It is unpopulated where it would be needed.** 805 of 3,515
  `external_activities` have a null `tool_id` — including **every one of the 15
  Dataflow activities in this write-up**. All four spot-checked (2460, 2735,
  2841, 3255) came back null.
- **It is already used for other things,** so repurposing it means auditing
  those uses and then keeping the values correct forever, across teams that have
  no reason to know a log query depends on them.

Getting this right means deciding the correct app for every resource, populating
it, and keeping it in sync — a much larger project than the query fix it would
enable. Worth doing if the portal wants a reliable application field for its own
sake; not worth blocking this on.

### Correctness caveat, whichever route

`none` is one of the projected `app` values, so some rows may not carry the app
you expect. Any `app` predicate should be additive — when in doubt, emit no
predicate rather than silently dropping rows. We checked this for CLUE against a
20-key sample and found `CLUE` only, but that is a sample, not a guarantee for
other applications.

## Issue 2 — Athena's failure reason is discarded

`athena_query_state` is set only from Athena's `get_query_info`
(`lib/report_server/reports/athena_run_ops.ex:39`), so any run in the `failed`
state reached Athena and Athena returned a `StateChangeReason` explaining why.
Those reasons are specific and actionable — `HIVE_EXCEEDED_PARTITION_LIMIT`,
`Query timeout`, `HIVE_S3_THROTTLING`, `CONSTRAINT_VIOLATION` each imply a
different next step for the user.

None of it is kept. `report_runs` has no error column, so the reason is dropped
on the floor. The user sees "Report status: Failed" and the API returns:

```json
{"error":"NOT_READY","message":"The report is not ready to download.",
 "athena_query_state":"failed"}
```

CloudWatch `/ecs/report-server` is no better — it logs the per-assignment
`Uploading learners to learners/<uuid>/<uuid>.json` lines and then nothing,
because from the application's point of view nothing went wrong.

The cost of this is measured in hours. Recovering the reason required knowing
the workgroup naming scheme, listing executions, and matching them to runs by
submission timestamp:

```
aws athena list-query-executions --work-group learn-concord-org-28-scytacki-concord-org
aws athena batch-get-query-execution --query-execution-ids <ids...>
```

That is not available to anyone without AWS credentials, which is to say: not
available to the people the report server exists to serve.

Suggested: store `athena_query_id` and Athena's `StateChangeReason` on
`report_runs`, expose both through `/api/v1/reports/:id`, and show the reason on
the run page. The query id alone would be a large improvement, since it makes the
execution findable.

## Issue 3 — the date range is effectively required

The form presents "Include dates: Earliest date / Latest date" as optional, and
the fields only appear once a first filter has a value
(`lib/report_server_web/live/report_live/form.html.heex:86-94`). Nothing
indicates that leaving them blank caps a log report at roughly 150 learners.

Suggested, in increasing order of helpfulness:

- Default the range to something bounded rather than unbounded.
- Warn when a log report is submitted without dates.
- Compute the learner count before submitting — `LearnerData.fetch/3` already
  runs against the portal *before* the Athena query is built, so the count is in
  hand — and tell the user when the run is unlikely to complete, with the
  partition arithmetic behind it.

That last one is cheap and would have saved every hour spent on this.

## Not an issue: the 256 KB SQL check

`AthenaDB.query` calls `check_query_size(sql)` before contacting Athena
(`lib/report_server/athena_db.ex:172`), rejecting SQL over 262,144 characters.
Recording this because it is a tempting explanation and it is wrong: the largest
SQL any of these runs generated was 141.5 KB, and every failure came back from
Athena itself. The check never fired.

## Workaround, and what it measures

Query Athena directly, adding `app = 'CLUE'` and a year bound, chunking the
secure keys, and joining the learner metadata locally rather than joining the
`learners` table in Athena. Scripts are in the cc-data-cli repository under
`local-data/log-events/`: `learner_keys.rb` exports one row per learner
including `portal_learners.secure_key`, and `athena_logs.py` runs the query.

This was validated against a run the report server had already completed
successfully — run 2285, 78 learners — and returned **the same 10,319 rows, the
same ids, and identical values across all eleven log columns**.

The comparison is the strongest argument for fixing issue 1:

| | Report server | Same query, with `app` constrained |
|---|---|---|
| Run 2285 (78 learners) | succeeded in **20 min** | succeeded in **7 s** |
| All 15 activities (3,669 learners) | never completed | **9m40s**, 258,916 rows |

Nothing about the second column is clever. It is the same SQL over the same
table with one extra predicate — one the form gives nobody a way to set.

Before relying on `app = 'CLUE'`, we checked the assumption flagged in issue 1: a
20-key sample of the largest assignment, queried with no `app` predicate,
returned rows under `CLUE` and nothing else, on both the partition and the
`application` column. That is a sample rather than a proof, and a real
implementation should still fall back to no predicate rather than risk dropping
rows.

This route needs AWS credentials and portal database access, so it is a
workaround for maintainers, not for researchers — which is precisely why the
first two issues matter.
