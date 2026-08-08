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
| Current student documents | not yet established | not yet established | — | **absent** — no Firestore or RTDB client exists anywhere in the CLI; every fetch path goes through the report server's HTTP API. |
| Document history entries | not yet established | not yet established | — | **absent** — see the terminology note above; `cc-data get history` is a different corpus entirely. |

Rows are added as research demands them. The list above is not a claim of
completeness.

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
