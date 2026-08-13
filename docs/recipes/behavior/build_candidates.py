#!/usr/bin/env python3
"""Rank episodes along the axis and write a review sheet you can verify.

  ./build_candidates.py

An episode is a maximal run of consecutive same-kind cycles. A document yields
a sequence of episodes, not a label -- the same student can work one way and
then the other in the same document.

Sampling is stratified rather than top-ranked. Ranking purely by strength shows
only the clearest cases and teaches nothing about the boundary, so the sheet
carries strong examples of each pole, a band straddling the threshold, and some
unclassified cycles as a check that the phenomenon is not being missed.
"""
import os
from datetime import datetime

import lib
# The burst gap these cycles were built at, recorded in the review sheet
# because composition rates move with it. Imported rather than restated: two
# copies of the same constant drift, and the sheet would then report a gap the
# cycles were not built at.
from build_cycles import BURST_GAP_S

CLUE_BASE = os.environ.get(
    "CC_CLUE_BASE", "https://collaborative-learning.concord.org/branch/master/")

# A trial-and-error burst touches at least this many distinct targets.
TE_TARGETS = 3
STRONG_CYCLES = 4      # episodes at least this long are "strong"
BOUNDARY_CYCLES = 2    # episodes this short sit on the boundary
PER_STRATUM = 15
MAX_GAP_CYCLES = 1     # an episode survives one unclassified cycle in the middle

CANDIDATE_COLUMNS = {
    "episode_id": "VARCHAR", "doc_id": "VARCHAR", "doc_key": "VARCHAR",
    "uid": "VARCHAR", "unit": "VARCHAR", "problem": "VARCHAR",
    "kind": "VARCHAR", "n_cycles": "BIGINT", "purity": "DOUBLE",
    "span_s": "DOUBLE", "started": "TIMESTAMP", "ended": "TIMESTAMP",
    "first_entry_id": "VARCHAR", "replay_url": "VARCHAR", "stratum": "VARCHAR",
}


def classify_cycle(row):
    """Label one cycle, or decline to. Most cycles decline.

    Composition carries the axis, because Task 4 established that pause length
    does not separate: 118,054 gaps decay smoothly with no second peak. This is
    also closer to CLUE-575, which defines trial and error as changing blocks
    rapidly "without systematicity (one change at a time)".

    Oscillation and undo are tested before the target count because they are
    direct evidence rather than a proxy: putting something in and taking it
    back out is trial and error even when only one target was touched, which
    the count alone would read as systematic. That ordering makes oscillation
    the most common route to a trial-and-error label -- see build_cycles.py on
    what it does and does not detect.
    """
    targets = row.get("n_distinct_targets") or 0
    if row.get("oscillation") or (row.get("undo_in_burst") or 0) > 0:
        return "trial_and_error"
    if targets >= TE_TARGETS:
        return "trial_and_error"
    # One change, then evidence the student checked it -- either by exercising
    # the program, by watching it afterward (the presence data covers the
    # pause with no other activity), or by writing something down. Composition
    # alone is too weak to call systematic: a single-target burst may just be
    # an interrupted one.
    if targets == 1 and (row.get("trial_after")
                         or row.get("pause_type") in ("documenting", "watching")):
        return "systematic"
    return "unclassified"


def replay_url(doc_key, entry_id):
    return "%s?studentDocument=%s&studentDocumentHistoryId=%s" % (
        CLUE_BASE, doc_key, entry_id)


