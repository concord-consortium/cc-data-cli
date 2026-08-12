#!/usr/bin/env python3
"""How much do students actually write, draw, and tabulate?

  ./report_documentation.py

CLUE-575 treats absence of documentation as part of trial-and-error, which
assumes documentation is common enough to be informative. Preliminary counts say
85% of documents with program edits have SOME documentation operation -- more
than expected -- but operation counts overstate written volume, because text
edits fire at roughly keystroke granularity. This separates the two questions:
how many documents document at all, and how substantial it is.

Provenance matters and is only partly recoverable: a documentation tile the
student copied from the curriculum was prompted, while one they created
themselves was self-directed. `handleDragCopyTiles` marks the former.
"""
import os

import lib


def main():
    derived = lib.ensure_derived()
    edits = os.path.join(derived, "edits.parquet")
    lib.require_file(edits)

    overall = lib.query("""
        WITH prog AS (
          SELECT DISTINCT doc_id FROM read_parquet('%s')
          WHERE class IN ('structure','parameter')
        ),
        doc AS (
          SELECT doc_id, count(*) AS n_ops,
                 count(DISTINCT tile_id) AS n_tiles
          FROM read_parquet('%s') WHERE class = 'documentation'
          GROUP BY doc_id
        )
        SELECT
          (SELECT count(*) FROM prog) AS docs_with_program_edits,
          count(d.doc_id) AS docs_documenting,
          -- CAST to BIGINT: sum() over an INTEGER yields HUGEINT, which the
          -- duckdb JSON writer renders as a string, breaking the print below.
          CAST(coalesce(sum(d.n_ops), 0) AS BIGINT) AS total_ops,
          median(d.n_ops) AS median_ops,
          quantile_cont(d.n_ops, 0.9) AS p90_ops,
          median(d.n_tiles) AS median_tiles
        FROM prog p LEFT JOIN doc d ON d.doc_id = p.doc_id
    """ % (edits, edits))[0]

    print("documents with program edits: %d" % overall["docs_with_program_edits"])
    print("  ...that document at all:    %d (%.1f%%)"
          % (overall["docs_documenting"],
             100.0 * overall["docs_documenting"] / overall["docs_with_program_edits"]))
    print("  documentation operations:   %d total, median %.0f/doc, p90 %.0f"
          % (overall["total_ops"], overall["median_ops"] or 0,
             overall["p90_ops"] or 0))
    print("  distinct tiles documented:  median %.0f/doc" % (overall["median_tiles"] or 0))

    prov = lib.query("""
        SELECT op, count(*) AS n FROM read_parquet('%s')
        WHERE class IN ('documentation', 'tile')
        GROUP BY op ORDER BY n DESC LIMIT 10
    """ % edits)
    print("\nby operation:")
    for r in prov:
        print("  %-24s %8d" % (r["op"], r["n"]))


if __name__ == "__main__":
    main()
