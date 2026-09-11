# Evidence for run/pause friction in Dataflow

Dataflow programs run continuously: a student edits a node and the program
keeps evaluating, tick after tick, with no action that says "I am done editing,
now run it." This document assembles the measured evidence, from the
[trial-and-error/systematicity detection pipeline](recipes/behavior/README.md),
for adding that action — a deliberate run/pause control — and states its limits
honestly. Every number below is aggregate over the Dataflow document corpus;
none identifies a student, class, school, or document.

The short version: the behaviour researchers most want to see — a student
finishing an edit and deliberately checking the result — is largely
unobservable in the data as it exists today, not because the pipeline built to
find it is weak, but because for most of the corpus the boundary it is looking
for was never drawn by the interface.

## 1. There is no edit/run boundary in the data, and none in the behaviour either

If students paused noticeably longer to "run and check" than to think between
edits, the distribution of gaps between consecutive program-edit operations
would show two humps — a fast cluster of mid-edit pauses and a slower cluster
of run-and-look pauses — with a valley between them. It does not.

118,054 operation gaps, binned on a log scale (`calibrate.py`,
`local-data/derived/calibration.md`):

| gap (s) | count | share |
|---|---|---|
| 0–1 | 32,942 | 27.9% |
| 1–2 | 6,551 | 5.5% |
| 2–3 | 12,405 | 10.5% |
| 3–5 | 16,567 | 14.0% |
| 5–8 | 13,650 | 11.6% |
| 8–13 | 11,005 | 9.3% |
| 13–21 | 8,016 | 6.8% |
| 21–34 | 5,397 | 4.6% |
| 34–60 | 4,425 | 3.7% |
| 60–120 | 3,330 | 2.8% |
| 120–300 | 2,449 | 2.1% |
| 300–900 | 1,317 | 1.1% |

The count decays smoothly and monotonically across four orders of magnitude,
0 to 900 seconds. There is no second peak and no valley anywhere in the range —
just gaps getting rarer as they get longer, the shape of "how long since the
last thing happened" with no second process superimposed on it.

**This is not an artifact of pooling "the student walked away" in with real
mid-session pauses.** Splitting the same gaps by whether they fall inside a
reconstructed log session (a burst of activity bounded by real login/logout
telemetry, not by an inferred threshold) barely moves the distribution:

| | inside a session | outside a session |
|---|---|---|
| median gap | 3.77s | 3.55s |
| p90 gap | 33.22s | 31.39s |

If long gaps were mostly "student closed the tab," restricting to inside-session
gaps should shorten the tail noticeably. It does not — p50 and p90 move by a few
percent, not by an order of magnitude. Whatever generates the gap distribution,
it is the same process whether or not the student is mid-session.

**The conclusion this supports is narrower than it first appears, and worth
stating precisely.** The *unlabelled* distribution cannot separate itself: no
threshold can be read off the shape above, because the shape has no feature to
read. That is a real limit, and it is why a detector that picks a pause
threshold by looking for a valley will pick an arbitrary number.

It does not mean pause length carries no information. Given an external label
for which gaps fall inside one edit-then-check cycle, the same gaps separate
sharply — median 4.2s within a cycle against 98.4s across one (§3, and
`build_cycles.py`, which uses that separation to set its burst threshold). The
signal is there; what is missing is the boundary that would label it. A
run/pause control would supply exactly that label, for every document rather
than for the minority where one can be inferred.

## 2. The cause is structural, not a measurement gap

The reason pause length does not separate the two behaviours is not that the
signal is present but hard to detect. For much of this corpus the *behaviour
itself doesn't have a boundary to detect*. A Dataflow program evaluates on every
tick regardless of whether the student just changed something, so a student
watching output after an edit and a student mid-edit look identical in the
data: both are "program running, values changing, nothing logged that
distinguishes intent." There is no client-side event that means "I finished
editing and now I am watching," because the interface never asks the student to
make that distinction. This is why later sections of the pipeline (tick
presence, session reconstruction, trial detection) exist at all — they are
attempts to infer a boundary from indirect evidence, because the direct
evidence was never recorded.

## 3. A trial is detectable only where an input changes

The one place the pipeline *can* see something boundary-like is where the
program's input goes constant, then changes, then goes constant again. That
shape ("static → changing → static") is a real, recoverable signal, independent
of pause length. Two detectors find it, on two different kinds of input.

