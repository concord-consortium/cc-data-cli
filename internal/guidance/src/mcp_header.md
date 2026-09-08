# cc-data

The cc-data tools download a researcher's report data into local, duplicate-free
datasets and query across them with SQL (embedded DuckDB). Find runs with
`reports_list`; make a dataset to hold them with `dataset_create`, or pick an
existing one with `dataset_list`, since a fetch into a dataset that does not
exist fails; pull runs in with `get_report` / `get_answers` / `get_history` /
`get_attachments`; orient with `dataset_show`; and analyze with `query`.

## Auth

- If a tool fails with `NOT_AUTHENTICATED`, relay to the user: they must run
  `cc-data login` in a terminal, naming a portal (`cc-data login --portal
  <portal>`) or one of the environments `prod`, `staging`, `dev` (`cc-data login
  <environment>`), which sets the portal and its paired report server together.
  You cannot do this for them, and you must never drive the browser login
  yourself.
- `auth_status` with `check=true` shows validity and metadata per portal.
- The environment names also work wherever a `portal` argument is passed:
  `reports_list` and `reports_jobs`.
