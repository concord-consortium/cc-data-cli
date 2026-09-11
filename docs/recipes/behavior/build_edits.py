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
      -- Runtime output stored under a node, not student edits. Both were
      -- previously classified `parameter`: `demoOutput` is the Demo Output
      -- node's live display (median 0.08s between rewrites), and
      -- `tickEntries/{id}/...` is the tick stream. The action filter above
      -- removes most ticks, but some arrive inside `setProgram` entries and
      -- survive it -- which is the design's point that classification must
      -- happen at the patch path, not the action name.
      WHEN regexp_matches(path, '/program/nodes/[^/]+/data/demoOutput$')
        THEN 'runtime'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/data/tickEntries/')
        THEN 'runtime'
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
  -- `entry_id` alone does not disambiguate patches: one entry commonly
  -- carries many patches (measured: ~48% of patches share a (created,
  -- entry_id) pair with at least one sibling, some entries over 1,000-way),
  -- and DuckDB's ROWS-framed window functions must still impose SOME
  -- physical order across those ties -- a different one, independently,
  -- for every window computed below. Two windows that disagree on a tied
  -- cluster's internal order can split what should be one coalesced
  -- operation into two, nondeterministically, between runs. `rec_idx` and
  -- `patch_idx` -- each patch's position in the JSON array it came from,
  -- which is fixed by the source data, not by DuckDB's execution plan --
  -- close that gap completely (verified against the real corpus: zero
  -- remaining ties on (created, entry_id, rec_idx, patch_idx)).
  rec AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id,
           unnest(json_extract(entry_json, '$.records[*]')) AS rec,
           unnest(range(CAST(json_array_length(entry_json, '$.records')
                             AS BIGINT))) AS rec_idx
    FROM src
  ),
  pat AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id, rec_idx,
           unnest(json_extract(rec, '$.patches[*]')) AS patch,
           unnest(range(CAST(json_array_length(rec, '$.patches')
                             AS BIGINT))) AS patch_idx
    FROM rec
  ),
  flat AS (
    SELECT doc_id, entry_id, idx, created, server_created, action, is_revert,
           entry_tile_id, rec_idx, patch_idx,
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
      -- (entry_id, rec_idx, patch_idx) breaks ties among rows sharing
      -- `created` -- including rows from the SAME entry -- so `is_new` (and
      -- everything downstream of it) is fully determined by the data, not
      -- by DuckDB's tie-breaking for a given run.
      CASE WHEN date_diff('millisecond',
             lag(created) OVER (PARTITION BY doc_id, class, target_id, op
                                ORDER BY created, entry_id, rec_idx, patch_idx),
             created) <= {window} THEN 0 ELSE 1 END AS is_new
    FROM targeted
  ),
  grouped AS (
    SELECT *,
      sum(is_new) OVER (PARTITION BY doc_id, class, target_id, op
                        ORDER BY created, entry_id, rec_idx, patch_idx
                        ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS grp
    FROM marked
  ),
  seqed AS (
    SELECT *,
      -- Deterministic ordinal within the group, so arg_min(entry_id, ...)
      -- below has a real tiebreaker instead of an ambiguous `created`.
      row_number() OVER (PARTITION BY doc_id, class, target_id, op, grp
                         ORDER BY created, entry_id, rec_idx, patch_idx) AS seq
    FROM grouped
  )
  SELECT
    g.doc_id, p.uid, p.portal_class_id, p.unit, p.problem, p.clock_suspect,
    any_value(g.tile_id) AS tile_id,
    -- min(), not any_value(): adding a node with inputs emits `/nodes/X` and
    -- `/nodes/X/inputs/y` patches sharing doc/class/target/op/created, which
    -- coalesce into one group with different subtypes (node vs connection),
    -- and any_value() picked among them arbitrarily.
    min(g.node_id) AS node_id,
    min(g.subtype) AS subtype,
    g.target_id, g.class, g.op,
    min(g.created) AS started,
    max(g.created) AS ended,
    count(*) AS n_patches,
    -- arg_min keyed by `seq`, not `created`: two patches from different
    -- entries can share a millisecond timestamp, which would otherwise make
    -- the replay-link entry_id ambiguous between runs.
    arg_min(g.entry_id, g.seq) AS first_entry_id
  FROM seqed g
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
    # Write to a temp path and only replace the previous good edits.parquet
    # after the sanity check below passes -- see docs/recipes/README.md's
    # "two things that will bite".
    tmp = out + ".tmp"
    build(p["history"], pop, tmp)

    rows = lib.query(
        "SELECT class, count(*) AS n FROM read_parquet('%s') "
        "GROUP BY class ORDER BY n DESC" % tmp)
    total = sum(r["n"] for r in rows)

    # `other` is the escape hatch for paths the taxonomy does not recognise.
    # A large bucket means the taxonomy has drifted from the data, which is a
    # correctness problem, not a cosmetic one.
    other = next((r["n"] for r in rows if r["class"] == "other"), 0)
    if total and other / total > 0.25:
        os.remove(tmp)
        sys.exit("'other' is %.1f%% of operations -- the taxonomy no longer "
                 "matches the data; review build_edits.CLASSIFY before "
                 "trusting anything downstream" % (100.0 * other / total))

    os.replace(tmp, out)
    print("edits: %d operations" % total)
    for r in rows:
        print("  %-14s %8d  %5.1f%%" % (r["class"], r["n"], 100.0 * r["n"] / total))


if __name__ == "__main__":
    main()
