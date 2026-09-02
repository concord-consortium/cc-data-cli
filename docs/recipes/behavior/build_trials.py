#!/usr/bin/env python3
"""Detect trials -- CURRENTLY MEASURING THE WRONG THING. See below.

  ./build_trials.py

WHAT THIS ACTUALLY DETECTS: the program's OUTPUT moving, not its input.

The signal is `sharedModel/variables/*/setValue`. Measured across the corpus,
that action is only ever called on Gripper (239,996), Heat Lamp (16,194),
Humidifier (5,215), Fan (1,722) and Simulation Mode (403) -- every one an
actuator the program drives. EMG, Surface Pressure, Target EMG, Pan
Temperature and Temperature, the actual inputs, receive ZERO setValue calls.

The writer is `sendDataToSimulatedOutput()` in CLUE's
src/plugins/dataflow/nodes/live-output-node.ts, which takes the Live Output
node's computed `nodeValue` and calls `outputVariable.setValue(val)`.

So `trial_after` currently means "after this burst, the program's simulated
output moved and then settled" -- NOT "the student varied an input". That is
weak evidence of checking, and biased: it requires a Live Output node wired to
a simulated output, and it only fires when the student's program works well
enough to propagate. A wave generator feeding an output moves it continuously
with no student involvement at all.

WHAT SHOULD REPLACE IT: the simulation's slider.

`/variables/N/value` is written by four different actions, and this reads the
wrong one:

    /content/step                    inputs   4.77M patches   621 docs
    .../variables/N/setValue         outputs    264k patches   432 docs
    .../variables/N/commitTemporaryValue  the SLIDER  6.4k     397 docs
    .../program/tickAndProcess       outputs     38k patches    51 docs

`commitTemporaryValue` is the student moving the simulation's slider.
brainwaves-gripper defines Target EMG as "the EMG set by the slider", rendered
by a VariableSlider with min 40, max 440, step 40, labelled relaxed -> flexed;
`EMG` is that value minus per-frame noise. 99.8% of committed values land
exactly on those slider steps, which confirms the attribution. 6,387 commits
across 397 documents, median 16s apart, so 4,939 distinct gestures at a 5s
threshold -- deliberate acts, not a drag stream.

Do NOT try the static->changing->static shape on the stepped input value: it
never holds still (0.0% of consecutive step values are unchanged), because the
simulation adds noise every frame. The slider makes that unnecessary.

Variables carry explicit `labels` in the recorded data -- ["input",
"sensor:emg-reading", ...] versus ["output", "live-output:Fan", ...] -- so
input and output are DECLARED, not inferred from names. A replacement should
read those rather than hard-code a variable list. Target EMG is labelled
neither, because it is the student's control rather than program plumbing.

NOT ALL SIMULATIONS HAVE A CONTROL. Three exist:

    brainwaves-gripper   VariableSlider on Target EMG     1,705 episodes
    potentiometer-servo  VariableSlider on Potentiometer      3 episodes
    terrarium            NO student control               132 episodes

terrarium is a closed loop: its Temperature and Humidity are driven by the
student's own Fan, Heat Lamp and Humidifier outputs, and it has no slider and
no onChange handler. For those 132 episodes no input signal exists even in
principle, so checking can only be evidenced by pausing or documenting. A
replacement should report nothing there rather than fall back to output
movement, which would quietly mix two kinds of evidence under one name -- the
mistake this docstring exists to record.

The log route is empty: the slider's onChangeComplete fires
SIMULATOR_TOOL_CHANGE, but there are ZERO such events in the log corpus, so
history is the only source for these sessions.

--- original notes, still accurate about the mechanics ---

Task 4 established that pause length cannot separate "still working" from
"stopped to look" -- 118,054 operation gaps decay smoothly with no second peak.
A Dataflow program runs continuously, so the student sees its output while
editing; there is no edit-then-observe cycle to find.

Values are read from entry_json rather than edits.parquet because the
coalescing step there does not carry patch values.

Detection is per-variable, not per-document: a document can carry more than one
Simulator variable, and lagging across all of them ordered only by time makes
consecutive rows alternate between variables, so `v IS DISTINCT FROM prev_v`
fires on nearly every row even when neither variable moved. `trial_id` stays a
document-unique ordinal, renumbered across variables by start time.
"""
import os

import lib

# Two changes further apart than this are separate trials. A student pausing
# briefly mid-flex has not started a new one.
TRIAL_GAP_S = 30

