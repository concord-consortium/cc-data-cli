# CLUE data inventory

What CLUE data a researcher wants, where it actually lives, and how to get it.

This file is written from real research sessions, not from reading schemas. Each
source gets a row in the table and a recipe below it once a fetch has actually
worked. Rows exist before their recipes; a row with no recipe is a known want,
not a solved problem.

See the [design doc](superpowers/specs/2026-08-07-clue-data-inventory-design.md)
for why this exists and how it is maintained.

This file has two jobs. One is to record what it takes to pull each dataset and
get it ready for research. The other is to show what a researcher *wants* to do
and currently cannot — the direct-Athena query below is a good example: it is
the obvious thing to want, it works, and it is out of reach for anyone without
AWS credentials.

The scripts behind every recipe are in [`recipes/`](recipes/), tracked, so they
outlive the laptop that ran them. Where a recipe runs into a defect in one of
the systems it touches, the write-up gets its own document rather than a
paragraph here:
[report-service: log reports fail on large, long-running assignments](report-service-log-query-issues.md).

> **This repo is public.** No credentials, tokens, or presigned URLs here, and no
> real student records — sample shapes carry redacted values. Fetched data lives
> in `local-data/`, which is gitignored, and is never committed. That folder
> holds real class, school, and teacher names; treat it as student-adjacent PII.

> **"History" means two different things.** In `cc-data`, `get history` fetches
> LARA *interactive-state* history from the report service: the snapshot series
> attached to a report-service answer record. CLUE *document* history is an
> unrelated corpus in CLUE's own storage. Same word, different data. Be explicit
> about which one a row refers to.

## Inventory

