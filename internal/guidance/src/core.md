## Datasets and portals

- Auth, datasets and downloaded data are all per portal, and a portal is always a
  full hostname.
- A dataset is identified by its portal and its name, spelled as a ref,
  `<portal>/<name>` (e.g. `learn.concord.org/wildfire`), where a bare `<name>`
  resolves under the configured default portal. That is the spelling everywhere a
  dataset is named, on the command line included. The one exception is the
  `dataset_create` MCP tool, which takes the two as separate arguments, with
  the portal optional and the same fallback, and whose `name` must not contain a
  slash.
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
  carries `run_id`, `type`, `slug`, `report_type`, `complete`).
- `report_prompts` — the prompt and correct-answer text keyed by the
  `res_<N>_<question_id>_*` columns.
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
- `downloads` — a manifest dimension table.
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
