# CLUE data inventory

What CLUE data a researcher wants, where it actually lives, and how to get it.

This file is written from real research sessions, not from reading schemas. Each
source gets a row in the table and a recipe below it once a fetch has actually
worked. Rows exist before their recipes; a row with no recipe is a known want,
not a solved problem.

See the [design doc](superpowers/specs/2026-08-07-clue-data-inventory-design.md)
for why this exists and how it is maintained.

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
| CLUE log events (clickstream) | not yet established | not yet established | — | **partial**, unverified — the report server's `student-actions` / `student-actions-with-metadata` / `teacher-actions` runs are Athena queries over the log database CLUE writes to, so CLUE events are expected to appear there, but this has not been confirmed against a real run. Even if it holds, `cc-data` can only download a run someone already created in the report-server web UI; it cannot start one. |
| Student document metadata (find documents, incl. by tile type) | Firestore `authed/learn_concord_org/documents` in `collaborative-learning-ec215` | Firebase service account for that project | [CLUE documents: finding them](#recipe-clue-documents-finding-them) | **absent** — no Firestore or RTDB client exists anywhere in the CLI; every fetch path goes through the report server's HTTP API. |
| Student document *content* | Firebase RTDB, `/authed/portals/learn_concord_org/classes/{classHash}/users/{uid}/documents/{docKey}` | same service account | — not yet fetched; only metadata has been | **absent** — same reason |
| Document history entries | Firestore `authed/learn_concord_org/documents/{docId}/history` | same Firebase service account | [CLUE document history](#recipe-clue-document-history) | **absent** — see the terminology note above; `cc-data get history` is a different corpus entirely. |
| History of the code, deployments, and databases that produced the data | nowhere yet — see [below](#a-missing-data-source-the-history-of-the-system-itself) | institutional memory | — | **absent**, and not obviously `cc-data`'s job |

Rows are added as research demands them. The list above is not a claim of
completeness.

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
