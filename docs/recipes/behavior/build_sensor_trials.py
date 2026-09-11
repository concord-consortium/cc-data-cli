#!/usr/bin/env python3
"""Detect trials on SENSOR inputs: static -> changing -> static readings.

  ./build_sensor_trials.py

build_trials.py covers the other input the student can drive: the Simulator's
slider, read from `commitTemporaryValue`. That covers only documents with a
Simulator tile whose simulation HAS a slider, and it reads a path a physical
sensor never writes.

A physical sensor's readings arrive somewhere else entirely: `tickAndProcess`
history entries, one per program tick, each carrying a `nodeValue` per node
under `nodes/{id}/data/tickEntries/{tick}`. build_edits.py classifies those as
`runtime`, correctly -- they are not student edits. They are, again, exactly
the student's observation, and this is the third time in this pipeline that the
stream discarded as machine noise turned out to carry the signal.

Detection differs from build_trials.py in one way that matters. A slider records
one committed value per release, so each entry is already a change. A sensor
records a reading per tick and is noisy, so this uses a rolling range instead: a window of WINDOW_TICKS consecutive
readings is `changing` when it spans more than CHANGE_FRACTION of that node's
observed range. Measured on the real corpus, resting EMG is quieter than that
suggests -- readings sit pinned at a floor value with a rolling range of
exactly zero -- but temperature and pressure sensors do drift, and the
fractional threshold adapts to each node's own scale rather than assuming one.

The window is centred, so a trial's boundaries are soft: it starts up to half a
window early and ends up to half a window late, inflating duration_s by roughly
WINDOW_TICKS worth of sampling. At the corpus median rate that is under two
seconds, and it is symmetric, so it does not bias one pole of the axis.

`sensor_kind` separates the populations, and callers should not pool them:

  physical   bound to a device (a serial, an Arduino pin). The student flexed,
             pressed, or heated something. This is the population that extends
             coverage beyond the Simulator.
  simulated  the node declares `virtual: true`, or its key starts with SIM*.
             Either way the reading is generated rather than measured. The
             `virtual` flag has to be checked FIRST: 548 nodes carry it with
             ids like `00001-VIR` that no prefix rule would catch, and reading
             the prefix alone filed every one of them as physical.
  unbound    no device chosen. Almost entirely NaN in practice, so it rarely
             survives to a trial at all.

One confound is worth stating because it does not apply to the Simulator: a
student has one mouse and cannot drag a slider while editing, but they can flex
one arm while editing with the other. Coupling to edits is therefore weaker
evidence here. main() measures how often that actually happens.

This is a standalone detector. build_cycles.py and build_candidates.py still
read trials.parquet only; folding sensor trials into the episode ranking is a
deliberate next step, not an oversight.
"""
import os
import sys

import lib

# Two readings further apart than this are different sittings. The stream is
# dense when it runs at all -- the corpus median gap between readings is 304ms.
SEGMENT_GAP_S = 30
# Readings per rolling window, centred on each reading.
WINDOW_TICKS = 10
# A window spanning more than this fraction of the node's range is changing.
CHANGE_FRACTION = 0.10
# Readings of stillness required on each side of a change for it to be a trial.
MIN_STATIC_TICKS = 10
# A floor, not a calibration: a healthy run finds several hundred trials, so a
# near-empty result means the patch shape changed, not that students stopped.
MIN_TRIALS = 50

