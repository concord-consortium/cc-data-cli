#!/usr/bin/env python3
"""Fold reviewed verdicts back into a calibration set.

  ./apply_verdicts.py

Reads review.md after you have filled in the verdict fields, writes
verdicts.csv, and reports precision per kind and per stratum. Precision on the
boundary stratum is the number that should move a threshold; precision on the
strong stratum mostly tells you the obvious cases are obvious.

This does not auto-tune. It reports what your judgements imply, and the
threshold change is a deliberate edit to thresholds.json.
"""
import csv
import os
import sys

import lib
# The sheet's format belongs to whoever writes it. This module owning a second
# parser is what let the two drift apart: when episode ids stopped being
# decimal, the regex here matched nothing and this tool silently reported no
# reviews at all, while the writer carried on unaware.
from build_candidates import parse_sheet


def precision(verdicts):
    out = {}
    for kind in {v["kind"] for v in verdicts}:
        judged = [v for v in verdicts
                  if v["kind"] == kind and v["verdict"] in ("confirmed", "rejected")]
        if not judged:
            continue
        confirmed = sum(1 for v in judged if v["verdict"] == "confirmed")
        out[kind] = confirmed / len(judged)
    return out


def main():
    derived = lib.ensure_derived()
    review = os.path.join(derived, "review.md")
    lib.require_file(review)

    with open(review) as handle:
        rows = parse_sheet(handle.read())
    if not rows:
        sys.exit("no episodes found in %s -- has the sheet's format changed?"
                 % review)

    out = os.path.join(derived, "verdicts.csv")
    with open(out, "w", newline="") as handle:
        writer = csv.DictWriter(
            handle, fieldnames=["episode_id", "kind", "stratum", "verdict", "note"])
        writer.writeheader()
        writer.writerows(rows)

    judged = [r for r in rows if r["verdict"] in ("confirmed", "rejected")]
    print("%d episodes, %d reviewed" % (len(rows), len(judged)))
    if not judged:
        sys.exit("no verdicts filled in yet -- edit review.md first")

    for kind, score in sorted(precision(rows).items()):
        print("  %-16s precision %.2f" % (kind, score))
    for stratum in ("strong", "boundary", "control"):
        subset = [r for r in judged if r["stratum"] == stratum]
        if subset:
            confirmed = sum(1 for r in subset if r["verdict"] == "confirmed")
            print("  %-16s %d/%d confirmed" % (stratum, confirmed, len(subset)))
    print("\nwrote %s" % out)
    print("Boundary precision is the number worth acting on. To change a "
          "threshold, edit thresholds.json and re-run build_cycles.py onward.")


if __name__ == "__main__":
    main()
