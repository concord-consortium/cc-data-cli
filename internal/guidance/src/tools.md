## Tools

- `version` — the cc-data binary version.
- `auth_status` — the portals with stored credentials; `check=true` validates each
  token over the network, one call per portal.
- `reports_list` — the user's report runs for a portal, which is where a `run_id`
  comes from.
- `reports_jobs` — a run's post-processing jobs.
- `get_report` — the report CSV for a run, into a dataset.
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
- `query` — SQL over one or more datasets, which is how every question about the
  data gets answered.