SQL = """
COPY (
  WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
  -- Which nodes are sensors, and what each is bound to. Read from the content
  -- snapshot: the tick stream carries readings but never says what produced
  -- them.
  tiles AS (
    SELECT c.doc_id, unnest(json_extract(c.content_json, '$.tileMap.*')) AS tile
    FROM read_parquet('{content}') c
    JOIN pop USING (doc_id)
    WHERE c.parse_ok
  ),
  nodes AS (
    SELECT doc_id, unnest(json_extract(tile, '$.content.program.nodes.*')) AS node
    FROM tiles
    WHERE json_extract_string(tile, '$.content.type') = 'Dataflow'
  ),
  snodes AS (
    SELECT doc_id,
           json_extract_string(node, '$.id') AS node_id,
           coalesce(json_extract_string(node, '$.data.sensorType'), '') AS sensor_type,
           CASE
             WHEN coalesce(json_extract_string(node, '$.data.sensor'), '') = ''
               THEN 'unbound'
             -- `virtual` is the node's own declaration and outranks the id's
             -- shape. Reading the prefix alone counted 548 nodes bound to ids
             -- like `00001-VIR` and `00008VIR` as physical, which is the whole
             -- population this column exists to isolate.
             WHEN json_extract_string(node, '$.data.virtual') = 'true'
               THEN 'simulated'
             WHEN json_extract_string(node, '$.data.sensor') LIKE 'SIM%'
               THEN 'simulated'
             ELSE 'physical'
           END AS sensor_kind
    FROM nodes
    WHERE json_extract_string(node, '$.name') = 'Sensor'
  ),
  -- min() rather than any_value(): a node id could appear in two tiles, and
  -- any_value picks nondeterministically, which makes the whole artifact
  -- differ between runs.
  snode AS (
    SELECT doc_id, node_id, min(sensor_type) AS sensor_type,
           min(sensor_kind) AS sensor_kind
    FROM snodes GROUP BY doc_id, node_id
  ),
  patch AS (
    SELECT h.doc_id, h.entry_id, h.created,
           unnest(json_extract(h.entry_json, '$.records[*].patches[*]')) AS p
    FROM read_parquet('{history}') h
    JOIN pop USING (doc_id)
    WHERE h.action LIKE '%/program/tickAndProcess'
  ),
  -- The `add` of a whole tickEntry is that tick's reading. Each entry also
  -- carries a `replace` on tickEntries/legacyTick/nodeValue restating a
  -- reading already recorded; the `$`-anchored path here excludes it, since
  -- that path continues past the tick id.
  raw AS (
    SELECT doc_id, entry_id, created,
           regexp_extract(json_extract_string(p, '$.path'),
                          '/program/nodes/([^/]+)/data/tickEntries/[^/]+$',
                          1) AS node_id,
           TRY_CAST(json_extract_string(p, '$.value.nodeValue') AS DOUBLE) AS v
    FROM patch
    WHERE json_extract_string(p, '$.op') = 'add'
      AND regexp_matches(json_extract_string(p, '$.path'),
                         '/data/tickEntries/[^/]+$')
  ),
  -- NaN is "no device reporting", not a reading of zero. Dropping it can stitch
  -- the two sides of a short dropout together; a dropout longer than
  -- SEGMENT_GAP_S becomes a segment break instead, which is the honest result.
  series AS (
    SELECT r.doc_id, r.node_id, s.sensor_kind, s.sensor_type,
           r.entry_id, r.created, min(r.v) AS v
    FROM raw r
    JOIN snode s USING (doc_id, node_id)
    WHERE r.v IS NOT NULL AND NOT isnan(r.v)
    -- Collapsing to one reading per (node, entry) makes (created, entry_id) a
    -- unique sort key, so every window below is deterministic. Ordering on a
    -- key with ties silently reorders rows between runs.
    GROUP BY r.doc_id, r.node_id, s.sensor_kind, s.sensor_type,
             r.entry_id, r.created
  ),
  -- The flag and its running sum are separate CTEs throughout: DuckDB refuses
  -- a window call nested inside another window call's argument.
  seg_flag AS (
    SELECT *,
      CASE WHEN lag(created) OVER w IS NULL
             OR date_diff('second', lag(created) OVER w, created) > {gap_s}
           THEN 1 ELSE 0 END AS seg_start
    FROM series
    WINDOW w AS (PARTITION BY doc_id, node_id ORDER BY created, entry_id)
  ),
  segmented AS (
    SELECT *,
      CAST(sum(seg_start) OVER (PARTITION BY doc_id, node_id
                                ORDER BY created, entry_id
                                ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS seg_id
    FROM seg_flag
  ),
  scaled AS (
    SELECT s.*, sc.node_range
    FROM segmented s
    JOIN (SELECT doc_id, node_id, max(v) - min(v) AS node_range
          FROM series GROUP BY doc_id, node_id) sc
      USING (doc_id, node_id)
  ),
  -- win_n < WINDOW_TICKS at a segment's edges, where the window runs off the
  -- end. Those readings are treated as static: a change we can only half see
  -- is not evidence.
  rolled AS (
    SELECT *,
           (node_range > 0
            AND count(*) OVER w = {window}
            AND max(v) OVER w - min(v) OVER w > {fraction} * node_range) AS changing
    FROM scaled
    WINDOW w AS (PARTITION BY doc_id, node_id, seg_id ORDER BY created, entry_id
                 ROWS BETWEEN {before} PRECEDING AND {after} FOLLOWING)
  ),
  -- `run_start` must not reuse `seg_start`: SELECT * carries the earlier column
  -- forward, and a duplicate name resolves to the stale one, which silently
  -- collapses every run in the segment into one.
  run_flag AS (
    SELECT *,
      CASE WHEN changing IS DISTINCT FROM
                lag(changing) OVER (PARTITION BY doc_id, node_id, seg_id
                                    ORDER BY created, entry_id)
           THEN 1 ELSE 0 END AS run_start
    FROM rolled
  ),
  runs AS (
    SELECT *,
      CAST(sum(run_start) OVER (PARTITION BY doc_id, node_id, seg_id
                                ORDER BY created, entry_id
                                ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
           AS BIGINT) AS run_id
    FROM run_flag
  ),
  run_agg AS (
    SELECT doc_id, node_id, sensor_kind, sensor_type, seg_id, run_id, changing,
           min(created) AS started, max(created) AS ended, count(*) AS n_ticks,
           date_diff('millisecond', min(created), max(created)) / 1000.0
             AS duration_s
    FROM runs
    GROUP BY doc_id, node_id, sensor_kind, sensor_type, seg_id, run_id, changing
  ),
  -- static -> changing -> static, all three inside one segment.
  bounded AS (
    SELECT *,
           lag(changing) OVER w AS prev_changing,
           lag(n_ticks) OVER w AS prev_ticks,
           lead(changing) OVER w AS next_changing,
           lead(n_ticks) OVER w AS next_ticks
    FROM run_agg
    WINDOW w AS (PARTITION BY doc_id, node_id, seg_id ORDER BY started)
  )
  SELECT doc_id,
         CAST(row_number() OVER (PARTITION BY doc_id
                                 ORDER BY started, node_id, seg_id) - 1
              AS BIGINT) AS trial_id,
         node_id, sensor_kind, sensor_type, started, ended, n_ticks, duration_s,
         'sensor' AS source
  FROM bounded
  WHERE changing
    AND prev_changing IS FALSE AND prev_ticks >= {min_static}
    AND next_changing IS FALSE AND next_ticks >= {min_static}
) TO '{out}' (FORMAT parquet, COMPRESSION zstd);
"""


