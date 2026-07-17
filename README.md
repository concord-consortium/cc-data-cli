# cc-data

A command-line tool for [Concord Consortium](https://concord.org) researchers to download report data (report CSVs, student answers, interactive state history, and file attachments) into local, named **datasets**, and query across all of it with SQL via an embedded [DuckDB](https://duckdb.org).

`cc-data` collapses "log into the report server → run a report → download a CSV → open it somewhere → join it against answers by hand" into a local workflow you (or Claude, on your behalf) can drive from the terminal: pull the data once, then ask questions of it, including natural-language questions about both temporal and state information, via the Claude Code skill the CLI installs.

> **Status: early development.** The commands and layout described below are the design target (see [REPORT-77](https://concord-consortium.atlassian.net/browse/REPORT-77) and the [REPORT-71 design doc](https://gist.github.com/dougmartin/034b3004e9cd42f6e9960a478358d622)); not everything is implemented yet.

## What it does

- **Authenticate once** against a report-server portal via a browser loopback login (`cc-data login`). Tokens are stored per portal in your OS keychain (macOS Keychain / Linux Secret Service / Windows Credential Manager), with a `0600` `~/.config/cc-data/credentials.json` fallback.
- **List your report runs** (`cc-data reports list`): the runs you authored on the report server.
- **Download a run's data** into a dataset:
  - `get report <run-id>`: the report CSV (polls until the Athena query succeeds; never writes a partial file).
  - `get answers <run-id>`: raw answer JSON for the run's learners, as JSONL.
  - `get history <run-id>`: the interactive state history series (full series by default), as JSONL.
  - `get attachments <run-id>`: S3 file attachments referenced by answers/history (open-response audio, offloaded CODAP/SageModeler state).
- **See what's in a dataset**: `cc-data dataset list` and `dataset show` render instant summaries from the manifest (downloads by type, answer/history/attachment counts, coverage, size, age). `--json` emits the same summary for Claude and scripts, and every `get` ends with a machine-readable result line so tooling can confirm what happened.
- **Query everything with SQL** (`cc-data query`, `cc-data repl`): an ephemeral in-memory DuckDB exposes unified `reports`, `answers`, and `history` views across everything in the dataset, plus `run_membership` (which runs' fetches covered which records), `attachment_files` (per-attachment metadata and local paths), `attachment_states` (queryable offloaded CODAP/SageModeler state), and a `downloads` provenance dimension table. Audio attachment content itself isn't SQL-queryable; `attachment_files` gives you its local path instead.
- **Teach Claude about itself**: on install, `cc-data` writes a Claude Code skill (and a one-line `~/.claude/CLAUDE.md` pointer) so Claude can download and query data for you, and ships `cc-data mcp`, an MCP stdio server for Claude Desktop using the same stored credential.

## Datasets

A dataset is a named, single-portal, user-managed workspace meant to combine many downloads (classes, activities, time periods) so you can query across them in one place. Answer and history records live in identity-keyed stores (exactly one current record per identity); report CSVs stay per-run files; small membership files record which identities each run's fetch covered:

```
~/cc-data/
  learn.concord.org/               # one folder per portal (separate login, separate run-id space)
    datasets/
      <name>/
        manifest.json              # provenance index: filters, coverage, counts, completeness, cursors
        report_584.csv             # report CSVs stay per-run (no per-row identity)
        report_612.csv
        answers.v3.jsonl           # one current record per (source, learner, question)
        history.v7.jsonl           # one record per history snapshot
        members_answers_584.jsonl  # which identities run 584's fetch covered
        attachments/               # audio .mp3, offloaded CODAP/SageModeler .json state
```

- **A dataset never contains duplicate data.** Every downloaded item is checked against what the dataset already holds by identity (an answer is this learner's answer to this question; a history record is one snapshot in that answer's series; an attachment file is one S3 object) and overwrites the existing copy; the incoming record was just read live, so it always wins. Overlapping runs, regenerated reports, and repeated pulls simply update records in place, and every `get` reports its counts (fetched/new/updated/removed). To compare pulls over time (e.g. answer data as it evolves), create a dataset per pull.
- `manifest.json` records the provenance the files can't: the run's filter selection (raw values and resolved human labels), coverage, history mode, completeness, merge counts, and resume cursors. The filesystem is the source of truth for what exists; `cc-data dataset reindex` rebuilds a lost or drifted manifest.
- Writes are atomic and paged downloads are resumable: pages append to a per-download segment with the cursor saved, then merge into the store as a new version with an atomic swap. An interrupted `get` picks up from its last cursor, a truncated pull is honestly marked incomplete, and queries only ever see complete, duplicate-free data.
- Datasets are purely local. `cc-data dataset delete` and `purge` only touch your disk.

Every `get`/`query`/`repl` names its dataset as `<portal>/<name>`; a bare `<name>` resolves under the optional `default_portal` config.

## Commands (sketch)

```
cc-data init                                  # install the Claude skill + prompt for login
cc-data login --portal <portal>               # browser loopback login; token stored per portal
cc-data logout                                # revoke the current token server-side
cc-data auth status                           # stored credentials per portal + default_portal
cc-data reports list --portal <portal>
cc-data reports jobs <run-id> --portal <portal>   # list a run's post-processing outputs
cc-data dataset create|list|show|rename|edit|delete|purge|reindex ...   # list/show take --json (and show --full)
cc-data get report      <run-id> --dataset <portal>/<name> [--job <id>]
cc-data get answers     <run-id> --dataset <portal>/<name>
cc-data get history     <run-id> --dataset <portal>/<name>
cc-data get attachments <run-id> --dataset <portal>/<name> [--answer <id>] [--url] [--inline]
cc-data query --dataset <portal>/<name> "SELECT ..." [--format table|csv|json|jsonl]
cc-data repl  --dataset <portal>/<name>
cc-data mcp                                   # MCP stdio server for Claude Desktop
```

Every command has real per-command help; `cc-data get answers --help` is the authoritative usage reference.

## Sensitive local data

**Datasets are sensitive student data.** Downloading turns transient exports into a durable local corpus that can contain student names: from report CSVs generated with `hide_names` off, from resolved manifest labels (class and teacher names), and from incidental PII in free-text responses. Treat dataset folders accordingly:

- Purge or delete datasets when you no longer need them (`cc-data dataset purge|delete`); don't archive them to shared drives. `dataset list`/`show` display each dataset's age so retention stays visible. Researchers legitimately keep datasets for years; cleanup is your call, but make it a deliberate one.
- If the OS keychain is unavailable, credentials fall back to `~/.config/cc-data/credentials.json` with `0600` permissions (on Windows, where POSIX mode bits don't exist, the file relies on your user profile's ACLs instead). Revoke tokens you no longer use via the report server's token-management UI or `cc-data logout`.
- **What Claude may auto-read:** the Claude skill directs Claude to run `cc-data dataset show --json` (a summary of labels, counts, and coverage rendered from `manifest.json`) to understand a dataset, not to read the raw JSONL/CSV data files by default. Querying student data through DuckDB is an explicit, user-driven step.
- A presigned attachment URL (from `get attachments --url`) is a short-lived, credential-free capability to one student's file; don't paste it into shared or persistent channels. Prefer downloading into the dataset over `--url`.
- **Manual token entry:** on a headless or SSH host, `cc-data login --token -` reads the token from stdin (piped, or an echo-off prompt on a TTY) — the recommended manual form. The bare `--token <value>` form works but is discouraged: flag values land in shell history and process lists.
- **Dataset-folder trust boundary:** the DuckDB sandbox confines `query`/`repl` to the named dataset folders. On the bundled DuckDB the sandbox resolves symlinks, so this is a *writable-files* boundary, not a symlink escape: anyone who can write into a dataset folder can plant files your queries then read as trusted, so a dataset folder inherits the trust of whoever can write to it.
- **`--server` trust boundary:** `cc-data login --server <origin>` drives the entire login and token exchange against `<origin>`. The CLI enforces an allowlist (a `concord.org`/`concordqa.org` host or subdomain, or a loopback host; http only for loopback) so a social-engineered `--server` cannot capture your login. Widening it is deliberately a code change.
- Claude never drives the browser login. On an expired or missing token the CLI emits a structured `NOT_AUTHENTICATED` error and a human runs `cc-data login`.

## Development

The CLI is written in Go. DuckDB is embedded via [`duckdb-go`](https://github.com/duckdb/duckdb-go) (cgo), so builds are per-platform native; there is no trivial cross-compile.

```
go build ./...
```

Releases are built with goreleaser on native GitHub Actions runners per platform, with a Homebrew formula for install (`brew install concord-consortium/tap/cc-data`). Note that `brew uninstall` removes only the binary; run `cc-data uninstall` first to remove the Claude skill, the `~/.claude/CLAUDE.md` pointer, and (optionally) your stored credentials. Your datasets are never removed automatically — `cc-data uninstall` prints where they remain.

A direct browser download of a macOS binary triggers a one-time online Gatekeeper check on first run (the binaries are notarized but not stapled); a `brew install` carries no quarantine, so Gatekeeper does not assess it.

The client/server seam is a versioned HTTP contract (`/api/v1/...`) against the Concord report server; this repo shares no code with it.

## License

[MIT](LICENSE)
