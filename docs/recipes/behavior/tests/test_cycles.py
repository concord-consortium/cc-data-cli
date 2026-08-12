import json
import os
import tempfile
import unittest

import build_cycles
import lib
from fixtures import LOG_COLUMNS, write_parquet

EDIT_COLUMNS = {
    "doc_id": "VARCHAR", "uid": "VARCHAR", "portal_class_id": "VARCHAR",
    "unit": "VARCHAR", "problem": "VARCHAR", "clock_suspect": "BOOLEAN",
    "tile_id": "VARCHAR", "node_id": "VARCHAR", "target_id": "VARCHAR",
    "class": "VARCHAR", "subtype": "VARCHAR", "op": "VARCHAR",
    "started": "TIMESTAMP", "ended": "TIMESTAMP", "n_patches": "BIGINT",
    "first_entry_id": "VARCHAR",
}

PRESENCE_COLUMNS = {
    "doc_id": "VARCHAR", "tile_id": "VARCHAR", "interval_id": "BIGINT",
    "started": "TIMESTAMP", "ended": "TIMESTAMP", "n_ticks": "BIGINT",
    "median_tick_ms": "DOUBLE", "rate_coarse": "BOOLEAN",
}

TRIAL_COLUMNS = {
    "doc_id": "VARCHAR", "trial_id": "BIGINT", "started": "TIMESTAMP",
    "ended": "TIMESTAMP", "n_changes": "BIGINT", "duration_s": "DOUBLE",
    "source": "VARCHAR",
}

THRESHOLDS = {"burst_gap_s": 5.0, "watch_min_s": 4.0, "watch_max_s": 300.0,
              "ui_staleness_s": 120.0}


def edit(started, target, cls="parameter", op="replace", doc="d1", entry="e"):
    return {"doc_id": doc, "uid": "u1", "portal_class_id": "7", "unit": "brain",
            "problem": "1.4", "clock_suspect": False, "tile_id": "tileA",
            "node_id": target, "target_id": target, "class": cls,
            "subtype": "node" if cls == "structure" else None, "op": op,
            "started": started, "ended": started, "n_patches": 1,
            "first_entry_id": entry}


