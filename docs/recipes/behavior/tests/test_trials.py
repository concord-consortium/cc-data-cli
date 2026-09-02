import os
import tempfile
import unittest

import build_population
import build_trials
import lib
from fixtures import CONTENT_COLUMNS, HISTORY_COLUMNS, history_entry, write_parquet

# The `action` column in history.parquet carries `{sharedModel}` in place of the
# real shared-model id; the patch path inside entry_json carries the real one.
# The fixtures keep that split so the action filter is exercised as it is in
# production.
COMMIT = ("/content/sharedModelMap/{sharedModel}/sharedModel"
          "/variables/%s/commitTemporaryValue")
VALUE = "/content/sharedModelMap/%s/sharedModel/variables/%s/value"
# What this detector used to read, and must now ignore: the Live Output node
# writing the program's OUTPUT back into the shared model.
SET_VALUE = ("/content/sharedModelMap/{sharedModel}/sharedModel"
             "/variables/%s/setValue")


def commit(entry_id, idx, created, value, var="0", sm="sm1", doc_id="d1"):
    """One slider release: a single `replace` on the variable's value."""
    return history_entry(
        doc_id, entry_id, idx, created, COMMIT % var,
        [{"op": "replace", "path": VALUE % (sm, var), "value": value}])


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

    def _build(self, entries):
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_trials.build(self.history, self.pop, self.out)
        return lib.query(
            "SELECT trial_id, var_id, n_changes, duration_s, source "
            "FROM read_parquet('%s') ORDER BY started, var_id" % self.out)

    def _run(self, samples, var="0", sm="sm1"):
        """samples: list of (created, value). Each becomes one slider commit."""
        return self._build([
            commit("e%d" % i, i, created, value, var=var, sm=sm)
            for i, (created, value) in enumerate(samples)
        ])

    def test_one_commit_is_a_trial(self):
        """A single deliberate move is the student driving the input, even
        though it spans no time. The old detector needed two samples to see
        one change; a commit is already a change."""
        rows = self._run([("2025-01-01 10:00:00", 200)])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_changes"], 1)
        self.assertAlmostEqual(rows[0]["duration_s"], 0.0, places=1)
        self.assertEqual(rows[0]["source"], "slider")

    def test_a_run_of_commits_becomes_one_trial(self):
        rows = self._run([
            ("2025-01-01 10:00:00", 40),
            ("2025-01-01 10:00:05", 200),
            ("2025-01-01 10:00:11", 360),
        ])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_changes"], 3)
        self.assertAlmostEqual(rows[0]["duration_s"], 11.0, places=1)

    def test_commits_further_apart_than_the_gap_are_separate_trials(self):
        """The shape this task exists to detect: the student drives the input,
        stops to edit, then drives it again."""
        rows = self._run([
            ("2025-01-01 10:00:00", 40),
            ("2025-01-01 10:00:10", 200),
            # over a minute of editing
            ("2025-01-01 10:01:30", 360),
            ("2025-01-01 10:01:40", 440),
        ])
        self.assertEqual([r["n_changes"] for r in rows], [2, 2])

    def test_commits_closer_than_the_gap_stay_one_trial(self):
        """A student pausing briefly mid-flex has not started a new trial."""
        rows = self._run([
            ("2025-01-01 10:00:00", 40),
            ("2025-01-01 10:00:25", 200),
            ("2025-01-01 10:00:50", 360),
        ])
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["n_changes"], 3)

    def test_output_setvalue_is_not_a_trial(self):
        """The correction this rewrite exists for. `setValue` is written by
        sendDataToSimulatedOutput() -- it is the program's output moving, which
        a wave generator wired to an output does with no student involvement.
        A document whose only variable writes are setValue must report nothing.
        """
        rows = self._build([
            history_entry("d1", "e%d" % i, i, "2025-01-01 10:00:%02d" % i,
                          SET_VALUE % "0",
                          [{"op": "replace", "path": VALUE % ("sm1", "0"),
                            "value": 100 + 10 * i}])
            for i in range(6)
        ])
        self.assertEqual(rows, [])

    def test_a_simulation_with_no_slider_reports_nothing(self):
        """terrarium's Temperature and Humidity are driven by the student's own
        Fan and Heat Lamp outputs; it has no slider. Its variables still move
        constantly, via `/content/step` and setValue, and none of that is
        evidence the student checked anything."""
        entries = []
        for i in range(8):
            created = "2025-01-01 10:00:%02d" % i
            entries.append(history_entry(
                "d1", "step%d" % i, 2 * i, created,
                "/content/sharedModelMap/sm1/sharedModel/variables/1/content/step",
                [{"op": "replace", "path": VALUE % ("sm1", "1"),
                  "value": 20.5 + i * 0.3}]))
            entries.append(history_entry(
                "d1", "out%d" % i, 2 * i + 1, created, SET_VALUE % "2",
                [{"op": "replace", "path": VALUE % ("sm1", "2"), "value": i * 5}]))
        self.assertEqual(self._build(entries), [])

    def test_two_variables_are_separate_trials(self):
        """A document can carry more than one control. Two sliders moved in the
        same window are two student gestures, not one."""
        entries = []
        for i in range(3):
            entries.append(commit("eA%d" % i, 2 * i,
                                  "2025-01-01 10:00:%02d" % (2 * i),
                                  40 * i, var="0"))
            entries.append(commit("eB%d" % i, 2 * i + 1,
                                  "2025-01-01 10:00:%02d" % (2 * i + 1),
                                  100 * i, var="3"))
        rows = self._build(entries)
        self.assertEqual([(r["var_id"], r["n_changes"]) for r in rows],
                         [("0", 3), ("3", 3)])

    def test_same_index_under_two_shared_models_stays_separate(self):
        """Two Simulator tiles both address their control as `variables/0`, so
        the shared-model id is part of the island partition even though it is
        not an output column.

        The gap is what exposes it. smA's two commits are 60s apart -- two
        trials. Partitioned on the index alone, smB's commit at 30s lands
        between them and bridges the gap, so smA's two moves fuse into one
        trial of 2. Grouping alone cannot undo that: the islands are already
        numbered by then."""
        rows = self._build([
            commit("eA0", 0, "2025-01-01 10:00:00", 40, var="0", sm="smA"),
            commit("eB0", 1, "2025-01-01 10:00:30", 80, var="0", sm="smB"),
            commit("eA1", 2, "2025-01-01 10:01:00", 120, var="0", sm="smA"),
        ])
        self.assertEqual([r["n_changes"] for r in rows], [1, 1, 1])

    def test_a_patch_on_another_path_is_ignored(self):
        """The action is not trusted on its own: only a `replace` on the
        variable's own `value` counts as a slider move."""
        rows = self._build([
            history_entry("d1", "e0", 0, "2025-01-01 10:00:00", COMMIT % "0",
                          [{"op": "replace",
                            "path": "/content/sharedModelMap/sm1/sharedModel"
                                    "/variables/0/name",
                            "value": "Target EMG"}]),
        ])
        self.assertEqual(rows, [])

    def test_trial_id_is_written_as_bigint(self):
        """sum() over an INTEGER yields HUGEINT, which Parquet cannot store, so
        COPY silently downcasts the column to DOUBLE. Same trap as Task 3."""
        self._run([("2025-01-01 10:00:00", 40), ("2025-01-01 10:00:01", 200)])
        types = {r["column_name"]: r["column_type"] for r in lib.query(
            "DESCRIBE SELECT * FROM read_parquet('%s')" % self.out)}
        self.assertEqual(types["trial_id"], "BIGINT")


if __name__ == "__main__":
    unittest.main()
