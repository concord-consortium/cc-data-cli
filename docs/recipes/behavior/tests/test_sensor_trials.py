import os
import tempfile
import unittest

import build_population
import build_sensor_trials
import lib
from fixtures import (CONTENT_SENSOR_COLUMNS, HISTORY_COLUMNS,
                      dataflow_content_json, sensor_node, tick_entry,
                      write_parquet)

# Long enough that a run of static readings clears MIN_STATIC_TICKS with room
# to spare, since the rolling window forces the first ticks of a segment static.
STATIC = 20
FLEX = 15


def flat(n, value="30"):
    return [value] * n


def ramp(n, lo=30, hi=500):
    """A flex: readings sweeping across most of the node's range."""
    step = (hi - lo) / float(max(1, n - 1))
    return ["%d" % int(lo + step * i) for i in range(n)]


class TestSensorTrials(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.content = os.path.join(self.dir, "content.parquet")
        self.history = os.path.join(self.dir, "history.parquet")
        self.pop = os.path.join(self.dir, "population.parquet")
        self.out = os.path.join(self.dir, "sensor_trials.parquet")

    def _content(self, nodes):
        write_parquet([
            {"doc_id": "d1", "doc_key": "d1", "uid": "u1",
             "portal_class_id": "7", "type": "problem", "unit": "brain",
             "problem": "1.4", "dataflow_tile_deleted": False,
             "content_json": dataflow_content_json(nodes), "parse_ok": True},
        ], self.content, CONTENT_SENSOR_COLUMNS)

    def _run(self, nodes, streams, start=0, step_ms=200):
        """streams: {node_id: [readings]}, all sampled on the same clock."""
        self._content(nodes)
        n = max(len(s) for s in streams.values())
        entries = []
        for i in range(n):
            ms = start + i * step_ms
            created = "2025-01-01 10:00:%02d.%03d" % (ms // 1000, ms % 1000)
            values = {node_id: s[i] for node_id, s in streams.items()
                      if i < len(s)}
            entries.append(tick_entry("d1", "e%04d" % i, i, created, values))
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_sensor_trials.build(self.history, self.content, self.pop, self.out)
        return lib.query(
            "SELECT trial_id, node_id, sensor_kind, sensor_type, n_ticks, "
            "duration_s, source FROM read_parquet('%s') "
            "ORDER BY started, node_id" % self.out)

    def test_a_resting_sensor_produces_no_trial(self):
        """An EMG left alone sits pinned at its floor value. That is the null
        case this detector must not fire on."""
        rows = self._run([sensor_node("n1")], {"n1": flat(80)})
        self.assertEqual(rows, [])

    def test_static_changing_static_is_one_trial(self):
        rows = self._run([sensor_node("n1")],
                         {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)})
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["sensor_kind"], "physical")
        self.assertEqual(rows[0]["sensor_type"], "emg-reading")
        self.assertEqual(rows[0]["source"], "sensor")

    def test_two_flexes_separated_by_rest_are_two_trials(self):
        rows = self._run([sensor_node("n1")], {
            "n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)
                  + ramp(FLEX) + flat(STATIC)})
        self.assertEqual(len(rows), 2)
        self.assertEqual([r["trial_id"] for r in rows], [0, 1])

    def test_change_without_a_static_lead_in_is_not_a_trial(self):
        """The stream starting mid-flex says nothing about when the student
        stopped editing, which is the whole point of the boundary."""
        rows = self._run([sensor_node("n1")], {"n1": ramp(FLEX) + flat(STATIC)})
        self.assertEqual(rows, [])

    def test_change_without_a_static_tail_is_not_a_trial(self):
        rows = self._run([sensor_node("n1")], {"n1": flat(STATIC) + ramp(FLEX)})
        self.assertEqual(rows, [])

    def test_a_gap_splits_segments_so_the_lead_in_does_not_carry_over(self):
        """Rest, then the document is closed for ten minutes, then the stream
        resumes already changing. The pre-gap rest is not a lead-in."""
        self._content([sensor_node("n1")])
        entries = []
        for i, value in enumerate(flat(STATIC)):
            entries.append(tick_entry("d1", "a%04d" % i, i,
                                      "2025-01-01 10:00:%02d" % i, {"n1": value}))
        later = ramp(FLEX) + flat(STATIC)
        for i, value in enumerate(later):
            entries.append(tick_entry("d1", "b%04d" % i, 1000 + i,
                                      "2025-01-01 10:20:%02d" % i, {"n1": value}))
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_sensor_trials.build(self.history, self.content, self.pop, self.out)
        self.assertEqual(lib.query(
            "SELECT * FROM read_parquet('%s')" % self.out), [])

    def test_a_simulated_sensor_is_labelled_not_physical(self):
        """A Sensor node fed by the Simulation tile carries a SIM* binding.
        Its trials are real, but they are the mouse-driven population the
        Simulator detector already covers, so they must stay separable."""
        rows = self._run([sensor_node("n1", sensor="SIMemg_key")],
                         {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)})
        self.assertEqual([r["sensor_kind"] for r in rows], ["simulated"])

    def test_an_unbound_sensor_is_labelled_unbound(self):
        rows = self._run([sensor_node("n1", sensor=None)],
                         {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)})
        self.assertEqual([r["sensor_kind"] for r in rows], ["unbound"])

    def test_a_dropout_does_not_hide_a_later_flex(self):
        """NaN means no device is reporting, and it has to be dropped rather
        than read as a number: left in, it poisons the node's range -- every
        comparison against a NaN bound is false -- and the real flex after the
        dropout stops registering at all."""
        rows = self._run([sensor_node("n1")], {
            "n1": flat(STATIC) + ["NaN"] * 5 + flat(STATIC)
                  + ramp(FLEX) + flat(STATIC)})
        self.assertEqual([r["node_id"] for r in rows], ["n1"])

    def test_a_second_node_does_not_smear_into_the_first(self):
        """The build_trials.py Finding 3 analogue. Two sensors write on every
        tick, so a window that forgets which node it is reading spans both
        streams at once. Each node here rests while the other flexes: pooled,
        every window sees a change and neither node gets the static lead-in
        and tail that make its flex a trial."""
        rows = self._run(
            [sensor_node("n1"), sensor_node("n2", sensor_type="fsr-reading")],
            {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)
                   + flat(FLEX) + flat(STATIC),
             "n2": flat(STATIC, value="7") + flat(FLEX, value="7")
                   + flat(STATIC, value="7") + ramp(FLEX, lo=7, hi=900)
                   + flat(STATIC, value="7")})
        self.assertEqual([(r["node_id"], r["trial_id"]) for r in rows],
                         [("n1", 0), ("n2", 1)])

    def test_both_nodes_flexing_together_are_detected_separately(self):
        rows = self._run(
            [sensor_node("n1"), sensor_node("n2", sensor_type="fsr-reading")],
            {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC),
             "n2": flat(STATIC) + ramp(FLEX) + flat(STATIC)})
        self.assertEqual(sorted(r["node_id"] for r in rows), ["n1", "n2"])
        self.assertEqual(sorted(r["trial_id"] for r in rows), [0, 1])

    def test_legacy_tick_replaces_are_ignored(self):
        """Real entries also `replace` tickEntries/legacyTick/nodeValue,
        restating a reading already recorded. Counting those as readings of
        their own changes what the window sees, so the result must be
        identical with and without them."""
        stream = flat(STATIC) + ramp(FLEX) + flat(STATIC)
        without = self._run([sensor_node("n1")], {"n1": stream})

        self._content([sensor_node("n1")])
        entries = []
        for i, value in enumerate(stream):
            ms = i * 200
            entries.append(tick_entry(
                "d1", "e%04d" % i, i,
                "2025-01-01 10:00:%02d.%03d" % (ms // 1000, ms % 1000),
                {"n1": value}, legacy={"n1": stream[max(0, i - 1)]}))
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_sensor_trials.build(self.history, self.content, self.pop, self.out)
        with_legacy = lib.query(
            "SELECT trial_id, node_id, sensor_kind, sensor_type, n_ticks, "
            "duration_s, source FROM read_parquet('%s') "
            "ORDER BY started, node_id" % self.out)

        self.assertEqual(len(without), 1)
        self.assertEqual(with_legacy, without)

    def test_a_node_that_is_not_a_sensor_is_ignored(self):
        """Every node writes a tickEntry each tick, including computed ones.
        Only inputs bound to a sensor are evidence about the student."""
        self._content([{"id": "n9", "name": "Transform",
                        "data": {"type": "Transform"}}])
        stream = flat(STATIC) + ramp(FLEX) + flat(STATIC)
        entries = [
            tick_entry("d1", "e%04d" % i, i,
                       "2025-01-01 10:00:%02d.%03d" % ((i * 200) // 1000,
                                                       (i * 200) % 1000),
                       {"n9": value})
            for i, value in enumerate(stream)]
        write_parquet(entries, self.history, HISTORY_COLUMNS)
        build_population.build(self.content, self.history, self.pop)
        build_sensor_trials.build(self.history, self.content, self.pop, self.out)
        self.assertEqual(lib.query(
            "SELECT * FROM read_parquet('%s')" % self.out), [])

    def test_trial_id_is_written_as_bigint(self):
        """sum() over an INTEGER yields HUGEINT, which Parquet cannot store, so
        COPY silently downcasts the column to DOUBLE."""
        self._run([sensor_node("n1")],
                  {"n1": flat(STATIC) + ramp(FLEX) + flat(STATIC)})
        types = {r["column_name"]: r["column_type"] for r in lib.query(
            "DESCRIBE SELECT * FROM read_parquet('%s')" % self.out)}
        self.assertEqual(types["trial_id"], "BIGINT")


if __name__ == "__main__":
    unittest.main()