class TestCycles(unittest.TestCase):
    def setUp(self):
        # Force a fixed, DST-free, non-UTC session timezone for every test in
        # this class. `lib.run_sql`/`lib.query` shell out to the duckdb CLI
        # via subprocess.run, which inherits os.environ, and DuckDB's implicit
        # TIMESTAMPTZ -> TIMESTAMP cast resolves through that session zone.
        # Leaving this to the ambient environment means
        # test_log_times_are_compared_in_utc only catches a dropped
        # `AT TIME ZONE 'UTC'` normalisation on machines whose local zone
        # happens to differ from UTC -- on a TZ=UTC runner the same bug
        # produces the numerically-correct answer by coincidence, and the
        # regression guard silently loses its coverage (verified: see
        # task-6-report.md's fix-round-1 appendix). Pacific/Honolulu has no
        # DST transitions, so the offset is deterministic for every date the
        # fixtures use.
        self._prev_tz = os.environ.get("TZ")
        os.environ["TZ"] = "Pacific/Honolulu"
        self.dir = tempfile.mkdtemp()
        self.edits = os.path.join(self.dir, "edits.parquet")
        self.presence = os.path.join(self.dir, "presence.parquet")
        self.trials = os.path.join(self.dir, "trials.parquet")
        self.logs = os.path.join(self.dir, "logs.parquet")
        self.out = os.path.join(self.dir, "cycles.parquet")

    def tearDown(self):
        if self._prev_tz is None:
            os.environ.pop("TZ", None)
        else:
            os.environ["TZ"] = self._prev_tz

    def _run(self, edits, presence=(), logs=(), trials=()):
        write_parquet(edits, self.edits, EDIT_COLUMNS)
        write_parquet(list(presence), self.presence, PRESENCE_COLUMNS)
        write_parquet(list(trials), self.trials, TRIAL_COLUMNS)
        write_parquet(list(logs), self.logs, LOG_COLUMNS)
        build_cycles.build(self.edits, self.presence, self.trials, self.logs,
                           THRESHOLDS, self.out)
        return lib.query(
            "SELECT cycle_id, n_changes, n_distinct_targets, pause_after_s, "
            "pause_type, oscillation, undo_in_burst, trial_after, trial_changes "
            "FROM read_parquet('%s') ORDER BY cycle_id" % self.out)

    def _covering_presence(self):
        return [{"doc_id": "d1", "tile_id": "tileA", "interval_id": 0,
                 "started": "2025-01-01 09:00:00", "ended": "2025-01-01 11:00:00",
                 "n_ticks": 5000, "median_tick_ms": 100.0, "rate_coarse": False}]

    def _log(self, event_time, event="DATAFLOW_TOOL_CHANGE", session="s1",
             nav_open="false"):
        return {"doc_key": "d1", "user_id": "u1", "session": session,
                "event": event, "event_time": event_time, "parameters": "{}",
                "extras": '{"navTabsOpen":%s}' % nav_open}

    def test_a_gap_longer_than_the_burst_gap_starts_a_new_cycle(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:02", "n2", entry="e2"),
            edit("2025-01-01 10:00:32", "n3", entry="e3"),
        ], self._covering_presence())
        self.assertEqual([r["n_changes"] for r in rows], [2, 1])
        self.assertAlmostEqual(rows[0]["pause_after_s"], 30.0, places=1)

    def test_burst_composition_is_recorded(self):
        """The axis now rests on this: how many distinct things changed
        before the student stopped."""
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:01", "n2", entry="e2"),
            edit("2025-01-01 10:00:02", "n3", entry="e3"),
        ], self._covering_presence())
        self.assertEqual(rows[0]["n_distinct_targets"], 3)
        self.assertEqual(rows[0]["n_changes"], 3)

    def test_a_trial_shortly_after_a_burst_is_linked_to_it(self):
        """The reconstructed 'set it up, then trial it' cycle."""
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:05:00", "n2", entry="e2"),
        ], self._covering_presence(), trials=[
            {"doc_id": "d1", "trial_id": 0, "started": "2025-01-01 10:00:30",
             "ended": "2025-01-01 10:00:45", "n_changes": 7,
             "duration_s": 15.0, "source": "simulation"},
        ])
        self.assertTrue(rows[0]["trial_after"])
        self.assertEqual(rows[0]["trial_changes"], 7)

    def test_a_distant_trial_is_not_linked(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 11:00:00", "n2", entry="e2"),
        ], self._covering_presence(), trials=[
            {"doc_id": "d1", "trial_id": 0, "started": "2025-01-01 10:30:00",
             "ended": "2025-01-01 10:30:15", "n_changes": 7,
             "duration_s": 15.0, "source": "simulation"},
        ])
        self.assertFalse(rows[0]["trial_after"])
        self.assertEqual(rows[0]["trial_changes"], 0)

    def test_adding_then_removing_the_same_target_is_oscillation(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", cls="structure", op="add", entry="e1"),
            edit("2025-01-01 10:00:01", "n1", cls="structure", op="remove", entry="e2"),
        ], self._covering_presence())
        self.assertTrue(rows[0]["oscillation"])

    def test_adding_one_target_and_removing_another_is_not_oscillation(self):
        """Ordinary editing. `bool_or(add) and bool_or(remove)` would wrongly
        fire here, which is why the check is a set intersection."""
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", cls="structure", op="add", entry="e1"),
            edit("2025-01-01 10:00:01", "n2", cls="structure", op="remove", entry="e2"),
        ], self._covering_presence())
        self.assertFalse(rows[0]["oscillation"])

    def test_undo_inside_a_burst_is_counted(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:01", "n1", cls="undo", op="undo", entry="e2"),
            edit("2025-01-01 10:00:02", "n2", entry="e3"),
        ], self._covering_presence())
        self.assertEqual(rows[0]["undo_in_burst"], 1)

    def test_a_session_covered_quiet_pause_with_ticks_is_watching(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], self._covering_presence(), logs=[
            self._log("2025-01-01 09:59:00"),
            self._log("2025-01-01 10:00:00"),
            self._log("2025-01-01 10:01:00"),
        ])
        self.assertEqual(rows[0]["pause_type"], "watching")

    def test_the_same_pause_without_ticks_is_only_watching_weak(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], [], logs=[
            self._log("2025-01-01 09:59:00"),
            self._log("2025-01-01 10:00:00"),
            self._log("2025-01-01 10:01:00"),
        ])
        self.assertEqual(rows[0]["pause_type"], "watching_weak")

    def test_a_pause_crossing_a_session_boundary_is_absent(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], self._covering_presence(), logs=[
            self._log("2025-01-01 09:59:00", session="s1"),
            self._log("2025-01-01 10:00:00", session="s1"),
            self._log("2025-01-01 10:00:40", session="s2"),
            self._log("2025-01-01 10:01:00", session="s2"),
        ])
        self.assertEqual(rows[0]["pause_type"], "absent")

    def test_a_document_with_neither_logs_nor_ticks_is_no_presence_data(self):
        """1,939 of 2,677 documents have no ticks. Calling their pauses
        `absent` would assert the student left on no evidence at all."""
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], [], logs=[])
        self.assertEqual(rows[0]["pause_type"], "no_presence_data")

    def test_text_editing_during_a_pause_makes_it_documenting(self):
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], self._covering_presence(), logs=[
            self._log("2025-01-01 10:00:00"),
            self._log("2025-01-01 10:00:20", event="TEXT_TOOL_CHANGE"),
        ])
        self.assertEqual(rows[0]["pause_type"], "documenting")

    def test_log_times_are_compared_in_utc(self):
        """Regression test for a silent timezone bug. logs.event_time is
        TIMESTAMP WITH TIME ZONE and history `created` is naive UTC; comparing
        them directly makes DuckDB resolve the naive side in the local zone,
        shifting every join by the UTC offset. These log times carry an explicit
        non-UTC offset denoting the same instants as the naive history times, so
        dropping the AT TIME ZONE 'UTC' normalisation pushes the events outside
        the pause and the verdict degrades away from `watching`."""
        rows = self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            edit("2025-01-01 10:00:40", "n2", entry="e2"),
        ], self._covering_presence(), logs=[
            self._log("2025-01-01 04:59:00-05"),
            self._log("2025-01-01 05:00:00-05"),
            self._log("2025-01-01 05:01:00-05"),
        ])
        self.assertEqual(rows[0]["pause_type"], "watching")

    def test_cycle_id_is_written_as_bigint(self):
        """sum() over an INTEGER yields HUGEINT, which Parquet cannot store."""
        self._run([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
        ], self._covering_presence())
        types = {r["column_name"]: r["column_type"] for r in lib.query(
            "DESCRIBE SELECT * FROM read_parquet('%s')" % self.out)}
        self.assertEqual(types["cycle_id"], "BIGINT")


