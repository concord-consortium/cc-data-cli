#!/usr/bin/env python3
"""Explode history entries into classified, coalesced student operations.

  ./build_edits.py

Why this exists: `history.parquet`'s `action` column is not a usable primitive.
Only 279 documents record granular program actions; 2,604 record edits as
`setProgram`, which is a catch-all -- the first `setProgram` entry sampled while
designing this turned out to be five node renames, not a program edit. The
uniform primitive is one level down, in `records[].patches[]`.

Classification needs both the patch path and the entry's action, because the
Dataflow and Simulation tiles write to the same shared-model paths a student
uses when filling in a table by hand. The discriminator is the action's root:
`tileMap/{tile}/content/...` was initiated by the student, `sharedModelMap/...`
by a running program.
"""
import os
import sys

import lib

# Order matters. `derived` must precede `parameter` (orderedDisplayName lives
# under data/), and the runtime rules must precede everything (a Simulation
# write can carry a path that otherwise looks like table editing).
CLASSIFY = """
    CASE
      WHEN path LIKE '%/program/values/%' THEN 'runtime'
      WHEN action LIKE '%/sharedModel/dataSet/addCanonicalCasesWithIDs' THEN 'runtime'
      WHEN regexp_matches(action, '/sharedModel/variables/[0-9]+/setValue$') THEN 'runtime'
      WHEN regexp_matches(action, '/sharedModel/variables/[0-9]+/commitTemporaryValue$')
        THEN 'ambiguous'
      WHEN path LIKE '%orderedDisplayName%' THEN 'derived'
      WHEN path LIKE '%/updatedHash' THEN 'derived'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/(x|y)$') THEN 'layout'
      WHEN path LIKE '%/programZoom%' THEN 'layout'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/inputs/') THEN 'structure'
      WHEN regexp_matches(path, '/program/nodes/[^/]+$') THEN 'structure'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/data/') THEN 'parameter'
      WHEN is_revert THEN 'undo'
      WHEN action = 'undo' THEN 'undo'
      WHEN action IN ('/addTile', '/deleteTile', '/content/userAddTile',
                      '/content/handleDragCopyTiles') THEN 'tile'
      WHEN regexp_matches(action,
             '/content/(setSlate|setCanonicalCaseValues|addCanonicalCases|addObject)$')
        THEN 'documentation'
      WHEN regexp_matches(action,
             '/sharedModel/dataSet/(setAttributeName|removeAttribute|addAttributeWithID)$')
        THEN 'documentation'
      ELSE 'other'
    END
"""

# One student gesture emits many history entries -- measured p50 gap 0s, p90 2s.
# Repeats of the same (class, target, op) inside this window are one operation.
COALESCE_MS = 2000

SQL = """
COPY (
  WITH pop AS (
    SELECT doc_id, uid, portal_class_id, unit, problem, clock_suspect
    FROM read_parquet('{population}')
  ),
  src AS (
    SELECT h.doc_id, h.entry_id, h.idx, h.created, h.server_created,
           h.action, h.is_revert, h.tile_id AS entry_tile_id, h.entry_json
    FROM read_parquet('{history}') h
    JOIN pop USING (doc_id)
    WHERE h.action NOT LIKE '%/content/step'
      AND h.action NOT LIKE '%tickAndProcess'
  ),
  rec AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id,
           unnest(json_extract(entry_json, '$.records[*]')) AS rec
    FROM src
  ),
  pat AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id,
           unnest(json_extract(rec, '$.patches[*]')) AS patch
    FROM rec
  ),
  flat AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id,
           json_extract_string(patch, '$.op') AS op,
           json_extract_string(patch, '$.path') AS path
    FROM pat
  ),
  classified AS (
    SELECT *,
      coalesce(nullif(regexp_extract(path, '/tileMap/([^/]+)', 1), ''),
               entry_tile_id) AS tile_id,
      nullif(regexp_extract(path, '/program/nodes/([^/]+)', 1), '') AS node_id,
      {classify} AS class,
      -- Splits `structure` so program size can be carried alongside a
      -- candidate: a burst of 5 changes on a 12-node program reads very
      -- differently from the same burst on a 2-node one.
      CASE
        WHEN regexp_matches(path, '/program/nodes/[^/]+/inputs/') THEN 'connection'
        WHEN regexp_matches(path, '/program/nodes/[^/]+$') THEN 'node'
        ELSE NULL
      END AS subtype
    FROM flat
  ),
  targeted AS (
    SELECT *, coalesce(node_id, tile_id) AS target_id FROM classified
  ),
  marked AS (
    SELECT *,
      CASE WHEN date_diff('millisecond',
             lag(created) OVER (PARTITION BY doc_id, class, target_id, op
                                ORDER BY created, entry_id),
             created) <= {window} THEN 0 ELSE 1 END AS is_new
    FROM targeted
  ),
  grouped AS (
    SELECT *,
      sum(is_new) OVER (PARTITION BY doc_id, class, target_id, op
                        ORDER BY created, entry_id
                        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS grp
    FROM marked
  )
  SELECT
    g.doc_id, p.uid, p.portal_class_id, p.unit, p.problem, p.clock_suspect,
    any_value(g.tile_id) AS tile_id,
    any_value(g.node_id) AS node_id,
    any_value(g.subtype) AS subtype,
    g.target_id, g.class, g.op,
    min(g.created) AS started,
    max(g.created) AS ended,
    count(*) AS n_patches,
    arg_min(g.entry_id, g.created) AS first_entry_id
  FROM grouped g
  JOIN pop p ON p.doc_id = g.doc_id
  GROUP BY g.doc_id, p.uid, p.portal_class_id, p.unit, p.problem,
           p.clock_suspect, g.target_id, g.class, g.op, g.grp
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(history, population, out):
    lib.require_file(history)
    lib.require_file(population)
    lib.run_sql(SQL.format(history=history, population=population, out=out,
                           classify=CLASSIFY, window=COALESCE_MS))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    pop = os.path.join(derived, "population.parquet")
    out = os.path.join(derived, "edits.parquet")
    build(p["history"], pop, out)

    rows = lib.query(
        "SELECT class, count(*) AS n FROM read_parquet('%s') "
        "GROUP BY class ORDER BY n DESC" % out)
    total = sum(r["n"] for r in rows)
    print("edits: %d operations" % total)
    for r in rows:
        print("  %-14s %8d  %5.1f%%" % (r["class"], r["n"], 100.0 * r["n"] / total))

    # `other` is the escape hatch for paths the taxonomy does not recognise.
    # A large bucket means the taxonomy has drifted from the data, which is a
    # correctness problem, not a cosmetic one.
    other = next((r["n"] for r in rows if r["class"] == "other"), 0)
    if total and other / total > 0.25:
        sys.exit("'other' is %.1f%% of operations -- the taxonomy no longer "
                 "matches the data; review build_edits.CLASSIFY before "
                 "trusting anything downstream" % (100.0 * other / total))


if __name__ == "__main__":
    main()
