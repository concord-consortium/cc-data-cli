#!/usr/bin/env python3
"""Detect trials: the student driving the simulation's INPUT with its slider.

  ./build_trials.py

A Simulator tile renders a `VariableSlider` for the one variable the student is
meant to control, and releasing that slider commits a value:

    /content/sharedModelMap/{sm}/sharedModel/variables/N/commitTemporaryValue

Each such entry carries exactly one patch -- a `replace` on
`.../variables/N/value` -- so one entry is one deliberate act. Measured on the
corpus: 6,387 commits, every one of them a single-patch replace, and ZERO where
the committed value equals its own inverse patch. There are no no-op commits to
filter out, so unlike the detector this replaces there is no lag-and-compare
step; a commit IS a change.

The variable being committed is the control, confirmed by name and label:

    Target EMG     390 documents   labels []
    Potentiometer    3 documents   labels ["input","position","decimalPlaces:0"]
    (unresolved)     4 documents   the creating `add` patch predates the
                                   retained history

Target EMG carries no label because it is the student's control rather than
program plumbing; brainwaves-gripper renders it with min 40, max 440, step 40,
labelled relaxed -> flexed, and `EMG` is that value minus per-frame noise. 99.8%
of committed values land exactly on those slider steps.

WHY THIS REPLACED THE PREVIOUS DETECTOR

This script used to read `sharedModel/variables/*/setValue` and look for a
static -> changing -> static shape. That action is written by
`sendDataToSimulatedOutput()` in CLUE's
src/plugins/dataflow/nodes/live-output-node.ts, and across the whole corpus it
fires ONLY on Gripper (239,996), Heat Lamp (16,194), Humidifier (5,215), Fan
(1,722) and Simulation Mode (403) -- every one an actuator the program drives.
The actual inputs receive zero setValue calls. So the old `trial_after` meant
"the program's output moved", which a wave generator wired to an output does
continuously with no student involvement at all.

Do not be tempted back to the input's own value stream. It is written by
`/content/step` (4.77M patches across 621 documents) and never holds still --
0.0% of consecutive values are unchanged -- because the simulation adds noise
every frame. The slider is what makes detection possible.

NOT ALL SIMULATIONS HAVE A CONTROL, AND THIS REPORTS NOTHING FOR THOSE

    EMG_and_claw               529 documents   6,270 commits
    potentiometer_chip_servo     4 documents      12 commits
    terrarium                   60 documents       0 commits

terrarium is a closed loop: its Temperature and Humidity are driven by the
student's own Fan, Heat Lamp and Humidifier outputs, and it has no slider and no
onChange handler. Those 60 documents produce no rows here, and that is the
intended result rather than a gap to patch -- falling back to output movement
would quietly mix two kinds of evidence under one name. For terrarium, checking
can only be evidenced by pausing or documenting. main() prints the per-simulation
counts so the zero stays visible in every run, not just in this docstring.

The 105 commits in 16 documents with no Simulator tile in the final snapshot are
kept. The tile was added, used, and later deleted; the trial still happened.

The log route is empty: the slider's onChangeComplete fires
SIMULATOR_TOOL_CHANGE, but there are ZERO such events in the log corpus, so
history is the only source for these sessions.

--- mechanics ---

TRIAL_GAP_S is a convention, not a measurement. Gaps between consecutive commits
decay smoothly in log space -- 147 under 1s, peaking at 946 in the 8-16s bucket,
then falling away to 560 over 256s -- with no second mode to cut at. Same shape
Task 4 found in operation gaps, and for the same reason: nothing in the data
marks where one bout of testing ends. 30s is carried over from the previous
detector so the threshold is not silently changed by this rewrite.

Detection is per-variable AND per-shared-model, not per-document. A document can
carry more than one Simulator variable, and two of them here reuse index 0 under
two different shared models; grouping on the index alone would merge two
students' controls into one series. `trial_id` stays a document-unique ordinal,
renumbered by start time.
"""
import os
import sys

import lib

# Two commits further apart than this are separate trials. A student pausing
# briefly mid-flex has not started a new one. See the docstring: no natural
# break exists in the distribution, so this is a convention.
TRIAL_GAP_S = 30

# A floor, not a calibration: a healthy run finds thousands of commits, so a
# near-empty result means the patch shape changed, not that students stopped
# moving the slider.
MIN_TRIALS = 100

