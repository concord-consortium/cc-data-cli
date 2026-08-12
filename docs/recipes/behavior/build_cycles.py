#!/usr/bin/env python3
"""Segment the edit stream into bursts and characterise each one.

  ./build_cycles.py

A cycle is one edit burst plus what followed it. The axis rests on burst
COMPOSITION -- how many distinct things changed before the student stopped --
because Task 4 established that pause length carries no signal: 119,057
operation gaps decay smoothly with no second peak, so there is nothing to
threshold. That is closer to CLUE-575's own wording anyway, which defines
trial and error as changing blocks rapidly "without systematicity (one change
at a time)".

Two channels sit alongside composition:
  trials    did the student exercise the program after this burst? (Task 5)
  pause     what the three presence channels say about the following gap

Pause classification joins ticks, log events, and carried-forward UI state.
Documents with neither sessions nor ticks get `no_presence_data`, never
`absent`: 1,939 of 2,677 documents have no ticks, and reading that as "the
student left" would be inventing a finding from missing data.

The burst gap is 5s, not thresholds.json's calibrated 32.56s p90. A 32.56s gap
merges most of a session into single bursts (17,503 bursts corpus-wide, mean
2.63 distinct targets) and destroys the composition signal; 5s keeps bursts at
gesture scale (54,406 bursts, 67.1% single-target). Task 4 established there is
no data-driven way to choose, so this is a judgement call and Task 8 says so.
"""
import json
import os

import lib

# A trial starting within this long of a burst ending is treated as that
# burst's trial.
TRIAL_WINDOW_S = 120
# Composition needs gesture-scale bursts; see the docstring.
BURST_GAP_S = 5.0

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
      CASE WHEN date_diff('millisecond',
             lag(ended) OVER (PARTITION BY doc_id, tile_id ORDER BY started),
             started) <= {burst_gap_ms} THEN 0 ELSE 1 END AS is_new
    FROM changes
  ),
  grouped AS (
    SELECT *,
      CAST(sum(is_new) OVER (PARTITION BY doc_id, tile_id ORDER BY started
                             ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS cycle_id,
      sum(CASE WHEN subtype = 'node' AND op = 'add' THEN 1
               WHEN subtype = 'node' AND op = 'remove' THEN -1
               ELSE 0 END)
        OVER (PARTITION BY doc_id, tile_id ORDER BY started
              ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS n_nodes_running,
      sum(CASE WHEN subtype = 'connection' AND op = 'add' THEN 1
               WHEN subtype = 'connection' AND op = 'remove' THEN -1
               ELSE 0 END)
        OVER (PARTITION BY doc_id, tile_id ORDER BY started
              ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS n_conns_running
    FROM marked
  ),
  bursts AS (
    SELECT doc_id, any_value(uid) AS uid, any_value(unit) AS unit,
           any_value(problem) AS problem, tile_id, cycle_id,
           min(started) AS burst_started,
           max(ended) AS burst_ended,
           count(*) AS n_changes,
           count(DISTINCT target_id) AS n_distinct_targets,
           list_distinct(list(class)) AS classes,
           -- True iff some ONE target was both added and removed in this burst.
           -- |A| + |R| > |A union R| exactly when A and R intersect. Testing
           -- bool_or(add) AND bool_or(remove) instead would fire when one target
           -- was added and a DIFFERENT one removed, which is ordinary editing.
           (count(DISTINCT CASE WHEN op = 'add' THEN target_id END)
            + count(DISTINCT CASE WHEN op = 'remove' THEN target_id END)
            > count(DISTINCT CASE WHEN op IN ('add', 'remove') THEN target_id END)
           ) AS oscillation,
           arg_max(n_nodes_running, started) AS n_nodes_after,
           arg_max(n_conns_running, started) AS n_connections_after,
           arg_min(first_entry_id, started) AS first_entry_id
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
      lead(burst_started) OVER (PARTITION BY doc_id, tile_id ORDER BY burst_started)
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
  tr AS (SELECT doc_id, started, n_changes FROM read_parquet('{trials}')),
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
    first_entry_id
  FROM annotated
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(edits, presence, trials, logs, thresholds, out):
    for f in (edits, presence, trials, logs):
        lib.require_file(f)
    lib.run_sql(SQL.format(
        edits=edits, presence=presence, trials=trials, logs=logs, out=out,
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
    out = os.path.join(derived, "cycles.parquet")
    build(os.path.join(derived, "edits.parquet"),
          os.path.join(derived, "presence.parquet"),
          os.path.join(derived, "trials.parquet"),
          p["logs"], thresholds, out)

    rows = lib.query(
        "SELECT pause_type, count(*) AS n FROM read_parquet('%s') "
        "GROUP BY pause_type ORDER BY n DESC" % out)
    total = sum(r["n"] for r in rows)
    print("cycles: %d (burst gap %.1fs)" % (total, BURST_GAP_S))
    for r in rows:
        print("  %-18s %8d  %5.1f%%" % (r["pause_type"], r["n"],
                                        100.0 * r["n"] / total))

    comp = lib.query(
        "SELECT count(*) FILTER (WHERE n_distinct_targets = 1) AS single, "
        "count(*) FILTER (WHERE n_distinct_targets >= 3) AS many, "
        "count(*) FILTER (WHERE oscillation) AS osc, "
        "count(*) FILTER (WHERE trial_after) AS trialed, "
        "count(*) AS n FROM read_parquet('%s')" % out)[0]
    print("composition: %.1f%% single-target, %.1f%% 3+ targets, "
          "%.1f%% oscillating, %.1f%% followed by a trial"
          % (100.0 * comp["single"] / comp["n"], 100.0 * comp["many"] / comp["n"],
             100.0 * comp["osc"] / comp["n"], 100.0 * comp["trialed"] / comp["n"]))


if __name__ == "__main__":
    main()
