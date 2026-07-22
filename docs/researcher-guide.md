# cc-data: Researcher Guide

This guide is for **Concord Consortium researchers** who want to pull their report
data onto their own machine and ask questions of it, either in plain English
through Claude Code, or directly with SQL.

`cc-data` replaces the "log into the report server → run a report → download a CSV
→ open it somewhere → join it against answers by hand" cycle with a local
workflow: pull the data once into a named **dataset**, then ask questions of all
of it at once: student answers, the full history of how those answers evolved,
clickstream action logs, and file attachments (audio, saved CODAP/SageModeler
documents), across many runs, activities, and classes in one place.

`cc-data` lets you query a dataset directly with SQL, the full power of a
database over all of your report data at once. But you do not need to know SQL to
use it: the most common way to work with `cc-data` is to talk to Claude Code in
ordinary language and let it pull and query the data for you, with SQL there
whenever you want to run it yourself.

> **`cc-data` downloads reports; it doesn't create them (yet).** You generate
> report runs in the report server's web interface, and `cc-data` pulls the results
> of runs that already exist. Kicking off a new report run from the command line or
> from Claude is **not supported today**; it's planned for a future release. For
> now: create the run in the report server, then ask `cc-data` (or Claude) to
> download it.

---

## 1. Installing the CLI

`cc-data` ships **prebuilt binaries for macOS and Linux** (on any other platform,
including Windows, you can build from source, see below). The macOS builds are
signed and notarized by Apple, so they install and run without security prompts.

### macOS (recommended: Homebrew)

```
brew tap --trust concord-consortium/tap
brew install cc-data
```

- The `--trust` is required. Homebrew 6.0 and later make you trust a third-party
  tap before installing from it (a supply-chain security measure). If you tapped
  without it, run `brew trust concord-consortium/tap` before `brew install`.
- Homebrew installs into its own prefix and removes the download quarantine, so
  there is no Gatekeeper prompt.
- Don't have Homebrew? Install it first from [brew.sh](https://brew.sh).

**Without Homebrew:** open the latest release on the
[Releases page](https://github.com/concord-consortium/cc-data-cli/releases) and,
under **Assets**, download the archive whose name ends in `_darwin_arm64.tar.gz`
(Apple Silicon) or `_darwin_amd64.tar.gz` (Intel). Unpack it and move `cc-data`
onto your `PATH`. The binary is notarized, so on first run macOS does a quick
online check with Apple and then lets it run.

### Linux (x86-64)

With Homebrew on Linux:

```
brew tap --trust concord-consortium/tap
brew install cc-data
```

Or download directly. Find the current version on the
[Releases page](https://github.com/concord-consortium/cc-data-cli/releases) (the
tag looks like `v0.1.0`); the assets follow the pattern
`cc-data_<version>_<os>_<arch>.tar.gz`. Set `VERSION` to that version without the
leading `v`:

```
VERSION=X.Y.Z   # replace with the current version from the Releases page, e.g. 0.1.0
curl -fsSL -o cc-data.tar.gz \
  "https://github.com/concord-consortium/cc-data-cli/releases/download/v${VERSION}/cc-data_${VERSION}_linux_amd64.tar.gz"
tar xzf cc-data.tar.gz
sudo mv cc-data /usr/local/bin/
```

Only x86-64 (amd64) Linux has a prebuilt binary; on ARM Linux, build from source.

### Build from source (any platform)

```
git clone https://github.com/concord-consortium/cc-data-cli.git
cd cc-data-cli
go build -o cc-data .
```

Requires Go 1.25 or newer and a C compiler (Xcode Command Line Tools on macOS,
`gcc`/`clang` on Linux), because the embedded DuckDB database is a cgo dependency.
A source build reports its version as `dev`, which is expected.

---

## 2. First-time setup

Two one-time steps: log in, then connect `cc-data` to Claude Code.

### Step 1: Log in

```
cc-data login
```

This opens your browser, you log in to the report server the normal way, and a
token is stored securely in your operating system's keychain (macOS Keychain,
Linux Secret Service, or Windows Credential Manager; where no OS keychain is
available, such as some headless Linux setups, it falls back to a protected file
in your config folder). You stay logged in until the token expires or you
`cc-data logout`.