SQL = """
COPY (
  WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
  patch AS (
    SELECT h.doc_id, h.entry_id, h.created,
           unnest(json_extract(h.entry_json, '$.records[*].patches[*]')) AS p
    FROM read_parquet('{history}') h
    JOIN pop USING (doc_id)
    WHERE h.action LIKE '%/sharedModel/variables/%/commitTemporaryValue'
  ),
  -- The path is matched as well as the action. Reading the value off the patch
  -- rather than off the action name is what makes the shared-model id
  -- available, and it means a future entry shape that writes some other path
  -- under this action drops out here instead of being counted as a slider move.
  commits AS (
    SELECT doc_id, entry_id, created,
           regexp_extract(json_extract_string(p, '$.path'),
                          '/sharedModelMap/([^/]+)/', 1) AS sm_id,
           regexp_extract(json_extract_string(p, '$.path'),
                          '/variables/([0-9]+)/value$', 1) AS var_id
    FROM patch
    WHERE json_extract_string(p, '$.op') = 'replace'
      AND regexp_matches(json_extract_string(p, '$.path'),
                         '/sharedModel/variables/[0-9]+/value$')
  ),
  -- The flag and its running sum are separate CTEs: DuckDB refuses a window
  -- call nested inside another window call's argument.
  marked AS (
    SELECT *,
      CASE WHEN lag(created) OVER w IS NULL
             OR date_diff('second', lag(created) OVER w, created) > {gap_s}
           THEN 1 ELSE 0 END AS is_new
    FROM commits
    WINDOW w AS (PARTITION BY doc_id, sm_id, var_id ORDER BY created, entry_id)
  ),
  grouped AS (
    SELECT *,
      -- CAST to BIGINT: sum() over an INTEGER yields HUGEINT, which Parquet
      -- cannot store, so COPY silently downcasts the column to DOUBLE.
      CAST(sum(is_new) OVER (PARTITION BY doc_id, sm_id, var_id
                             ORDER BY created, entry_id
                             ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS local_trial_id
    FROM marked
  ),
  -- Trials stay per-variable rather than merged per-document: two sliders moved
  -- in the same window are two separate student gestures. `trial_id` is then
  -- renumbered doc-wide (ordered by start time) purely so it remains a stable,
  -- unique ordinal per document.
  per_var AS (
    SELECT doc_id, sm_id, var_id, local_trial_id,
           min(created) AS started,
           max(created) AS ended,
           count(*) AS n_changes,
           date_diff('millisecond', min(created), max(created)) / 1000.0 AS duration_s
    FROM grouped
    GROUP BY doc_id, sm_id, var_id, local_trial_id
  )
  SELECT doc_id,
         CAST(row_number() OVER (PARTITION BY doc_id
                                 ORDER BY started, sm_id, var_id, local_trial_id) - 1
              AS BIGINT) AS trial_id,
         var_id, started, ended, n_changes, duration_s,
         'slider' AS source
  FROM per_var
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""

# Which simulation each document ran, so main() can show that terrarium
# contributes nothing. Read from the content snapshot, which is the only place
# the simulation is named; a document whose Simulator tile was deleted before
# the snapshot falls into the '(no simulator tile)' row rather than vanishing.
BY_SIM_SQL = """
WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
tiles AS (
  SELECT c.doc_id, unnest(json_extract(c.content_json, '$.tileMap.*')) AS tile
  FROM read_parquet('{content}') c JOIN pop USING (doc_id) WHERE c.parse_ok
),
sim AS (
  SELECT doc_id, min(json_extract_string(tile, '$.content.simulation')) AS simulation
  FROM tiles
  WHERE json_extract_string(tile, '$.content.type') = 'Simulator'
  GROUP BY doc_id
),
t AS (
  SELECT doc_id, count(*) AS trials, sum(n_changes) AS moves
  FROM read_parquet('{trials}') GROUP BY doc_id
)
SELECT coalesce(s.simulation, '(no simulator tile)') AS simulation,
       count(*) AS docs,
       count(*) FILTER (WHERE t.doc_id IS NOT NULL) AS docs_with_trials,
       CAST(coalesce(sum(t.trials), 0) AS BIGINT) AS trials,
       CAST(coalesce(sum(t.moves), 0) AS BIGINT) AS moves
FROM pop LEFT JOIN sim s USING (doc_id) LEFT JOIN t USING (doc_id)
GROUP BY 1 ORDER BY trials DESC
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
    population = os.path.join(derived, "population.parquet")
    build(p["history"], population, tmp)

    row = lib.query(
        "SELECT count(*) AS trials, count(DISTINCT doc_id) AS docs, "
        # CAST: sum() over a BIGINT yields HUGEINT, which the JSON output
        # renders as a string and the %d below then refuses.
        "CAST(sum(n_changes) AS BIGINT) AS moves, "
        "count(*) FILTER (WHERE n_changes >= 3) AS substantial, "
        "median(n_changes) AS med_changes, median(duration_s) AS med_dur, "
        "quantile_cont(duration_s, 0.9) AS p90_dur "
        "FROM read_parquet('%s')" % tmp)[0]

    if row["trials"] < MIN_TRIALS:
        os.remove(tmp)
        sys.exit("only %d slider trials, below the floor of %d -- check that "
                 "commitTemporaryValue entries still carry a single replace on "
                 ".../variables/N/value" % (row["trials"], MIN_TRIALS))

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

    by_sim = lib.query(BY_SIM_SQL.format(
        population=population, content=p["content"], trials=tmp))

    os.replace(tmp, out)
    print("slider trials: %d across %d documents (%d moves, %d with >=3 moves)"
          % (row["trials"], row["docs"], row["moves"], row["substantial"]))
    print("  median %.0f moves, %.1fs; p90 duration %.1fs"
          % (row["med_changes"], row["med_dur"], row["p90_dur"]))
    if coupled["n"]:
        print("  %d of %d (%.1f%%) follow a program edit within 120s"
              % (coupled["preceded"], coupled["n"],
                 100.0 * coupled["preceded"] / coupled["n"]))
    print("by simulation:")
    for r in by_sim:
        note = "  <- no student control, so no trial signal exists" \
            if r["simulation"] == "terrarium" else ""
        print("  %-24s %4d docs, %3d with trials, %5d trials%s"
              % (r["simulation"], r["docs"], r["docs_with_trials"],
                 r["trials"], note))


if __name__ == "__main__":
    main()
