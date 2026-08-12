#!/usr/bin/env python3
"""Select the documents this analysis runs over, and flag unusable clocks.

  ./build_population.py

Population: documents of type `problem` that have history. Publications carry
no history by construction, and personal documents come from the standalone
Dataflow app, which predates history support.

`clock_suspect` marks documents whose `created` timestamps run backwards by
more than 60s when ordered by (idx, entry_id). Sub-second backsteps are
ordinary index ties from concurrent writes and are not flagged. The flag is
carried rather than filtered here -- edits are built for the whole population,
and the exclusion happens at the cycles stage, where durations start to matter.
"""
import os
import sys

import lib

SQL = """
COPY (
  WITH hist AS (
    SELECT DISTINCT doc_id FROM read_parquet('{history}')
  ),
  backsteps AS (
    SELECT doc_id FROM (
      SELECT doc_id, created,
             lag(created) OVER (PARTITION BY doc_id ORDER BY idx, entry_id) AS prev
      FROM read_parquet('{history}')
    )
    WHERE prev IS NOT NULL
      AND date_diff('second', created, prev) > 60
    GROUP BY doc_id
  )
  SELECT
    c.doc_id, c.doc_key, c.uid, c.portal_class_id, c.type, c.unit, c.problem,
    c.dataflow_tile_deleted,
    (c.doc_id IN (SELECT doc_id FROM backsteps)) AS clock_suspect
  FROM read_parquet('{content}') c
  WHERE c.type = 'problem'
    AND c.doc_id IN (SELECT doc_id FROM hist)
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(content, history, out):
    lib.require_file(content)
    lib.require_file(history)
    lib.run_sql(SQL.format(content=content, history=history, out=out))


def main():
    p = lib.paths()
    lib.ensure_derived()
    out = os.path.join(p["derived"], "population.parquet")
    build(p["content"], p["history"], out)

    total = lib.scalar("SELECT count(*) FROM read_parquet('%s')" % out)
    suspect = lib.scalar(
        "SELECT count(*) FROM read_parquet('%s') WHERE clock_suspect" % out)
    print("population: %d documents, %d clock-suspect, %d usable"
          % (total, suspect, total - suspect))

    # Refuse to hand a surprising corpus to later stages. These are the counts
    # the design was written against; a large drift means the source data
    # changed and the thresholds need re-deriving.
    if total < 2500:
        sys.exit("population of %d is far below the expected 2,832 -- "
                 "check that history.parquet is complete" % total)


if __name__ == "__main__":
    main()
