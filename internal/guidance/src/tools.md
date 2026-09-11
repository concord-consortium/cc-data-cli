## Tools

- `version` — the cc-data binary version.
- `auth_status` — the portals with stored credentials; `check=true` validates each
  token over the network, one call per portal.
- `reports_list` — the user's report runs for a portal, which is where a `run_id`
  comes from.
- `reports_jobs` — a run's post-processing jobs.
- `reports_filter_options` — the values a report filter dimension offers the user,
  narrowed by any selections already made. Use it to assemble a filter without the
  web form, or on its own to answer what data the user can see.
- `reports_create` — a new report run from a report slug and a filter, which is
  how a run is made without the web form. Assemble the filter with
  `reports_filter_options` first.
- `reports_duplicate` — a fresh snapshot of an existing run. Athena runs are frozen
  once they finish, so duplicating is how they are re-run; a Portal report is
  computed live, so re-read it with `get_report` instead and pass `force` only if
  a second run id is genuinely wanted.
- `get_report` — the report CSV for a run, into a dataset. A Portal report is
  computed per request, so re-read it by passing `refresh` rather than
  duplicating the run.
- `get_answers`, `get_history` — a run's student answers, and the full series of
  how each answer's interactive state evolved, into a dataset.
- `get_attachments` — a run's file attachments, into a dataset. Fetch that run's
  answers or history first; attachments are reached through those records.
- `dataset_create` — a new, empty dataset to pull runs into.
- `dataset_list` — every dataset across all portals.
- `dataset_show` — one dataset's holdings, per-type totals, download table and
  warnings. Read this rather than the dataset's files.
- `dataset_rename`, `dataset_edit` — a dataset's name, and its description.
- `dataset_delete` — the whole dataset, folder and data together.
- `dataset_purge` — a dataset's downloaded data, keeping the dataset itself.
- `dataset_reindex` — rebuilds a dataset's manifest from what is on disk, for a
  dataset whose summary looks wrong.
- `dataset_materialize` — writes each of a dataset's views to a Parquet file
  that queries then read instead of the raw JSONL and CSV. It never changes an
  answer, so it is a speed decision, not a correctness one.
- `query` — SQL over one or more datasets, which is how every question about the
  data gets answered.
