#!/usr/bin/env python3
"""Calibrate the burst gap against trials, and report the evidence.

  ./calibrate_burst_gap.py

calibrate.py picks the burst gap from the p90 of the pooled gap distribution.
Task 4 established that distribution has no valley to read a threshold from --
it decays smoothly across four orders of magnitude -- so the p90 is a
convention, not a measurement. The gaps were unlabelled, and were being asked
to separate themselves.

Trials supply the missing label. A trial is a stretch where the program's input
was changing, which means the student had stopped editing and was exercising
the program. So:

  within-cycle gap   between two edits that both fall between the same pair of
                     consecutive trials. One edit-then-check cycle by
                     construction, so a burst threshold must NOT split it.
  across-cycle gap   an edit-to-edit gap that a trial falls inside. The student
                     stopped, exercised, and came back, so a threshold MUST
                     split it.

Sweeping a candidate threshold against both labelled classes gives a real
decision curve. Two further measures score the resulting segmentation rather
than the individual gaps, because the burst rule chains -- one wrongly bridged
gap welds two bursts together, and the merged burst can bridge to a third:

  swallowed   share of bursts containing a trial. The burst chained across a
              boundary the student actually drew. Rises as the gap grows.
  fragments   bursts per trial-bounded cycle. Ideal is 1. Falls as it grows.

And one ground truth, computed with no threshold involved at all: the distinct
targets changed inside a trial-bounded span ARE that cycle's composition. A
threshold whose bursts do not reproduce that distribution is mis-segmenting.

Writes `burst_calibration.md` alongside calibration.md. This reports; it does
not write thresholds.json. build_cycles.BURST_GAP_S is a stated constant, and
changing it is a decision a person makes after reading this.
"""
import os

import build_cycles
import lib

CANDIDATES = [5.0, 8.0, 12.0, 15.0, 20.0, 25.0, 32.91, 45.0]
# Gaps this long are a student returning the next day. Including them flatters
# every threshold equally and hides the differences that matter.
MAX_ACROSS_S = 3600
EDIT_CLASSES = "('structure','parameter')"

# All trials, both detectors, as one anchor. See build_cycles.py on why
# simulated-sensor trials are kept despite echoing some Simulator trials.
ANCHOR_SQL = """
CREATE OR REPLACE TEMP TABLE anchor AS
  SELECT doc_id, started, ended FROM read_parquet('{trials}')
  UNION ALL
  SELECT doc_id, started, ended FROM read_parquet('{sensor_trials}');

CREATE OR REPLACE TEMP TABLE ed AS
  SELECT doc_id, started, ended, target_id FROM read_parquet('{edits}')
  WHERE class IN %s AND NOT clock_suspect;

-- One row per consecutive pair of trials: the editing window between them.
CREATE OR REPLACE TEMP TABLE spans AS
WITH t AS (
  SELECT doc_id, started, ended,
         row_number() OVER (PARTITION BY doc_id ORDER BY started, ended) AS rn
  FROM (SELECT DISTINCT doc_id, started, ended FROM anchor)
)
SELECT a.doc_id, a.ended AS s0, b.started AS s1
FROM t a JOIN t b ON a.doc_id = b.doc_id AND b.rn = a.rn + 1
WHERE b.started > a.ended;

CREATE OR REPLACE TEMP TABLE span_edits AS
SELECT s.doc_id, s.s0, s.s1, e.started, e.ended, e.target_id
FROM spans s JOIN ed e
  ON e.doc_id = s.doc_id AND e.started >= s.s0 AND e.ended <= s.s1;

CREATE OR REPLACE TEMP TABLE within AS
SELECT date_diff('millisecond', prev_end, started) / 1000.0 AS gap_s
FROM (SELECT *, lag(ended) OVER (PARTITION BY doc_id, s0, s1
                                 ORDER BY started, ended) AS prev_end
      FROM span_edits)
WHERE prev_end IS NOT NULL;

CREATE OR REPLACE TEMP TABLE across AS
SELECT gap_s FROM (
  SELECT doc_id, prev_end, started,
         date_diff('millisecond', prev_end, started) / 1000.0 AS gap_s
  FROM (SELECT doc_id, started,
               lag(ended) OVER (PARTITION BY doc_id ORDER BY started, ended)
                 AS prev_end
        FROM ed)
  WHERE prev_end IS NOT NULL
) g
WHERE EXISTS (SELECT 1 FROM anchor a
              WHERE a.doc_id = g.doc_id
                AND a.started >= g.prev_end AND a.ended <= g.started);
""" % EDIT_CLASSES


def _pct(sql):
    return lib.scalar(sql)


