# Detecting the trial-and-error ↔ systematicity axis in Dataflow work

Design for the first axis of [CLUE-575](https://concord-consortium.atlassian.net/browse/CLUE-575),
*SPIKE: Researcher Prompting on Algorithm Design with AI*.

CLUE-575 asks for detectors for four algorithmic-thinking behaviours — trial and
error, systematicity, decomposition, reusing — plus a fifth, vaguer goal about
the class gallery. This design covers **one axis only**: items 1 and 2, read as
two poles of a single continuum rather than two independent detectors. The
ticket writes them as an explicit contrast ("it would be interesting to compare
those students with the above"), and both reduce to the same measurable
structure: how many things change before the student stops to look, and whether
they stop to look at all.

Decomposition and reusing are out of scope here. They need different machinery —
program-graph reconstruction and copy-provenance respectively — and each
deserves its own design.

> **Scope note.** This is a detection design over already-fetched local data. It
> does not touch `cc-data`'s CLI surface, and it fetches nothing new. Everything
> it needs is in `local-data/`, described in
> [`docs/clue-data-inventory.md`](../../clue-data-inventory.md).

## The problem the design has to solve

Judging whether a change was made systematically requires knowing when the
student stopped editing and started watching the program run. **CLUE never
records that boundary.** A Dataflow program executes continuously; there is no
run button, no compile step, no "I'm done editing" signal. The obvious fix —
have students mark it — was considered elsewhere and is not available for data
already collected.

So the boundary has to be inferred. Everything below is, in one way or another,
a reconstruction of that missing marker from evidence that was recorded for
other reasons.

## What the corpus actually supports

Findings established while designing this, each verified against the data or the
CLUE source. They are load-bearing: the design changed shape three times as they
came in.

### The `action` column is the wrong primitive

`history.parquet`'s `action` column looks like the natural unit of student
activity. It is not. Only 279 documents record granular actions like
`/program/addConnection`; 2,604 record edits as `setProgram`, which is a
catch-all. The first `setProgram` entry sampled turned out to be five node
*renames*, not a program edit at all.

The real primitive is one level down. Every history entry's `entry_json`
carries `records[].patches[]` — JSON-patch operations with exact paths:

```
/content/tileMap/{tile}/content/program/nodes/146/data/orderedDisplayName
```

Patch paths are uniform across the whole corpus regardless of which action name
wraps them. This is why the design's first step is a re-encoding rather than a
query: the semantic edit stream has to be built before anything can be measured.

This vindicates the instinct behind the original request — that the history may
need saving "in an additional way that makes temporal patterns more evident."
It is not a convenience; without it the corpus cannot be measured consistently.

### Runtime output masquerades as student activity

`program/values/*/currentValues/nodeValue` and `.../recentValues/nodeValue`
patches are the program's *computed output*, not anything the student did — and
they appear inside entries whose `action` is not a tick. The inventory's advice
to filter ticks by action name is necessary but not sufficient; classification
must happen at the patch path.

**A third such path was missed, and the implementation inherited the gap.**
`program/nodes/{id}/data/demoOutput` is the Demo Output node's live display
value, rewritten as the program runs. It is not in the list above, so
`build_edits.CLASSIFY` — which catches the two paths that are — sweeps it into
`parameter` as though a student had set it.

The evidence that it is machine output: the median gap between consecutive
`demoOutput` changes is 0.08s and 87% fall within 2 seconds, which is not a
human edit rate. It is emitted under the `setProgram` action rather than a tick
action, which is why filtering by action name does not reach it — precisely the
insufficiency this section warns about.

It survives coalescing. After the 2-second window collapses repeats, roughly
17,200 operations remain, against 48,100 `parameter` edits and 93,100
`structure` edits.

**And it is not the only one.** Tick values are stored under each node at
`program/nodes/{id}/data/tickEntries/{id}/nodeValue`, so the same branch
catches them too. `build_edits.py` excludes tick ACTIONS
(`action NOT LIKE '%tickAndProcess'`), which removes the bulk — but 14,648 of
these patches arrive inside `setProgram` entries and pass that filter, leaving
6,310 operations after coalescing. This is the section's own point restated:
filtering by action name is not sufficient, because the same data arrives under
more than one action.

Together the two account for roughly 23,500 of the 48,100 `parameter` edits —
about half the class is runtime output rather than student action.

**This does not merely inflate counts; it manufactures cycles.** A burst built
only from these patches is a cycle with no student edit in it. Episode ep004287
is labelled `systematic` across five cycles, and four of them are
`parameter`-only bursts whose entire content is tick values — cycle 1 is the
single patch
`/program/nodes/7@…/data/tickEntries/7bHv…/nodeValue`. Only cycle 3 contains a
real edit (a Transform node added, its operator set to Ramp). The reviewer's
note on that episode, written before any of this was known, was that it was
"hard to see what they are actually changing in the program."

So the correction removes edits, and will also remove whole cycles and
therefore some episodes. It changes `n_changes` and `n_distinct_targets`, which
is what carries a cycle over the three-target trial-and-error threshold. It
does not affect `oscillation`, which needs a structural add and remove. The
size of the correction is unmeasured — see Open questions.

### One student gesture is many history entries

Measured across the 200 busiest documents: consecutive non-tick entries have a
median gap of 0s and a p90 of 2s. "One change at a time" therefore cannot be
counted in history entries. Entries must be coalesced into semantic operations
first, or every student will look frantic.

### `created` is a client clock

`created` is stamped in the student's browser when the entry begins recording
([`history.ts:41`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/history/history.ts#L41),
`types.optional(types.Date, () => new Date())`). `server_created` is Firestore's
own timestamp at upload
([`firestore-history-manager.ts:239`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/history/firestore-history-manager.ts#L239)).

The two agree at the median, but the 1st and 99th percentiles of
`created → server_created` are **−55s and +53s**. The negative tail is the
proof of skew: an entry cannot be created after it was stored.

| use | column | why |
|---|---|---|
| durations within one document | `created` | same browser, same clock — skew cancels |
| anything across documents or students | `server_created` | otherwise comparing unsynchronised clocks |

`created` marks the *start* of an entry, which stays in `recording` state until
all its records are added
([`history.ts:43-44`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/history/history.ts#L43-L44)),
so on a multi-record entry it is the beginning, not the completion.

**Monotonicity.** Ordering by `idx` then `entry_id`, 1,036 documents contain at
least one backwards step. Most are harmless: 11,026 steps are ≤1s (index ties
from concurrent writes, the anomaly the inventory already documents) and 3,397
fall between 1s and 60s. The serious ones — 8,350 steps over 60s, the worst
about two days — are concentrated in **52 documents**, which are excluded from
timing analysis rather than silently averaged in.

### `entry_uid` is empty, and correctly so

Null on all 6,381,134 entries. There are two Firestore history managers and only
one of them stamps a uid:

| manager | Firestore write | uid |
|---|---|---|
| concurrent | `{ ...getSnapshot(entry), uid: uploaderUid }` ([`firestore-history-manager-concurrent.ts:345`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/history/firestore-history-manager-concurrent.ts#L345)) | stamped at upload |
| non-concurrent | `JSON.stringify(getSnapshot(entry))` ([`firestore-history-manager.ts:237-242`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/history/firestore-history-manager.ts#L237-L242)) | never written |

Real students only ever used the non-concurrent path — concurrent documents were
never deployed to classrooms — so no entry in this corpus could carry a uid. The
field was also added 2026-04-29 (`0c49c3634`, CLUE-516), after 5.4M of the 6.38M
entries were already written.

**This is not a data gap.** A non-concurrent document has exactly one author,
already recorded as `doc_uid`; there is nothing to disambiguate. The only
situation where authorship is genuinely ambiguous is the concurrent one, and
that manager already stamps it. The empty column is a correct consequence of
which manager wrote the corpus, not a missed opportunity.

It is still worth recording in the inventory, because the column is listed there
and looks usable.

### Ticks are a usable presence heartbeat, with caveats

`content/step` and `program/tickAndProcess` are 54% of all history entries, and
the inventory rightly says to filter them out for authoring behaviour. For this
design they are the opposite of noise: they are the only continuous evidence
that a student had the document open.

Across the 200 busiest documents the median inter-tick gap is 0s and the p90 is
1s. Only 2,152 tick gaps exceed 5 minutes. It is a dense signal.

Two caveats bound it:

- **Rate varies.** Of 785 documents with enough ticks to measure, 764 tick at
  ≤2s, 16 at 2–15s, and 5 at 15–90s. Chosen `setProgramDataRate` values across
  the corpus are 100ms (669×), 50ms (142×), 1000ms (100×), 500ms (71×), 10000ms
  (49×) and 60000ms (33×). The 1-minute rate is real but rare, roughly 2.7% of
  measurable documents. The presence threshold must therefore be per-session and
  adaptive, not a constant.
- **Ticks do not stop when the tile scrolls out of view.** They stop when the
  document is closed. So the evidence is asymmetric: *absence* of ticks is strong
  (they left), *presence* of ticks is weak (they had it open, possibly while
  doing something else entirely).

### Log `extras` carries UI state on every event

`extras` is populated on **100% of all 258,916 log rows**, carrying
`navTabsOpen`, `selectedNavTab`, `workspaceMode`, `problemPath`, `group`,
`role`, and `tzOffset`. Each log event is a snapshot of where the student's
attention was, so the state can be carried forward between events.

For Dataflow edits the state genuinely varies rather than being constant:

| navTabsOpen | selectedNavTab | workspaceMode | `DATAFLOW_TOOL_CHANGE` |
|---|---|---|---|
| true | problems | 1-up | 61,941 |
| false | problems | 1-up | 44,108 |
| true | class-work | 1-up | 1,310 |
| — | problems | 4-up | 605 |

**But `navTabsOpen` is ambiguous.** It logs `navTabContentShown`
([`logger.ts:169`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/lib/logger.ts#L169)),
defined as `dividerPosition > kDividerMin`
([`persistent-ui.ts:55-57`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/stores/persistent-ui/persistent-ui.ts#L55-L57)).
The companion view `workspaceShown` (`dividerPosition < kDividerMax`) is **not
logged**, so three UI states collapse to two:

| dividerPosition | UI | `navTabsOpen` |
|---|---|---|
| at min | workspace full, curriculum hidden | `false` |
| between | split — both visible | `true` |
| at max | curriculum full, workspace hidden | `true` |

`true` conflates "my program is visible beside the curriculum" with "my program
is not on screen at all," so only `false` is usable — and it covers 41% of
Dataflow edits, which is enough to be worth having.

### A document-open event exists; scroll and visibility do not

`VIEW_SHOW_DOCUMENT` (5,920×) is logged from `setPrimaryDocument`
([`workspace.ts:29-34`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/models/stores/workspace.ts#L29-L34)),
so it fires whenever the student switches which document occupies the right-hand
workspace. It is a genuine open event, not a proxy. Its practical reach is
limited for this corpus: a student working steadily in one problem document
generates it once, so it marks transitions between documents rather than
bounding a working session.

There is **no close event** — `CLOSE_SORTED_DOCUMENT` (20×) applies only to the
sorted-work view — and **no scroll or tile-visibility events**. CLUE's
`LogEventName` enum has neither; the only near-miss, `EXEMPLAR_VISIBILITY_UPDATE`,
is unrelated. The 60 distinct event types present in the corpus confirm it.

This is what leaves the scroll blind spot: ticks continue while the Dataflow tile
is scrolled out of view, and nothing records that it happened.

### Documentation is partly curriculum-scaffolded

Every `brain` problem embeds 7–22 Text tiles, but those are curriculum prose,
not student writing. Tables are embedded in only four problems (1.3 ×3, 2.1 ×3,
1.5, 2.2). The usable distinction is provenance: a documentation tile the
student **copied from the curriculum** (`COPY_TILE` / `handleDragCopyTiles`) is
prompted, while one they **created themselves** (`userAddTile` / `CREATE_TILE`)
is self-directed.

This was checked against `../clue-curriculum` at `51915c6`, which is today's
`main` and **not necessarily what students saw** — the unit param `brain`
resolves to whatever was current at launch time. Documentation is therefore kept
as a separate labelled channel rather than folded into a single score.

## Approach

Three were considered:

- **A. Derived episode table plus an interpretable feature detector.** Build the
  semantic edit stream, segment it, score with explicit features, emit ranked
  candidates with replay links.
- **B. Compact-and-read.** Same compaction, but render each episode as a
  narrative and have a model label it with a rationale.
- **C. Differential sequence mining** (Kinnebrew, Loretz & Biswas 2013, *JEDM*
  5(1), ERIC EJ1115377) — abstract to an action alphabet with `-REL/-IRR/-MULT`
  tagging, mine frequent subsequences, rank by differential frequency between
  groups. Summarised in the `meaning-in-interactions` research notes under
  `papers/differential-sequence-mining-kinnebrew-2013.md`, which is a separate
  repository and so not linked from here.

**A was chosen**, structured so B can layer on the same artifact. All three need
the identical compaction step, and A is the only one that produces replayable
examples immediately — which is the sole route to the ground truth that B and C
both require. C additionally needs a group contrast (high vs low performers)
that does not exist for this corpus yet, and its output is patterns rather than
examples a researcher can verify.

## Architecture

Four artifacts, each built by its own idempotent script in
`docs/recipes/behavior/`, writing to `local-data/derived/` (gitignored). Each
reads only the artifact before it, so any stage can be rebuilt alone.

```
content.parquet ──→ population list (type, unit, dataflow_tile_deleted)
                          │
history.parquet ──────────┼──→ edits.parquet ────┐
                          │                      ├──→ cycles.parquet
history.parquet (runtime) ┴──→ presence.parquet ─┤
                                                 │
logs.parquet ────────────────────────────────────┘
                                                 │
                                                 ↓
                                     candidates.parquet ──→ review sheet (md)
                                                 ↑                  │
                                          recalibration ←── verdicts.csv
```

`content.parquet` is used once, at the start, to define the population: `type`
selects problem documents, `unit` records which curriculum they belong to, and
`dataflow_tile_deleted` identifies the abandoned-tile documents so they can be
reported separately after the fact. It contributes no per-event data.

### 1. `edits.parquet` — the semantic edit stream

One row per semantic student operation, produced by exploding
`entry_json → records[] → patches[]` and classifying each path.

Classification needs **both** the patch path and the entry's action. Path alone
is insufficient because the Dataflow tile and the Simulation tile write to the
same shared-model paths a student uses when filling in a table by hand. The
discriminating rule is the action's root: an action rooted at
`tileMap/{tile}/content/…` was initiated by the student on that tile, while one
rooted at `sharedModelMap/…` is written by a running program.

| class | matched by | counts as a change |
|---|---|---|
| `structure` | path `program/nodes/{id}` add/remove; path `program/nodes/{id}/inputs/*` add/remove | yes |
| `parameter` | path `program/nodes/{id}/data/*`, **excluding** `orderedDisplayName` | yes |
| `layout` | path `program/nodes/{id}/x\|y`; path `programZoom/*` | no |
| `runtime` | path `program/values/*`; actions `content/step`, `tickAndProcess`, `sharedModel/dataSet/addCanonicalCasesWithIDs`, `sharedModel/variables/*/setValue` | **never** — presence only |
| `documentation` | actions on a `tileMap` root: `setSlate` (path `content/text`), `setCanonicalCaseValues`, `addCanonicalCases`, `addObject`; plus `sharedModel/dataSet/setAttributeName\|removeAttribute\|addAttributeWithID` | yes, separate channel |
| `tile` | actions `/addTile`, `/deleteTile`, `/content/userAddTile`, `/content/handleDragCopyTiles` | yes; the last also flags curriculum provenance |
| `undo` | action `undo`, or `is_revert` | yes, separate channel |
| `ambiguous` | action `sharedModel/variables/*/commitTemporaryValue` | no — excluded, but recorded |
| `other` | anything unmatched | no — recorded so the unclassified tail stays visible |

**Three traps this table exists to avoid**, each measured:

- `sharedModel/variables/*/setValue` is **263,536** entries of the Simulation
  updating itself, and `sharedModel/dataSet/addCanonicalCasesWithIDs` is
  **82,316** entries of Dataflow recording sensor readings into a table. Together
  that is ~345k entries that read as student data-entry and are not.
- `setSlate` is an *action* name; the patch path it produces is
  `tileMap/{t}/content/text`. Matching the string `setSlate` against paths finds
  nothing.
- `orderedDisplayName` is a derived rename that cascades across every node when
  one is added — 100 of 2,918 sampled `data/*` patches. Counting it inflates the
  parameter class on exactly the events that already count as `structure`.

`ambiguous` and `other` exist so that misclassification shows up as a growing
bucket rather than as silently wrong counts. Their sizes are reported by the
build.

Columns: `doc_id, uid, portal_class_id, unit, problem, tile_id, entry_id, idx,
created, server_created, class, op, target_kind, target_id, node_type,
value_before, value_after, from_curriculum, clock_suspect`.

**Coalescing.** Consecutive rows sharing `(class, target_id, op)` within a
2-second window collapse to one operation, retaining first and last timestamps.
This is what makes "one change at a time" countable, and the 2-second figure
comes from the measured p90 above.

**Clock-suspect documents are built, not skipped.** `edits.parquet` covers the
full population including the 51 flagged documents, so the artifact stays
complete and reusable for questions that do not depend on timing. The exclusion
happens one stage later, at `cycles.parquet`, which is where durations start to
matter. That is what the `clock_suspect` column is for.

Two known limits, recorded rather than solved. Node *type* appears on the `add`
of a whole node but not on later `data/*` changes, so it must be carried forward
per node id within a document. And `value_before` comes from `inversePatches`,
which the index-anomaly documents may interleave oddly.

### 2. `presence.parquet` — when the student had the document open

One row per continuous presence interval per `(doc_id, tile_id)`: maximal runs
of runtime events whose gap does not exceed an adaptive threshold of
`max(60s, 6 × observed median tick interval)` for that session. Columns:
`start, end, n_ticks, median_tick_ms, rate_coarse`.

`rate_coarse` flags sessions whose tick rate is slow enough that presence
resolution is poor, so a gap there is not over-read.

### 3. `cycles.parquet` — the reconstructed edit/run boundary

The unit of analysis is a **cycle**: one edit burst plus the pause that follows
it. Three levels of segmentation:

1. **Session** — a maximal run of operations staying inside presence coverage,
   per `(doc_id, tile_id)`. A gap with no ticks ends a session however short; a
   tick-covered gap does not, however long.
2. **Burst** — within a session, a maximal run of change-class operations whose
   inter-operation gaps stay under the burst threshold.
3. **Pause** — the gap after a burst, classified by a three-channel join.

**Pause classification** combines ticks, log events during the pause, and UI
state carried forward from the last log event:

| ticks | log events in pause | `navTabsOpen` | reading |
|---|---|---|---|
| yes | none | `false` | **watching the program** — the strongest available claim |
| yes | none | `true` | present, attention unknown |
| yes | `TEXT_TOOL_CHANGE` / `TABLE_TOOL_CHANGE` | any | documenting |
| yes | `SHOW_TAB_SECTION` / `VIEW_SHOW_DOCUMENT` / `SHOW_WORK` | any | reading curriculum or others' work |
| no | — | — | left the document |

Carrying UI state forward is an assumption, not an observation, so it expires:
beyond a staleness bound since the last log event the state reverts to
`unknown`.

**Per-cycle features:** `n_changes`, `n_distinct_targets`, classes touched,
`pause_after_s`, `pause_type`, `oscillation` (a target added then removed, or a
parameter returned to a prior value), `undo_in_burst`, and documentation
operations during the pause split by provenance.

**Context, not classification:** `n_nodes`, `n_connections` folded forward from
the edit stream. Five changes to a twelve-node program read very differently
from five changes to a two-node program when deciding what to replay. These play
no part in the axis score.

### 4. `candidates.parquet` and the review sheet

**The axis is applied per cycle, as two indicators rather than one blended
score:**

- **Systematic** — one distinct target changed, followed by a `watching the
  program` pause. One change, then look at it.
- **Trial and error** — several distinct targets changed with no watching pause
  after, or oscillation within the burst.

Cycles satisfying neither are **unclassified**, and most are expected to be.
This is deliberate: a detector that labels everything cannot be wrong, and
cannot be checked.

**An episode is a maximal run of consecutive same-kind cycles.** A document
yields a *sequence* of episodes, not a label — which is what makes "for the same
document there might be times of each of these things" representable at all.

Each candidate row carries `doc_id`, `uid`, class, unit/problem, kind, cycle
count, purity, wall-clock span, the per-cycle features behind it, and a **replay
URL**:

```
?studentDocument=<doc_key>&studentDocumentHistoryId=<entry_id>
```

CLUE accepts both params
([`url-params.ts:50-52`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/utilities/url-params.ts#L50-L52),
wired at
[`initialize-app.tsx:119`](https://github.com/concord-consortium/collaborative-learning/blob/master/src/initialize-app.tsx#L119)),
and `entry_id` is already a column in `history.parquet` — so verification opens
at the exact moment rather than requiring scrubbing.

## Verification is an output, not an afterthought

The review sheet is generated Markdown with a verdict column: **confirmed /
rejected / ambiguous**, plus a free-text note. Verdicts are saved to
`local-data/derived/verdicts.csv` and become the calibration set — thresholds
are re-tuned against them and the sheet regenerated.

This is the only mechanism by which the detector improves, and the only way
approach B would ever acquire ground truth to be evaluated against.

**Sampling is stratified, not top-ranked.** Ranking purely by strength would
show only the clearest cases and teach us nothing about the boundary. The first
sheet contains the strongest examples of each pole, a band straddling the
threshold, and a few random unclassified cycles as a check that the phenomenon
is not being missed entirely.

## Calibration

Thresholds are derived from the data, not invented:

| threshold | derived from |
|---|---|
| coalescing window (2s) | p90 of inter-entry gaps — measured |
| burst gap | distribution of inter-operation gaps within sessions |
| watching-pause range | distribution of pause lengths, looking for separation between "still typing" and "stopped to look" |
| presence gap | `max(60s, 6 × median tick interval)` per session — measured rate distribution |
| UI-state staleness bound | distribution of inter-log-event gaps |

Chosen values **and the histograms they came from** are recorded, so a later
reader can see why one number and not another.

## Scope

**Included:** the 2,832 documents of type `problem` that have history —
`brain` 2,473, `seeit` 271, `vibe` 54, `clueful` 33, `qa` 1. Removing the 51
clock-suspect documents among them leaves a working population of **2,781**.

The documents whose Dataflow tile was created and later deleted are **not
filtered out**. 71 of the 72 already sit inside this population — they are not
an addition to it — and for trial and error they are the most relevant documents
in the corpus. Excluding them would come close to excluding the phenomenon under
study.

**Excluded:** publications and personal publications (no history by
construction); personal documents (864, almost all from the standalone Dataflow
app, which predates history support); the single `planning` document and the
`learningLog` documents, which are not type `problem`; and the 52 clock-suspect
documents (51 of which fall inside the population above).

## Testing

Each build script gets a small fixture-based test — a handful of real history
entries with known patch shapes, asserting the classification, the coalescing
window, and the pause classification behave as specified. Fixtures are redacted
and committed; the corpus is not.

Two properties are asserted over the full build rather than a fixture, because
they are the failure modes that would otherwise pass silently:

- Every `edits.parquet` row round-trips to an `entry_id` present in
  `history.parquet` — no invented rows.
- No `runtime`-class patch ever contributes to a change count.

Following `build-parquet.sh`'s precedent, each script **refuses to build from
incomplete inputs** rather than producing a quietly smaller output. That
convention exists because a glob matching fewer files silently overwrote 6.27M
history entries with 110k once already.

## What gets written back to the inventory

Regardless of whether the detection succeeds, these are facts about the corpus
that outlive this question and belong in
[`docs/clue-data-inventory.md`](../../clue-data-inventory.md):

- The patch-path taxonomy, and that `action` is the wrong primitive.
- `program/values/*` patches are runtime output, not student edits.
- `created` is a client clock; `server_created` is the server's.
- The 52 clock-suspect documents.
- `entry_uid` is null throughout, because the non-concurrent history manager
  never writes it — and why that is correct rather than a gap.
- `extras` carries full UI state on 100% of log rows.
- `navTabsOpen` is ambiguous between two of three UI states.

## Instrumentation recommendations

Leslie's comment on CLUE-575 anticipates that "one outcome of this story could
be suggestions for additional tagging of data." Three emerged from this design,
each stated with the analysis it would have unblocked:

1. **Log `dividerPosition` or `workspaceShown` alongside `navTabsOpen`.** One
   field. It resolves the three-into-two collapse above completely, turning 59%
   of Dataflow edits from "attention unknown" into a definite state.
2. **An explicit "done editing, now running" marker.** This entire design is an
   inference of something a single event would state outright. It is the
   highest-value addition by a wide margin.
3. **A scroll event carrying the visible percentage of each tile.** Not a
   binary in-viewport flag — tiles are routinely *partly* visible, and the
   fraction is the informative part. Two things make this valuable beyond
   fixing the presence blind spot:

   - **It would measure attention directly.** Presence is currently inferred
     from ticks that keep running while the tile is scrolled away. Percentage
     visible would replace the inference with an observation.
   - **Scrolling is itself a behavioural signal here.** In parts of the
     curriculum a Simulation tile drives the Dataflow program, standing in for
     real hardware — 506 of the 2,832 documents in this population (18%) contain
     one. On a small screen a student cannot see the Simulation tile and their
     program at once, so they scroll back and forth between them. That
     oscillation is evidence of exactly the edit-then-observe cycle this design
     is trying to reconstruct, and it is currently invisible.

   A document-open event already exists (`VIEW_SHOW_DOCUMENT`, above), so it is
   not requested here; a close event would still help but matters far less than
   the scroll data.

## Open questions

- **Does the watching pause separate cleanly?** The design assumes pause lengths
  are distinguishable between "still working" and "stopped to look." If the
  distribution is unimodal, the systematic pole loses its anchor and the axis
  would have to rest on burst composition alone. This is the first thing
  calibration will reveal, and it is a genuine risk to the approach.
- **Is `brain` one population or several?** It spans 2022 to 2026 across 57
  classes. Curriculum, CLUE, and the Dataflow tile all changed in that window.
  Whether cycles from 2022 and 2026 are comparable is unresolved, and the
  inventory's "history of the system itself" gap is exactly why it cannot be
  answered from the data alone.
- **How much does the runtime-output correction move the rates?** Roughly half
  of `parameter` edits are runtime rather than student action — Demo Output
  display churn plus tick values arriving under non-tick actions (see "Runtime
  output masquerades as student activity"). Excluding them lowers `n_changes`
  and `n_distinct_targets`, so some cycles fall back under the three-target
  threshold; and because some bursts are made *entirely* of these patches,
  whole cycles and some episodes disappear. Every rate in the calibration and
  every count in the review sheet moves. Whether the axis survives it intact,
  or the systematic pole was partly an artifact, is unknown until it is run
  both ways.
- **How many verdicts are enough?** Recalibration needs a set large enough to
  move thresholds without overfitting to a handful of replays. Deferred until
  the first sheet exists and the rate of review is known.
