import os
import tempfile
import unittest

import build_population
import build_presence
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet

TICK = "/content/tileMap/tileA/content/step"


class TestPresence(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.content = os.path.join(self.dir, "content.parquet")
        self.history = os.path.join(self.dir, "history.parquet")
        self.pop = os.path.join(self.dir, "population.parquet")
        self.out = os.path.join(self.dir, "presence.parquet")
        write_parquet([
            {"doc_id": "d1", "doc_key": "d1", "uid": "u1", "portal_class_id": "7",
             "type": "problem", "unit": "brain", "problem": "1.4",
             "dataflow_tile_deleted": False},
        ], self.content, CONTENT_COLUMNS)

    def _run(self, entries):
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_presence.build(self.history, self.pop, self.out)
        return lib.query(
            "SELECT interval_id, n_ticks, rate_coarse FROM read_parquet('%s') "
            "ORDER BY started" % self.out)

    def _ticks(self, times):
        return [history_entry("d1", "t%d" % i, i, t, TICK, [])
                for i, t in enumerate(times)]

    def test_a_long_silence_splits_one_interval_into_two(self):
        rows = self._run(self._ticks([
            "2025-01-01 10:00:00", "2025-01-01 10:00:01", "2025-01-01 10:00:02",
            # 10-minute silence: the document was closed
            "2025-01-01 10:10:02", "2025-01-01 10:10:03",
        ]))
        self.assertEqual([r["n_ticks"] for r in rows], [3, 2])

    def test_a_slow_program_does_not_fragment_into_many_intervals(self):
        """A 1-minute data rate is legal; 60s gaps must not read as absence."""
        rows = self._run(self._ticks([
            "2025-01-01 10:00:00", "2025-01-01 10:01:00", "2025-01-01 10:02:00",
            "2025-01-01 10:03:00", "2025-01-01 10:04:00", "2025-01-01 10:05:00",
        ]))
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_ticks"], 6)
        self.assertTrue(rows[0]["rate_coarse"])

    def test_a_fast_program_is_not_marked_coarse(self):
        rows = self._run(self._ticks([
            "2025-01-01 10:00:00", "2025-01-01 10:00:01", "2025-01-01 10:00:02",
            "2025-01-01 10:00:03",
        ]))
        self.assertFalse(rows[0]["rate_coarse"])


if __name__ == "__main__":
    unittest.main()
