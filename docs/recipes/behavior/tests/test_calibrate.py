import unittest

import calibrate


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


if __name__ == "__main__":
    unittest.main()