SQL = """
COPY (
  WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
  -- A document can carry more than one Simulator variable (e.g. two sliders).
  -- var_id keeps each variable's own static->changing->static shape separate;
  -- without it, `seq` below lags across whichever variable happened to write
  -- next, so two variables being written in the same window alternate rows
  -- and look like constant change even when neither variable actually moved.
  raw AS (
    SELECT h.doc_id, h.entry_id, h.created,
      regexp_extract(h.action, '/variables/([0-9]+)/', 1) AS var_id,
      CAST(unnest(json_extract(h.entry_json, '$.records[*].patches[*].value'))
           AS VARCHAR) AS v
    FROM read_parquet('{history}') h
    JOIN pop USING (doc_id)
    WHERE h.action LIKE '%/sharedModel/variables/%/setValue'
  ),
  seq AS (
    SELECT doc_id, var_id, entry_id, created, v,
           lag(v) OVER (PARTITION BY doc_id, var_id
                        ORDER BY created, entry_id) AS prev_v
    FROM raw
  ),
  -- Only actual changes count. The slider sitting still rewrites the same value
  -- repeatedly, and that is not a trial.
  chg AS (
    SELECT doc_id, var_id, entry_id, created FROM seq
    WHERE prev_v IS NOT NULL AND v IS DISTINCT FROM prev_v
  ),
  marked AS (
    SELECT *,
      CASE WHEN date_diff('second',
             lag(created) OVER (PARTITION BY doc_id, var_id
                                ORDER BY created, entry_id),
             created) > {gap_s}
        OR lag(created) OVER (PARTITION BY doc_id, var_id
                              ORDER BY created, entry_id) IS NULL
      THEN 1 ELSE 0 END AS is_new
    FROM chg
  ),
  grouped AS (
    SELECT *,
      -- CAST to BIGINT: sum() over an INTEGER yields HUGEINT, which Parquet
      -- cannot store, so COPY silently downcasts the column to DOUBLE.
      CAST(sum(is_new) OVER (PARTITION BY doc_id, var_id
                             ORDER BY created, entry_id
                             ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS local_trial_id
    FROM marked
  ),
  -- Trials stay per-variable rather than merged per-document: two sliders
  -- moving in the same window are two separate student gestures, and folding
  -- them into one trial would recreate the exact cross-variable smearing this
  -- fix removes. `trial_id` is then renumbered doc-wide (ordered by start
  -- time) purely so it remains a stable, unique ordinal per document, as it
  -- was before this document could carry more than one variable.
  per_var AS (
    SELECT doc_id, var_id, local_trial_id,
           min(created) AS started,
           max(created) AS ended,
           count(*) AS n_changes,
           date_diff('millisecond', min(created), max(created)) / 1000.0 AS duration_s
    FROM grouped
    GROUP BY doc_id, var_id, local_trial_id
  )
  SELECT doc_id,
         CAST(row_number() OVER (PARTITION BY doc_id
                                 ORDER BY started, var_id, local_trial_id) - 1
              AS BIGINT) AS trial_id,
         var_id, started, ended, n_changes, duration_s,
         'simulation' AS source
  FROM per_var
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(history, population, out):
    lib.require_file(history)
    lib.require_file(population)
    lib.run_sql(SQL.format(history=history, population=population, out=out,
                           gap_s=TRIAL_GAP_S))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    out = os.path.join(derived, "trials.parquet")
    # Write to a temp path first -- see docs/recipes/README.md's "two things
    # that will bite" -- so a crash mid-COPY cannot leave trials.parquet
    # truncated or clobber a good one.
    tmp = out + ".tmp"
    build(p["history"], os.path.join(derived, "population.parquet"), tmp)

    row = lib.query(
        "SELECT count(*) AS trials, count(DISTINCT doc_id) AS docs, "
        "count(*) FILTER (WHERE n_changes >= 3) AS substantial, "
        "median(n_changes) AS med_changes, median(duration_s) AS med_dur, "
        "quantile_cont(duration_s, 0.9) AS p90_dur "
        "FROM read_parquet('%s')" % tmp)[0]

    # How many trials follow a program edit? This is the reconstructed
    # "set it up, then trial it" cycle, and its rate is a headline number for
    # the friction argument in Task 8.
    coupled = lib.query("""
        SELECT count(*) AS n,
          count(*) FILTER (WHERE EXISTS (
            SELECT 1 FROM read_parquet('%s') e
            WHERE e.doc_id = t.doc_id
              AND e.class IN ('structure','parameter')
              AND e.ended <= t.started
              AND date_diff('second', e.ended, t.started) <= 120)) AS preceded
        FROM read_parquet('%s') t
    """ % (os.path.join(derived, "edits.parquet"), tmp))[0]

    os.replace(tmp, out)
    print("trials: %d across %d documents (%d with >=3 changes)"
          % (row["trials"], row["docs"], row["substantial"]))
    print("  median %.0f changes, %.1fs; p90 duration %.1fs"
          % (row["med_changes"], row["med_dur"], row["p90_dur"]))
    if coupled["n"]:
        print("  %d of %d (%.1f%%) follow a program edit within 120s"
              % (coupled["preceded"], coupled["n"],
                 100.0 * coupled["preceded"] / coupled["n"]))


if __name__ == "__main__":
    main()