def _episodes(cycles_path):
    """Fold cycles into same-kind runs, tolerating one unclassified cycle in
    the middle.

    The tolerance is what makes `purity` mean anything. Maximal *strictly*
    same-kind runs are 100% pure by construction, so the number would carry no
    information -- and real episodes would fragment on a single ambiguous
    pause, which is common given how weak the tick signal is.
    """
    rows = lib.query(
        "SELECT doc_id, uid, unit, problem, tile_id, cycle_id, burst_started, "
        "n_changes, n_distinct_targets, pause_type, oscillation, undo_in_burst, "
        "trial_after, trial_changes, pause_after_s, n_nodes_after, "
        "first_entry_id FROM read_parquet('%s') "
        "ORDER BY doc_id, tile_id, burst_started" % cycles_path)

    episodes = []
    current = None

    def close(ep):
        if not ep:
            return
        # Trailing tolerated cycles are not part of the episode.
        ep["n_cycles"] -= ep.pop("pending")
        ep["purity"] = ep["n_kind"] / ep["n_cycles"]
        ep["span_s"] = (datetime.fromisoformat(ep["ended"])
                        - datetime.fromisoformat(ep["started"])).total_seconds()
        ep.pop("key")
        ep.pop("n_kind")
        episodes.append(ep)

    for row in rows:
        kind = classify_cycle(row)
        key = (row["doc_id"], row["tile_id"])
        if current and current["key"] == key:
            if kind == current["kind"]:
                current["n_cycles"] += 1
                current["n_kind"] += 1
                current["pending"] = 0
                current["ended"] = row["burst_started"]
                continue
            if kind == "unclassified" and current["pending"] < MAX_GAP_CYCLES:
                current["pending"] += 1
                current["n_cycles"] += 1
                continue
        close(current)
        current = None
        if kind != "unclassified":
            current = {"key": key, "kind": kind, "doc_id": row["doc_id"],
                       "uid": row["uid"], "unit": row["unit"],
                       "problem": row["problem"],
                       "n_cycles": 1, "n_kind": 1, "pending": 0,
                       "started": row["burst_started"],
                       "ended": row["burst_started"],
                       "first_entry_id": row["first_entry_id"]}
    close(current)
    return episodes


def _stratum(ep):
    if ep["n_cycles"] >= STRONG_CYCLES:
        return "strong"
    if ep["n_cycles"] >= BOUNDARY_CYCLES:
        return "boundary"
    return "control"


def main():
    derived = lib.ensure_derived()
    cycles = os.path.join(derived, "cycles.parquet")
    lib.require_file(cycles)

    keys = {r["doc_id"]: r["doc_key"] for r in lib.query(
        "SELECT doc_id, doc_key FROM read_parquet('%s')"
        % os.path.join(derived, "population.parquet"))}

    episodes = _episodes(cycles)
    for i, ep in enumerate(episodes):
        ep["episode_id"] = "ep%06d" % i
        ep["stratum"] = _stratum(ep)
        ep["doc_key"] = keys.get(ep["doc_id"], ep["doc_id"])
        ep["replay_url"] = replay_url(ep["doc_key"], ep["first_entry_id"])

    out = os.path.join(derived, "candidates.parquet")
    lib.write_parquet(
        [{k: ep.get(k) for k in CANDIDATE_COLUMNS} for ep in episodes],
        out, CANDIDATE_COLUMNS)

    print("episodes: %d (%d systematic, %d trial-and-error)"
          % (len(episodes),
             sum(1 for e in episodes if e["kind"] == "systematic"),
             sum(1 for e in episodes if e["kind"] == "trial_and_error")))
    print("wrote candidates.parquet")

    lines = ["# Review sheet", "",
             "Each row is one episode. Open the replay link, watch what the "
             "student actually did, and fill in the verdict column.", "",
             "Verdicts: `confirmed`, `rejected`, `ambiguous`. Save this file, "
             "then run `apply_verdicts.py`.", "",
             "Strata: **strong** = a long run, **boundary** = a short run near "
             "the threshold, **control** = a single cycle. Boundary and control "
             "rows matter most -- they are where the thresholds are wrong.", "",
             "Built at a burst gap of %.1fs, calibrated against trial-bounded "
             "cycles (see `build_cycles.py`). Composition rates still move with "
             "that number, and it is calibrated on the documents that have "
             "detectable trials rather than the whole corpus, so treat the "
             "rates as approximate and compare students against each other."
             % BURST_GAP_S, ""]

    for kind in ("systematic", "trial_and_error"):
        for stratum in ("strong", "boundary", "control"):
            picked = [e for e in episodes
                      if e["kind"] == kind and e["stratum"] == stratum][:PER_STRATUM]
            if not picked:
                continue
            lines += ["## %s — %s (%d shown)" % (kind, stratum, len(picked)), "",
                      "| episode | unit/problem | cycles | replay | verdict | note |",
                      "|---|---|---|---|---|---|"]
            for e in picked:
                lines.append("| %s | %s %s | %d | [replay](%s) |  |  |" % (
                    e["episode_id"], e["unit"] or "-", e["problem"] or "-",
                    e["n_cycles"], e["replay_url"]))
            lines.append("")

    with open(os.path.join(derived, "review.md"), "w") as handle:
        handle.write("\n".join(lines))
    print("wrote review.md")


if __name__ == "__main__":
    main()
