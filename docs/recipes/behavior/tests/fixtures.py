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

# build_sensor_trials.py reads the document's content snapshot to learn which
# nodes are sensors, so its fixtures need the two columns the other stages
# ignore.
CONTENT_SENSOR_COLUMNS = dict(
    CONTENT_COLUMNS, content_json="VARCHAR", parse_ok="BOOLEAN")

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


def sensor_node(node_id, sensor="00008VIR", sensor_type="emg-reading"):
    """One Sensor node as it appears in a Dataflow program's `nodes` map.

    `sensor` is the device binding: a serial or pin for a physical device, a
    `SIM*` key when the reading comes from a Simulation tile, absent when the
    student has not chosen one.
    """
    data = {"type": "Sensor", "plot": False, "sensorType": sensor_type}
    if sensor is not None:
        data["sensor"] = sensor
    return {"id": node_id, "name": "Sensor", "data": data}


def dataflow_content_json(nodes, tile_id="tileA"):
    """A content snapshot holding one Dataflow tile with the given nodes."""
    return json.dumps({
        "tileMap": {
            tile_id: {
                "id": tile_id,
                "content": {
                    "type": "Dataflow",
                    "program": {"nodes": {n["id"]: n for n in nodes}},
                },
            },
        },
    })


def tick_entry(doc_id, entry_id, idx, created, values,
               tick_id=None, tile_id="tileA", legacy=None):
    """One `tickAndProcess` history entry writing a nodeValue per node.

    `values` maps node id to the reading, which is a string in the real data
    ("0", "212", "NaN") rather than a number.

    `legacy` maps node id to a value for the sibling `replace` patch on
    `tickEntries/legacyTick/nodeValue` that real entries also carry. It
    restates an already-recorded reading, so a detector must not treat it as
    a reading of its own.
    """
    tick = tick_id or ("tick%s" % entry_id)
    patches = [
        {"op": "add",
         "path": "/content/tileMap/%s/content/program/nodes/%s"
                 "/data/tickEntries/%s" % (tile_id, node_id, tick),
         "value": {"open": True, "nodeValue": value}}
        for node_id, value in values.items()
    ]
    for node_id, value in (legacy or {}).items():
        patches.append(
            {"op": "replace",
             "path": "/content/tileMap/%s/content/program/nodes/%s"
                     "/data/tickEntries/legacyTick/nodeValue" % (tile_id, node_id),
             "value": value})
    return history_entry(
        doc_id, entry_id, idx, created,
        "/content/tileMap/%s/content/program/tickAndProcess" % tile_id,
        patches, tile_id=tile_id)
