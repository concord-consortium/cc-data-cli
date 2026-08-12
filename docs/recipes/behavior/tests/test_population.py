import os
import tempfile
import unittest

import build_population
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet


class TestPopulation(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.content = os.path.join(self.dir, "content.parquet")
        self.history = os.path.join(self.dir, "history.parquet")
        self.out = os.path.join(self.dir, "population.parquet")

    def _content(self, rows):
        write_parquet(rows, self.content, CONTENT_COLUMNS)

    def _history(self, rows):
        write_parquet(rows, self.history, HISTORY_COLUMNS)

    def test_selects_problem_documents_that_have_history(self):
        self._content([
            {"doc_id": "d1", "doc_key": "d1", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
            {"doc_id": "d2", "doc_key": "d2", "uid": "u1", "portal_class_id": "7",
             "type": "publication", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
            {"doc_id": "d3", "doc_key": "d3", "uid": "u2", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
        ])
        self._history([
            history_entry("d1", "e1", 0, "2025-01-01 10:00:00", "/addTile", []),
            history_entry("d2", "e2", 0, "2025-01-01 10:00:00", "/addTile", []),
        ])
        build_population.build(self.content, self.history, self.out)
        rows = lib.query("SELECT doc_id FROM read_parquet('%s') ORDER BY doc_id" % self.out)
        # d2 is a publication, d3 has no history
        self.assertEqual([r["doc_id"] for r in rows], ["d1"])

    def test_flags_documents_whose_clock_runs_backwards(self):
        self._content([
            {"doc_id": "ok", "doc_key": "ok", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
            {"doc_id": "bad", "doc_key": "bad", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
        ])
        self._history([
            history_entry("ok", "a1", 0, "2025-01-01 10:00:00", "/addTile", []),
            history_entry("ok", "a2", 1, "2025-01-01 10:00:30", "/addTile", []),
            # a 30s backstep is under the 60s bar, so this document stays clean
            history_entry("ok", "a3", 2, "2025-01-01 10:00:10", "/addTile", []),
            history_entry("bad", "b1", 0, "2025-01-01 10:00:00", "/addTile", []),
            # a 10-minute backstep is over the bar
            history_entry("bad", "b2", 1, "2025-01-01 09:50:00", "/addTile", []),
        ])
        build_population.build(self.content, self.history, self.out)
        rows = lib.query(
            "SELECT doc_id, clock_suspect FROM read_parquet('%s') ORDER BY doc_id" % self.out)
        flags = {r["doc_id"]: r["clock_suspect"] for r in rows}
        self.assertEqual(flags, {"bad": True, "ok": False})


if __name__ == "__main__":
    unittest.main()