- With no options, login targets the **production portal, `learn.concord.org`**.
- To use a different portal, pass `--portal`, e.g.
  `cc-data login --portal learn.portal.staging.concord.org`. Each portal is a
  separate login with its own data.
- Logins are per portal: you can be logged into several at once, and each
  dataset belongs to exactly one of them.

Most researchers only ever use production (`learn.concord.org`); the staging
portals (like `learn.portal.staging.concord.org`) are for testing. You don't
"switch" between portals; every dataset and command names its portal, so being
logged into more than one simply lets you work with each.

> **You always run `cc-data login` yourself: this is a deliberate security
> boundary.** Claude never drives the browser login on your behalf. Authenticating
> to the report server is what grants access to real student data, so that step is
> kept in your hands and out of the automated loop: a human completes the browser
> login, and the resulting token lives in your OS keychain where Claude can use it
> but never sees it. If Claude hits an expired or missing token it will stop and
> ask you to run `cc-data login` rather than trying to authenticate itself.

### Step 2: Connect it to Claude Code

```
cc-data init
```

This does three things:

1. Installs the **cc-data skill** into Claude Code, teaching Claude how to fetch
   and query your data.
2. Registers the **cc-data MCP server** with Claude Code so Claude can call
   `cc-data` directly.
3. Adds a one-line pointer to Claude's configuration file (`CLAUDE.md`, in your
   home folder) so Claude knows the skill exists.

Run it once. It's safe to re-run: it reports "already registered" rather than
duplicating anything. (Pass `--no-mcp` if you only want the skill and not the MCP
server.) After this, start (or restart) Claude Code and you're ready.

---

## 3. Using cc-data in Claude Code

Once setup is done, **just ask Claude in plain English.** Claude recognizes when
your question is about report data and uses `cc-data` to answer it, creating a
dataset, downloading the runs it needs, and querying across them.

You might say things like:

> "Pull report run 584 from learn.concord.org into a dataset called `wildfire`,
> and download the answers and history for it too."

> "In the `wildfire` dataset, how many students answered each question, and what
> did they write for the open-response prompt on page 3?"

> "Download the student-actions log for run 187 and tell me how much time each
> student spent on task and who disengaged."

Claude will download what it needs, run the queries, and explain the results.
Under the hood it's the same commands you could run yourself (see §4), so you can
always ask Claude to "show me the SQL you ran" or "give me that as a CSV."

A few things worth knowing about the division of labor:

- **You** log in (`cc-data login`) and decide when data is no longer needed.
- **Claude** downloads runs, organizes them into datasets, and writes/runs the
  queries.
- Claude reads a lightweight **summary** of a dataset (labels, counts, coverage)
  to orient itself; it does not dump raw student records into the conversation
  unless you ask it to query them, which is an explicit step.

This guide covers **Claude Code**, which is what `cc-data init` sets up. A
one-click **Claude Desktop** extension that uses the same underlying
`cc-data mcp` server is planned but not yet available; `cc-data init` does not
configure Claude Desktop today.

---

## 4. Using cc-data yourself (in the terminal)

You don't have to go through Claude: you can drive `cc-data` directly. A full
session looks like this.

### Find your report runs

`cc-data` downloads runs you've *already generated* in the report server. To see
which runs you can pull, and their IDs:

```
cc-data reports list
```

This lists your report runs with their `run_id`, `slug`, and state (add `--json`
to include the `report_type`). (Add `--portal <portal>` for a non-production
portal.) Through Claude, the same thing is just asking "what report runs do I
have?"

### A complete session

```
# 1. Create a dataset to hold your data (named <portal>/<name>)
cc-data dataset create learn.concord.org/wildfire

# 2. Download a run's data into it (use a run_id from `reports list`)
cc-data get report   584 --dataset learn.concord.org/wildfire
cc-data get answers  584 --dataset learn.concord.org/wildfire
cc-data get history  584 --dataset learn.concord.org/wildfire
# attachments reference answer/history records, so fetch those first
cc-data get attachments 584 --dataset learn.concord.org/wildfire

# 3. Ask a question with SQL
cc-data query --dataset learn.concord.org/wildfire \
  "SELECT question_id, count(*) AS answered FROM answers GROUP BY question_id"

# 4. Or explore interactively
cc-data repl --dataset learn.concord.org/wildfire
```

