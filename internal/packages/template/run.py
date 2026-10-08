#!/usr/bin/env python3
"""A Researcher Dashboard package: counts a class's answers and the learners who wrote them.

It runs the same way under `cc-data package run` on a laptop and on the dashboard's VM. It
reads the scope from RD_SCOPE_FILE, pulls its own data into RD_DATASET with cc-data, counts
through cc-data's views, and writes display.md, summary.txt and counts.json to the scope's
output_dir. Stdlib only: the VM installs no Python packages.

It reads the class through a Student ID Mapping run, which the portal computes on every
download, so re-reading it picks up learners who joined since, and which has no limit on how
many assignments a class has. It reuses the researcher's own run for the class when there is
one and otherwise creates one, on the server the dataset's portal names.
"""
import json
import os
import subprocess
import sys


def cc_data(*args):
    result = subprocess.run(["cc-data", *args], capture_output=True, text=True)
    if result.returncode != 0:
        detail = " ".join(p for p in (result.stderr.strip(), result.stdout.strip()) if p)
        raise RuntimeError(f"cc-data {' '.join(args[:2])} exited {result.returncode}: {detail or '(no output)'}")
    return result.stdout


def query(dataset, sql):
    return json.loads(cc_data("query", "--dataset", dataset, sql, "--format", "json"))


def mapping_run(portal, class_id):
    """The newest Student ID Mapping run filtered to exactly this class, else a new one."""
    runs = json.loads(cc_data("reports", "list", "--portal", portal, "--json"))["runs"]
    mine = [r["run_id"] for r in runs
            if r.get("slug") == "student-id-mapping"
            and (r.get("report_filter") or {}).get("filters") == ["class"]
            and (r.get("report_filter") or {}).get("class") == [class_id]]
    if mine:
        return max(int(run) for run in mine)
    created = json.loads(cc_data(
        "reports", "create", "--portal", portal, "--report-slug", "student-id-mapping",
        "--report-filter", json.dumps({"class": [class_id]}), "--json"))
    return int(created["run"]["run_id"])


def main():
    with open(os.environ["RD_SCOPE_FILE"], encoding="utf-8") as handle:
        scope = json.load(handle)
    # RD_DATASET is the full <portal>/<name> ref; scope["dataset"] is the bare name.
    dataset = os.environ["RD_DATASET"]
    class_id = int(scope["classes"][0]["class_id"])

    try:
        cc_data("dataset", "create", dataset)
    except RuntimeError as err:
        if "exists" not in str(err).lower():
            raise
    run = mapping_run(os.environ["CC_DATA_PORTAL"], class_id)
    cc_data("get", "report", str(run), "--dataset", dataset, "--refresh")
    cc_data("get", "answers", str(run), "--dataset", dataset)

    rows = query(dataset, f"SELECT count(*) AS n FROM report_{run}")[0]["n"]
    if rows == 0:
        raise RuntimeError("no report-service access to this class, or the class has no data")
    answers = query(dataset, f"SELECT count(*) AS n FROM run_answers WHERE run_id = {run}")[0]["n"]
    # By user_id, never by endpoint: a student has one endpoint per assignment.
    learners = query(dataset, f"""
        SELECT count(DISTINCT m.user_id) AS n FROM student_id_mapping m
        JOIN run_answers a ON a.remote_endpoint = m.run_remote_endpoint
        WHERE a.run_id = {run}""")[0]["n"]

    out = scope["output_dir"]
    with open(os.path.join(out, "display.md"), "w", encoding="utf-8") as handle:
        handle.write(f"# Class {class_id}\n\n- {answers} answers\n- {learners} learners with at least one answer\n")
    with open(os.path.join(out, "summary.txt"), "w", encoding="utf-8") as handle:
        handle.write(f"{answers} answers from {learners} learners\n")
    with open(os.path.join(out, "counts.json"), "w", encoding="utf-8") as handle:
        json.dump({"answers": answers}, handle)


if __name__ == "__main__":
    try:
        main()
    except Exception as err:
        print(str(err), file=sys.stderr)
        sys.exit(1)
