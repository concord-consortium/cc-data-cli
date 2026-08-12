import os
import tempfile
import unittest

import build_edits
import build_population
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet

T = "/content/tileMap/tileA/content"


def patch(op, path, value=None):
    p = {"op": op, "path": path}
    if value is not None:
        p["value"] = value
    return p


class TestEdits(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.content = os.path.join(self.dir, "content.parquet")
        self.history = os.path.join(self.dir, "history.parquet")
        self.pop = os.path.join(self.dir, "population.parquet")
        self.out = os.path.join(self.dir, "edits.parquet")
        write_parquet([
            {"doc_id": "d1", "doc_key": "d1", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
        ], self.content, CONTENT_COLUMNS)

    def _run(self, entries):
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_edits.build(self.history, self.pop, self.out)
        return lib.query(
            "SELECT class, op, target_id, n_patches FROM read_parquet('%s') "
            "ORDER BY started, class, target_id" % self.out)

    def test_classifies_each_kind_of_patch(self):
        rows = self._run([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00",
                          T + "/setProgram",
                          [patch("add", T + "/program/nodes/n1")]),
            history_entry("d1", "e2", 1, "2025-01-01 10:00:10",
                          T + "/setProgram",
                          [patch("add", T + "/program/nodes/n2/inputs/tilt")]),
            history_entry("d1", "e3", 2, "2025-01-01 10:00:20",
                          T + "/setProgram",
                          [patch("replace", T + "/program/nodes/n1/data/threshold", 30)]),
            history_entry("d1", "e4", 3, "2025-01-01 10:00:30",
                          T + "/setProgramZoom",
                          [patch("replace", T + "/programZoom/dx", 5)]),
            history_entry("d1", "e5", 4, "2025-01-01 10:00:40",
                          T + "/setSlate",
                          [patch("replace", T + "/text", "notes")]),
        ])
        got = [(r["class"], r["target_id"]) for r in rows]
        self.assertEqual(got, [
            ("structure", "n1"),
            ("structure", "n2"),
            ("parameter", "n1"),
            ("layout", "tileA"),
            ("documentation", "tileA"),
        ])

    def test_runtime_writes_are_never_student_changes(self):
        """The three traps from the design, asserted individually."""
        rows = self._run([
            # the Simulation updating itself
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00",
                          "/content/sharedModelMap/sm1/sharedModel/variables/3/setValue",
                          [patch("replace",
                                 "/content/sharedModelMap/sm1/sharedModel/variables/3/value", 1)]),
            # Dataflow recording sensor readings into a table
            history_entry("d1", "e2", 1, "2025-01-01 10:00:10",
                          "/content/sharedModelMap/sm1/sharedModel/dataSet/addCanonicalCasesWithIDs",
                          [patch("add",
                                 "/content/sharedModelMap/sm1/sharedModel/dataSet/cases/49")]),
            # the program's computed output
            history_entry("d1", "e3", 2, "2025-01-01 10:00:20",
                          T + "/setProgram",
                          [patch("replace", T + "/program/values/6/currentValues/nodeValue", 2)]),
        ])
        self.assertEqual({r["class"] for r in rows}, {"runtime"})

    def test_ordered_display_name_is_derived_not_a_parameter_change(self):
        rows = self._run([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00",
                          T + "/setProgram",
                          [patch("replace",
                                 T + "/program/nodes/n1/data/orderedDisplayName", "Number 1")]),
        ])
        self.assertEqual([r["class"] for r in rows], ["derived"])

    def test_student_table_editing_is_documentation(self):
        rows = self._run([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00",
                          T + "/setCanonicalCaseValues",
                          [patch("replace",
                                 "/content/sharedModelMap/sm1/sharedModel/dataSet"
                                 "/attributes/1/values/3", "7")]),
        ])
        self.assertEqual([r["class"] for r in rows], ["documentation"])

    def test_coalesces_one_gesture_into_one_operation(self):
        """Sub-second repeats of the same op on the same target are one change."""
        rows = self._run([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00.000",
                          T + "/setProgram",
                          [patch("replace", T + "/program/nodes/n1/data/threshold", 10)]),
            history_entry("d1", "e2", 1, "2025-01-01 10:00:00.400",
                          T + "/setProgram",
                          [patch("replace", T + "/program/nodes/n1/data/threshold", 20)]),
            history_entry("d1", "e3", 2, "2025-01-01 10:00:01.100",
                          T + "/setProgram",
                          [patch("replace", T + "/program/nodes/n1/data/threshold", 30)]),
            # 9 seconds later: a separate deliberate change
            history_entry("d1", "e4", 3, "2025-01-01 10:00:10.000",
                          T + "/setProgram",
                          [patch("replace", T + "/program/nodes/n1/data/threshold", 40)]),
        ])
        self.assertEqual([(r["class"], r["n_patches"]) for r in rows],
                         [("parameter", 3), ("parameter", 1)])

    def test_unmatched_paths_land_in_other_rather_than_vanishing(self):
        rows = self._run([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00",
                          "/content/somethingNew",
                          [patch("replace", "/content/somethingNew/field", 1)]),
        ])
        self.assertEqual([r["class"] for r in rows], ["other"])


if __name__ == "__main__":
    unittest.main()
