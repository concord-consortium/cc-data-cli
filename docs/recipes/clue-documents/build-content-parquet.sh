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
META="metadata.jsonl"
OUT="content.parquet"

[ -f "$IN" ] || { echo "no $IN" >&2; exit 1; }
# The Firestore metadata is optional so this script still works before
# download-metadata.ts has been run; the offering columns come out NULL.
[ -f "$META" ] || echo "note: no $META -- offering_id will be null (run download-metadata.ts)" >&2

echo "== building $OUT =="
duckdb -c "
COPY (
  SELECT c.*,
         m.offering_id, m.rtdb_offering_id, m.fs_investigation, m.fs_problem,
         m.fs_unit, m.visibility, m.doc_kind, m.network
  FROM read_json('${IN}',
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
    }) c
  -- LEFT JOIN, and metadata.jsonl is one row per doc_key (asserted below), so
  -- the row count cannot change. \`kind\` is renamed \`doc_kind\` to keep it
  -- clear of the episode 'kind' used in the behaviour recipes.
  LEFT JOIN (
    SELECT doc_key, offering_id, rtdb_offering_id, fs_investigation, fs_problem,
           fs_unit, visibility, kind AS doc_kind, network
    FROM read_json('${META}',
      format = 'newline_delimited',
      columns = {
        doc_key: 'VARCHAR', fs_doc_id: 'VARCHAR', offering_id: 'VARCHAR',
        fs_unit: 'VARCHAR', fs_investigation: 'VARCHAR', fs_problem: 'VARCHAR',
        fs_context_id: 'VARCHAR', fs_uid: 'VARCHAR', fs_type: 'VARCHAR',
        network: 'VARCHAR', visibility: 'VARCHAR', kind: 'VARCHAR',
        strategies: 'VARCHAR[]', n_matches: 'BIGINT',
        rtdb_offering_id: 'VARCHAR', rtdb_metadata_found: 'BOOLEAN',
        found: 'BOOLEAN'
      })
  ) m USING (doc_key)
) TO '${OUT}' (FORMAT parquet, COMPRESSION zstd);
"

if [ -f "$META" ]; then
  meta_rows=$(wc -l < "$META" | tr -d ' ')
  meta_keys=$(duckdb -noheader -list -c "SELECT count(DISTINCT doc_key) FROM read_json('${META}', format='newline_delimited', columns={doc_key: 'VARCHAR'});")
  if [ "$meta_rows" != "$meta_keys" ]; then
    echo "MISMATCH: $META has $meta_rows rows but $meta_keys distinct keys -- the join would fan out" >&2
    exit 1
  fi
fi

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
