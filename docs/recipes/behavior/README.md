# Behaviour detection recipes

Builds the derived datasets behind
[the trial-and-error/systematicity design](../../superpowers/specs/2026-08-12-dataflow-behavior-detection-design.md).

Everything reads from `local-data/` and writes to `local-data/derived/`, both
gitignored. Nothing here fetches; run the `clue-documents/` and `log-events/`
recipes first.

## Order

Each stage reads only the previous stage's output.

1. `build_population.py` — which documents are in scope
2. `build_edits.py` — semantic student operations, from JSON patches
3. `build_presence.py` — when the document was open, from program ticks
4. `calibrate.py` — histograms, and the thresholds derived from them
5. `build_trials.py` — static→changing→static runs on a Simulator input
6. `build_cycles.py` — edit bursts and the pauses that follow them
7. `build_candidates.py` — ranked episodes plus a Markdown review sheet
8. `apply_verdicts.py` — folds your review back into the thresholds
9. `report_documentation.py` — how much students actually write, draw, and
   tabulate (a report, not a pipeline stage; run any time after `build_edits.py`)

## Running

```
python3 build_population.py     # and so on, in order
```

`CC_DATA_LOCAL` overrides the path to `local-data/`.

## Tests

```
python3 -m unittest discover -s tests -v
```

Fixtures are synthetic. Real history entries contain student-written text and
this repository is public.
