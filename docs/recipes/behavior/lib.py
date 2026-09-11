#!/usr/bin/env python3
"""Shared helpers for the behaviour-detection recipes.

DuckDB is invoked as a CLI subprocess rather than imported. The Python module
is not installed and these recipes deliberately have no pip dependencies --
same convention as athena_logs.py.
"""
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_LOCAL = os.path.abspath(os.path.join(HERE, "..", "..", "..", "local-data"))


def paths(local=None):
    root = os.path.abspath(local or os.environ.get("CC_DATA_LOCAL", DEFAULT_LOCAL))
    return {
        "root": root,
        "content": os.path.join(root, "clue-documents", "content.parquet"),
        "history": os.path.join(root, "clue-documents", "history.parquet"),
        "logs": os.path.join(root, "log-events", "logs.parquet"),
        "derived": os.path.join(root, "derived"),
    }


def require_file(path):
    if not os.path.exists(path):
        sys.exit("missing input: %s\n(set CC_DATA_LOCAL, or re-run the fetch recipes)" % path)


def run_sql(sql):
    subprocess.run(["duckdb", "-c", sql], check=True)


def query(sql):
    out = subprocess.check_output(["duckdb", "-json", "-c", sql], text=True)
    out = out.strip()
    return json.loads(out) if out else []


def scalar(sql):
    rows = query(sql)
    if not rows:
        return None
    return list(rows[0].values())[0]


def ensure_derived(local=None):
    d = paths(local)["derived"]
    os.makedirs(d, exist_ok=True)
    return d


def write_parquet(rows, path, columns):
    """Write dict rows to Parquet via the duckdb CLI, with declared types.

    Types are declared rather than inferred for the same reason the existing
    build scripts declare them: read_json samples rows, so numeric-looking id
    strings get inferred as integers and every downstream join breaks.

    Used by the tests to build fixtures, and by build_candidates.py to write
    its output from Python-side rows.
    """
    jsonl = path + ".jsonl"
    with open(jsonl, "w") as handle:
        for row in rows:
            handle.write(json.dumps(row, default=str) + "\n")
    cols = ", ".join("%s: '%s'" % (k, v) for k, v in columns.items())
    subprocess.run(
        ["duckdb", "-c",
         "COPY (SELECT * FROM read_json('%s', format='newline_delimited', "
         "columns={%s})) TO '%s' (FORMAT parquet);" % (jsonl, cols, path)],
        check=True, capture_output=True)
    os.remove(jsonl)
