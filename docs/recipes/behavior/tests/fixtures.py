"""Synthetic fixtures for the behaviour-detection tests.

Everything here is hand-authored. Real history entries carry student-written
text inside patch values, and this repository is public.
"""
import json

from lib import write_parquet  # noqa: F401  -- re-exported for the tests

HISTORY_COLUMNS = {
    "doc_id": "VARCHAR", "entry_id": "VARCHAR", "idx": "BIGINT",
    "created": "TIMESTAMP", "server_created": "TIMESTAMP",
    "action": "VARCHAR", "tile_id": "VARCHAR", "is_revert": "BOOLEAN",
    "entry_json": "VARCHAR",
}

CONTENT_COLUMNS = {
    "doc_id": "VARCHAR", "doc_key": "VARCHAR", "uid": "VARCHAR",
    "portal_class_id": "VARCHAR", "type": "VARCHAR", "unit": "VARCHAR",
    "problem": "VARCHAR", "dataflow_tile_deleted": "BOOLEAN",
}

LOG_COLUMNS = {
    "doc_key": "VARCHAR", "user_id": "VARCHAR", "session": "VARCHAR",
    "event": "VARCHAR", "event_time": "TIMESTAMP WITH TIME ZONE",
    "parameters": "VARCHAR", "extras": "VARCHAR",
}


def history_entry(doc_id, entry_id, idx, created, action, patches,
                  tile_id="tileA", is_revert=False, server_created=None):
    """One synthetic history row whose entry_json matches the real shape."""
    entry = {
        "id": entry_id,
        "action": action,
        "created": created,
        "records": [{"action": action, "patches": patches, "inversePatches": []}],
    }
    return {
        "doc_id": doc_id, "entry_id": entry_id, "idx": idx,
        "created": created, "server_created": server_created or created,
        "action": action, "tile_id": tile_id, "is_revert": is_revert,
        "entry_json": json.dumps(entry),
    }
