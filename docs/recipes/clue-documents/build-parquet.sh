#!/usr/bin/env bash
# Converts the downloaded history JSONL into Parquet and verifies it.
#
#   ./build-parquet.sh
#
# Column types are declared explicitly rather than inferred. read_json_auto
# samples rows to guess types, and several columns here would be guessed wrong:
# `problem` and `investigation` are numeric-looking strings ("0", "1"), and
# columns that are null in the sampled rows would be typed as NULL and silently
# dropped.

set -euo pipefail
cd "$(dirname "$0")"

HIST="history"
OUT="history.parquet"
DOCS_OUT="history_documents.parquet"

if [ ! -d "$HIST" ]; then echo "no $HIST directory" >&2; exit 1; fi

# Guard against building a partial Parquet over a complete one.
#
# This happened once, and cost a full re-download. The JSONL is deleted after a
# successful build to reclaim disk, leaving only .meta.json per document. A later
# rebuild then read `history/*.jsonl`, matched only the handful of documents
# downloaded since, and overwrote 6.27M entries with 110k -- silently, because a
# glob that matches fewer files is not an error.
#
# Comparing .jsonl against .meta.json is not enough: during a download the two
# counts pass through equality, so a rebuild mid-download still looks fine. The
# real invariant is the source list -- every document that should have history
# must have a .jsonl right now.
SRC="clue_dataflow_history_all.json"
[ -f "$SRC" ] || SRC="clue_dataflow_history.json"

expected=$(python3 - "$SRC" <<'PY'
import json, sys
# classes whose only assignment was the standalone Dataflow app or the
# /branch/dataflow build; their documents have no history (see download-history.ts)
DATAFLOW_APP_CLASSES = {"20769","20770","20771","20772","28","8011",
                        "20717","21479","21518","21004"}
docs = json.load(open(sys.argv[1]))
print(sum(1 for d in docs if d.get("has_history")
          and d.get("portal_class_id") not in DATAFLOW_APP_CLASSES))
PY
)
n_jsonl=$(find "$HIST" -name '*.jsonl' | wc -l | tr -d ' ')

if [ "$n_jsonl" != "$expected" ]; then
  echo "REFUSING TO BUILD: $n_jsonl .jsonl files, but $SRC expects $expected" >&2
  echo "documents with history. Building now would write a partial Parquet." >&2
  echo >&2
  echo "If a download is still running, wait for it. If the JSONL was deleted" >&2
  echo "to reclaim disk, delete the matching .meta.json files so the download" >&2
  echo "refetches them rather than skipping them:" >&2
  echo "  python3 -c \"import glob,os;j={os.path.basename(p)[:-6] for p in glob.glob('$HIST/*.jsonl')};[os.remove(m) for m in glob.glob('$HIST/*.meta.json') if os.path.basename(m)[:-10] not in j]\"" >&2
  echo >&2
  echo "To build from what is on disk anyway, pass --allow-partial." >&2
  [ "${1:-}" = "--allow-partial" ] || exit 1
  echo "--allow-partial given; building from $n_jsonl of $expected documents." >&2
fi

if [ -f "$OUT" ]; then
  echo "existing $OUT: $(duckdb -noheader -list -c "SELECT count(*) FROM '${OUT}';" 2>/dev/null || echo '?') entries, \
$(duckdb -noheader -list -c "SELECT count(DISTINCT doc_id) FROM '${OUT}';" 2>/dev/null || echo '?') documents"
fi

echo "== building $OUT =="
duckdb -c "
COPY (
  SELECT * FROM read_json('${HIST}/*.jsonl',
    format = 'newline_delimited',
    columns = {
      doc_id: 'VARCHAR', doc_uid: 'VARCHAR', portal_class_id: 'VARCHAR',
      unit: 'VARCHAR', investigation: 'VARCHAR', problem: 'VARCHAR',
      entry_id: 'VARCHAR', idx: 'BIGINT', prev_entry_id: 'VARCHAR',
      created: 'TIMESTAMP', server_created: 'TIMESTAMP',
      model: 'VARCHAR', action_raw: 'VARCHAR', action: 'VARCHAR',
      tile_id: 'VARCHAR', shared_model_id: 'VARCHAR',
      n_records: 'BIGINT', undoable: 'BOOLEAN', is_revert: 'BOOLEAN',
      entry_uid: 'VARCHAR', state: 'VARCHAR', parse_ok: 'BOOLEAN',
      entry_json: 'VARCHAR'
    })
) TO '${OUT}' (FORMAT PARQUET, COMPRESSION ZSTD);
"

echo "== building $DOCS_OUT =="
duckdb -c "
COPY (
  SELECT * FROM read_json('${HIST}/*.meta.json',
    columns = {
      doc_id: 'VARCHAR', expected_max_idx: 'BIGINT', entries_fetched: 'BIGINT',
      min_idx: 'BIGINT', max_idx: 'BIGINT', distinct_idx: 'BIGINT',
      unparsed_entries: 'BIGINT', fetched_at: 'TIMESTAMP'
    })
) TO '${DOCS_OUT}' (FORMAT PARQUET, COMPRESSION ZSTD);
"

echo
echo "== verification =="
duckdb -c "
CREATE VIEW h AS SELECT * FROM '${OUT}';
CREATE VIEW d AS SELECT * FROM '${DOCS_OUT}';

SELECT 'entries (parquet)'        AS check, count(*)::VARCHAR AS value FROM h
UNION ALL SELECT 'entries (sum of per-doc counts)', sum(entries_fetched)::VARCHAR FROM d
UNION ALL SELECT 'documents (parquet)',   count(DISTINCT doc_id)::VARCHAR FROM h
UNION ALL SELECT 'documents (meta files)', count(*)::VARCHAR FROM d
UNION ALL SELECT 'documents with 0 entries', count(*) FILTER (WHERE entries_fetched = 0)::VARCHAR FROM d
UNION ALL SELECT 'students (doc_uid)',     count(DISTINCT doc_uid)::VARCHAR FROM h
UNION ALL SELECT 'portal classes',         count(DISTINCT portal_class_id)::VARCHAR FROM h
UNION ALL SELECT 'distinct tiles',         count(DISTINCT tile_id)::VARCHAR FROM h
UNION ALL SELECT 'parse failures',         count(*) FILTER (WHERE NOT parse_ok)::VARCHAR FROM h
UNION ALL SELECT 'null created',           count(*) FILTER (WHERE created IS NULL)::VARCHAR FROM h
UNION ALL SELECT 'null idx',               count(*) FILTER (WHERE idx IS NULL)::VARCHAR FROM h;
"

echo
echo "== index anomalies (recorded, not errors) =="
duckdb -c "
CREATE VIEW d AS SELECT * FROM '${DOCS_OUT}';
SELECT
  count(*) FILTER (WHERE entries_fetched <> expected_max_idx + 1) AS count_mismatch,
  count(*) FILTER (WHERE distinct_idx < entries_fetched)          AS duplicate_idx,
  count(*) FILTER (WHERE max_idx - min_idx + 1 <> distinct_idx)   AS has_gaps,
  count(*) FILTER (WHERE min_idx <> 0)                            AS not_zero_based,
  count(*)                                                        AS documents
FROM d WHERE entries_fetched > 0;
"

echo
echo "== action mix =="
duckdb -c "
SELECT action, count(*) AS n, round(100.0*count(*)/sum(count(*)) OVER (), 2) AS pct
FROM '${OUT}' GROUP BY action ORDER BY n DESC LIMIT 15;
"

echo
echo "== sizes =="
du -sh "${HIST}" "${OUT}" "${DOCS_OUT}"