If you've configured a default portal, you can use a bare dataset name
(`wildfire`) instead of the full `learn.concord.org/wildfire`. The queryable views
(`reports`, `answers`, `history`, ...) are described in the next section.

### Getting results out

By default `query` prints a table. For analysis in R, Python, or a spreadsheet,
pick a machine-readable format and redirect it to a file:

```
cc-data query --dataset learn.concord.org/wildfire --format csv \
  "SELECT * FROM answers" > answers.csv
```

`--format` accepts `table` (the default), `csv`, `json`, or `jsonl`. Working
through Claude instead? Just ask it to "save those results as a CSV", it runs the
same command.

---

## 5. How datasets are organized

A **dataset** is a named, single-portal workspace that you fill with as many
downloads as you like, different runs, activities, classes, or time periods, so
you can query across all of them together. Datasets are stored on your own machine
in a `cc-data` folder in your home folder, organized by portal (for example, a
dataset named `wildfire` on `learn.concord.org` lives in
`cc-data/learn.concord.org/datasets/wildfire/`).

- A dataset is identified by `<portal>/<name>`, e.g.
  `learn.concord.org/wildfire`. (If you set a default portal, a bare `wildfire`
  works too.)
- Into a dataset you can pull four kinds of data for any run:
  - **report**: the report CSV (answers reports, and log/action reports).
  - **answers**: the raw student answer records.
  - **history**: the full series of how each answer's interactive state evolved.
  - **attachments**: the files answers reference: open-response audio, and
    offloaded (too-big-to-inline) CODAP/SageModeler documents.
- **A dataset never holds duplicate data.** If you re-download a run, or pull two
  overlapping runs, records are matched by identity (this learner's answer to this
  question; one snapshot in that answer's history) and updated in place: the
  freshly downloaded copy wins. So you can pull freely without creating
  duplicates.
- Because of that, to **compare data over time** (e.g. answers in the fall vs. the
  spring), make a **separate dataset per pull** and query across them.
- Datasets are **purely local** and can contain **sensitive student data**:
  names in CSVs, class/teacher labels, and incidental PII in free-text answers.
  Delete or purge them when you're done (`cc-data dataset purge <ref>`); don't
  archive them to shared drives.

When you (or Claude) query a dataset, the data is exposed as a set of SQL
**views** you can ask questions of:

| View | What it holds |
|---|---|
| `reports` | Report CSV rows across all runs, with a `run_id` column. Covers both answer reports and log/action reports. |
| `answers` | Student answers, one current record per learner-question. |
| `history` | Every interactive-state snapshot, how each answer evolved over time. |
| `report_prompts` | The prompt and correct-answer text for each question. |
| `attachment_files` | One row per downloaded file (audio, saved docs) with its type and local path. |
| `attachment_content` | The text/JSON content of every saved CODAP/SageModeler snapshot, queryable and diffable. |
| `run_membership`, `downloads` | Provenance, which run's fetch covered which records, and what each download was. |

### Report types

You generate report runs in the report server, and `cc-data` downloads whichever
runs you've created. There are **five kinds of report** (across three
`report_type`s: `answers`, `usage`, and `log`), all fetchable with `get report`
and all queryable through the same `reports` view. `cc-data` records each run's
type (shown by `dataset show`) so you can tell them apart.

**Student data, one row per student:**

- **Student Answers** (slug `student-answers`, type `answers`): the most detailed
  student report: student, class, teacher, and school identity plus each student's
  answer to every question in the resource(s) in your query. This is "what each
  student answered."
- **Assignment Usage by Student** (slug `student-assignment-usage`, type `usage`),
  the same student/class/teacher/school identity and per-resource summary counts
  (such as the total number of questions and answers), but **without** the
  individual answer text. Good for participation and completion at a glance.

