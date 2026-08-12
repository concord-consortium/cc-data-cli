#!/usr/bin/env bash
# Converts the direct-Athena log pull into Parquet, joining the portal learner
# metadata that the report server would have joined in Athena.
#
#   ./build-parquet.sh
#
# Inputs:
#   clue_dataflow_logs.csv  log rows (athena_logs.py)
#   learner_keys.csv        one row per learner incl. secure_key (learner_keys.rb)
#
# Column types are declared explicitly. Inference would type the numeric-looking
# id columns as BIGINT, which breaks joins against the history dataset where the
# same ids are VARCHAR, and would guess wrong on `parameters`/`extras`, which are
# JSON blobs that are empty on most rows.

set -euo pipefail
cd "$(dirname "$0")"

OUT="logs.parquet"

for f in clue_dataflow_logs.csv learner_keys.csv; do
  [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
done

echo "== building $OUT =="
duckdb -c "
COPY (
  SELECT
    log.id, log.session, log.application, log.activity, log.event,
    log.event_value, log.time, log.parameters, log.extras,
    log.run_remote_endpoint, log.timestamp,
    lk.activity_id, lk.learner_id, lk.student_id, lk.class_id, lk.class_name,
    lk.school_name, lk.user_id, lk.primary_user_id, lk.offering_id,
    lk.runnable_url,
    -- unit and problem are query parameters on the runnable URL
    regexp_extract(lk.runnable_url, 'unit=([^&]+)', 1) AS unit,
    regexp_extract(lk.runnable_url, 'problem=([^&]+)', 1) AS problem,
    -- CLUE puts the document and tile identity inside the parameters JSON.
    -- Lifted to columns because they are the join to content.parquet
    -- (doc_key) and history.parquet (doc_id, tile_id); leaving them buried
    -- means every downstream query re-parses the JSON. Null on events with no
    -- document context (logins, navigation).
    json_extract_string(log.parameters, '\$.documentKey')   AS doc_key,
    json_extract_string(log.parameters, '\$.documentType')  AS doc_type,
    json_extract_string(log.parameters, '\$.documentUid')   AS doc_uid,
    json_extract_string(log.parameters, '\$.tileId')        AS tile_id,
    -- log `time` is UNIX seconds, not milliseconds; dividing by 1000 silently
    -- yields January 1970 rather than an error
    to_timestamp(log.time) AS event_time
  FROM read_csv('clue_dataflow_logs.csv',
    header = true, quote = '\"', escape = '\"',
    columns = {
      id: 'VARCHAR', session: 'VARCHAR', application: 'VARCHAR',
      activity: 'VARCHAR', event: 'VARCHAR', event_value: 'VARCHAR',
      time: 'DOUBLE', parameters: 'VARCHAR', extras: 'VARCHAR',
      run_remote_endpoint: 'VARCHAR', timestamp: 'VARCHAR'
    }) log
  JOIN read_csv('learner_keys.csv',
    header = true, quote = '\"', escape = '\"',
    columns = {
      activity_id: 'VARCHAR', learner_id: 'VARCHAR', student_id: 'VARCHAR',
      class_id: 'VARCHAR', class_name: 'VARCHAR', school_name: 'VARCHAR',
      user_id: 'VARCHAR', primary_user_id: 'VARCHAR', offering_id: 'VARCHAR',
      username: 'VARCHAR', last_run: 'VARCHAR', runnable_url: 'VARCHAR',
      secure_key: 'VARCHAR'
    }) lk
    ON lk.secure_key = regexp_extract(log.run_remote_endpoint, '([^/]+)\$', 1)
) TO '${OUT}' (FORMAT parquet, COMPRESSION zstd);
"

echo
echo "== verifying =="
duckdb -c "
SELECT count(*) AS rows,
       count(DISTINCT id) AS distinct_ids,
       count(DISTINCT user_id) AS users,
       count(DISTINCT class_id) AS classes,
       count(DISTINCT activity_id) AS activities,
       count(DISTINCT unit) AS units
FROM '${OUT}';
"
duckdb -c "SELECT unit, count(*) AS rows, count(DISTINCT user_id) AS users
           FROM '${OUT}' GROUP BY unit ORDER BY rows DESC;"
duckdb -c "SELECT event, count(*) AS n FROM '${OUT}'
           GROUP BY event ORDER BY n DESC LIMIT 15;"

# The CSV row count must survive the join; a mismatch means a log row had no
# matching learner, which should be impossible and would silently lose data.
csv_rows=$(duckdb -noheader -list -c "
  SELECT count(*) FROM read_csv('clue_dataflow_logs.csv', header = true,
    quote = '\"', escape = '\"', all_varchar = true);")
pq_rows=$(duckdb -noheader -list -c "SELECT count(*) FROM '${OUT}';")
echo
if [ "$csv_rows" = "$pq_rows" ]; then
  echo "row counts match: $pq_rows"
else
  echo "MISMATCH: csv=$csv_rows parquet=$pq_rows" >&2; exit 1
fi
ls -lh "$OUT"
