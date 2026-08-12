#!/usr/bin/env python3
"""Derive the pipeline's thresholds from measured distributions.

  ./calibrate.py

Writes thresholds.json for build_cycles.py, and calibration.md showing the
histograms each number came from -- so a later reader can see why 4 seconds
and not 10.

The load-bearing question this answers is whether pause lengths separate into
"still working" and "stopped to look". If the distribution is unimodal, the
systematic pole has no anchor and the axis has to rest on burst composition
alone. calibration.md reports that shape explicitly rather than burying it.
"""
import json
import os
import statistics
import sys

import lib

# A burst gap below the coalescing window would split one gesture in two.
MIN_BURST_GAP_S = 2.0
GAP_EDGES = [0, 1, 2, 3, 5, 8, 13, 21, 34, 60, 120, 300, 900]

# Refuse to calibrate from too little data. A full corpus run currently
# produces on the order of 100k+ operation gaps; with an empty or truncated
# edits.parquet, `_gaps_between_operations` returns [] and `choose_burst_gap`
# silently falls back to MIN_BURST_GAP_S, so an unmeasured 2.0s threshold
# would flow into thresholds.json without complaint. This is the same style
# of gate as build_population.py's population-size check.
MIN_GAP_COUNT = 1000


def histogram(values, edges):
    """Count values into len(edges)-1 bins; anything past the last edge goes
    into the final bin rather than being dropped."""
    counts = [0] * (len(edges) - 1)
    for v in values:
        placed = False
        for i in range(len(edges) - 1):
            if edges[i] <= v < edges[i + 1]:
                counts[i] += 1
                placed = True
                break
        if not placed and v >= edges[-1]:
            counts[-1] += 1
    return counts


def choose_burst_gap(gaps):
    """The p90 of observed inter-operation gaps, floored at the coalescing
    window. Most consecutive operations inside real work are close together;
    the p90 is where the tail of deliberate pauses begins."""
    if not gaps:
        return MIN_BURST_GAP_S
    ordered = sorted(gaps)
    idx = max(0, min(len(ordered) - 1, int(0.90 * len(ordered)) - 1))
    p90 = ordered[idx]
    return max(MIN_BURST_GAP_S, float(p90))


def _gaps_between_operations(edits):
    # Tiebreaker on (class, target_id, op): DuckDB gives no ordering
    # guarantee among rows sharing `started`, and plenty do -- within one
    # (doc_id, tile_id), 47,732 of 141,184 structure/parameter rows share a
    # `started` value with at least one sibling (simultaneous edits to
    # different targets). (doc_id, class, target_id, op, started) is
    # edits.parquet's actual grouping key and is verified unique, so it's a
    # real tiebreaker rather than another ambiguous column.
    rows = lib.query("""
        SELECT date_diff('millisecond', prev_ended, started) / 1000.0 AS gap_s
        FROM (
          SELECT started,
                 lag(ended) OVER (PARTITION BY doc_id, tile_id
                                  ORDER BY started, class, target_id, op) AS prev_ended
          FROM read_parquet('%s')
          WHERE class IN ('structure', 'parameter')
            AND NOT clock_suspect
        )
        WHERE prev_ended IS NOT NULL
          AND date_diff('millisecond', prev_ended, started) BETWEEN 0 AND 3600000
    """ % edits)
    return [r["gap_s"] for r in rows]


def _md_table(title, edges, counts):
    total = sum(counts) or 1
    lines = ["### %s" % title, "", "| range (s) | count | share |", "|---|---|---|"]
    for i, n in enumerate(counts):
        lines.append("| %g–%g | %d | %.1f%% |" % (edges[i], edges[i + 1], n,
                                                  100.0 * n / total))
    lines.append("")
    return lines


def calibrate(edits, derived):
    """Compute thresholds and the calibration histogram, and write both.

    Refuses (before writing anything) if there are too few gaps to calibrate
    from, and writes via a temp-file-then-replace so a later, unrelated
    failure can never leave thresholds.json or calibration.md truncated.
    """
    gaps = _gaps_between_operations(edits)

    # See MIN_GAP_COUNT's comment. Checked before any write, so a bad run
    # never touches the previous good thresholds.json/calibration.md.
    if len(gaps) < MIN_GAP_COUNT:
        sys.exit("only %d operation gaps -- far below the %d expected from a "
                 "usable corpus; check that edits.parquet is complete before "
                 "trusting any threshold derived from it"
                 % (len(gaps), MIN_GAP_COUNT))

    burst_gap = choose_burst_gap(gaps)

    # Pauses are gaps longer than the burst gap. Their shape decides whether
    # the systematic pole is detectable at all.
    pauses = [g for g in gaps if g > burst_gap]
    counts = histogram(gaps, GAP_EDGES)

    thresholds = {
        "burst_gap_s": round(burst_gap, 2),
        # A watch has to outlast the burst gap, and stop before it becomes a
        # class transition. Both ends are re-checked against the histogram
        # below; they are starting points, not conclusions.
        "watch_min_s": round(burst_gap, 2),
        "watch_max_s": 300.0,
        "ui_staleness_s": 120.0,
        "generated_from": {
            "operation_gaps": len(gaps),
            "pauses": len(pauses),
            "median_gap_s": round(statistics.median(gaps), 2) if gaps else None,
            "median_pause_s": round(statistics.median(pauses), 2) if pauses else None,
        },
    }

    lines = ["# Calibration", "",
             "Generated by `calibrate.py`. These histograms are why the "
             "thresholds are what they are.", "",
             "Operations counted: `structure` and `parameter` only, on "
             "documents that are not clock-suspect.", ""]
    lines += _md_table("Gaps between consecutive operations", GAP_EDGES, counts)
    lines += ["### Chosen thresholds", "", "```json",
              json.dumps(thresholds, indent=2), "```", "",
              "### The open risk", "",
              "If the histogram above is smooth and unimodal, pause length "
              "does not separate 'still working' from 'stopped to look', and "
              "the systematic pole has no anchor. Read the shape before "
              "trusting any candidate the pipeline emits.", ""]

    thresholds_path = os.path.join(derived, "thresholds.json")
    calibration_path = os.path.join(derived, "calibration.md")
    thresholds_tmp = thresholds_path + ".tmp"
    calibration_tmp = calibration_path + ".tmp"
    with open(thresholds_tmp, "w") as handle:
        json.dump(thresholds, handle, indent=2)
    with open(calibration_tmp, "w") as handle:
        handle.write("\n".join(lines))
    os.replace(thresholds_tmp, thresholds_path)
    os.replace(calibration_tmp, calibration_path)

    return thresholds, gaps, pauses


def main():
    derived = lib.ensure_derived()
    edits = os.path.join(derived, "edits.parquet")
    lib.require_file(edits)

    thresholds, gaps, pauses = calibrate(edits, derived)

    print("burst_gap_s=%.2f  watch=%.2f–%.0fs  (from %d gaps, %d pauses)"
          % (thresholds["burst_gap_s"], thresholds["watch_min_s"],
             thresholds["watch_max_s"], len(gaps), len(pauses)))
    print("wrote thresholds.json and calibration.md")


if __name__ == "__main__":
    main()
