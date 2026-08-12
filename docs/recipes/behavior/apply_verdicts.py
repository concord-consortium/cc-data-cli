#!/usr/bin/env python3
"""Fold reviewed verdicts back into a calibration set.

  ./apply_verdicts.py

Reads review.md after you have filled in the verdict column, writes
verdicts.csv, and reports precision per kind and per stratum. Precision on the
boundary stratum is the number that should move a threshold; precision on the
strong stratum mostly tells you the obvious cases are obvious.

This does not auto-tune. It reports what your judgements imply, and the
threshold change is a deliberate edit to thresholds.json.
"""
import csv
import os
import re
import sys

import lib

HEADING = re.compile(r"^##\s+(\S+)\s+—\s+(\w+)")
ROW = re.compile(r"^\|\s*(ep\d+)\s*\|")


def parse_review(text):
    rows = []
    kind = stratum = ""
    for line in text.splitlines():
        heading = HEADING.match(line)
        if heading:
            kind, stratum = heading.group(1), heading.group(2)
            continue
        if not ROW.match(line):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        # episode | unit/problem | cycles | replay | verdict | note
        rows.append({"episode_id": cells[0], "kind": kind, "stratum": stratum,
                     "verdict": cells[4] if len(cells) > 4 else "",
                     "note": cells[5] if len(cells) > 5 else ""})
    return rows


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
        rows = parse_review(handle.read())

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