def build(history, content, population, out):
    lib.require_file(history)
    lib.require_file(content)
    lib.require_file(population)
    lib.run_sql(SQL.format(
        history=history, content=content, population=population, out=out,
        gap_s=SEGMENT_GAP_S, window=WINDOW_TICKS, fraction=CHANGE_FRACTION,
        before=WINDOW_TICKS // 2, after=WINDOW_TICKS - WINDOW_TICKS // 2 - 1,
        min_static=MIN_STATIC_TICKS))


FUNNEL_SQL = """
WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
tickdocs AS (
  SELECT DISTINCT h.doc_id FROM read_parquet('{history}') h
  JOIN pop USING (doc_id) WHERE h.action LIKE '%/program/tickAndProcess'
),
tiles AS (
  SELECT c.doc_id, unnest(json_extract(c.content_json, '$.tileMap.*')) AS tile
  FROM read_parquet('{content}') c JOIN pop USING (doc_id) WHERE c.parse_ok
),
nodes AS (
  SELECT doc_id, unnest(json_extract(tile, '$.content.program.nodes.*')) AS node
  FROM tiles WHERE json_extract_string(tile, '$.content.type') = 'Dataflow'
),
sn AS (
  SELECT doc_id,
    CASE WHEN coalesce(json_extract_string(node, '$.data.sensor'), '') = ''
           THEN 'unbound'
         WHEN json_extract_string(node, '$.data.virtual') = 'true'
           THEN 'simulated'
         WHEN json_extract_string(node, '$.data.sensor') LIKE 'SIM%'
           THEN 'simulated'
         ELSE 'physical' END AS kind
  FROM nodes WHERE json_extract_string(node, '$.name') = 'Sensor'
)
SELECT 1 AS ord, 'documents in the analysis population' AS step,
       count(*) AS n FROM pop
UNION ALL SELECT 2, '  ...containing a Sensor node',
  count(DISTINCT doc_id) FROM sn
UNION ALL SELECT 3, '  ...with that Sensor bound to a device',
  count(DISTINCT doc_id) FROM sn WHERE kind = 'physical'
UNION ALL SELECT 4, '  ...and carrying tickAndProcess history',
  count(DISTINCT s.doc_id) FROM sn s JOIN tickdocs t USING (doc_id)
  WHERE s.kind = 'physical'
UNION ALL SELECT 5, '  ...yielding at least one detected trial',
  count(DISTINCT doc_id) FROM read_parquet('{trials}') WHERE sensor_kind = 'physical'
ORDER BY ord
"""


