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

**The conclusion this supports:** pause length is not a usable signal for "the
student stopped editing to look at the result." Any detector built on pause
duration alone — including thresholds this pipeline calibrated and used — is
reading noise where it hopes to read intent.

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

The one place the pipeline *can* see something boundary-like is where a
Simulator tile drives program input: if the input value is constant, then
changes, then goes constant again, that shape ("static → changing → static") is
a real, recoverable signal, independent of pause length. This is what
`build_trials.py` finds.

Of the 506 documents in the analysis population that contain a Simulator tile,
405 (80.0%) show at least one such trial. Across those 405 documents there are
3,821 trials in total. 1,799 of them (47.1%) begin within 120 seconds of a
program-structure edit — the pattern of "change the program, then work the
input to see what happens," which is close to the observable proxy for
"finished editing, now checking."

**Stated against the whole population this pipeline works over, not just the
Simulator subset:** 2,727 documents contain a program edit at all
(`report_documentation.py`, live run). The 405 documents with a detected trial
are 14.9% of that population — call it roughly one document in seven. For the
other 85%, this pipeline has no boundary-detecting signal at all, because there
is no varying input to watch.

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
  not only the ~15% where a varying input happens to reveal it.
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
it. The Simulator subpopulation — the 405 documents discussed in §3 — already
yields a usable trial signal from existing data, with no interface change and
no added friction for those students. Nearly half of its trials (47.1%) line up
with a preceding program edit inside a two-minute window, which is a real,
actionable proxy for "student finished editing, then checked the result." Any
argument for friction has to weigh its cost against extending coverage from
that ~15% of documents to the rest — not against having no signal at all.

**The claim this document supports is narrower than "friction will fix
learning."** It is that the behaviour of interest — systematic edit-then-check
versus rapid trial-and-error — is largely unobservable today outside a minority
of documents, for structural reasons a better detector cannot work around, and
that an explicit run/pause control is the direct way to make it observable
everywhere rather than the ~15% where an input happens to vary.

## Instrumentation recommendations

Two logging gaps came up while building this pipeline and share the same
evidence base, independent of whether run/pause friction is added:

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