def anchor_report(derived, edits):
    setup = ANCHOR_SQL.format(
        trials=os.path.join(derived, "trials.parquet"),
        sensor_trials=os.path.join(derived, "sensor_trials.parquet"),
        edits=edits)

    def q(sql):
        return lib.query(setup + sql)

    shape = q("""
      SELECT 'within a cycle' AS gap_kind, count(*) AS n,
             median(gap_s) AS p50, quantile_cont(gap_s, 0.75) AS p75,
             quantile_cont(gap_s, 0.9) AS p90 FROM within
      UNION ALL
      SELECT 'across a cycle', count(*), median(gap_s),
             quantile_cont(gap_s, 0.75), quantile_cont(gap_s, 0.9) FROM across
    """)
    scope = q("""SELECT (SELECT count(*) FROM (SELECT DISTINCT doc_id, s0, s1
                          FROM span_edits)) AS spans,
                        (SELECT count(DISTINCT doc_id) FROM span_edits) AS docs
              """)[0]
    truth = q("""
      SELECT count(*) AS spans, avg(targets) AS mean_targets,
             100.0 * count(*) FILTER (WHERE targets = 1) / count(*) AS pct_single,
             100.0 * count(*) FILTER (WHERE targets >= 3) / count(*) AS pct_3plus
      FROM (SELECT doc_id, s0, s1, count(DISTINCT target_id) AS targets,
                   count(*) AS changes
            FROM span_edits GROUP BY 1, 2, 3)
      WHERE changes >= 2
    """)[0]
    sweep = q("""
      WITH c(t) AS (VALUES %s)
      -- CAST: a bare decimal literal comes back DECIMAL, which the JSON
      -- output renders as a string and every %%f format below then rejects.
      SELECT CAST(t AS DOUBLE) AS threshold_s,
        100.0 * (SELECT count(*) FROM within WHERE gap_s <= t)
              / (SELECT count(*) FROM within) AS pct_within_kept,
        100.0 * (SELECT count(*) FROM across WHERE gap_s > t AND gap_s < %d)
              / (SELECT count(*) FROM across WHERE gap_s < %d) AS pct_across_split
      FROM c ORDER BY t
    """ % (", ".join("(%s)" % g for g in CANDIDATES), MAX_ACROSS_S, MAX_ACROSS_S))
    for row in sweep:
        row["balanced"] = (row["pct_within_kept"] + row["pct_across_split"]) / 2
    return shape, scope, truth, sweep


def segmentation_report(derived, p, thresholds, scratch):
    """Rebuild cycles at each candidate gap and score the segmentation."""
    trials = os.path.join(derived, "trials.parquet")
    sensor_trials = os.path.join(derived, "sensor_trials.parquet")
    rows = []
    for gap in CANDIDATES:
        t = dict(thresholds)
        t["burst_gap_s"] = gap
        t["watch_min_s"] = gap
        out = os.path.join(scratch, "cycles_%s.parquet" % str(gap).replace(".", "_"))
        build_cycles.build(os.path.join(derived, "edits.parquet"),
                           os.path.join(derived, "presence.parquet"),
                           trials, sensor_trials, p["logs"], t, out)
        comp = lib.query("""
            SELECT count(*) AS bursts, avg(n_distinct_targets) AS mean_targets,
              100.0 * count(*) FILTER (WHERE n_distinct_targets = 1) / count(*)
                AS pct_single,
              100.0 * count(*) FILTER (WHERE n_distinct_targets >= 3) / count(*)
                AS pct_3plus
            FROM read_parquet('%s')""" % out)[0]
        swallowed = _pct("""
            SELECT 100.0 * count(*) FILTER (WHERE EXISTS (
                     SELECT 1 FROM (SELECT doc_id, started, ended
                                    FROM read_parquet('%s')
                                    UNION ALL
                                    SELECT doc_id, started, ended
                                    FROM read_parquet('%s')) tr
                     WHERE tr.doc_id = c.doc_id
                       AND tr.started > c.burst_started
                       AND tr.ended < c.burst_ended)) / count(*)
            FROM read_parquet('%s') c""" % (trials, sensor_trials, out))
        frag = _pct("""
            WITH t AS (
              SELECT doc_id, started, ended,
                     row_number() OVER (PARTITION BY doc_id
                                        ORDER BY started, ended) AS rn
              FROM (SELECT DISTINCT doc_id, started, ended FROM (
                      SELECT doc_id, started, ended FROM read_parquet('%s')
                      UNION ALL
                      SELECT doc_id, started, ended FROM read_parquet('%s')))
            ),
            spans AS (
              SELECT a.doc_id, a.ended AS s0, b.started AS s1
              FROM t a JOIN t b ON a.doc_id = b.doc_id AND b.rn = a.rn + 1
              WHERE b.started > a.ended
            ),
            per_span AS (
              SELECT s.doc_id, s.s0, s.s1, count(*) AS bursts,
                     sum(c.n_changes) AS changes
              FROM spans s JOIN read_parquet('%s') c
                ON c.doc_id = s.doc_id
               AND c.burst_started >= s.s0 AND c.burst_ended <= s.s1
              GROUP BY 1, 2, 3
            )
            SELECT median(bursts) FROM per_span WHERE changes >= 2
            """ % (trials, sensor_trials, out))
        os.remove(out)
        rows.append((gap, comp, swallowed, frag))
    return rows


