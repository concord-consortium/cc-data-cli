# Behaviour detection recipes

Builds the derived datasets behind
[the trial-and-error/systematicity design](../../superpowers/specs/2026-08-12-dataflow-behavior-detection-design.md).

Everything reads from `local-data/` and writes to `local-data/derived/`, both
gitignored. Nothing here fetches; run the `clue-documents/` and `log-events/`
recipes first.

## Order

Run these in order; each reads earlier stages' outputs.

1. `build_population.py` — which documents are in scope
2. `build_edits.py` — semantic student operations, from JSON patches
3. `build_presence.py` — when the document was open, from program ticks
4. `calibrate.py` — histograms, and the thresholds derived from them
5. `build_trials.py` — static→changing→static on a Simulator variable, which
   the student drives with the mouse
6. `build_sensor_trials.py` — the same shape on a *sensor's readings*, read
   from program ticks. For a physically-bound sensor this is a gesture step 5
   cannot see at all: the student flexing an EMG, pressing a pad. Also needs
   `content.parquet`, to learn which nodes are sensors.
7. `build_cycles.py` — edit bursts and the pauses that follow them. Reads
   **both** trial artifacts: a trial from either detector is evidence the
   student stopped editing to exercise the program.
8. `build_candidates.py` — ranked episodes plus a Markdown review sheet
9. `apply_verdicts.py` — folds your review back into the thresholds

Two more are reports rather than pipeline stages. Neither is read downstream;
run either any time after the stage it depends on.

- `calibrate_burst_gap.py` — calibrates `build_cycles.BURST_GAP_S` against
  trials as an external anchor, and writes `burst_calibration.md`. Needs both
  trial artifacts and `build_edits.py`. It reports; it does not write
  `thresholds.json`, so changing the gap stays a decision a person makes.
- `report_documentation.py` — how much students actually write, draw, and
  tabulate. Needs `build_edits.py`.

## Running

```
python3 build_population.py     # and so on, in order
```

`CC_DATA_LOCAL` overrides the path to `local-data/`.

## Thresholds

`calibrate.py` derives most thresholds from measured distributions and writes
`thresholds.json`. The burst gap is the exception: `build_cycles.py` overrides
it with a constant calibrated by `calibrate_burst_gap.py`, because the pooled
gap distribution has no feature to read a threshold from and its p90 is a
convention rather than a measurement. See `build_cycles.py`'s docstring for
why, and `burst_calibration.md` for the evidence.

## Tests

```
python3 -m unittest discover -s tests -v
```

Fixtures are synthetic. Real history entries contain student-written text and
this repository is public.
