#!/usr/bin/env python3
"""Fetch CLUE log events straight from Athena, bypassing the report server.

Why this exists: the report server's generated SQL constrains only the
secure_key partition, so every learner expands across all 15 `app` values and
all 37x12 year/month combinations -- ~6,660 S3 prefixes per learner. Runs over a
few hundred learners spend 30 minutes listing prefixes and time out. See
docs/report-service-log-query-issues.md.

This adds `app = 'CLUE'` and a year bound, which the form cannot express, and
chunks the secure keys so each query stays small. Learner metadata is NOT joined
here -- we already have it in learner_keys.csv and join it locally, which also
avoids depending on the S3 learner files the report server uploads.

  ./athena_logs.py --activities 2735,2739 --out validate_2285.csv
  ./athena_logs.py --activities 2460 --out brain_2460.csv

Reads learner_keys.csv (produced by learner_keys.rb) from the script directory.
"""

import argparse, csv, io, json, os, subprocess, sys, time

# Dataflow log rows carry serialised program state in `parameters`/`extras`;
# real rows exceed csv's default 128 KB field limit.
csv.field_size_limit(sys.maxsize)

HERE = os.path.dirname(os.path.abspath(__file__))
# report-service names each user's workgroup "<portal_server> <user_id> <email>"
# with every non-[a-z0-9] character replaced by "-" (athena_db.ex:103). Yours is
# not mine: set CC_ATHENA_WORKGROUP, or find it with
#   aws athena list-work-groups --query 'WorkGroups[].Name' --output text | tr '\t' '\n' | grep <you>
WORKGROUP = os.environ.get("CC_ATHENA_WORKGROUP")
if not WORKGROUP:
    sys.exit("set CC_ATHENA_WORKGROUP (see comment above)")
DATABASE = "log_ingester_production"
TABLE = '"log_ingester_production"."logs_by_app_and_secure_key"'

# The log columns the student-actions report selects. The learner columns are
# joined locally instead.
LOG_COLS = ["id", "session", "application", "activity", "event", "event_value",
            "time", "parameters", "extras", "run_remote_endpoint", "timestamp"]


def aws(*args, parse=True):
    out = subprocess.check_output(["aws"] + list(args) + ["--output", "json"])
    return json.loads(out) if parse else out


def build_sql(keys, year_from, year_to, app):
    key_list = ", ".join("'%s'" % k for k in keys)
    cols = ", ".join('log."%s" AS %s' % (c, c) for c in LOG_COLS)
    return (
        f"SELECT {cols}\n"
        f"FROM {TABLE} log\n"
        f"WHERE log.app = '{app}'\n"
        f"  AND log.year BETWEEN {year_from} AND {year_to}\n"
        f"  AND log.secure_key IN ({key_list})"
    )


def start(sql):
    r = aws("athena", "start-query-execution", "--query-string", sql,
            "--work-group", WORKGROUP,
            "--query-execution-context", f"Database={DATABASE}")
    return r["QueryExecutionId"]


def wait(qid, poll=10):
    while True:
        r = aws("athena", "get-query-execution", "--query-execution-id", qid)
        st = r["QueryExecution"]["Status"]
        if st["State"] in ("SUCCEEDED", "FAILED", "CANCELLED"):
            return st, r["QueryExecution"].get("Statistics", {}), \
                r["QueryExecution"]["ResultConfiguration"]["OutputLocation"]
        time.sleep(poll)


def fetch_result(output_location):
    """Athena writes results as a single CSV object at <output>/<qid>.csv."""
    return subprocess.check_output(["aws", "s3", "cp", output_location, "-"])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--activities", required=True,
                    help="comma-separated portal activity (runnable) ids")
    ap.add_argument("--out", required=True)
    ap.add_argument("--chunk", type=int, default=150,
                    help="secure keys per query; keeps prefix probing bounded")
    ap.add_argument("--year-from", type=int, default=2022)
    ap.add_argument("--year-to", type=int, default=2026)
    ap.add_argument("--app", default="CLUE")
    args = ap.parse_args()

    wanted = set(args.activities.split(","))
    with open(os.path.join(HERE, "learner_keys.csv")) as f:
        rows = [r for r in csv.DictReader(f) if r["activity_id"] in wanted]
    keys = sorted({r["secure_key"] for r in rows})
    if not keys:
        sys.exit("no learners for those activities")
    print(f"{len(rows)} learner rows, {len(keys)} distinct secure keys", file=sys.stderr)

    chunks = [keys[i:i + args.chunk] for i in range(0, len(keys), args.chunk)]
    print(f"{len(chunks)} chunk(s) of up to {args.chunk}", file=sys.stderr)

    # Each chunk lands in its own file, written via .tmp + rename, so a partial
    # file is never mistaken for a complete one and a re-run resumes.
    cache = os.path.join(HERE, "chunks")
    os.makedirs(cache, exist_ok=True)

    total_scanned = 0.0
    for i, chunk in enumerate(chunks, 1):
        path = os.path.join(cache, f"chunk_{i:03d}.csv")
        if os.path.exists(path):
            print(f"  chunk {i}/{len(chunks)} cached", file=sys.stderr)
            continue
        sql = build_sql(chunk, args.year_from, args.year_to, args.app)
        st, stats, loc = wait(start(sql))
        secs = round((stats.get("TotalExecutionTimeInMillis") or 0) / 1000)
        mb = round((stats.get("DataScannedInBytes") or 0) / 1e6, 1)
        total_scanned += mb
        if st["State"] != "SUCCEEDED":
            print(f"  chunk {i}/{len(chunks)} {st['State']}: "
                  f"{st.get('StateChangeReason','')[:160]}", file=sys.stderr)
            sys.exit(1)
        with open(path + ".tmp", "wb") as f:
            f.write(fetch_result(loc))
        os.rename(path + ".tmp", path)
        n = sum(1 for _ in csv.reader(open(path))) - 1
        print(f"  chunk {i}/{len(chunks)} ok  {n} rows  {secs}s  {mb} MB",
              file=sys.stderr)

    header, total = None, 0
    with open(args.out, "w", newline="") as out:
        w = csv.writer(out)
        for i in range(1, len(chunks) + 1):
            with open(os.path.join(cache, f"chunk_{i:03d}.csv"), newline="") as f:
                rdr = csv.reader(f)
                h = next(rdr)
                if header is None:
                    header = h
                    w.writerow(header)
                elif h != header:
                    sys.exit(f"chunk {i} header differs from chunk 1")
                for row in rdr:
                    w.writerow(row)
                    total += 1
    print(f"\n{total} rows -> {args.out}  ({total_scanned:.1f} MB scanned this run)",
          file=sys.stderr)


if __name__ == "__main__":
    main()
