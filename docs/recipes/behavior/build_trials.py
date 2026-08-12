#!/usr/bin/env python3
"""Detect trials: stretches where the program's INPUT was changing.

  ./build_trials.py

Task 4 established that pause length cannot separate "still working" from
"stopped to look" -- 118,054 operation gaps decay smoothly with no second peak.
A Dataflow program runs continuously, so the student sees its output while
editing; there is no edit-then-observe cycle to find.

What is detectable is the student deliberately exercising the program: dragging
the simulation slider makes the input go static -> changing -> static, and that
changing stretch is a trial. Measured on the real corpus: 405 of the 506
documents with a Simulator tile show such episodes, and 47.1% of episodes follow
a program edit within two minutes.

The signal is `sharedModel/variables/*/setValue`, which build_edits.py classifies
as `runtime` -- correctly, since it is not a student EDIT. It is, however,
exactly the student's OBSERVATION. Values are read from entry_json rather than
edits.parquet because the coalescing step there does not carry patch values.

Limit worth stating: a student working on the output side (a wave generator
feeding an output node) has an input that never stops, so no trial boundary
exists for them even in principle. This detects a bounded subpopulation, and the
size of that subpopulation is itself a finding.

Detection is per-variable, not per-document: a document can carry more than one
Simulator variable, and lagging across all of them ordered only by time makes
consecutive rows alternate between variables, so `v IS DISTINCT FROM prev_v`
fires on nearly every row even when neither variable moved. Trials are reported
per-variable rather than merged back into one trial per document, because two
variables changing in the same window are two separate student gestures, not
one -- merging them would recreate the same smearing this fix removes.
`trial_id` stays a document-unique ordinal, renumbered across variables by
start time, so its contract (one integer identifying a trial within a
document) is unchanged for downstream consumers.
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
