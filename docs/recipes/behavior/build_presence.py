#!/usr/bin/env python3
"""Reconstruct when the student had the document open, from program ticks.

  ./build_presence.py

`content/step` and `program/tickAndProcess` are 54% of all history entries and
the inventory rightly says to filter them out when studying authoring. Here
they are the point: a Dataflow program runs continuously while the document is
open, so the ticks are the only continuous evidence that the student had not
left. Measured across the 200 busiest documents, the median inter-tick gap is
0s and the p90 is 1s.

The evidence is asymmetric and the rest of the pipeline must treat it that way.
Ticks stopping is strong evidence the student left. Ticks running is weak
evidence they were watching -- they keep ticking while the tile is scrolled out
of view, because CLUE logs no scroll or visibility event.

The gap threshold is per-session rather than constant because the data rate is
student-settable down to one tick per minute. Chosen rates across the corpus
are 100ms (669x), 50ms, 1000ms, 500ms, 10000ms (49x) and 60000ms (33x).
"""
import os

import lib

FLOOR_MS = 60000        # never split on a gap shorter than this
MULTIPLIER = 6          # ... or shorter than this many expected ticks
COARSE_MS = 15000       # above this median rate, presence resolution is poor
RATE_CEILING_MS = 90000 # gaps above this (60s max rate + jitter) cannot be
                        # real ticks -- excluding them from the rate keeps an
                        # absence from inflating the threshold that is
                        # supposed to detect it

SQL = """
COPY (
  WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
  ticks AS (
    SELECT h.doc_id, h.tile_id, h.created
    FROM read_parquet('{history}') h
    JOIN pop USING (doc_id)
    WHERE h.action LIKE '%/content/step'
       OR h.action LIKE '%tickAndProcess'
  ),
  gapped AS (
    SELECT *,
      date_diff('millisecond',
        lag(created) OVER (PARTITION BY doc_id, tile_id ORDER BY created),
        created) AS gap_ms
    FROM ticks
  ),
  rate AS (
    SELECT doc_id, tile_id, median(gap_ms) AS median_tick_ms
    FROM gapped WHERE gap_ms > 0 AND gap_ms <= {rate_ceiling}
    GROUP BY doc_id, tile_id
  ),
  marked AS (
    SELECT g.*, r.median_tick_ms,
      greatest({floor}, {mult} * coalesce(r.median_tick_ms, 1000)) AS threshold_ms,
      CASE WHEN g.gap_ms IS NULL
             OR g.gap_ms > greatest({floor}, {mult} * coalesce(r.median_tick_ms, 1000))
           THEN 1 ELSE 0 END AS is_new
    FROM gapped g
    LEFT JOIN rate r USING (doc_id, tile_id)
  ),
  grouped AS (
    SELECT *,
      CAST(sum(is_new) OVER (PARTITION BY doc_id, tile_id ORDER BY created
                        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS interval_id
    FROM marked
  )
  SELECT doc_id, tile_id, interval_id,
         min(created) AS started,
         max(created) AS ended,
         count(*) AS n_ticks,
         any_value(median_tick_ms) AS median_tick_ms,
         coalesce(any_value(median_tick_ms) > {coarse}, FALSE) AS rate_coarse
  FROM grouped
  GROUP BY doc_id, tile_id, interval_id
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(history, population, out):
    lib.require_file(history)
    lib.require_file(population)
    lib.run_sql(SQL.format(history=history, population=population, out=out,
                           floor=FLOOR_MS, mult=MULTIPLIER, coarse=COARSE_MS,
                           rate_ceiling=RATE_CEILING_MS))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    out = os.path.join(derived, "presence.parquet")
    build(p["history"], os.path.join(derived, "population.parquet"), out)

    row = lib.query(
        "SELECT count(*) AS intervals, count(DISTINCT doc_id) AS docs, "
        "count(*) FILTER (WHERE rate_coarse) AS coarse, "
        "median(date_diff('second', started, ended)) AS median_len_s "
        "FROM read_parquet('%s')" % out)[0]
    print("presence: %d intervals across %d documents, %d coarse-rate, "
          "median length %ss" % (row["intervals"], row["docs"], row["coarse"],
                                 row["median_len_s"]))


if __name__ == "__main__":
    main()