| Data | Where it lives | Access needed | Recipe | cc-data today |
|---|---|---|---|---|
| Which units/problems use a given tile type | `clue-curriculum` repo + CLUE's `curriculum-config.json` | public GitHub read | [Curriculum: tile usage](#recipe-curriculum-tile-usage) | **absent** — out of scope; `cc-data` knows nothing about CLUE curriculum |
| Portal classes that ran a given CLUE assignment | Portal MySQL (`portal` db) via the Rails app on the production ECS cluster | AWS IAM + SSH to an ECS instance + `sudo docker` | [Portal: classes that ran an assignment](#recipe-portal-classes-that-ran-an-assignment) | **absent** — no portal DB access; `reports list` only shows report runs you authored |
| CLUE log events (clickstream) | Athena log database, directly (`logs_by_app_and_secure_key`), or via a `student-actions-with-metadata` run on the report server | direct: AWS IAM + portal DB for secure keys. Via report server: login to create the run; API token to download | [CLUE log events](#recipe-clue-log-events) | **partial**, now confirmed — CLUE events do appear (verified: every row of a real run had `application: CLUE`). `cc-data` can download such a run but cannot create one; creation is a LiveView form, not an API. Runs over a few hundred learners spanning several school years cannot be completed at all — see [report-service issues](report-service-log-query-issues.md). |
| Student document metadata (find documents, incl. by tile type) | Firestore `authed/learn_concord_org/documents` in `collaborative-learning-ec215` | Firebase service account for that project | [CLUE documents: finding them](#recipe-clue-documents-finding-them) | **absent** — no Firestore or RTDB client exists anywhere in the CLI; every fetch path goes through the report server's HTTP API. |
| Student document *content* | Firebase RTDB, `/authed/portals/learn_concord_org/classes/{classHash}/users/{uid}/documents/{docKey}` | same service account | [CLUE document content](#recipe-clue-document-content) | **absent** — same reason |
| Document history entries | Firestore `authed/learn_concord_org/documents/{docId}/history` | same Firebase service account | [CLUE document history](#recipe-clue-document-history) | **absent** — see the terminology note above; `cc-data get history` is a different corpus entirely. |
| History of the code, deployments, and databases that produced the data | nowhere yet — see [below](#a-missing-data-source-the-history-of-the-system-itself) | institutional memory | — | **absent**, and not obviously `cc-data`'s job |
| Derived behaviour datasets (edits, presence, cycles, candidates) | `local-data/derived/`, built from the three Parquet files | none beyond the local data | [Behaviour detection](recipes/behavior/README.md) | **absent** — a research pipeline, not a CLI concern |

Rows are added as research demands them. The list above is not a claim of
completeness.

## Using the local datasets

The recipes below describe how each dataset was *fetched*. This section is about
querying what has already been fetched, which is what a new analysis session
actually needs.

**These files are on one laptop.** They live in `local-data/`, which is
gitignored and holds real student-adjacent data. Nothing here is reproducible
from the repository alone — if the files are missing, re-run the recipes.

### The research question they were built for

Identify four student behaviours in Dataflow tile use: **trial and error**,
**systematicity**, **decomposition**, and **reusing**. Mostly by replaying
individual documents, but systematicity in particular may span several documents
by the same student, since a student can create multiple documents to try
different approaches.

### The three files

| File | Grain | Size |
|---|---|---|
| `local-data/clue-documents/content.parquet` | one row per document | 4,674 docs, 6.8 MB |
| `local-data/clue-documents/history.parquet` | one row per history entry | 6,381,134 entries / 2,854 docs, 881 MB |
| `local-data/log-events/logs.parquet` | one row per log event | 258,916 rows, 22 MB |

Query them directly with DuckDB; no import step, no database:

```
duckdb -c "SELECT count(*) FROM 'local-data/clue-documents/content.parquet';"
```

**`content.parquet`** — `doc_id`, `doc_key` (identical), `uid`, `context_id`,
`portal_class_id`, `type`, `unit`, `problem`, `meta_tools[]`, `discovery`,
`dataflow_tile_deleted`, `found`, `change_count`, `version`, `self_uid`,
`self_doc_key`, `self_class_hash`, `parse_ok`, `n_tiles`, `tile_types[]`,
`tile_type_counts` (JSON), `n_dataflow_tiles`, `n_rows`, `n_shared_models`,
`n_annotations`, `content_json`.

**`history.parquet`** — `doc_id`, `doc_uid`, `portal_class_id`, `unit`,
`investigation`, `problem`, `entry_id`, `idx`, `prev_entry_id`, `created`,
`server_created`, `model`, `action_raw`, `action`, `tile_id`, `shared_model_id`,
`n_records`, `undoable`, `is_revert`, `entry_uid`, `state`, `parse_ok`,
`entry_json`.

**`logs.parquet`** — `id`, `session`, `application`, `activity`, `event`,
`event_value`, `time`, `parameters`, `extras`, `run_remote_endpoint`,
`timestamp`, `activity_id`, `learner_id`, `student_id`, `class_id`,
`class_name`, `school_name`, `user_id`, `primary_user_id`, `offering_id`,
`runnable_url`, `unit`, `problem`, `doc_key`, `doc_type`, `doc_uid`, `tile_id`,
`event_time`.

### How they join

| | content | history | logs |
|---|---|---|---|
| document | `doc_key` / `doc_id` | `doc_id` | `doc_key` |
| student | `uid` | `doc_uid` | `user_id` |
| class | `portal_class_id` | `portal_class_id` | `class_id` |
| tile | — (inside `content_json`) | `tile_id` | `tile_id` |

All are VARCHAR, deliberately — the numeric-looking ids must not be typed as
integers or the joins break. A worked three-way join:

```sql
WITH d AS (
  SELECT doc_id, doc_key, unit, type, discovery
  FROM 'local-data/clue-documents/content.parquet'
  WHERE discovery = 'log-events' LIMIT 1
)
SELECT d.doc_id, d.type, d.unit,
  (SELECT count(*) FROM 'local-data/clue-documents/history.parquet' h
     WHERE h.doc_id = d.doc_id) AS history_entries,
  (SELECT count(*) FROM 'local-data/log-events/logs.parquet' l
     WHERE l.doc_key = d.doc_key AND l.event = 'DATAFLOW_TOOL_CHANGE') AS df_events
FROM d;
-- -NqhUrR6RhCacbK-Up4y | problem | brain | 2197 | 7
```

### Things that will mislead you if you do not know them

- **Coverage is uneven, and not randomly.** History exists for 2,854 of 4,674
  documents; logs name 2,610 of them. History was skipped for ten classes whose
  only assignment was the standalone Dataflow app, which predates history
  support. "No history" therefore does not mean "no work".
- **`discovery` is not decoration.** 72 documents have `discovery = 'log-events'`
  and `dataflow_tile_deleted = true`: their Dataflow tile was created and later
  deleted, so they are invisible to any query over current tile state. For trial
  and error they may be the *most* relevant documents. Decide explicitly whether
  a given analysis includes them.
- **Ticks dominate history.** `content/step` (41%) and `program/tickAndProcess`
  (13%) are over half of all entries and record the Dataflow program running,
  not the student doing anything. Filter them out for authoring behaviour; keep
  them to study program execution.
- **Index anomalies are real.** 73 documents have duplicate or gapped `idx`
  values, from concurrent writes. Order by `idx` then `entry_id`, and do not
  de-duplicate on `idx` — the duplicates are distinct entries.
- **`Placeholder` tiles inflate `n_tiles`,** appearing in 3,788 documents.
  Subtract them when counting authored tiles.
- **`change_count` is null on `publication` and `personalPublication`.** They are
  snapshots with no edit counter; use it only on live documents.
- **Use `event_time`, not `time`.** The raw `time` column is UNIX *seconds*.
  Dividing by 1000 out of habit yields January 1970 rather than an error.
- **Publications duplicate work.** `publication` and `personalPublication` rows
  are copies of another document's content at a moment in time. Counting all
  document types together double-counts student work.
- **Unit codes are aliased.** `brain` is Neural Engineering — the bulk of the
  corpus, 3,329 of 4,674 documents (2,474 of those with history) — and `dfe` is
  the Dataflow example unit. The raw values `neural-engineering` and
  `dataflow-example` also appear in URLs.
- **`unit` and `problem` are null on 890 documents.** Personal documents are not
  tied to a curriculum problem, so any per-unit grouping silently drops them —
  and personal documents are exactly where a student's own multi-document
  experiments would live. Group by `type` first, or join through
  `portal_class_id` instead.
- **Raw payloads are kept.** `entry_json` and `content_json` hold the original
  JSON. Every derived column is a convenience; when one looks wrong, the source
  is right there.
- **`action` is not the unit of student activity.** Only 279 documents record
  granular program actions; 2,604 record edits as `setProgram`, which is a
  catch-all — one sampled `setProgram` entry was five node renames. The uniform
  primitive is `records[].patches[]` inside `entry_json`.
- **Some patches are the program writing, not the student.**
  `program/values/*` is computed output, `sharedModel/variables/*/setValue`
  (263,536 entries) is the Simulation updating itself, and
  `sharedModel/dataSet/addCanonicalCasesWithIDs` (82,316) is Dataflow recording
  sensor readings into a table. All three read as student data entry. The
  discriminator is the action's root: `tileMap/{tile}/content/…` is
  student-initiated, `sharedModelMap/…` is not.
- **`created` is a client clock, `server_created` is the server's.** The
  1st/99th percentiles of their difference are −55s/+53s — the negative tail
  proves skew, since an entry cannot be created after it is stored. Use
  `created` only for durations within one document. 52 documents have backsteps
  over 60s and are unusable for timing.
- **`entry_uid` is null on all 6,381,134 entries,** and correctly so: only the
  concurrent history manager stamps it, real students only used the
  non-concurrent one, and those documents have exactly one author anyway.
- **Log `extras` carries UI state on 100% of rows** — `navTabsOpen`,
  `selectedNavTab`, `workspaceMode`, `problemPath`, `group`, `tzOffset`. But
  `navTabsOpen` conflates two of three divider states (split view and
  curriculum-fullscreen both log `true`), so only `false` is unambiguous.

## A missing data source: the history of the system itself

Research data spans years. The system that produced it is not the system whose
code you are reading today. Applications get renamed, split, and merged;
curriculum moves between repositories; a feature that is off by default now was
once a separate build with it always on; a field that is populated now was added
after half the corpus was written. None of this is visible in the current code,
the current schema, or the database — and an agent reasoning confidently from any
of those will produce answers that are wrong in ways nobody catches, because
every individual step looks correct.

This is a **data source in its own right**, and today it exists only as
institutional memory.

Everything in this file that took a second pass to get right came from this gap:

- The Dataflow tile appeared in documents whose curriculum could not have
  allowed it, because a standalone Dataflow application wrote into the same
  database. Nothing in CLUE's code or Firestore says that application ever
  existed.
- CLUE curriculum used to live inside `collaborative-learning` before moving to
  `clue-curriculum`, so old units may not be in the repo that now holds units.
- The `unit` URL param is usually a code but is sometimes a full URL into a
  `clue-curriculum` branch, a convention with no marker in the data.
- `tools` metadata is populated by a sync hook added at some point, so documents
  older than it are invisible to tile-type queries and nothing distinguishes
  "no Dataflow tile" from "written before we recorded tiles".

The same class of knowledge exists for the other systems a researcher is likely
to look at — **Activity Player** and **CODAP** — and for the standalone
interactives, which are the most likely to have been renamed, retired, or
quietly re-pointed at a different backend.

**Open question: where should this live?** Two defensible homes, not resolved:

- *In each application's repository.* Closest to the code it describes,
  maintained by the team that made the changes, and versioned alongside them. The
  people who know are the people already committing there. But a researcher then
  has to know which repositories to consult, and each team has to keep writing it
  down for an audience it never sees.
- *In a central research-facing inventory* like this file. One place for a
  researcher or their agent to look, written in terms of research questions
  rather than commits. But it duplicates knowledge that belongs to another team,
  and drifts as soon as they change something without knowing this file exists.

A plausible resolution is per-application history in each repo with this file
pointing at it, but that is a decision to make deliberately, not a default to
fall into. Recording it here so the choice is not lost.

## Recipes

### Recipe: curriculum tile usage

**Question it answers:** which units and problems involve a given CLUE tile type,
separating "the tile is available" from "students are told to make one".

Analyze the default branch without disturbing whatever branch is checked out:

```
cd clue-curriculum
git fetch origin
git archive origin/main curriculum | tar -x -C <scratch>/curriculum-main
```

Three signals, which must be kept separate because they mean different things:

1. **Toolbar** — unit `content.json` → `config.toolbar[]` contains
   `{"id": "<TileType>"}`. Students *can* add one.
2. **Embedded** — a tile object with `"type": "<TileType>"` appears in a section
   `content.json`. Students can copy it out of the curriculum.
3. **Instruction** — curriculum text naming the tile alongside an action verb
   (add / drag / create / place). Students are *told* to make one.

A unit's problems are listed in `content.json` → `investigations[].problems[]`,
and each problem's `sections[]` holds paths relative to the unit directory, so
section files resolve exactly rather than by globbing. Walking the section JSON
recursively is more robust than assuming a tile container shape — the format
varies across units.

The tile type string comes from the CLUE source, not from guessing: e.g.
`kDataflowTileType = "Dataflow"` in
`src/plugins/dataflow/model/dataflow-content.ts`.

**Identity:** a problem is `(unit code, investigation ordinal, problem ordinal)`.
The portal spells this `unit=<code>&problem=<inv>.<prob>`.

**Notes**

- **Unit codes in URLs are aliased.** `src/clue/curriculum-config.json` in the
  CLUE repo has a `unitCodeMap`; `unit=neural-engineering` resolves to the
  `brain` directory, `unit=dataflow-example` to `dfe`. Matching a URL on the
  directory name alone silently misses assignments.
- `defaultUnit` is `sas`, so a CLUE URL with no `unit` param means Stretching and
  Shrinking, not "any unit".
- Some units are near-duplicates of others. `vibe` repeats much of `brain`, with
  problems titled `PRIOR Lesson 1.2` and identical body text. Counting across
  both double-counts the same curriculum.
- A unit can embed a tile it does not put in the toolbar (`clueful` embeds
  Dataflow tiles but omits Dataflow from its toolbar), so signals 1 and 2 really
  are independent.
- Many units enabling a tile are dev/demo units (`example*`, `qa*`, `help`,
  `dfe`). Do not filter them out up front — check whether a real class ever ran
  them, which is a portal question, not a curriculum one.

### Recipe: portal classes that ran an assignment

**Question it answers:** which portal classes actually ran a CLUE assignment
(as opposed to merely having it assigned).

**Finding the machine.** The Rails app runs on the `production` ECS cluster:

```
aws ecs list-clusters
aws ecs list-services --cluster production          # AppService / WorkerService / SolrService
aws ecs list-tasks --cluster production --service-name <AppService>
aws ecs describe-tasks --cluster production --tasks <task-ids>          # -> containerInstanceArn
aws ecs describe-container-instances --cluster production \
    --container-instances <ci-ids>                                     # -> ec2InstanceId
aws ec2 describe-instances --instance-ids <i-...>                      # -> public IP
```

**Connecting.** IAM users are provisioned as Linux users on the cluster
machines, so the SSH user is your own IAM username and the key is the one
registered in IAM (`aws iam list-ssh-public-keys --user-name <you>`; compare
fingerprints with `ssh-keygen -lf`). Open one multiplexed master and reuse it:

```
ssh -M -S ~/.ssh/cm-learn-prod.sock -o ControlPersist=4h -N <you>@<ip>
ssh -S ~/.ssh/cm-learn-prod.sock <you>@<ip> "<command>"      # no re-auth
```

**Running Rails.** Pipe a script over stdin so nothing has to be copied into a
production container, and so results come back to your machine rather than being
written inside it:

```
ssh -S <sock> <you>@<ip> "sudo -n docker exec -i <app-container> bundle exec rails runner -" < script.rb
```

**The query.** `ExternalActivity.url` holds the CLUE URL. Parse `unit` and
`problem` out of it in Ruby rather than matching with `LIKE` per unit — one
broad `LIKE '%collaborative-learning%'` to narrow, then real URL parsing.
For "ran" rather than "assigned", use `Report::Learner` (table
`report_learners`), which carries `last_run` plus denormalized `class_id`,
`class_name`, `school_name`, `teachers_name` — so one table answers the whole
question without joining through `Portal::Offering`.

**Identity:** a result row is `(class_id, runnable_id)`. A class appears once per
activity it ran, so a distinct-class count must dedupe on `class_id`.

**Not yet captured: the offering id.** "Offering" is the portal's name for an
assignment — a specific runnable assigned to a specific class — and its id is the
key several other systems use to locate a class's data. `report_learners` already
carries `offering_id`, so adding it is a one-line change to the row hash in
`find_dataflow_classes.rb`; it was left out only because nothing needed it yet.
Add it the moment a downstream lookup asks for an assignment rather than a class.

**Sample row** (values redacted):

```
unit,problem,has_dataflow,class_id,class_name,school,teachers,activity_id,activity_name,students_run,first_run,last_run
brain,1.4,true,<int>,<class name>,<school name>,<teacher name>,<int>,<activity name>,<int>,<timestamp>,<timestamp>
```

**Notes**

- **The portal assigns CLUE problem-by-problem, not unit-by-unit.** `brain` is 14
  separate `ExternalActivity` records, one per problem. Filtering on unit alone
  counts classes that only ran problems with no tile of interest — for Dataflow
  that was 324 class-activity pairs and 60 classes, versus 220 and 58 once
  restricted to the problems that actually involve Dataflow. Always intersect
  with the per-problem curriculum result.
- **CLUE is not one host, and filtering on `collaborative-learning` loses
  classes.** Narrowing with `url LIKE '%collaborative-learning%'` looks safe and
  silently excluded an entire application: `dataflow-app.concord.org`
  ("Dataflow 3.0"), plus a dedicated Dataflow build at
  `collaborative-learning.concord.org/branch/dataflow/` that carries no `unit`
  param at all. Together those are 4 activities run by 10 classes and 71
  students between 2019-12 and 2025-03 — none of which appear in a
  curriculum-unit search, because they have no curriculum unit. Search on the
  feature (`url LIKE '%dataflow%'`) as well as the host, and check the host
  distribution before trusting a count.
- **CLUE runs from branch builds.** Beyond the bare host, assignments use
  `/branch/master/` (32), `/branch/dataflow/` (3), and one-off authoring
  branches. The `unit`/`problem` query params parse the same way, so path
  variation is harmless as long as the host filter does not exclude them.
- **The `unit` param is sometimes a full URL, not a code.** 10 activities pass
  `unit=https://models-resources.concord.org/clue-curriculum/branch/<branch>/<unit>/content.json`
  to run a unit from a `clue-curriculum` branch. Matching unit codes against the
  repo's directory names misses these; parse the URL and take the second-to-last
  path segment as the unit. None of the current examples are Dataflow units
  (`mods`, `sas`, `qa`), but the pattern will bite any tile-type search.
- The model is `Report::Learner`, not `ReportLearner`. `ReportLearner.count`
  raises.
- `last_run` distinguishes ran from assigned. A `Portal::Offering` existing only
  means the teacher assigned it.
- **App and worker tasks share EC2 instances.** Do not assume one instance is
  "the web box" — check `describe-tasks` per service. Any instance running an
  App task can run `rails runner`; picking one that runs *only* app tasks just
  keeps you off a busy machine.
- `ControlPersist` makes the SSH master background itself once established, so
  the `ssh -M -N` command appearing to "exit" is success, not failure. Check with
  `ssh -S <sock> -O check <host>`.
- The container has no `bash` — use `sh`. The Docker socket needs `sudo`. App
  root is `/rigse`, `RAILS_ENV=production`.
- `rails runner` output is preceded by initializer warnings on stderr; parse
  results out with an explicit marker rather than assuming the first line.
- The ECS security group allows port 22 from `0.0.0.0/0`. Noted, not acted on.

**The 15 Dataflow activities**, since every later recipe takes these as input.
Learner counts are from the census; they are what determines whether a report
run will complete.

| id | Activity | Learners |
|---|---|---|
| 2460 | Lesson 0 — Using CLUE and Dataflow Programming | 670 |
| 2462 | Lesson 1.2 — How does electricity help us move? | 561 |
| 2464 | Lesson 1.4 — How do muscles work? | 528 |
| 2465 | Lesson 1.5 — Can you control a robot with your muscles? | 522 |
| 2463 | Lesson 1.3 — How does the brain control movement? | 519 |
| 2467 | Lesson 2.2 — How do we perceive touch? | 401 |
| 2468 | Lesson 2.3 — Can robots sense objects? | 277 |
| 2735 | See-It Sensors | 39 |
| 2739 | See-It Activity 2 | 39 |
| 3530 | Intro to CLUE and Dataflow: First Look | 34 |
| 3255 | CLUEs to Collaboration: Computational Thinking | 31 |
| 3529 | Intro to CLUE and Dataflow: MiniClass Activity | 31 |
| 3052 | Lesson 3.3 — Turn Analog Changes into Program State | 15 |
| 2841 | Test dfe CLUE Activity | 1 |
| 3053 | Lesson 3.4 — Create Mechanical Output | 1 |

3,669 learners in total across 57 classes. The per-activity counts sum to
exactly that, because `report_learners` rows are per offering.

### Recipe: CLUE documents, finding them

**Question it answers:** which student documents exist for a given unit/problem,
which contain a given tile type, and which class each belongs to. This covers
*finding* documents — their metadata. Document **content** lives in the realtime
database and has not been fetched yet.

**Access.** Firestore metadata lives in the `collaborative-learning-ec215`
project. The established path is a service account key in the CLUE repo's
`scripts/` folder, per `scripts/README.md`: generate a private key from the
Firebase console's service accounts page, save it as
`scripts/serviceAccountKey.json`, and run scripts with `npx tsx <script>.ts`.
`scripts/lib/script-utils.ts` has the path helpers — `getFirestoreBasePath`,
`getFirestoreClassesPath`, `getFirebaseBasePath` — so paths never need to be
hand-built.

**Where things are** (production, portal-authenticated):

| What | Path |
|---|---|
| Document metadata | Firestore `authed/learn_concord_org/documents/{docId}` |
| Class records | Firestore `authed/learn_concord_org/classes/{...}` |
| Document content | RTDB `/authed/portals/learn_concord_org/classes/{classHash}/users/{uid}/documents/{docKey}` |

**The shortcut that matters.** Document metadata carries a `tools` array listing
the tile types present in the content, maintained by the client's content-sync
hook. So "every document containing a Dataflow tile" is one query:

```ts
docs.where("tools", "array-contains", "Dataflow")
```

This beats going unit → problem → document, because it also catches *personal*
and *learning log* documents, which carry no unit at all. Read the tile-type
string from the CLUE source rather than guessing it.

**Identity:** a document is `documents/{docId}`. `key` is its id in the realtime
database, `uid` the owner, `context_id` the class. Problem-family documents carry
`unit` / `investigation` / `problem`; personal and learning-log documents carry
`unit: null` and are located by class instead.

**Mapping a CLUE class to a portal class:** a document's `context_id` *is* the
portal's `portal_clazzes.class_hash`, so the portal maps them directly and
completely:

```ruby
Portal::Clazz.where(class_hash: context_ids)
```

Do **not** map via Firestore's `classes` collection, whose `uri` field holds the
portal class URL. That route looks reasonable and quietly loses classes — see the
notes.

**Notes**

- **`tools` is not universally populated, and the gap is invisible to the
  query.** Documents whose metadata was written incompletely have no `tools`
  field, and an `array-contains` query silently skips them: 311 of 7289 `brain`
  documents, 23 of 85 `clueful`, 14 of 404 `seeit`, 11 of 71 `vibe`. Those same
  documents also have no `createdAt`, which is the tell — they are the population
  the repo's own `scripts/find-documents-missing-metadata.ts` exists to repair.
  Any count from a `tools` query is a floor, not a total.
- **Class records are stored under two ids** — `classes/{contextid}` and
  `classes/{network}_{contextid}` — so the collection has more records than
  classes. Deduplicate on the `context_id` field rather than trusting the
  document id.
- **Firestore's `classes` collection is incomplete; do not map through it.**
  7 of the 70 classes holding Dataflow documents have no record there at all,
  and those 7 held 828 of the 889 personal-family documents — so mapping via
  `classes/{ctx}.uri` dropped 18% of the corpus while looking like it had
  succeeded. Every one of those 70 `context_id`s resolves through
  `Portal::Clazz.class_hash`. The failure mode is the dangerous kind: a partial
  answer with no error.
- **Personal Dataflow work came from a different application.** Of 889
  personal-family Dataflow documents, only 23 are in classes that ran a CLUE
  curriculum problem; 825 are in classes whose only assignment was the
  standalone Dataflow app (see the portal recipe's note on hosts). That app
  produces personal documents and no problem documents, and enables the Dataflow
  tile regardless of curriculum. So "personal documents containing Dataflow" and
  "students assigned Dataflow curriculum" are largely disjoint populations, and
  a cohort built from CLUE curriculum assignments misses nearly all of the
  former.
- `firebase-admin` in the CLUE repo is 11.0.1, which predates `count()`
  aggregations. Count with projected fetches (`.select(...)` plus paging on
  `startAfter`) instead; `q.count is not a function` is what the old version
  looks like.
- Personal documents are the bulk of the non-problem work and carry `unit: null`.
  Do not filter them out by requiring a unit.
- A unit with curriculum but no portal assignments has no documents either —
  `tinker` returned zero.
- **A tile can appear in documents belonging to no unit at all.** The toolbar
  config is what lets students add a given tile, so a document's tile set is
  normally bounded by its unit's curriculum. Standalone applications that write
  into the same Firestore ignore that: the Dataflow app enables its tile
  unconditionally and produces unit-less personal documents. When a tile turns
  up where the curriculum says it cannot, look for a different application
  writing to the same database before doubting the curriculum analysis.

### Recipe: CLUE document history

**Question it answers:** which documents have a replayable edit history, and how
much of it. This is CLUE *document* history — not `cc-data`'s `get history`.

History entries are one Firestore document each, in a `history` subcollection
under the document they belong to, with a monotonic `index` field. So existence
and size come from a single read per document:

```ts
firestore.collection(`${docsPath}/${docId}/history`)
  .orderBy("index", "desc").limit(1).get()
```

An empty result means no history; otherwise the top entry's `index` is the entry
count. Run it concurrently — 40 at a time checked 4,602 documents in a few
minutes. The metadata field `lastHistoryEntry` looks like a cheaper proxy but
applies only to concurrent-history documents, so it was not relied on.

**What the Dataflow corpus looks like** (of 4,602 documents containing a
Dataflow tile):

| Population | Docs | With history |
|---|---|---|
| CLUE documents | 3,741 | 2,782 (74.4%) |
| Dataflow-app / `/branch/dataflow/` classes | 861 | 0 (0.0%) |

Among CLUE documents, by type: `problem` 2,761/2,819, `personal` 16/21,
`learningLog` 4/6, `planning` 1/1, **`publication` 0/893**,
`personalPublication` 0/1.

**Notes**

- **Publications never have history.** All 893 are copies made at publish time,
  so they carry none. Any "documents containing tile X" count includes them and
  overstates the population usable for history research by that much.
- **History postdates the standalone Dataflow application.** All 861 documents
  from those classes have zero entries — not a sampling artifact, the whole
  population. Personal Dataflow work from the standalone app cannot be studied
  historically at all.
- **Histories are large.** Median 941 entries per document, max 93,399, 6.25
  million entries across the corpus. Any plan to fetch history *content* rather
  than counts should size itself against that before starting; the CLUE code has
  a known FIXME about loading histories without paging.
- Entry counts are long-tailed: 1,345 documents have 1,000+ entries while 37 have
  fewer than 10. A minimum-entries floor is worth setting explicitly rather than
  treating "has history" as a usable population.

#### Downloading them

Done for the Dataflow corpus; see the
[dataset design](superpowers/specs/2026-08-10-clue-history-dataset-design.md).
Result: **2,854 documents, 6,381,134 entries** — 2,782 documents found by the
`tools` query plus 72 found only through log events (below). 15 GB of JSONL
compresses to **881 MB of Parquet** (ZSTD), which is the query surface. Zero
parse failures.

**Delete the JSONL at your peril.** It is tempting to clear the 15 GB once the
Parquet exists, and we did. A later rebuild then read `history/*.jsonl`, matched
only the handful of documents downloaded since, and overwrote 6.27M entries with
110k — silently, because a glob that matches fewer files is not an error.
Recovery was a full re-download. `build-parquet.sh` now refuses to build unless
every document the source list says has history has its JSONL present; comparing
`.jsonl` against `.meta.json` counts is *not* sufficient, because during a
download those two counts pass through equality.

A re-download is a decent integrity check, incidentally: the second pull matched
the first except for 213 entries in one document, all created that day, because
the corpus is live.

- **Build the file incrementally.** Assembling a document's entries into one
  string and calling `writeFileSync` fails with `Invalid string length` on
  documents whose history exceeds V8's maximum string size. One real document
  (90,662 entries) hit this. Write in chunks through a file descriptor instead.
- **Declare column types; do not let DuckDB infer them.** `read_json_auto`
  samples rows, and `problem`/`investigation` are numeric-looking strings
  (`"0"`, `"1"`), while `model` is null on older entries — inference types those
  wrongly or drops them.
- **Index anomalies are real and worth keeping.** 73 of 2,854 documents disagree
  with a simple count: 69 have duplicate indices, 4 have gaps, none start at a
  non-zero index. Duplicates are genuinely distinct entries (distinct
  `entry_id`s) sharing an index, matching the concurrent-write flakiness CLUE's
  own `history-framework.md` describes. Storing them preserves that signal;
  de-duplicating on index would destroy it.
- **Ticks dominate, as expected.** `content/step` (41.2%) plus
  `program/tickAndProcess` (13.3%) are 54% of all entries. Real editing is
  `setSlate` (22.8%), `setProgramZoom` (10.4%), and `setProgram` (5.4%).
- The action path carries ids (`/content/tileMap/<tileId>/content/setSlate`), so
  they are split into a `tile_id` column and the action normalised to
  `{tile}`. Without that, actions cannot be grouped and per-tile work cannot be
  isolated.

### Recipe: CLUE document content

**Question it answers:** what a document actually contains — which tiles, how
many, arranged how — as the final saved state rather than as a stream of edits.

Content lives in the **RTDB**, not Firestore, at
`/authed/portals/learn_concord_org/classes/{classHash}/users/{uid}/documents/{key}`.

**One path covers every document type.** `publications` and
`personalPublications` under the class path hold only *metadata* pointing back
at the owning user's document, so the user-document path above is the whole
corpus (`src/lib/firebase.ts`, `getUserDocumentPath` / `getDocumentPath`).
Publications do not need a second fetch, and looking for their content under the
publication path finds nothing.

`context_id` from the Firestore document metadata is the `classHash` in this
path — the same equivalence that maps CLUE documents to portal classes.

[`docs/recipes/clue-documents/download-content.ts`](recipes/clue-documents/download-content.ts)
does the fetch and
[`build-content-parquet.sh`](recipes/clue-documents/build-content-parquet.sh)
converts it. Results for the Dataflow corpus:

- **4,600 of 4,602 documents fetched**, 0 parse failures, at ~378 docs/s with
  concurrency 20 — the whole corpus in under a minute, 6.7 MB as Parquet. This
  is by far the cheapest of the CLUE datasets; there is no reason to sample it.
- **The 2 missing are not student data** — both personal documents belonging to
  one Concord staff account in one class. Firestore metadata outlived the RTDB
  content.
- **`content` is a JSON *string*,** not a nested object. Parse it, then read
  `tileMap`, `rowMap`, `rowOrder`, `sharedModelMap`, `annotations`.
- **The raw string is kept alongside the derived columns.** Deriving tile counts
  is lossy and nothing can re-derive what was not stored.

**Metadata and content agree, which is worth knowing given they are written by
different code paths.** Firestore's `tools` array and the actual `tileMap`
disagreed on Dataflow presence for **0 of 4,600** documents. That does not
retire the caveat that `tools` is populated by a sync hook added later — a
document written before it would be absent from the query that built this list
in the first place, so this corpus cannot reveal that gap.

What the content shows about the corpus:

| type | docs | mean tiles | mean Dataflow tiles | mean changeCount |
|---|---|---|---|---|
| problem | 2,819 | 11.0 | 1.33 | 667 |
| publication | 893 | 10.6 | 1.24 | — |
| personal | 862 | 1.1 | 1.00 | 71 |
| personalPublication | 19 | 2.3 | 1.00 | — |
| learningLog | 6 | 4.3 | 1.33 | 72 |
| planning | 1 | 6.0 | 1.00 | 22 |

Personal documents average **1.1 tiles** — essentially a bare Dataflow tile —
against 11 for problem documents. Whatever multi-document strategies students
used, personal documents are not scaled-down copies of problem documents.

`Placeholder` appears in 3,717 documents, so tile counts include empty slots;
subtract it when counting authored tiles. After Dataflow, the tiles that co-occur
are `Text` (3,132), `Table` (1,208), `Image` (664) and `Simulator` (590).

`changeCount` is null on both publication types — they are snapshots, so there
is no edit counter. Use it as an activity measure only on live documents.

### Recipe: CLUE log events

**Question it answers:** what students clicked, typed, and ran, as a timestamped
event stream — including `DATAFLOW_TOOL_CHANGE`, which is the Dataflow tile's own
event.

**Confirmed:** every row of a real run came back with `application: CLUE`, so
CLUE events genuinely land in the report server's log database. The earlier
"expected but unverified" note is resolved.

**Creating a run needs a browser.** `/api/v1` is read-only for runs — index,
show, download, answers, history, jobs, attachment presign, and nothing else.
Creation is a Phoenix LiveView at `/reports/new/:slug`, so it is a websocket
form, not a REST call. Everything *after* creation is plain API.

**Token.** Generate one in the browser at `/reports/cli-token` (it is shown
once), then use `Authorization: Bearer <token>`. Check it with
`GET /api/v1/tokens/current`. No PKCE or loopback needed for read access.

**Downloading.** `GET /api/v1/reports/:id/download` returns
`{download_url, filename, expires_in_seconds}`. Fetch the `download_url`
**without** the bearer token — it is already a standalone presigned capability.

**Joining to the other datasets.** The output carries `class_id`, `user_id`,
`student_id`, `offering_id`, and `runnable_url` (which holds `?unit=…&problem=…`).
`class_id` matches the portal class ids; `user_id` matches CLUE's document
`uid`. So logs, documents, and history all join on ids without needing names.

**They also join at the document and tile level, which is the useful part.**
CLUE writes `documentKey`, `documentType`, `documentUid` and `tileId` into the
log row's `parameters` JSON. `documentKey` is the same identifier as
`content.parquet`'s `doc_key` and `history.parquet`'s `doc_id`, and `tileId` is
`history.parquet`'s `tile_id` — so a single tile's edit history and the events
logged against it line up directly. `build-parquet.sh` lifts all four out of the
JSON into columns, since otherwise every downstream query re-parses it.

How well they actually join, for this corpus:

| | |
|---|---|
| Log rows carrying a `documentKey` | 218,966 of 258,916 (84.6%) |
| Distinct documents named in logs | 3,899 |
| …also in `content.parquet` | 2,610 |
| …also in `history.parquet` | 2,490 |
| `content.parquet` documents having logs | 2,610 of 4,674 (56%) |
| `history.parquet` documents having logs | 2,490 of 2,854 (87%) |
| Distinct `tileId`s in logs | 22,168, of which 14,125 appear in history |

The 15% of rows with no `documentKey` are events with no document context —
logins, navigation, tab switches. That is expected, not loss.

**The logs reveal 72 documents where a Dataflow tile was created and then
deleted.** Of the 1,361 log documents missing from `content.parquet`, 1,289 have
no Dataflow events at all — ordinary documents in the same classes, correctly
out of scope. But 72 *do* have `DATAFLOW_TOOL_CHANGE` events.

Probing each one in both stores settled what they are, and it is not what it
first looked like:

- **All 72 exist**, in the RTDB *and* in Firestore. Nothing was deleted at the
  document level.
- **None of them contains a Dataflow tile now.** Their tiles are `Text`,
  `Placeholder`, `Image`, `Table`.
- **66 have an explicit `DELETE_TILE` event with `objectType: "Dataflow"`.**

So a student added a Dataflow tile, worked in it — a median of 7 Dataflow events,
up to 357 — and then deleted it. Firestore's `tools` array is correct: the
document does not currently contain a Dataflow tile. The corpus query was also
correct. The documents are missing because **`tools` describes the current state,
and the corpus was defined by a current-state property.**

This is not a bug to fix in the discovery step; it is a limit on what that step
can mean. A corpus built from "documents that contain a Dataflow tile" silently
excludes every document where a student tried Dataflow and abandoned it — which,
for research into **trial and error**, is close to excluding the phenomenon
under study.

**The abandoned work is fully recoverable.** All 72 retain their history:
109,740 entries, median 906 per document, up to 15,846. The deleted tile's
entire life is still in the history subcollection, because history records
mutations rather than state.

Two lessons worth generalising:

- **The three datasets are built by different code paths, so each bounds the
  others' completeness.** Only the logs could reveal this, because only the logs
  record what happened rather than what remains.
- **Prefer defining a corpus by events over current state** when the research
  question is about process. Current state answers "what did they end up with";
  it cannot answer "what did they try".

#### Why runs fail — partition projection, not query size

Runs fail with no explanation: the API reports only `athena_query_state:
"failed"`, the run page says only "Failed", and nothing appears in CloudWatch
beyond the per-assignment `Uploading learners to learners/<uuid>/<uuid>.json`
lines.

**The error is recoverable, but only from Athena.** `athena_query_state` is set
solely from Athena's own `get_query_info` (`athena_run_ops.ex:39`), so a
"failed" run *did* reach Athena and Athena has a `StateChangeReason` for it. The
run's `athena_query_id` is not exposed by the API, but the executions sit in the
user's own workgroup, named `<portal_server> <user_id> <email>` with every
non-`[a-z0-9]` character replaced by `-` (`athena_db.ex:103`) — for example
`learn-concord-org-28-scytacki-concord-org`:

```
aws athena list-query-executions --work-group <name> --max-results 15
aws athena batch-get-query-execution --query-execution-ids <ids...>
```

Match executions to runs by submission time. Twelve executions covering our runs
gave three distinct reasons:

| Secure keys in run | SQL size | Outcome |
|---|---|---|
| 78, 97 | 4–5 KB | **succeeded** (20 and 26 min) |
| 176, 670, 694 | 8–28 KB | `HIVE_EXCEEDED_PARTITION_LIMIT` after 21–28 min |
| 519–561 | 21–23 KB | `Query timeout` at 30 min |
| 2130, 3493, 3669 | 82–142 KB | `CONSTRAINT_VIOLATION`, instantly |

**The root cause is partition projection.** The Glue table
`log_ingester_production.logs_by_app_and_secure_key` is partitioned on
`app`/`year`/`month`/`secure_key` with projection enabled: `app` is an enum of
15 values, `year` ranges 2014–2050 (37), `month` 1–12, and `secure_key` is
`injected` — meaning its values come from the `IN (...)` list in the WHERE
clause. The generated SQL constrains **only** `secure_key`, so every key
multiplies out across all 15 × 37 × 12 = **6,660** app/year/month combinations.
Athena refuses a query that could read more than 1,000,000 partitions, which
puts the ceiling at roughly **150 learners per run**. That matches what we saw:
97 keys succeeded, 176 keys failed.

The other two reasons are the same problem at different scales — a few hundred
keys spends 30 minutes enumerating partitions and times out; a few thousand
overflows the injected-partition expansion outright and is rejected in a second.

**The 256 KB check never fired.** `AthenaDB.query` does call
`check_query_size(sql)` before contacting Athena (`athena_db.ex:172`), but the
largest SQL we generated was 141.5 KB. Query size is a red herring at this
scale.

**Date bounds help, but are not sufficient.** `apply_date_range`
(`report_query.ex:156`) emits `log.year`/`log.month` predicates alongside the
timestamp comparison, so setting a start and end date on the form prunes the
projection directly. Bounding to the actual span of Dataflow usage — 2022-08
through 2026-08, 49 months — cuts 6,660 combinations per key to 15 × 49 = 735,
lifting the partition ceiling from ~150 learners to roughly 1,360.

We tested this: six re-runs of the failed assignments, each with that date range,
one assignment per run (plus one run grouping the four small ones).
`HIVE_EXCEEDED_PARTITION_LIMIT` never appeared again — but **all six still
failed**, five with `Query timeout` at 30 minutes and one with
`HIVE_S3_THROTTLING` (an S3 503, "please reduce your request rate").

The revealing number is what they scanned: **32–72 MB each, in half an hour.**
These queries are not data-bound. Every `secure_key × app × year × month` is a
separate S3 prefix, so ~520 learners × 15 apps × 49 months is ~382,000 prefixes
listed to read 40 MB. The time is object-store round trips, not scanning.

Two corollaries that cost us a cycle each:

- **Narrower dates will not rescue the big lessons.** Their real spans, from the
  portal, are 2022-08 through 2025-12–2026-08 — they genuinely cover the whole
  range. The units that succeeded are the ones that are naturally narrow
  (2735/2739 ran 2023-05 to 2024-04).
- **Do not run several at once.** The two successful runs had two queries in
  flight; running six produced an outright S3 503. Concurrency is part of the
  budget.

**The untapped lever is `app`.** It is an enum of 15 values and the generated SQL
never constrains it, even though the report knows perfectly well it is querying
CLUE. Adding `app = 'CLUE'` would cut prefix probing 15×, from ~382,000 to
~25,000. The form cannot express it — see
[report-service-log-query-issues.md](report-service-log-query-issues.md).

Consequences worth knowing before debugging:

- **Always set a date range,** even though it is optional and looks like a
  convenience filter. It is necessary, just not sufficient.
- **The error message exists and is then discarded.** `report_runs` has no error
  column, so the server knows exactly why and tells nobody — while Athena's own
  reason is specific and actionable.
- **A single assignment can fail on its own.** One Neural Engineering lesson
  failed alone, so "one assignment per run" is not a safe rule.
- **A class filter does not help here** — see below.
- **Do not size batches from a Dataflow-only class list.** Counts drawn from
  classes that ran Dataflow can undercount, because the report spans every class
  that ever ran the assignment. Count the learner population directly instead.

**Where this leaves a researcher.** For assignments in the low hundreds of
learners and a narrow date span, the report system works. For a multi-year
assignment with several hundred learners — which is exactly what a longitudinal
research question looks like — it does not, and there is no combination of form
filters that makes it work. The remaining routes are to chunk into runs of ~150
learners (roughly 25 runs for this corpus), or to bypass the report server and
query Athena directly with `app` constrained. Only the second is available to
someone who does not have AWS credentials, which is to say: neither is available
to a researcher.

#### Counting the learner population before you submit a run

The portal query the report server runs is reproducible on its own
(`LearnerData.fetch/3` in `learner_data.ex`), which lets you see exactly how
many learners — and therefore how many injected partitions — a run will involve
*before* spending 30 minutes finding out. [`docs/recipes/log-events/learner_census.rb`](recipes/log-events/learner_census.rb)
is that query reduced to a per-assignment, per-class census.

Two things it settled for the Dataflow set:

- **3,669 learners across all 15 activities**, and the per-activity counts sum to
  exactly that — `report_learners` rows are per offering, so no learner is
  double-counted across assignments.
- **Every class that ever ran one of the 15 activities is already in our list.**
  57 distinct classes appear, all 57 in the 60 we identified from the curriculum
  and portal (the other 3 have offerings but no learner runs). There is no
  outside population. That is why a `class` filter cannot shrink these runs:
  there is nothing to exclude.

A super-admin gets `:all` from `get_allowed_project_ids`, which applies no
project scoping (`report_utils.ex:112`), so the reproduced query matches what
the report server would run. A researcher scoped to specific projects would see
fewer learners — which is itself worth surfacing to them.

#### The route that worked: query Athena directly

Once the learner census exists, the report server is not needed. Each learner's
`portal_learners.secure_key` is the only thing the Athena query requires, and
[`learner_keys.rb`](recipes/log-events/learner_keys.rb) exports it alongside the
learner metadata. [`athena_logs.py`](recipes/log-events/athena_logs.py) then runs
the query itself:

- **adds `app = 'CLUE'` and a year bound**, which the form cannot express and
  which is what makes the query cheap;
- **selects only the log columns** and skips the `"report-service"."learners"`
  join entirely, so it does not depend on the learner JSON the report server
  uploads to S3. The metadata is joined locally in DuckDB instead;
- **chunks the secure keys** (150 per query) and caches each chunk to its own
  file, so a re-run resumes rather than refetching.

The whole query is this — worth reading, because the difference between it and
the one that times out is a single line:

```sql
SELECT log.id, log.session, log.application, log.activity, log.event,
       log.event_value, log.time, log.parameters, log.extras,
       log.run_remote_endpoint, log.timestamp
FROM "log_ingester_production"."logs_by_app_and_secure_key" log
WHERE log.app = 'CLUE'                     -- the line the report server omits
  AND log.year BETWEEN 2022 AND 2026
  AND log.secure_key IN ('<key>', '<key>', ...)   -- 150 at a time
```

Run it in your own workgroup, which report-service names
`<portal_server> <user_id> <email>` with every non-`[a-z0-9]` character replaced
by `-` (`athena_db.ex:103`):

```
aws athena list-work-groups --query 'WorkGroups[].Name' --output text \
  | tr '\t' '\n' | grep <your-username>
export CC_ATHENA_WORKGROUP=learn-concord-org-<id>-<email-with-dashes>
```

The secure keys come from the portal — one row per learner, the same rows
report-service uploads to S3 before building its query:

```sql
SELECT DISTINCT po.runnable_id AS activity_id, rl.learner_id, rl.class_id,
       rl.user_id, ea.url AS runnable_url, pl.secure_key
FROM report_learners rl
JOIN portal_learners pl ON (rl.learner_id = pl.id)
JOIN users u ON (u.id = rl.user_id)
JOIN portal_offerings po ON (po.id = rl.offering_id)
JOIN external_activities ea ON (po.runnable_type = 'ExternalActivity'
                                AND po.runnable_id = ea.id)
JOIN portal_student_clazzes psc ON (psc.student_id = rl.student_id)
JOIN portal_teacher_clazzes ptc ON (ptc.clazz_id = psc.clazz_id
                                    AND rl.class_id = ptc.clazz_id)
WHERE po.runnable_id IN (<activity ids>)
```

**Validated before being trusted.** Re-running the one query that had already
succeeded through the report server (run 2285, 78 learners) returned **10,319
rows — the same count, the same 10,319 ids, and identical values across all
eleven log columns.** It took **7 seconds against the report server's 20
minutes**.

Then the whole corpus, all 15 activities, 3,669 learners:

| | Report server | Direct |
|---|---|---|
| Result | 6 of 8 runs failed; brain never completed | 258,916 rows |
| Time | 30-minute timeouts | **9m40s**, 25 chunks at ~13 s each |
| Scanned | 32–72 MB per failed run | 292 MB total |

Coverage against the history corpus went from **336 of 2,782 documents (12%) to
2,681 (96%)**; `brain` went from 8 log rows to 240,819 across 747 users. Every
one of the 258,916 rows matched a learner — no orphans.

Two things worth knowing if you repeat this:

- **Raise the CSV field limit.** Dataflow rows carry serialised program state in
  `parameters`/`extras` and exceed Python's default 128 KB field limit.
- **`app = 'CLUE'` was checked, not assumed.** A 20-key sample of the largest
  brain lesson, queried with no `app` predicate, returned rows under `CLUE` and
  nothing else — on both the partition and the `application` column. That is a
  sample, not a proof for the whole corpus, but it is the same assumption the
  proposed report-service fix would rest on.

This route needs AWS credentials and portal database access, so it closes the
gap for a maintainer and not for a researcher. That is the point of
[the report-service write-up](report-service-log-query-issues.md).

#### `hide_names` hides students, not everyone

With `hide_names: true`, `student_name` becomes a numeric id and `username`
becomes a hash. But `teachers` still carries full teacher names **and email
addresses**, and `school` and `class` remain plain names. The output is still
personal data and belongs in `local-data/`, not anywhere shareable.

#### Driving the LiveView form

Worth writing down because two things silently do nothing:

- The filter picker is a LiveSelect. Its options exist in the DOM only while the
  dropdown is open; the search box updates its own placeholder with the match
  count (`*term*: N options available`) even when no list is rendered.
- `element.click()` on an option does nothing. Dispatching
  `mouseover`/`mousedown`/`mouseup`/`click` works, and so does a real CDP click.
  Either way the selection lands **asynchronously** via a server round trip, so
  poll `input[name="filter_form[filter1][]"]` rather than checking immediately.

#### Making this researcher-accessible

Everything above needs AWS IAM, SSH to a production box, and `sudo docker`. A
researcher has none of that. Options for closing the gap, from a read of
`report-service/server` and `rigse`:

The gap is narrower than it looks, because **both halves nearly exist already**:

- The report server's *assignment* filter already does LIKE-based search over
  `external_activities`, already scoped to the researcher's projects via
  `allowed_project_ids` — `reports/report_filter_query.ex`, the
  `get_filter_query(:assignment, ...)` clause.
- The standard report column set already carries `runnable_url`, `class_id`,
  `class`, `school`, `offering_id`, and `last_run` (`reports/report_query.ex`),
  read from the same `report_learners` table this recipe queries directly
  (`reports/athena/learner_data.ex`).

**Discovery — finding resources by URL pattern**

1. *Add `url` to the report server's assignment filter.* The where clause is
   `["external_activities.name LIKE ?"]`; making it also match
   `external_activities.url` is that line plus a `num_params` bump. Inherits the
   project scoping for free, and leaves the researcher inside the tool that
   produces the who/when data. Searching `unit=brain` returns all 14
   problem-activities and the researcher multi-selects the ones they want, which
   surfaces the per-problem structure instead of requiring prior knowledge of it.
   **Recommended** — smallest change, best placed.
2. *Index `url` in the portal's Solr search.* `url` is absent from
   `ExternalActivity`'s `searchable do` block. Reaches the portal UI and other
   search consumers, but substring matching a URL fits Solr's tokenizer poorly
   (needs ngram or wildcard configuration) and requires a reindex — more work,
   worse matching, further from where the answer is needed.
3. *A dedicated Pundit-scoped portal endpoint* taking a URL pattern, alongside
   `API::V1::ResearchClassesController`. Cleaner API story than 1, but a new
   endpoint plus a new client where 1 is two lines on an existing path.

**Visibility — telling the researcher what they cannot see**

Scoping is project-based, so the useful signal is the count of out-of-scope
matches *plus the project names they sit in*: "8 more assignments match, in
projects X and Y." That is enough for an AI agent to tell the researcher whom to
ask for access, and project names are not student data. A bare count is safer but
close to useless — it gives an agent nothing to act on. Going further, to project
admin names or emails, is the first version that discloses people, and should be
a deliberate decision rather than a default.

Note honestly: revealing that matching resources *exist* is itself a small
disclosure. For named research projects that is acceptable, but it is a choice,
not free.

**Dependency**

None of this reaches `cc-data` until `cc-data` can *create* report runs; today it
only downloads runs that already exist. So the near-term shape is: the researcher
does discovery and run creation in the report server web UI, then `cc-data` pulls
the result. Option 1 makes that workflow possible; it does not make it
automatable.
