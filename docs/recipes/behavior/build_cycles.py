#!/usr/bin/env python3
"""Segment the edit stream into bursts and characterise each one.

  ./build_cycles.py

A cycle is one edit burst plus what followed it. The axis rests on burst
COMPOSITION -- how many distinct things changed before the student stopped --
because Task 4 established that pause length carries no signal: 118,054
operation gaps decay smoothly with no second peak, so there is nothing to
threshold. That is closer to CLUE-575's own wording anyway, which defines
trial and error as changing blocks rapidly "without systematicity (one change
at a time)".

Three channels sit alongside the target count:
  oscillation  did the student put something in and take it back out?
  trials       did the student exercise the program after this burst? (Task 5)
  pause        what the three presence channels say about the following gap

`oscillation` is true when one target was both added and removed inside the
same burst -- the student adding a node, then deleting it again, which is
trial and error whatever the target count says. It is computed as
|A| + |R| > |A union R|, which holds exactly when the added and removed sets
intersect; `bool_or(add) AND bool_or(remove)` would instead fire when one
target was added and a DIFFERENT one removed, which is ordinary editing.

Two things about it are worth knowing, because build_candidates.py tests it
FIRST and it therefore decides more trial-and-error labels than the target
count does.

It only sees structural add/remove. The design document also calls a parameter
returned to a prior value oscillation -- setting a value to 5, then 9, then 5
again -- and that half is NOT implemented. Parameter edits are 46,290 `replace`
operations carrying no before/after value in edits.parquet, so detecting a
revisit would mean re-reading patch values from history. A student who
oscillates only on parameters is invisible to this flag.

That re-read has since been shown to be cheap. `replace` patches do carry the
NEW value -- what they lack is the old one -- so the sequence of values a
parameter took is recoverable directly from history, which is enough to spot a
return to an earlier one. Worked example: episode ep002590 is labelled
`systematic`, and its parameter sequence is Less Than, Greater Than, Less Than,
Equal, Less Than on a single Logic node. Each cycle touched one target and
ended in a watching pause, so every rule here votes systematic, while the value
sequence is the design's parameter oscillation exactly. Closing this gap would
move labels, not just add a flag.

It is not, however, an artifact of the burst gap, which was the obvious worry
when the gap moved from 5s to 25s. The raw rate does climb with the gap (22.4%
of cycles at 5s, 40.4% at 25s), but almost all of that increase is overlap with
the 3+-targets rule: the share of cycles oscillation labels that nothing else
would have caught is 18.4%, 19.3% and 19.5% at 5s, 12s and 25s. Its independent
contribution is stable.

Pause classification joins ticks, log events, and carried-forward UI state.
Documents with neither sessions nor ticks get `no_presence_data`, never
`absent`: 1,939 of 2,677 documents have no ticks, and reading that as "the
student left" would be inventing a finding from missing data.

The burst gap is 25s, calibrated against trials as an external anchor rather
than chosen. Task 4 concluded no data-driven choice was possible, having looked
for a valley in the pooled gap distribution and found none; the gaps were
unlabelled, so they were being asked to separate themselves. Trials supply the
labels. Edits falling between two consecutive trials are one edit-then-check
cycle by construction -- the student exercised the program, edited, exercised it
again -- so every gap inside that span is a within-cycle gap, and a gap that a
trial falls inside is an across-cycle gap. Measured over 1,364 such spans in
303 documents: within-cycle gaps have a median of 4.2s, across-cycle gaps
98.4s. Sweeping the threshold against both labelled classes puts the optimum on
a flat plateau from 15s to 25s.

The earlier 5s value was a mistake, and the anchor shows it in two ways. A
trial-bounded cycle was cut into a median of THREE bursts, and the composition
it produced (67.1% single-target, 9.6% three-or-more) is close to the inverse
of what the spans themselves show (20.4% single-target, 46.9% three-or-more,
measured with no threshold involved). The 67.1% was fragmentation, not gesture
scale. The failure mode feared at the long end -- bursts chaining across a
boundary the student actually drew -- barely occurs: at 33s only 1.6% of bursts
contain a trial.

25s sits at the top of the plateau and is the shortest gap that reproduces the
anchor's fragmentation (median one burst per trial-bounded cycle). It remains
an override of thresholds.json's 32.91s p90, but a narrower one, and for a
stated reason rather than a hunch.

Every number above comes from `calibrate_burst_gap.py`, which writes
`burst_calibration.md`. Re-run it rather than trusting this paragraph.

`watch_min_s` moves with the burst gap, in main(). A pause must outlast
`watch_min_s` before it can be classified `watching`, and a pause shorter than
the burst gap cannot exist -- it would have been absorbed into the burst -- so
the two are one setting, not two.
"""
import json
import os

