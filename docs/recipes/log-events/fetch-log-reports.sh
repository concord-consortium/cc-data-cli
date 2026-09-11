#!/usr/bin/env bash
# Polls the student-actions-with-metadata runs covering the 15 Dataflow
# activities, then downloads each CSV once Athena finishes.
#
# Athena caps a query at 1,000,000 projected partitions and the generated SQL
# constrains only secure_key, so a run covering more than ~150 learners fails
# unless a date range is set (see RESUME-STATE.md). The 15 activities are split
# across 8 runs that partition the set with no overlap, so the CSVs concatenate
# cleanly.
#
#   ./fetch-log-reports.sh

set -uo pipefail
cd "$(dirname "$0")"

TOKEN=$(tr -d '[:space:]' < ../.report-token)
BASE=https://report-server.concord.org
# Default: the two original successes plus the six re-runs that added a date
# range. Override by passing run ids as arguments.
RUNS=(2285 2286 2293 2294 2295 2296 2297 2298)
[ "$#" -gt 0 ] && RUNS=("$@")

api() { curl -s -H "Authorization: Bearer $TOKEN" "$BASE$1"; }

echo "waiting for ${#RUNS[@]} runs to finish..."
while :; do
  pending=0; line=""
  for id in "${RUNS[@]}"; do
    st=$(api "/api/v1/reports/$id" | python3 -c "import json,sys;print(json.load(sys.stdin).get('athena_query_state'))" 2>/dev/null)
    line+="$id=$st "
    [ "$st" = "running" ] && pending=$((pending+1))
  done
  echo "  $line"
  [ "$pending" -eq 0 ] && break
  sleep 30
done

echo
echo "downloading..."
ok=0; bad=0
for id in "${RUNS[@]}"; do
  env=$(api "/api/v1/reports/$id/download")
  url=$(printf '%s' "$env" | python3 -c "import json,sys;print(json.load(sys.stdin).get('download_url',''))" 2>/dev/null)
  if [ -z "$url" ]; then
    echo "  run $id: NO URL -> $(printf '%s' "$env" | head -c 160)"
    bad=$((bad+1)); continue
  fi
  # the presigned URL is a bare S3 capability; it must not carry the bearer token
  curl -s -o "run_${id}.csv" "$url"
  rows=$(( $(wc -l < "run_${id}.csv") - 1 ))
  echo "  run $id: $rows data rows -> run_${id}.csv"
  ok=$((ok+1))
done

echo
echo "downloaded $ok, failed $bad"
[ "$ok" -gt 0 ] && echo "headers identical across files: $(
  for f in run_*.csv; do head -1 "$f"; done | sort -u | wc -l | tr -d ' '
) distinct header(s)"
