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