def write_report(path, shape, scope, truth, sweep, seg, current):
    out = ["# Burst-gap calibration", "",
           "Generated by `calibrate_burst_gap.py`. `build_cycles.py` currently "
           "uses **%.1fs**." % current, "",
           "## The anchor", "",
           "%d trial-bounded editing spans across %d documents. Edits inside "
           "one span are a single edit-then-check cycle by construction."
           % (scope["spans"], scope["docs"]), "",
           "| gap kind | n | p50 | p75 | p90 |", "|---|---|---|---|---|"]
    for r in shape:
        out.append("| %s | %d | %.1fs | %.1fs | %.1fs |"
                   % (r["gap_kind"], r["n"], r["p50"], r["p75"], r["p90"]))
    out += ["", "The pooled distribution these come from has no valley "
            "(`calibration.md`). Labelled by the anchor, the same gaps "
            "separate.", "",
            "## Threshold sweep", "",
            "Across-cycle gaps longer than %ds are excluded: they are "
            "next-day returns and every threshold splits them."
            % MAX_ACROSS_S, "",
            "| threshold | within-cycle kept | across-cycle split | balanced |",
            "|---|---|---|---|"]
    for r in sweep:
        out.append("| %.1fs | %.1f%% | %.1f%% | %.1f%% |"
                   % (r["threshold_s"], r["pct_within_kept"],
                      r["pct_across_split"], r["balanced"]))
    out += ["", "## Resulting segmentation", "",
            "Scored on the bursts each threshold produces, not on gaps in "
            "isolation, because the burst rule chains.", "",
            "| threshold | bursts | mean targets | 1-target | 3+ targets | "
            "swallowed a trial | bursts per cycle |",
            "|---|---|---|---|---|---|---|"]
    for gap, comp, swallowed, frag in seg:
        out.append("| %.1fs | %d | %.2f | %.1f%% | %.1f%% | %.1f%% | %.1f |"
                   % (gap, comp["bursts"], comp["mean_targets"],
                      comp["pct_single"], comp["pct_3plus"], swallowed, frag))
    out += ["", "## Ground truth", "",
            "Distinct targets changed inside a trial-bounded span, over %d "
            "spans with 2+ changes. No burst threshold is involved, so this is "
            "what a correct segmentation should reproduce:" % truth["spans"], "",
            "- mean **%.2f** distinct targets" % truth["mean_targets"],
            "- **%.1f%%** single-target, **%.1f%%** three-or-more"
            % (truth["pct_single"], truth["pct_3plus"]), "",
            "## Limits", "",
            "The anchor exists only in documents with a detectable trial, so "
            "this is calibrated on a minority of the corpus and assumed, not "
            "shown, to describe the rest. A trial-bounded span is one "
            "observation cycle but need not be one burst -- a student can edit, "
            "think, and edit again without touching the input -- which biases "
            "the within-cycle distribution long.", ""]
    with open(path, "w") as handle:
        handle.write("\n".join(out))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    edits = os.path.join(derived, "edits.parquet")
    for f in (edits, os.path.join(derived, "trials.parquet"),
              os.path.join(derived, "sensor_trials.parquet")):
        lib.require_file(f)

    import json
    with open(os.path.join(derived, "thresholds.json")) as handle:
        thresholds = json.load(handle)

    shape, scope, truth, sweep = anchor_report(derived, edits)
    seg = segmentation_report(derived, p, thresholds, derived)
    out = os.path.join(derived, "burst_calibration.md")
    write_report(out, shape, scope, truth, sweep, seg,
                 build_cycles.BURST_GAP_S)

    best = max(sweep, key=lambda r: r["balanced"])
    print("anchor: %d spans, %d documents" % (scope["spans"], scope["docs"]))
    for r in shape:
        print("  %-16s n=%-6d p50=%.1fs p90=%.1fs"
              % (r["gap_kind"], r["n"], r["p50"], r["p90"]))
    print("sweep optimum: %.1fs (balanced %.1f%%); build_cycles uses %.1fs"
          % (best["threshold_s"], best["balanced"], build_cycles.BURST_GAP_S))
    print("ground truth: %.1f%% single-target, %.1f%% 3+ targets"
          % (truth["pct_single"], truth["pct_3plus"]))
    print("wrote %s" % os.path.basename(out))


if __name__ == "__main__":
    main()
