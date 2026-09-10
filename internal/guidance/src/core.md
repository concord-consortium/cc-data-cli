## Datasets and portals

- Auth, datasets and downloaded data are all per portal, and a portal is always a
  full hostname.
- A dataset is identified by its portal and its name, spelled as a ref,
  `<portal>/<name>` (e.g. `learn.concord.org/wildfire`), where a bare `<name>`
  resolves under the configured default portal. That is the spelling everywhere a
  dataset is identified, on the command line included. `dataset_create` is the
  exception: it takes the two as separate arguments, with the portal optional and
  the same fallback, and its `name` must not contain a slash. A bare name is also
  what `dataset_rename` takes as `new_name`, which renames within the same portal.
- A dataset's portal is always a hostname and never an environment alias, because
  it also names the folder the data lives in. `prod`, `staging` and `dev` are
  **refused** when naming a dataset, and the error names the hostname to use.
- An environment alias is accepted, and expanded, wherever a `portal` selects a
  server to read from rather than a folder to write into.
- If a fetch returns `NOT_AUTHENTICATED`, check the dataset's portal is a
  hostname before relaying a login.

## Runs and their data

- Datasets are duplicate-free by construction: re-fetching a run replaces its
  records. Create a dataset per point-in-time pull to compare over time.
- Report runs have a `report_type` (`answers`, `log`, `usage`). Log runs (slug
  `student-actions`) are fetchable as a report too and yield a clickstream
  event log (columns include `session`, `application`, `activity`, `event`,
  `event_value`, `time`, `parameters`, `extras`, `run_remote_endpoint`,
  `timestamp`, `user_id`, `primary_user_id`): process, timing, and sequence data.
  A dataset's summary lists each download's `report_type`, so the run kind is
  obvious.
  - The two time columns are in different units: `time` is epoch **seconds**
    (`to_timestamp(time)`), `timestamp` is epoch **milliseconds**
    (`to_timestamp(timestamp/1000)`). Both resolve to the same instant; use
    `timestamp` for sub-second ordering within a session. Order an event trace by
    `timestamp` (or `time`), not row order. Passing `timestamp` straight to
    `to_timestamp` gives year 57814, and dividing `time` by 1000 gives 1970, so
    match the unit. `parameters` and `extras` are VARCHAR holding JSON: parse with
    `::JSON`, e.g. `json_extract_string(extras::JSON, '$.activityPage')` (a bar
    change's `parameters` are `{bar, value, via}`).

## Views

Query these views (single dataset: unqualified; multi-dataset: schema-qualified
like `wildfire_2026.answers`):

- `reports` — report CSV rows unioned across runs, with a `run_id` column.
  `res_<N>_*` columns are **positional per run**: N is the resource's index in
  that run's list, so the same prefix can mean different activities across runs
  and one activity can shift position. Read `res_<N>` columns run-scoped (a
  per-run view, or `WHERE run_id = <id>`), using `res_<N>_name` to identify the
  resource. For cross-run question-level analysis use the `answers`/`history`
  stores plus `report_prompts`, which key by position-independent
  `remote_endpoint`/`question_id`. `res_<N>_<question_id>_answer` columns are
  VARCHAR (they hold prompt text on pseudo-header rows), so aggregate numerically
  with `TRY_CAST`, e.g. `sum(TRY_CAST(res_1_q_answer AS DOUBLE))`. Runs are
  combined by column name (`UNION ALL BY NAME`), so runs with different schemas
  coexist: a column present in only some runs is NULL for the rows of runs that
  lack it (not an error, no misalignment). For example a
  `student-actions-with-metadata` run adds roster columns (`student_name`,
  `class`, `learner_id`, ...) that are NULL for a plain `student-actions` run's
  rows, while shared columns like `event`/`time` populate for both. When a column
  exists only for some runs, scope by `run_id` (or filter via `downloads`, which
  carries `run_id`, `type`, `slug`, `report_type`, `hide_names`, `complete`).
  **`student_name` and `username` mean different things run by run.** Where a run
  hid names, `student_name` holds the student id and `username` a hash, under the
  same column names, so runs fetched under different roles are union-compatible
  and blended here with nothing in the row to tell them apart. Four of the five
  Athena reports and the Portal metadata report are all affected. Read
  `hide_names` before counting or grouping by a name, joining `downloads`
  **type-qualified** because a run has one `downloads` row per download and an
  unqualified join multiplies the rows: `reports r JOIN downloads d ON d.run_id
  = r.run_id AND d.type = 'report'`. It is NULL for a download whose filter is
  not on disk, which is not the same as false. Hiding names also changes the
  column's **type**, since a student id is numeric: `student_name` scans as
  BIGINT for a run that hid names and VARCHAR for one that did not, so cast
  (`student_name::VARCHAR`) when comparing or grouping it across runs.
- `report_prompts` — the prompt and correct-answer text keyed by the
  `res_<N>_<question_id>_*` columns.
- `logs`: the log-type report CSVs (`student-actions`,
  `student-actions-with-metadata`, `teacher-actions`) unioned with `run_id`, plus
  four parsed columns. The original `parameters`, `extras`, `time` and `timestamp`
  columns are retained unchanged alongside them.
  `parameters_json` and `extras_json` are the payload and the UI-state snapshot as
  JSON, so `extras_json->>'selectedNavTab'` works directly, and `->>` on them is
  always safe. Each parsed column keeps its source string beside it, and that is the
  discriminator whenever a NULL matters: `parameters IS NOT NULL AND parameters_json
  IS NULL` is a value that was present and did not parse, while a NULL source column
  means there was nothing to parse (an empty field, or a run whose CSV never carried
  that column at all).
  **`event_time` and `received_time` are different clocks, not one instant at two
  resolutions.** `event_time` comes from `time`, the client device's own clock
  rounded to seconds, which the ingester replaces with the server clock when the
  client sends nothing usable. `received_time` comes from `timestamp`, server
  receipt in milliseconds. Ordering or measuring intervals on `event_time` alone
  ties a large share of adjacent events and mixes two clocks across rows, so
  prefer `received_time` for sequence and interval work and read the difference
  between them as device-clock skew rather than as latency. Both are timezone-naive
  `TIMESTAMP` holding **UTC** by convention, which is what makes them comparable to
  each other and to the stores' `_fetched_at`.
  The three reports do not have the same shape, so **`username` means up to five
  different things here**: absent for `student-actions`, the student's for
  `student-actions-with-metadata`, the teacher's for `teacher-actions`, and a
  salted hash instead of either where the run hid names (`student_name` then holds
  the student id). Before counting or grouping any name-bearing column, join
  `downloads` type-qualified for **both** `slug` and `hide_names`, since neither
  alone distinguishes the five: `logs l JOIN downloads d ON d.run_id = l.run_id AND
  d.type = 'report'`. `hide_names` is NULL where the run's filter is not on disk,
  which the `reports` entry above explains and which is not the same as false.
- `answers`, `history` — the identity-keyed stores (double-decoded
  `report_state`; no dedup needed).
- `run_membership` — one row per membership line with `run_id` and `type`. Join
  **type-qualified**: `answers a JOIN run_membership m USING
  (source_key, remote_endpoint, question_id) WHERE m.run_id = 584 AND m.type =
  'answers'`. History joins add `history_id` to the USING list.
- Reports-to-stores join: `reports.res_<N>_remote_endpoint =
  answers.remote_endpoint`, with `res_<N>_<question_id>_*` pairing to
  `answers.question_id`.
- `attachment_files` — per-file metadata (including `content_type`) and local paths.
- `attachment_states` — offloaded CODAP/SageModeler state as `filename`, `id12`,
  `name`, raw `content`, and `state = TRY_CAST(content AS JSON)`. Extract with
  `state->>'$.path'` (scalar) or `unnest(from_json(state, '["json"]'))` (arrays);
  `state IS NULL AND content IS NOT NULL` marks a file that failed to parse.
  Narrow: only the state the current answer points at.
- `attachment_content` view: like `attachment_states` but covers EVERY offloaded
  text/JSON attachment (`id12`, `name`, `source`, `public_path`, `content`,
  `state`), not just the current-answer one, so you can diff every saved snapshot
  of a doc across a session's history. Binary attachments (audio, images) are
  excluded here (not UTF-8 text) but remain downloadable via `attachment_files`.
- `student_id_mapping` — one row per `learner_id` from Student ID Mapping runs,
  deduplicated across runs with the latest fetch winning. Join to `answers` and
  `history` on `run_remote_endpoint = remote_endpoint`. A NULL
  `run_remote_endpoint` is a learner with no secure key, not missing data: every
  such learner carries the same endpoint string, so the join key is withheld
  rather than attributing one learner's answers to all of them. To check whether
  one run repeated a learner, compare that run's own row count with its distinct
  learner count: `SELECT count(*), count(DISTINCT learner_id) FROM
  report_<run_id>`. Do not compare against this view's rows for that run: it
  deduplicates **across** runs, so a learner a later run also holds is absent
  here without the earlier run having repeated anything.
- `student_metadata` — one row per `learner_id` from Student Metadata runs, same
  dedupe and the same `run_remote_endpoint` rule, carrying the names and roster
  labels the mapping view deliberately has none of. Join to
  `student_id_mapping` on `learner_id`. `hide_names` is the run's own setting:
  where it is true, `student_name` holds the student id and `username` a hash,
  so a dataset holding runs fetched under different roles is filterable rather
  than silently mixed. It is NULL for any download whose filter was not
  recorded, which includes every download made before cc-data recorded filters
  and any recovered by a reindex with no manifest. The same rows also reach
  `reports`, which has no such column, so name-sensitive work belongs on this
  view or on a type-qualified `downloads` join.
- `downloads` — a manifest dimension table: `run_id`, `type`, `slug`,
  `report_type`, `hide_names` and `complete`. It is where a per-download fact
  belongs, so it is the join for anything that varies by run rather than by row.
  **One row per download, not per run**: a run that had its report, answers and
  history pulled has three, so join it type-qualified (`AND d.type = 'report'`)
  or the join fans out.
- Per-run views: `report_<run>`, `answers_<run>`, `history_<run>`, and
  `report_<run>_job_<job>` for a run that has post-processing jobs.

Learner identity (within a portal): in `answers`/`history`, a learner-run is keyed
by `remote_endpoint` (one per student per offering-run); `platform_user_id` can
split within a run and `source_key` is the data-source host, so do not count
learners by either. Join to `reports` on `remote_endpoint = res_<N>_remote_endpoint`
to attach the person: `user_id` is the Portal user (the learner) and `learner_id`
is that user in one offering. Count distinct learners by `user_id` (or `learner_id`);
cross-portal identity is out of scope.

## Identity columns

A record is identified across the stores by these columns, which are also the
`USING` key when joining a store to `run_membership`:

- `source_key` — the data-source host the record came from.
- `remote_endpoint` — one per student per offering-run, the learner-run key.
- `question_id` — the question within the resource.
- `history_id` — one snapshot within an answer's history. Only history joins
  need it; answers joins use the other three.

## Multi-dataset (longitudinal)

More than one dataset can be registered for a single query; each registers under
its own schema. There are no implicit cross-dataset unions — write them
explicitly with provenance, e.g.
`SELECT * FROM fall_2026.answers UNION ALL BY NAME SELECT * FROM spring_2027.answers`.

## Sensitive data

Datasets hold sensitive student data. You may auto-read a dataset's summary; do
not dump raw JSONL stores into the conversation by default. Suggest purging a
dataset when its data is no longer needed rather than archiving it to shared
drives.