def print_funnel(p, derived, out):
    """Where the physical-sensor population is lost.

    The drop that matters is between a bound sensor and a tick history: the
    readings only exist in documents recent enough to have `tickAndProcess`
    entries, which is a fraction of the corpus. That gap is an instrumentation
    limit, not a statement about what students did.
    """
    rows = lib.query(FUNNEL_SQL.format(
        population=os.path.join(derived, "population.parquet"),
        history=p["history"], content=p["content"], trials=out))
    print("physical-sensor population:")
    for row in rows:
        print("  %-42s %5d" % (row["step"], row["n"]))


def main():
    p = lib.paths()
    derived = lib.ensure_derived()
    out = os.path.join(derived, "sensor_trials.parquet")
    # Temp-then-replace: `COPY ... TO out` would destroy a good artifact before
    # the checks below could reject the new one.
    tmp = out + ".tmp"
    build(p["history"], p["content"],
          os.path.join(derived, "population.parquet"), tmp)

    total = lib.scalar("SELECT count(*) FROM read_parquet('%s')" % tmp)
    if total < MIN_TRIALS:
        os.remove(tmp)
        sys.exit("only %d sensor trials, below the floor of %d -- check that "
                 "tickAndProcess entries still carry tickEntries patches"
                 % (total, MIN_TRIALS))

    by_kind = lib.query(
        "SELECT sensor_kind, count(*) AS trials, "
        "count(DISTINCT doc_id) AS docs, count(DISTINCT node_id) AS nodes, "
        "median(duration_s) AS med_dur, "
        "quantile_cont(duration_s, 0.9) AS p90_dur "
        "FROM read_parquet('%s') GROUP BY 1 ORDER BY trials DESC" % tmp)

    edits = os.path.join(derived, "edits.parquet")
    # Two questions at once. `preceded` is the reconstructed "set it up, then
    # trial it" cycle. `overlapping` is the two-handed confound: a student
    # editing WHILE the sensor changes did not stop to observe, and the rate of
    # that is what tells us whether the coupling above can be trusted.
    coupling = lib.query("""
        SELECT t.sensor_kind, count(*) AS n,
          count(*) FILTER (WHERE EXISTS (
            SELECT 1 FROM read_parquet('%s') e
            WHERE e.doc_id = t.doc_id AND e.class IN ('structure','parameter')
              AND e.ended <= t.started
              AND date_diff('second', e.ended, t.started) <= 120)) AS preceded,
          count(*) FILTER (WHERE EXISTS (
            SELECT 1 FROM read_parquet('%s') e
            WHERE e.doc_id = t.doc_id AND e.class IN ('structure','parameter')
              AND e.started <= t.ended AND e.ended >= t.started)) AS overlapping
        FROM read_parquet('%s') t GROUP BY 1
    """ % (edits, edits, tmp)) if os.path.exists(edits) else []
    coupling = {r["sensor_kind"]: r for r in coupling}

    os.replace(tmp, out)
    print_funnel(p, derived, out)
    print("sensor trials: %d" % total)
    for row in by_kind:
        print("  %-9s %5d trials, %3d documents, %3d nodes; "
              "median %.1fs, p90 %.1fs"
              % (row["sensor_kind"], row["trials"], row["docs"], row["nodes"],
                 row["med_dur"], row["p90_dur"]))
        c = coupling.get(row["sensor_kind"])
        if c and c["n"]:
            print("            %d (%.1f%%) follow an edit within 120s; "
                  "%d (%.1f%%) overlap an edit"
                  % (c["preceded"], 100.0 * c["preceded"] / c["n"],
                     c["overlapping"], 100.0 * c["overlapping"] / c["n"]))


if __name__ == "__main__":
    main()
