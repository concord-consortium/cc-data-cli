import os
import tempfile
import unittest

import build_candidates
import lib


def cycle(targets, pause_type, oscillation=False):
    return {"n_distinct_targets": targets, "pause_type": pause_type,
            "oscillation": oscillation}


class TestClassifyCycle(unittest.TestCase):
    def test_one_change_then_watching_is_systematic(self):
        self.assertEqual(
            build_candidates.classify_cycle(cycle(1, "watching")), "systematic")

    def test_many_targets_with_no_watching_pause_is_trial_and_error(self):
        self.assertEqual(
            build_candidates.classify_cycle(cycle(4, "absent")), "trial_and_error")

    def test_oscillation_is_trial_and_error_regardless_of_target_count(self):
        self.assertEqual(
            build_candidates.classify_cycle(cycle(1, "absent", oscillation=True)),
            "trial_and_error")

    def test_one_change_with_an_unknown_pause_is_not_claimed_as_systematic(self):
        """Most cycles should be unclassified. A detector that labels
        everything cannot be checked."""
        self.assertEqual(
            build_candidates.classify_cycle(cycle(1, "present_unknown")),
            "unclassified")

    def test_a_single_change_followed_by_leaving_is_unclassified(self):
        self.assertEqual(
            build_candidates.classify_cycle(cycle(1, "absent")), "unclassified")


class TestReplayUrl(unittest.TestCase):
    def test_builds_a_clue_history_deep_link(self):
        url = build_candidates.replay_url("dockey1", "entry7")
        self.assertIn("studentDocument=dockey1", url)
        self.assertIn("studentDocumentHistoryId=entry7", url)


CYCLE_COLUMNS = {
    "doc_id": "VARCHAR", "uid": "VARCHAR", "unit": "VARCHAR",
    "problem": "VARCHAR", "tile_id": "VARCHAR", "cycle_id": "BIGINT",
    "burst_started": "TIMESTAMP", "n_changes": "BIGINT",
    "n_distinct_targets": "BIGINT", "pause_type": "VARCHAR",
    "oscillation": "BOOLEAN", "undo_in_burst": "BIGINT",
    "trial_after": "BOOLEAN", "trial_changes": "BIGINT",
    "n_nodes_after": "DOUBLE", "pause_after_s": "DOUBLE",
    "first_entry_id": "VARCHAR",
}


def cycle_row(i, targets, pause_type, oscillation=False):
    return {"doc_id": "d1", "uid": "u1", "unit": "brain", "problem": "1.4",
            "tile_id": "tileA", "cycle_id": i,
            "burst_started": "2025-01-01 10:%02d:00" % i,
            "n_changes": targets, "n_distinct_targets": targets,
            "pause_type": pause_type, "oscillation": oscillation,
            "pause_after_s": 30.0, "first_entry_id": "e%d" % i}


class TestEpisodes(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.cycles = os.path.join(self.dir, "cycles.parquet")

    def _episodes(self, rows):
        lib.write_parquet(rows, self.cycles, CYCLE_COLUMNS)
        return build_candidates._episodes(self.cycles)

    def test_one_tolerated_gap_keeps_an_episode_together_and_lowers_purity(self):
        eps = self._episodes([
            cycle_row(0, 1, "watching"),
            cycle_row(1, 2, "present_unknown"),   # unclassified, tolerated
            cycle_row(2, 1, "watching"),
        ])
        self.assertEqual(len(eps), 1)
        self.assertEqual(eps[0]["n_cycles"], 3)
        self.assertAlmostEqual(eps[0]["purity"], 2.0 / 3.0)

    def test_two_consecutive_gaps_end_the_episode(self):
        eps = self._episodes([
            cycle_row(0, 1, "watching"),
            cycle_row(1, 2, "present_unknown"),
            cycle_row(2, 2, "present_unknown"),
            cycle_row(3, 1, "watching"),
        ])
        self.assertEqual(len(eps), 2)
        self.assertEqual([e["n_cycles"] for e in eps], [1, 1])
        self.assertEqual([e["purity"] for e in eps], [1.0, 1.0])

    def test_a_change_of_kind_starts_a_new_episode(self):
        eps = self._episodes([
            cycle_row(0, 1, "watching"),
            cycle_row(1, 4, "absent"),
        ])
        self.assertEqual([e["kind"] for e in eps],
                         ["systematic", "trial_and_error"])


if __name__ == "__main__":
    unittest.main()