class TestWatchMinCoupling(unittest.TestCase):
    """Regression test for finding 2: build_cycles.main() overrides
    burst_gap_s to 5.0 (see the module docstring) but must couple
    watch_min_s to the same override. This exercises main() itself, not
    build(), because the coupling bug lives in main()'s threshold handling --
    build() tests that pass an explicit THRESHOLDS dict never see it."""

    def setUp(self):
        self._prev_tz = os.environ.get("TZ")
        os.environ["TZ"] = "Pacific/Honolulu"
        self._prev_local = os.environ.get("CC_DATA_LOCAL")
        self.root = tempfile.mkdtemp()
        os.environ["CC_DATA_LOCAL"] = self.root
        self.derived = os.path.join(self.root, "derived")
        os.makedirs(self.derived, exist_ok=True)
        os.makedirs(os.path.join(self.root, "log-events"), exist_ok=True)

    def tearDown(self):
        if self._prev_tz is None:
            os.environ.pop("TZ", None)
        else:
            os.environ["TZ"] = self._prev_tz
        if self._prev_local is None:
            os.environ.pop("CC_DATA_LOCAL", None)
        else:
            os.environ["CC_DATA_LOCAL"] = self._prev_local

    def test_a_pause_between_the_new_burst_gap_and_the_old_watch_min_is_watching(self):
        # A calibrate.py run before this fix would have written watch_min_s
        # equal to the calibrated burst_gap_s (~32.57s in the real corpus).
        with open(os.path.join(self.derived, "thresholds.json"), "w") as handle:
            json.dump({"burst_gap_s": 32.57, "watch_min_s": 32.57,
                       "watch_max_s": 300.0, "ui_staleness_s": 120.0}, handle)

        write_parquet([
            edit("2025-01-01 10:00:00", "n1", entry="e1"),
            # 10s pause: longer than the burst-gap override (5.0s) so this
            # starts a new cycle, but shorter than the OLD watch_min_s
            # (32.57s). Uncoupled, this pause can never be `watching`.
            edit("2025-01-01 10:00:10", "n2", entry="e2"),
        ], os.path.join(self.derived, "edits.parquet"), EDIT_COLUMNS)
        write_parquet(
            self._covering_presence(),
            os.path.join(self.derived, "presence.parquet"), PRESENCE_COLUMNS)
        write_parquet([], os.path.join(self.derived, "trials.parquet"), TRIAL_COLUMNS)
        write_parquet([
            self._log("2025-01-01 09:59:00"),
            self._log("2025-01-01 10:00:00"),
            self._log("2025-01-01 10:01:00"),
        ], os.path.join(self.root, "log-events", "logs.parquet"), LOG_COLUMNS)

        build_cycles.main()

        rows = lib.query(
            "SELECT pause_type FROM read_parquet('%s') ORDER BY cycle_id"
            % os.path.join(self.derived, "cycles.parquet"))
        self.assertEqual(rows[0]["pause_type"], "watching")

    def _covering_presence(self):
        return [{"doc_id": "d1", "tile_id": "tileA", "interval_id": 0,
                 "started": "2025-01-01 09:00:00", "ended": "2025-01-01 11:00:00",
                 "n_ticks": 5000, "median_tick_ms": 100.0, "rate_coarse": False}]

    def _log(self, event_time, event="DATAFLOW_TOOL_CHANGE", session="s1",
             nav_open="false"):
        return {"doc_key": "d1", "user_id": "u1", "session": session,
                "event": event, "event_time": event_time, "parameters": "{}",
                "extras": '{"navTabsOpen":%s}' % nav_open}


if __name__ == "__main__":
    unittest.main()