import lib

# A trial starting within this long of a burst ending is treated as that
# burst's trial.
TRIAL_WINDOW_S = 120
# Anchored on trial-bounded cycles; see the docstring.
BURST_GAP_S = 25.0

SQL = """
COPY (
  WITH e AS (
    SELECT * FROM read_parquet('{edits}')
    WHERE NOT clock_suspect
      AND class IN ('structure', 'parameter', 'documentation', 'tile', 'undo')
  ),
  changes AS (
    SELECT * FROM e WHERE class IN ('structure', 'parameter')
  ),
  marked AS (
    SELECT *,
      -- `(class, target_id, op)` breaks ties among rows sharing `started`:
      -- DuckDB gives no ordering guarantee among tied rows, so without a
      -- tiebreaker `is_new` (and everything downstream) can differ between
      -- runs. `first_entry_id` alone is NOT enough here -- it can repeat
      -- across different (class, target_id, op) rows that were coalesced
      -- from the same originating history entry (measured: 47,732 of
      -- 141,184 `changes` rows share a `started` value with at least one
      -- other row in the same (doc_id, tile_id) partition) -- but
      -- (doc_id, class, target_id, op, started) is edits.parquet's actual
      -- grouping key and is verified unique.
      CASE WHEN date_diff('millisecond',
             lag(ended) OVER (PARTITION BY doc_id, tile_id
                              ORDER BY started, class, target_id, op),
             started) <= {burst_gap_ms} THEN 0 ELSE 1 END AS is_new
    FROM changes
  ),
  grouped AS (
    SELECT *,
      CAST(sum(is_new) OVER (PARTITION BY doc_id, tile_id
                             ORDER BY started, class, target_id, op
                             ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS cycle_id,
      sum(CASE WHEN subtype = 'node' AND op = 'add' THEN 1
               WHEN subtype = 'node' AND op = 'remove' THEN -1
               ELSE 0 END)
        OVER (PARTITION BY doc_id, tile_id ORDER BY started, class, target_id, op
              ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS n_nodes_running,
      sum(CASE WHEN subtype = 'connection' AND op = 'add' THEN 1
               WHEN subtype = 'connection' AND op = 'remove' THEN -1
               ELSE 0 END)
        OVER (PARTITION BY doc_id, tile_id ORDER BY started, class, target_id, op
              ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS n_conns_running,
      -- Deterministic ordinal within (doc_id, tile_id), used below instead of
      -- `started` as the arg_min/arg_max sort key: two rows can share
      -- `started`, and arg_min/arg_max break ties among equal keys
      -- arbitrarily, which would let a burst's `first_entry_id` -- the
      -- replay URL a researcher opens -- change between runs.
      row_number() OVER (PARTITION BY doc_id, tile_id
                         ORDER BY started, class, target_id, op) AS seq
    FROM marked
  ),
  bursts AS (
    SELECT doc_id, any_value(uid) AS uid, any_value(unit) AS unit,
           any_value(problem) AS problem, tile_id, cycle_id,
           min(started) AS burst_started,
           max(ended) AS burst_ended,
           count(*) AS n_changes,
           count(DISTINCT target_id) AS n_distinct_targets,
           -- list_sort, not just list_distinct: list()/list_distinct() give
           -- no guarantee about the ORDER elements land in within one
           -- group, which is a second, independent source of nondeterminism
           -- from the row-ordering ties fixed above -- an aggregate, not a
           -- window function, so the (started, class, target_id, op)
           -- tiebreaker doesn't reach it.
           list_sort(list_distinct(list(class))) AS classes,
           -- True iff some ONE target was both added and removed in this burst.
           -- |A| + |R| > |A union R| exactly when A and R intersect. Testing
           -- bool_or(add) AND bool_or(remove) instead would fire when one target
           -- was added and a DIFFERENT one removed, which is ordinary editing.
           (count(DISTINCT CASE WHEN op = 'add' THEN target_id END)
            + count(DISTINCT CASE WHEN op = 'remove' THEN target_id END)
            > count(DISTINCT CASE WHEN op IN ('add', 'remove') THEN target_id END)
           ) AS oscillation,
           arg_max(n_nodes_running, seq) AS n_nodes_after,
           arg_max(n_conns_running, seq) AS n_connections_after,
           arg_min(first_entry_id, seq) AS first_entry_id,
           -- The last edit of the burst, by the same deterministic
           -- ordinal. An episode's end marker is built from this, so it
           -- must not wobble between runs any more than first_entry_id may.
           arg_max(first_entry_id, seq) AS last_entry_id
    FROM grouped
    GROUP BY doc_id, tile_id, cycle_id
  ),
  with_undo AS (
    SELECT b.*,
      (SELECT count(*) FROM read_parquet('{edits}') u
        WHERE u.doc_id = b.doc_id AND u.tile_id = b.tile_id
          AND u.class = 'undo'
          AND u.started >= b.burst_started
          AND u.started <= b.burst_ended) AS undo_in_burst
    FROM bursts b
  ),
  paced AS (
    SELECT *,
      lead(burst_started) OVER (PARTITION BY doc_id, tile_id
                                ORDER BY burst_started, cycle_id)
        AS next_burst_started
    FROM with_undo
  ),
  timed AS (
    SELECT *,
      CASE WHEN next_burst_started IS NULL THEN NULL
           ELSE date_diff('millisecond', burst_ended, next_burst_started) / 1000.0
      END AS pause_after_s
    FROM paced
  ),
  lg AS (
    SELECT doc_key AS doc_id, session, event,
           (event_time AT TIME ZONE 'UTC') AS t,
           json_extract_string(extras, '$.navTabsOpen') AS nav_open
    FROM read_parquet('{logs}')
    WHERE doc_key IS NOT NULL
  ),
  sess AS (
    SELECT doc_id, session, min(t) AS s_start, max(t) AS s_end
    FROM lg GROUP BY doc_id, session
  ),
  has_sess AS (SELECT DISTINCT doc_id FROM sess),
  pres AS (SELECT doc_id, started, ended FROM read_parquet('{presence}')),
  has_pres AS (SELECT DISTINCT doc_id FROM pres),
  -- Both trial detectors count. build_trials.py sees a Simulator variable
  -- moving under the mouse; build_sensor_trials.py sees a sensor's readings
  -- move, which for a physically-bound sensor is a gesture the first detector
  -- cannot observe at all. Simulated-sensor trials are kept too: where a
  -- Simulation tile drives a Sensor node, only 12 of those 33 documents also
  -- appear in trials.parquet, so dropping them would discard 21 documents'
  -- worth of evidence to avoid double-counting in 12. The double-count is
  -- real but harmless here -- `trial_after` is a boolean, and `trial_changes`
  -- takes a max() rather than a sum(), so an echoed gesture cannot inflate it.
  tr AS (
    SELECT doc_id, started, n_changes FROM read_parquet('{trials}')
    UNION ALL
    SELECT doc_id, started, n_ticks AS n_changes
    FROM read_parquet('{sensor_trials}')
  ),
  annotated AS (
    SELECT t.*,
      (t.doc_id IN (SELECT doc_id FROM has_sess)) AS logs_available,
      (t.doc_id IN (SELECT doc_id FROM has_pres)) AS ticks_available,
      EXISTS (SELECT 1 FROM sess s
              WHERE s.doc_id = t.doc_id
                AND s.s_start <= t.burst_ended
                AND s.s_end >= t.next_burst_started) AS in_session,
      EXISTS (SELECT 1 FROM pres p
              WHERE p.doc_id = t.doc_id
                AND p.started <= t.burst_ended
                AND p.ended >= t.next_burst_started) AS ticks_cover,
      coalesce((SELECT max(tr.n_changes) FROM tr
                WHERE tr.doc_id = t.doc_id
                  AND tr.started >= t.burst_ended
                  AND date_diff('second', t.burst_ended, tr.started)
                      <= {trial_window_s}), 0) AS trial_changes,
      (SELECT count(*) FROM lg l
        WHERE l.doc_id = t.doc_id
          AND l.t > t.burst_ended AND l.t < t.next_burst_started) AS n_log_events,
      (SELECT count(*) FROM lg l
        WHERE l.doc_id = t.doc_id
          AND l.t > t.burst_ended AND l.t < t.next_burst_started
          AND l.event IN ('TEXT_TOOL_CHANGE', 'TABLE_TOOL_CHANGE',
                          'DRAWING_TOOL_CHANGE')) AS n_doc_events,
      (SELECT count(*) FROM lg l
        WHERE l.doc_id = t.doc_id
          AND l.t > t.burst_ended AND l.t < t.next_burst_started
          AND l.event IN ('SHOW_TAB_SECTION', 'SHOW_TAB', 'VIEW_SHOW_DOCUMENT',
                          'SHOW_WORK', 'VIEW_SHOW_COMPARISON_DOCUMENT')) AS n_nav_events,
      (SELECT arg_max(l.nav_open, l.t) FROM lg l
        WHERE l.doc_id = t.doc_id
          AND l.t <= t.burst_ended
          AND date_diff('second', l.t, t.burst_ended) <= {staleness_s}
      ) AS nav_open
    FROM timed t
  )
  SELECT
    doc_id, uid, unit, problem, tile_id, cycle_id,
    burst_started, burst_ended, n_changes, n_distinct_targets, classes,
    oscillation, undo_in_burst,
    n_nodes_after, n_connections_after,
    n_doc_events AS doc_ops_in_pause,
    pause_after_s,
    CASE
      WHEN pause_after_s IS NULL THEN 'unknown'
      WHEN pause_after_s > {watch_max_s} THEN 'long'
      WHEN n_doc_events > 0 THEN 'documenting'
      WHEN n_nav_events > 0 THEN 'reading'
      WHEN logs_available AND NOT in_session THEN 'absent'
      WHEN NOT logs_available AND ticks_available AND NOT ticks_cover THEN 'absent'
      WHEN NOT logs_available AND NOT ticks_available THEN 'no_presence_data'
      WHEN n_log_events > 0 THEN 'present_unknown'
      WHEN nav_open IS DISTINCT FROM 'false' THEN 'present_unknown'
      WHEN pause_after_s < {watch_min_s} THEN 'present_unknown'
      WHEN ticks_cover THEN 'watching'
      ELSE 'watching_weak'
    END AS pause_type,
    (trial_changes > 0) AS trial_after,
    trial_changes,
    logs_available, ticks_available, in_session, ticks_cover,
    first_entry_id,
    last_entry_id
  FROM annotated
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(edits, presence, trials, sensor_trials, logs, thresholds, out):
    for f in (edits, presence, trials, sensor_trials, logs):
        lib.require_file(f)
    lib.run_sql(SQL.format(
        edits=edits, presence=presence, trials=trials,
        sensor_trials=sensor_trials, logs=logs, out=out,
        burst_gap_ms=int(thresholds.get("burst_gap_s", BURST_GAP_S) * 1000),
        watch_min_s=thresholds["watch_min_s"],
        watch_max_s=thresholds["watch_max_s"],
        staleness_s=thresholds["ui_staleness_s"],
        trial_window_s=TRIAL_WINDOW_S))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    with open(os.path.join(derived, "thresholds.json")) as handle:
        thresholds = json.load(handle)
    # Deliberately override the calibrated p90: see the module docstring.
    thresholds["burst_gap_s"] = BURST_GAP_S
    # watch_min_s must move with burst_gap_s: the CASE below requires a pause
    # to outlast watch_min_s before it can be called `watching`, and if
    # watch_min_s is left at the calibrated ~32s while burst_gap_s drops to
    # 5s, every pause between 5s and ~32s is forced to `present_unknown`,
    # silently suppressing the `watching` signal that single-target cycles
    # need to be called `systematic`. The two thresholds are not independent
    # and must be set together.
    thresholds["watch_min_s"] = BURST_GAP_S
    out = os.path.join(derived, "cycles.parquet")
    # Write to a temp path first -- see docs/recipes/README.md's "two things
    # that will bite" -- so a crash mid-COPY cannot leave cycles.parquet
    # truncated or clobber a good one.
    tmp = out + ".tmp"
    build(os.path.join(derived, "edits.parquet"),
          os.path.join(derived, "presence.parquet"),
          os.path.join(derived, "trials.parquet"),
          os.path.join(derived, "sensor_trials.parquet"),
          p["logs"], thresholds, tmp)

    rows = lib.query(
        "SELECT pause_type, count(*) AS n FROM read_parquet('%s') "
        "GROUP BY pause_type ORDER BY n DESC" % tmp)
    total = sum(r["n"] for r in rows)

    comp = lib.query(
        "SELECT count(*) FILTER (WHERE n_distinct_targets = 1) AS single, "
        "count(*) FILTER (WHERE n_distinct_targets >= 3) AS many, "
        "count(*) FILTER (WHERE oscillation) AS osc, "
        "count(*) FILTER (WHERE trial_after) AS trialed, "
        "count(*) AS n FROM read_parquet('%s')" % tmp)[0]

    os.replace(tmp, out)
    print("cycles: %d (burst gap %.1fs)" % (total, BURST_GAP_S))
    for r in rows:
        print("  %-18s %8d  %5.1f%%" % (r["pause_type"], r["n"],
                                        100.0 * r["n"] / total))
    print("composition: %.1f%% single-target, %.1f%% 3+ targets, "
          "%.1f%% oscillating, %.1f%% followed by a trial"
          % (100.0 * comp["single"] / comp["n"], 100.0 * comp["many"] / comp["n"],
             100.0 * comp["osc"] / comp["n"], 100.0 * comp["trialed"] / comp["n"]))


if __name__ == "__main__":
    main()
