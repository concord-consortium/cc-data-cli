#!/usr/bin/env bash
# Converts the downloaded document content JSONL into Parquet and verifies it.
#
#   ./build-content-parquet.sh
#
# Column types are declared rather than inferred, for the same reasons as
# build-parquet.sh: `unit` and `problem` are numeric-looking strings, the id
# columns must stay VARCHAR to join against history.parquet and logs.parquet,
# and columns that are null on the two not-found rows would otherwise be typed
# NULL and dropped.

set -euo pipefail
cd "$(dirname "$0")"

IN="content.jsonl"
OUT="content.parquet"

[ -f "$IN" ] || { echo "no $IN" >&2; exit 1; }

echo "== building $OUT =="
duckdb -c "
COPY (
  SELECT * FROM read_json('${IN}',
    format = 'newline_delimited',
    columns = {
      doc_id: 'VARCHAR', doc_key: 'VARCHAR', uid: 'VARCHAR',
      context_id: 'VARCHAR', portal_class_id: 'VARCHAR', type: 'VARCHAR',
      unit: 'VARCHAR', problem: 'VARCHAR', meta_tools: 'VARCHAR[]',
      discovery: 'VARCHAR', dataflow_tile_deleted: 'BOOLEAN',
      found: 'BOOLEAN', change_count: 'BIGINT', version: 'VARCHAR',
      self_uid: 'VARCHAR', self_doc_key: 'VARCHAR', self_class_hash: 'VARCHAR',
      parse_ok: 'BOOLEAN', n_tiles: 'BIGINT', tile_types: 'VARCHAR[]',
      tile_type_counts: 'JSON', n_dataflow_tiles: 'BIGINT', n_rows: 'BIGINT',
      n_shared_models: 'BIGINT', n_annotations: 'BIGINT',
      content_json: 'VARCHAR'
    })
) TO '${OUT}' (FORMAT parquet, COMPRESSION zstd);
"

echo
echo "== verifying =="
duckdb -c "
SELECT count(*) AS docs,
       count(DISTINCT doc_id) AS distinct_ids,
       sum(found::INT) AS found,
       sum((NOT found)::INT) AS missing,
       count(DISTINCT portal_class_id) AS classes,
       count(DISTINCT uid) AS users
FROM '${OUT}';
"
duckdb -c "SELECT type, count(*) AS docs, round(avg(n_tiles), 1) AS mean_tiles,
                  round(avg(n_dataflow_tiles), 2) AS mean_df_tiles,
                  round(avg(change_count), 0) AS mean_changes
           FROM '${OUT}' WHERE found GROUP BY type ORDER BY docs DESC;"
duckdb -c "SELECT t AS tile_type, count(*) AS documents
           FROM '${OUT}', unnest(tile_types) AS u(t)
           WHERE found GROUP BY t ORDER BY documents DESC LIMIT 15;"

# The JSONL row count must survive the conversion.
in_rows=$(wc -l < "$IN" | tr -d ' ')
pq_rows=$(duckdb -noheader -list -c "SELECT count(*) FROM '${OUT}';")
echo
if [ "$in_rows" = "$pq_rows" ]; then
  echo "row counts match: $pq_rows"
else
  echo "MISMATCH: jsonl=$in_rows parquet=$pq_rows" >&2; exit 1
fi
ls -lh "$OUT"