**A Simulator variable, driven by the mouse** (`build_trials.py`). Of the 506
documents in the analysis population that contain a Simulator tile, 405 (80.0%)
show at least one such trial, 3,821 trials in all. 1,799 of them (47.1%) begin
within 120 seconds of a program-structure edit — the pattern of "change the
program, then work the input to see what happens," which is close to the
observable proxy for "finished editing, now checking."

**A sensor, driven by the student's body** (`build_sensor_trials.py`). Sensor
readings arrive as program ticks rather than as document edits, and a sensor is
noisy where a slider is exact, so this detector compares a rolling range
against each node's own scale instead of testing values for equality. On
sensors bound to a real device — an EMG being flexed, a pressure pad pressed —
it finds 281 trials across 22 documents, median 5.3s and p90 20.4s. 170 of them
(60.5%) follow a program edit within 120 seconds, a higher coupling rate than
the Simulator's 47.1%.

One confound applies here that does not apply to the Simulator. A student has
one mouse and cannot drag a slider while editing, but they can flex one arm and
work the mouse with the other, so an edit and a reading-change could be
simultaneous rather than sequential. Measured, this is rare: 26 of the 281
trials (9.3%) overlap an edit in time. Students do mostly stop editing before
they flex.

**The ceiling on this is instrumentation, not behaviour.** Per-tick node values
are only written into document history for documents recent enough to record
them — 273 of the 2,832 in the analysis population. For everyone else the
readings were never saved, whatever the student did:

| documents | count |
|---|---|
| in the analysis population | 2,832 |
| …containing a Sensor node | 1,447 |
| …with that Sensor bound to a device | 1,071 |
| …and carrying `tickAndProcess` history | 52 |
| …yielding at least one detected trial | 22 |

The collapse from 1,071 to 52 is the missing recording, not missing students.

**Stated against the whole population this pipeline works over:** 2,727
documents contain a program edit at all (`report_documentation.py`, live run).
405 have a Simulator trial and 22 have a physical-sensor trial; 20 of the 22
are documents the Simulator detector never saw. Together they cover 425
documents, 15.6% of that population — call it roughly one document in six. For
the other 84%, this pipeline has no boundary-detecting signal at all, because
no varying input was recorded to watch.

**These trials are also the only usable anchor in the corpus.** Because a trial
marks a moment the student stopped editing and exercised the program, edits
falling between two consecutive trials are one edit-then-check cycle by
construction. That labels the gaps §1 could not separate: across 1,364 such
spans in 303 documents, gaps *within* a cycle have a median of 4.2s, while a
gap that a trial falls *inside* has a median of 98.4s. The pipeline's burst
threshold is calibrated on that separation rather than guessed
(`calibrate_burst_gap.py`, `burst_calibration.md`). The limitation is the same
one as everywhere else in this section — the anchor exists only in documents
that have a detectable trial, so a threshold set on 303 documents is assumed,
not shown, to describe the other ~2,500.

## 4. For output-side work, no boundary exists even in principle

Trial detection depends on an *input* changing under student control. Programs
that instead drive an *output* — a wave generator feeding a simulated motor or
light, with no sensor or slider in the loop — never produce a static-changing-
static shape, because nothing about them is ever static while the program runs.
There is no proxy left to infer from; the boundary this section is looking for
does not exist in the program's behaviour, only in the student's head. No
amount of better inference recovers it from history data, because the data
never contained it.

## 5. What friction would buy

An explicit run/pause control — a button or gesture that means "stop
evaluating; I am editing" and its counterpart "go; run what I built" — would
turn every one of the above into a directly recorded fact instead of an
inference:

- The edit/observe boundary would exist for **every** document with a program,
  not only the ~16% where a varying input happens to be recorded and reveal it.
- The multi-stage inference chain this pipeline needed to approximate that
  boundary — classifying patches into semantic operations, reconstructing log
  sessions from raw events, inferring presence from tick cadence, detecting
  trials from input variation — would be unnecessary for this question. A
  `run` event and a `pause` event replace all four.
- Output-only programs, currently invisible to any boundary detection, would
  become observable, because the boundary would no longer depend on the
  program having a visible input to watch.

## 6. What it would cost

This should not be a one-sided case. Run/pause friction is a real cost to
students: it adds a step to a loop that currently has none, in an interface
built around immediate, continuous feedback, and immediate feedback is
generally good for learning a system's behaviour. Introducing a control a
student must remember to use is a genuine interaction-design tradeoff, not a
free instrumentation win, and it changes the learning experience for every
student, not just the ones being studied.