**Action logs, one row per logged event (a clickstream):**

- **Student Actions** (slug `student-actions`, type `log`): the low-level log
  event stream for learners: focus, scroll, page changes, model- and tool-level
  interactions, and idle/timeout events, each with timing. This is the data behind
  the process, engagement, and sequence questions in the next section.
- **Student Actions with Metadata** (slug `student-actions-with-metadata`, type
  `log`), everything in Student Actions plus Portal roster columns (student,
  teacher, class, school, permission forms, portal ids), so you can break behavior
  down by student, class, or teacher.
- **Teacher Actions** (slug `teacher-actions`, type `log`): the same kind of event
  stream but for **teacher** actions (in activities, the Teacher Edition, and the
  class dashboard), filtered by teacher and/or activity over a date range.

The three `log` reports share the same clickstream columns; `student-answers` and
`student-assignment-usage` share the same per-student, per-resource shape.

**Other reports (not pullable by `cc-data`).** The report server also offers
several **portal reports**, aggregate metrics computed directly from the Portal
rather than from Athena: *Summary Metrics by Assignment*, *Detailed Metrics by
Assignment*, *Teacher Status*, *Detailed Metrics by School*, and *Summary Metrics
by Subject Area*. These are served in the report server's web interface and are
**not currently downloadable through `cc-data`**, which pulls only the Athena
report types listed above.

---

## 6. What kinds of questions can I ask?

Here's the range of questions the data supports, grouped by the kind of data they
draw on. You can ask any of these of Claude in plain English — **you don't need to
know SQL.** The SQL snippets shown throughout this section are just there to
illustrate what Claude writes and runs for you under the hood; you can read them to
see what's happening, or copy them into `cc-data query --dataset <ref> "..."` if
you'd rather run them yourself, but you never have to write them.

### About what students answered (answers / report data)

- How many students answered each question? How many left it blank?
- What did students write for a given open-response prompt?
- For a numeric or multiple-choice question, what's the distribution of answers?
- Which students got a particular question right?

```sql
-- Response counts per question
SELECT question_id, count(*) AS answered
FROM answers
GROUP BY question_id
ORDER BY answered DESC;
```

### About how answers evolved (interactive state history)

- How did a student's CODAP document or model change over the session?
- What did the saved state look like at each snapshot, and what changed between
  them?
- Which students revised their answer versus answering once?

The `history` store and the `attachment_content` view make **every saved snapshot**
queryable, so you can diff a student's work across the whole session, not just
see the final state.

> **History is only captured where it was enabled in authoring.** Interactive state
> history is recorded only for activities and sequences that have it turned on in
> the authoring system; for anything without it enabled, there's simply no
> snapshot series to download, and `get history` will come back empty. If you're
> counting on history for an analysis, confirm it was enabled for those activities
> before (or when) they ran.

### About how students worked (action logs / clickstream)

These come from the `student-actions` log report and are some of the richest
questions `cc-data` can answer:

- **Time on task**: how long was each student actively engaged? (Sum the spans
  *within* work sessions, not first-to-last wall-clock, which can cross days.)
- **Engagement / disengagement**: who hit idle warnings or let the session time
  out, and who worked straight through?
- **Navigation**: how did students move through the activity's pages, and in what
  order?
- **Tool interaction**: how many times did students adjust a bar chart, drag a
  slider, or run a model, and what values did they set?
- **Attention**: which interactives got the most focus and scroll time?
- **Revision behavior**: did students re-record audio responses or redo their
  work?

```sql
-- Idle warnings and timeouts per student (a disengagement signal)
SELECT user_id,
  count(*) FILTER (WHERE event = 'show_idle_warning') AS idle_warnings,
  count(*) FILTER (WHERE event = 'session_timeout')   AS timeouts
FROM reports
WHERE run_id = 187
GROUP BY user_id;
```

> Log timestamps come in two columns with different units: `time` is epoch
> **seconds** (`to_timestamp(time)`) and `timestamp` is epoch **milliseconds**
> (`to_timestamp(timestamp/1000)`). Order an event trace by one of these, not by
> row order.

