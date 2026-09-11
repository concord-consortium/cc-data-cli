import os
import tempfile
import unittest

import build_population
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet


class TestMainRefusesToDestroyAGoodArtifact(unittest.TestCase):
    """Finding 4 regression: docs/recipes/README.md documents an incident
    where a rebuild silently overwrote 6.27M rows with 110k. build_population
    must write to a temp path, run its population-size gate, and only then
    replace the previous population.parquet -- never destroy a good one
    before the gate has had a chance to refuse."""

    def setUp(self):
        self._prev_local = os.environ.get("CC_DATA_LOCAL")
        self.root = tempfile.mkdtemp()
        os.environ["CC_DATA_LOCAL"] = self.root
        os.makedirs(os.path.join(self.root, "clue-documents"), exist_ok=True)
        self.content = os.path.join(self.root, "clue-documents", "content.parquet")
        self.history = os.path.join(self.root, "clue-documents", "history.parquet")
        self.out = os.path.join(self.root, "derived", "population.parquet")

    def tearDown(self):
        if self._prev_local is None:
            os.environ.pop("CC_DATA_LOCAL", None)
        else:
            os.environ["CC_DATA_LOCAL"] = self._prev_local

    def test_a_failed_population_gate_leaves_the_previous_parquet_intact(self):
        # A previous good run: enough documents to pass the gate.
        content_rows = [
            {"doc_id": "d%d" % i, "doc_key": "d%d" % i, "uid": "u1",
             "portal_class_id": "7", "type": "problem", "unit": "brain",
             "problem": "1.4", "dataflow_tile_deleted": False}
            for i in range(2600)
        ]
        history_rows = [
            history_entry("d%d" % i, "e%d" % i, 0, "2025-01-01 10:00:00",
                          "/addTile", [])
            for i in range(2600)
        ]
        write_parquet(content_rows, self.content, CONTENT_COLUMNS)
        write_parquet(history_rows, self.history, HISTORY_COLUMNS)
        build_population.main()
        good_size = os.path.getsize(self.out)
        self.assertGreater(good_size, 0)

        # A bad rebuild: history.parquet truncated to almost nothing, the
        # exact shape of the incident docs/recipes/README.md describes.
        write_parquet(history_rows[:5], self.history, HISTORY_COLUMNS)
        with self.assertRaises(SystemExit):
            build_population.main()

        # The previous good artifact must still be there, unchanged.
        self.assertTrue(os.path.exists(self.out))
        self.assertEqual(os.path.getsize(self.out), good_size)
        rows = lib.query(
            "SELECT count(*) AS n FROM read_parquet('%s')" % self.out)
        self.assertEqual(rows[0]["n"], 2600)


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
