import os
import tempfile
import unittest

import build_population
import build_trials
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet

SET_VALUE = "/content/sharedModelMap/sm1/sharedModel/variables/2/setValue"
VALUE_PATH = "/content/sharedModelMap/sm1/sharedModel/variables/2/value"


class TestTrials(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.content = os.path.join(self.dir, "content.parquet")
        self.history = os.path.join(self.dir, "history.parquet")
        self.pop = os.path.join(self.dir, "population.parquet")
        self.out = os.path.join(self.dir, "trials.parquet")
        write_parquet([
            {"doc_id": "d1", "doc_key": "d1", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
        ], self.content, CONTENT_COLUMNS)

    def _run(self, samples):
        """samples: list of (created, value). Each becomes one setValue entry."""
        entries = [
            history_entry("d1", "e%d" % i, i, created, SET_VALUE,
                          [{"op": "replace", "path": VALUE_PATH, "value": value}])
            for i, (created, value) in enumerate(samples)
        ]
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_trials.build(self.history, self.pop, self.out)
        return lib.query(
            "SELECT trial_id, n_changes, duration_s, source "
            "FROM read_parquet('%s') ORDER BY started" % self.out)

    def test_a_constant_input_produces_no_trial(self):
        """The slider sitting still is not a trial, however many times the
        value is rewritten."""
        rows = self._run([
            ("2025-01-01 10:00:00", 0), ("2025-01-01 10:00:01", 0),
            ("2025-01-01 10:00:02", 0), ("2025-01-01 10:00:03", 0),
        ])
        self.assertEqual(rows, [])

    def test_a_run_of_changes_becomes_one_trial(self):
        rows = self._run([
            ("2025-01-01 10:00:00", 0),
            ("2025-01-01 10:00:01", 10),
            ("2025-01-01 10:00:02", 20),
            ("2025-01-01 10:00:03", 30),
        ])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_changes"], 3)
        self.assertAlmostEqual(rows[0]["duration_s"], 2.0, places=1)
        self.assertEqual(rows[0]["source"], "simulation")

    def test_static_changing_static_changing_gives_two_trials(self):
        """The shape this task exists to detect: the student exercises the
        program, stops, then exercises it again."""
        rows = self._run([
            ("2025-01-01 10:00:00", 0),
            ("2025-01-01 10:00:01", 10),
            ("2025-01-01 10:00:02", 20),
            # a minute of stillness
            ("2025-01-01 10:01:10", 20),
            ("2025-01-01 10:01:11", 20),
            # exercised again
            ("2025-01-01 10:02:20", 40),
            ("2025-01-01 10:02:21", 50),
        ])
        self.assertEqual([r["n_changes"] for r in rows], [2, 2])

    def test_changes_separated_by_less_than_the_gap_stay_one_trial(self):
        """A student pausing briefly mid-flex has not started a new trial."""
        rows = self._run([
            ("2025-01-01 10:00:00", 0),
            ("2025-01-01 10:00:01", 10),
            ("2025-01-01 10:00:20", 20),
            ("2025-01-01 10:00:25", 30),
        ])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_changes"], 3)

    def test_trial_id_is_written_as_bigint(self):
        """sum() over an INTEGER yields HUGEINT, which Parquet cannot store, so
        COPY silently downcasts the column to DOUBLE. Same trap as Task 3."""
        self._run([
            ("2025-01-01 10:00:00", 0), ("2025-01-01 10:00:01", 10),
        ])
        types = {r["column_name"]: r["column_type"] for r in lib.query(
            "DESCRIBE SELECT * FROM read_parquet('%s')" % self.out)}
        self.assertEqual(types["trial_id"], "BIGINT")


if __name__ == "__main__":
    unittest.main()