### Broken down by class, school, or teacher (action logs with metadata)

With a `student-actions-with-metadata` run, any of the engagement questions above
can be grouped by `student_name`, `class`, `school`, or teacher.

### Across time (longitudinal)

Because each point-in-time pull is its own dataset, you can compare them
explicitly:

```sql
-- Answers as they stood in two pulls
SELECT * FROM fall_2026.answers
UNION ALL BY NAME
SELECT * FROM spring_2027.answers;
```

To run that yourself, name both datasets on the command line and give each an
alias to query it by (`--dataset` is repeatable, `alias=ref` sets the schema):

```
cc-data query \
  --dataset fall_2026=learn.concord.org/fall_2026 \
  --dataset spring_2027=learn.concord.org/spring_2027 \
  "SELECT * FROM fall_2026.answers UNION ALL BY NAME SELECT * FROM spring_2027.answers"
```

Ask Claude to "compare the `fall_2026` and `spring_2027` datasets" and it will
write the cross-dataset query for you.

---

## 7. A few things to keep in mind

- **One dataset belongs to one portal.** Run IDs are specific to a portal (and to
  the report server behind it); the same run number on a different portal is a
  different run. Keep production and staging data in separate datasets.
- **Combining report types is fine: expect some blank columns.** You can keep
  different report types in one dataset (say, an answers report alongside an action
  log). Rows are combined by column name, so a column that only one report type
  has, for example the roster columns (`student_name`, `class`, `school`, ...) that
  a *student-actions-with-metadata* log adds over a plain *student-actions* log,
  shows up blank for rows from reports that don't carry it. Nothing is lost or
  misaligned; the blanks are expected. If you're asking about a column only some
  runs have, scope the question to a specific run.
- **Comparing over time means separate datasets.** Because a dataset never keeps
  duplicates (re-pulling a run overwrites its records), to compare data across time (fall vs. spring, before vs. after), pull each
  point in time into its own dataset and query across them.
- **Counting students.** A distinct-student count is based on the Portal user, not
  on session or data-source identifiers, so a raw count of sessions or endpoints
  will overcount people. If a student count looks off, that's worth checking, or
  just ask Claude to "count distinct students."

---

## 8. Troubleshooting

- **`NOT_AUTHENTICATED`, or a prompt to "run `cc-data login`".** Your token is
  missing or expired. Run `cc-data login` (add `--portal` if you're not on
  production). Claude will stop and ask you to do this rather than logging in
  itself.
- **"Not found," or a run you can see in the report server comes back empty.**
  You're almost certainly pointed at a different portal or report server than the
  one that has the run. Run IDs are specific to a portal *and* to the report server
  behind it. Check what your login targets with `cc-data auth status`, and re-run
  `cc-data login` with the right `--portal` (and `--server`, if you use a
  non-default report server) if it's wrong.
- **"dataset is busy" / "download busy".** Another `get` or query is already
  running against that dataset. Wait for it to finish and retry; `cc-data`
  serializes access so a dataset is never left half-written.
- **Not sure what a command expects.** Every command has real help, e.g.
  `cc-data get answers --help`, the authoritative reference for its flags.

---

## 9. Sensitive data and cleanup

Downloading turns a transient export into a durable local corpus that can contain
student names and other PII. Keep only what you need, and purge datasets when you
no longer need them:

```
cc-data dataset list          # see what you have, and how old each dataset is
cc-data dataset purge <ref>   # remove a dataset's data from disk
```

Presigned attachment links are short-lived, credential-free capabilities to a
student's file; don't paste them into shared or persistent channels.

To remove `cc-data`'s Claude Code integration (the skill, the `CLAUDE.md`
pointer, and the MCP registration), run `cc-data uninstall`. It leaves your
datasets untouched and prints
where they live, so you can decide what to keep and remove the rest with
`cc-data dataset purge`/`delete`.

---

For command-level detail, every command has real help: `cc-data get answers
--help`, `cc-data query --help`, and so on are the authoritative reference. For
the full design and security model, see the main [README](../README.md).
