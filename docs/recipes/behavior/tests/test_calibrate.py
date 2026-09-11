import os
import tempfile
import unittest

import calibrate
import lib

EDIT_COLUMNS = {
    "doc_id": "VARCHAR", "uid": "VARCHAR", "portal_class_id": "VARCHAR",
    "unit": "VARCHAR", "problem": "VARCHAR", "clock_suspect": "BOOLEAN",
    "tile_id": "VARCHAR", "node_id": "VARCHAR", "target_id": "VARCHAR",
    "class": "VARCHAR", "subtype": "VARCHAR", "op": "VARCHAR",
    "started": "TIMESTAMP", "ended": "TIMESTAMP", "n_patches": "BIGINT",
    "first_entry_id": "VARCHAR",
}


def edit(started, target, doc="d1"):
    return {"doc_id": doc, "uid": "u1", "portal_class_id": "7", "unit": "brain",
            "problem": "1.4", "clock_suspect": False, "tile_id": "tileA",
            "node_id": target, "target_id": target, "class": "parameter",
            "subtype": None, "op": "replace", "started": started,
            "ended": started, "n_patches": 1, "first_entry_id": "e"}


class TestHistogram(unittest.TestCase):
    def test_counts_values_into_bins(self):
        counts = calibrate.histogram([0.5, 1.5, 1.7, 9.0], [0, 1, 2, 10])
        self.assertEqual(counts, [1, 2, 1])

    def test_values_beyond_the_last_edge_land_in_the_final_bin(self):
        counts = calibrate.histogram([100.0], [0, 1, 2])
        self.assertEqual(counts, [0, 1])

    def test_empty_input_gives_all_zero_bins(self):
        self.assertEqual(calibrate.histogram([], [0, 1, 2]), [0, 0])


class TestBurstGap(unittest.TestCase):
    def test_picks_the_p90_of_within_burst_gaps(self):
        # Within-burst gaps must clear MIN_BURST_GAP_S (2.0s) themselves, or
        # the floor -- not the p90 computation -- would be what this test
        # observes.
        gaps = [3.0] * 90 + [30.0] * 10
        self.assertAlmostEqual(calibrate.choose_burst_gap(gaps), 3.0, places=1)

    def test_never_returns_below_the_coalescing_window(self):
        """A burst gap under 2s would split a single coalesced gesture."""
        self.assertGreaterEqual(calibrate.choose_burst_gap([0.1] * 100), 2.0)


class TestCalibrateGuard(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.edits = os.path.join(self.dir, "edits.parquet")

    def test_refuses_to_calibrate_from_too_few_gaps(self):
        """Below MIN_GAP_COUNT, calibrate() must refuse rather than write an
        unmeasured MIN_BURST_GAP_S fallback into thresholds.json -- the
        scenario in finding 4: an empty/short edits.parquet would otherwise
        make `choose_burst_gap` fall back to 2.0s silently."""
        lib.write_parquet([
            edit("2025-01-01 10:00:00", "n1"),
            edit("2025-01-01 10:00:10", "n2"),
        ], self.edits, EDIT_COLUMNS)
        with self.assertRaises(SystemExit):
            calibrate.calibrate(self.edits, self.dir)
        self.assertFalse(
            os.path.exists(os.path.join(self.dir, "thresholds.json")))

    def test_a_failed_check_leaves_the_previous_thresholds_intact(self):
        """The repo's 'refuse rather than emit smaller' rule: a bad rebuild
        must not destroy a previously written good thresholds.json."""
        good_path = os.path.join(self.dir, "thresholds.json")
        with open(good_path, "w") as handle:
            handle.write('{"burst_gap_s": 32.57}')
        lib.write_parquet([
            edit("2025-01-01 10:00:00", "n1"),
            edit("2025-01-01 10:00:10", "n2"),
        ], self.edits, EDIT_COLUMNS)
        with self.assertRaises(SystemExit):
            calibrate.calibrate(self.edits, self.dir)
        with open(good_path) as handle:
            self.assertIn("32.57", handle.read())


if __name__ == "__main__":
    unittest.main()