It is also worth being honest that the current pipeline is not blind without
it. The 425 documents discussed in §3 already yield a usable trial signal from
existing data, with no interface change and no added friction for those
students — 47.1% of Simulator trials and 60.5% of physical-sensor trials line
up with a preceding program edit inside a two-minute window, which is a real,
actionable proxy for "student finished editing, then checked the result." Any
argument for friction has to weigh its cost against extending coverage from
that ~16% of documents to the rest — not against having no signal at all.

**And part of that gap is a recording gap, which is cheaper to close than an
interaction is to change.** 1,071 documents in the population have a sensor
bound to a device, but only 52 recorded the tick values that make its readings
visible. Widening that recording would extend the existing detector to
documents whose students already did the thing worth measuring, without asking
any student to press anything new. It would not help output-side programs (§4),
and it would not turn a proxy into a stated intention — but it should be
priced in before friction is, because it is the same evidence at lower cost.

**The claim this document supports is narrower than "friction will fix
learning."** It is that the behaviour of interest — systematic edit-then-check
versus rapid trial-and-error — is largely unobservable today outside a minority
of documents, for structural reasons a better detector cannot work around, and
that an explicit run/pause control is the direct way to make it observable
everywhere rather than the ~16% where a varying input happens to be recorded.

## 7. A randomised subset, rather than friction for everyone

The choice is not between friction for all students and friction for none.
The run/pause rule could apply to a randomly chosen subset — of classes, or of
students — leaving everyone else on the current continuous-evaluation
interface. This is worth considering on its own merits and not only as a
cheaper compromise, because as an experiment it answers questions that
universal friction cannot.

**It fixes the generalisation problem this whole document has.** Every
threshold here is calibrated on documents that happen to expose a boundary:
the 425 with a varying recorded input (§3), and within those, the 268 used to
anchor the burst threshold. Those documents are not a random sample — they are
the ones with a Simulator tile or a bound sensor. A randomised arm produces
exact edit/run transitions on documents chosen at random, which is the only
way to know whether what has been calibrated on that minority describes
everyone else.

**It measures friction's own effect, which universal friction cannot.** With
the control everywhere, you learn what students do under friction and have
nothing to compare it against; any change in the behaviour is confounded with
the change in the interface. Randomised, the difference between arms *is* the
measurement.

**It validates the inferred detector directly.** The treatment arm records the
edit/observe boundary explicitly. The control arm has only the inference chain
described in §2. Running the detector on the control arm and asking whether it
recovers what the treatment arm states outright is the calibration this
pipeline actually needs, and no amount of additional inference substitutes for
it.

**Design cautions.** Randomising per session would let a student meet
different rules on different days, which confounds within-student comparison
and is likely to be confusing to the student; randomising by class or by
student is cleaner, at a cost in sample size. The arms have to be comparable
on the activity being measured, so the treated arm needs enough sessions to
span the same range of work rather than a token slice. And this is more
product work than universal friction, not less — two behaviours to build,
document, and support, rather than one.

**What it does not change.** Treated students still bear the full interaction
cost described in §6; the saving is that untreated students do not. Output-side
programs (§4) become observable only within the treated arm. And a smaller
treated population means less statistical power than universal friction would
give, which is the price of keeping a control.

## Instrumentation recommendations

Three logging gaps came up while building this pipeline and share the same
evidence base, independent of whether run/pause friction is added:

- **Record per-tick node values for every program, not only recent ones.** A
  sensor's readings are the only evidence of what the student physically did,
  and they exist in history only where `tickAndProcess` entries do: 52 of the
  1,071 population documents whose sensor is bound to a device (§3). This is
  the single largest recoverable gap in the evidence base, and unlike the other
  two it needs no new event type — only that the values already computed on
  every tick are written down.

- **Log `dividerPosition` alongside `navTabsOpen`.** The log `extras` field is
  populated on 100% of rows and already carries `navTabsOpen`, but that flag
  conflates two of the three states a student's divider can be in — split view
  and curriculum-fullscreen both log `navTabsOpen: true`. Only `navTabsOpen:
  false` is unambiguous, and it covers 41% of Dataflow edit events. Logging the
  actual divider position would make the other 59% interpretable instead of
  discarded.
- **A scroll event carrying each tile's visible percentage.** Nothing in the
  current logs records whether a given tile — in particular, the Simulator or
  output tile a student would need to see to check a run — was actually on
  screen at a given moment. Presence inference currently leans on program tick
  cadence as a proxy for "document open," which is itself only available for a
  minority of documents (`content/step` reaches 504 of 506 documents with a
  Simulator tile, but only 113 of 2,326 without one). A direct visibility event
  would replace that proxy with a measured fact for every tile, not just the
  ones that happen to tick.
