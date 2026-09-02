import os
import tempfile
import unittest
from datetime import datetime
from urllib.parse import parse_qs, urlparse

import build_candidates
import build_descriptions
import lib
from fixtures import HISTORY_COLUMNS, history_entry, write_parquet


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
    def _params(self, url):
        return parse_qs(urlparse(url).query)

    def test_builds_a_clue_history_deep_link(self):
        url = build_candidates.replay_url("dockey1", "entry7", "72536", "166319")
        p = self._params(url)
        self.assertEqual(p["studentDocument"], ["dockey1"])
        self.assertEqual(p["studentDocumentHistoryId"], ["entry7"])

    def test_carries_every_parameter_clue_requires_to_launch(self):
        """CLUE refuses the launch without these: authDomain starts the OAuth
        redirect that logs the researcher in, and portal.ts throws outright on
        a missing class or offering, or a reportType other than `offering`."""
        p = self._params(
            build_candidates.replay_url("dockey1", "entry7", "72536", "166319"))
        self.assertEqual(p["authDomain"], [build_candidates.PORTAL])
        self.assertEqual(p["researcher"], ["true"])
        self.assertEqual(p["reportType"], ["offering"])
        self.assertEqual(p["class"],
                         ["%s/api/v1/classes/72536" % build_candidates.PORTAL])
        self.assertEqual(p["offering"],
                         ["%s/api/v1/offerings/166319" % build_candidates.PORTAL])

    def test_omits_unit_and_problem(self):
        """With `offering` present CLUE reads both from the offering's
        activity_url and ignores the params, so sending them is misleading."""
        p = self._params(
            build_candidates.replay_url("dockey1", "entry7", "72536", "166319"))
        self.assertNotIn("unit", p)
        self.assertNotIn("problem", p)

    def test_no_url_without_an_offering_id(self):
        """Better no link than one that lands on 'Missing offering parameter!'"""
        self.assertIsNone(
            build_candidates.replay_url("dockey1", "entry7", "72536", None))

    def test_no_url_without_a_class_id(self):
        self.assertIsNone(
            build_candidates.replay_url("dockey1", "entry7", None, "166319"))

    def test_document_keys_are_escaped(self):
        """Document keys are Firebase push ids and can contain `-` and `_`,
        but the portal URLs in the same query string carry `:` and `/`."""
        url = build_candidates.replay_url("dockey1", "entry7", "72536", "166319")
        self.assertNotIn("https://learn.concord.org/api", url.split("?", 1)[1])
        self.assertEqual(self._params(url)["class"],
                         ["%s/api/v1/classes/72536" % build_candidates.PORTAL])


class TestEpisodeDate(unittest.TestCase):
    def test_takes_the_day_the_episode_started(self):
        self.assertEqual(
            build_candidates.episode_date({"started": "2025-03-14 09:26:53"}),
            "2025-03-14")

    def test_handles_an_iso_t_separator(self):
        self.assertEqual(
            build_candidates.episode_date({"started": "2025-03-14T09:26:53"}),
            "2025-03-14")

    def test_a_missing_timestamp_does_not_break_the_row(self):
        self.assertEqual(build_candidates.episode_date({}), "-")
        self.assertEqual(build_candidates.episode_date({"started": None}), "-")


CYCLE_COLUMNS = {
    "doc_id": "VARCHAR", "uid": "VARCHAR", "unit": "VARCHAR",
    "problem": "VARCHAR", "tile_id": "VARCHAR", "cycle_id": "BIGINT",
    "burst_started": "TIMESTAMP", "n_changes": "BIGINT",
    "n_distinct_targets": "BIGINT", "pause_type": "VARCHAR",
    "oscillation": "BOOLEAN", "undo_in_burst": "BIGINT",
    "trial_after": "BOOLEAN", "trial_changes": "BIGINT",
    "n_nodes_after": "DOUBLE", "pause_after_s": "DOUBLE",
    "first_entry_id": "VARCHAR", "last_entry_id": "VARCHAR",
}


def cycle_row(i, targets, pause_type, oscillation=False):
    return {"doc_id": "d1", "uid": "u1", "unit": "brain", "problem": "1.4",
            "tile_id": "tileA", "cycle_id": i,
            "burst_started": "2025-01-01 10:%02d:00" % i,
            "n_changes": targets, "n_distinct_targets": targets,
            "pause_type": pause_type, "oscillation": oscillation,
            "pause_after_s": 30.0,
            "first_entry_id": "e%d" % i, "last_entry_id": "e%dz" % i}


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


class TestEpisodeEndEntry(unittest.TestCase):
    """The end marker must name the last *labelled* cycle of the episode."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.cycles = os.path.join(self.dir, "cycles.parquet")

    def _episodes(self, rows):
        lib.write_parquet(rows, self.cycles, CYCLE_COLUMNS)
        return build_candidates._episodes(self.cycles)

    def test_spans_from_the_first_cycle_to_the_last(self):
        eps = self._episodes([
            cycle_row(0, 1, "watching"),
            cycle_row(1, 1, "watching"),
            cycle_row(2, 1, "watching"),
        ])
        self.assertEqual(len(eps), 1)
        self.assertEqual(eps[0]["first_entry_id"], "e0")
        self.assertEqual(eps[0]["last_entry_id"], "e2z")

    def test_a_trailing_tolerated_cycle_does_not_become_the_end(self):
        """close() drops trailing tolerated cycles from n_cycles, so letting
        one set the end marker would point past the episode it describes."""
        eps = self._episodes([
            cycle_row(0, 1, "watching"),
            cycle_row(1, 1, "watching"),
            cycle_row(2, 2, "present_unknown"),   # unclassified, tolerated
        ])
        self.assertEqual(len(eps), 1)
        self.assertEqual(eps[0]["n_cycles"], 2)
        self.assertEqual(eps[0]["last_entry_id"], "e1z")

    def test_a_single_cycle_episode_ends_where_its_burst_ends(self):
        eps = self._episodes([cycle_row(0, 4, "absent")])
        self.assertEqual(eps[0]["first_entry_id"], "e0")
        self.assertEqual(eps[0]["last_entry_id"], "e0z")


class TestDroppedReviews(unittest.TestCase):
    """Which reviewed rows a rebuild is about to lose.

    The check must compare against the episodes actually written into the
    sheet. Comparing against every episode makes an episode that survives but
    falls out of the sample look carried forward when its note goes nowhere.
    """

    KEPT = {"ep0001": ("", "a note"), "ep0002": ("ok", "")}

    def test_a_sampled_episode_is_not_dropped(self):
        self.assertEqual(
            build_candidates._dropped_reviews(
                self.KEPT, [{"episode_id": "ep0001"}, {"episode_id": "ep0002"}]),
            [])

    def test_an_episode_that_survives_but_is_not_sampled_is_dropped(self):
        """The bug this exists to stop: ep0002 is still an episode, so a check
        against the full episode list would call it carried forward, but only
        sampled rows are rendered and it is not one."""
        self.assertEqual(
            build_candidates._dropped_reviews(
                self.KEPT, [{"episode_id": "ep0001"}]),
            ["ep0002"])

    def test_an_episode_that_vanished_entirely_is_dropped(self):
        self.assertEqual(
            build_candidates._dropped_reviews(self.KEPT, []),
            ["ep0001", "ep0002"])


SHEET = (
    "# Review sheet\n\n"
    "## systematic — strong (3 shown)\n\n"
    "### ep0001\n\n"
    "- **verdict:** confirmed\n"
    "- **note:** looks right\n"
    "- **date:** 2024-03-01\n"
    "- **unit/problem:** brain 3\n"
    "- **cycles:** 4\n"
    "- **changes:** 6 param\n"
    "- **replay:** [start][ep0001-start] · [end][ep0001-end]\n\n"
    "```\n"
    "1. set Control.controlOperator = Hold Prior\n"
    "   -> tested Target EMG x1, 45s\n"
    "```\n\n"
    "### ep0002\n\n"
    "- **verdict:** \n"
    "- **note:** \n"
    "- **date:** 2024-03-02\n"
    "- **cycles:** 4\n\n"
    "## trial_and_error — control (1 shown)\n\n"
    "### ep0003\n\n"
    "- **verdict:** \n"
    "- **note:** history error\n"
    "- **date:** 2024-03-03\n\n"
    "[ep0001-start]: https://example.org/a\n"
    "[ep0001-end]: https://example.org/b\n"
)


class TestEpisodeSection(unittest.TestCase):
    """What one episode looks like in the sheet, and that it reads back."""

    EP = {"episode_id": "ep0001", "doc_id": "-NrGclyWd2sBVJyJ7Zhj",
          "unit": "brain", "problem": "3", "n_cycles": 4,
          "started": "2024-02-26 16:54:07.123",
          "ended": "2024-02-26 16:57:01.456",
          "first_entry_idx": 2944, "last_entry_idx": 3068, "n_entries": 9330,
          "replay_url": "https://clue.example/?a=1&b=2",
          "end_url": "https://clue.example/?a=1&b=3"}

    def section(self, **over):
        return build_candidates._episode_section(
            {**self.EP, **over}, over.pop("_kept", {}), {})

    def field(self, key, **over):
        prefix = "- **%s:**" % key
        return next(l for l in self.section(**over) if l.startswith(prefix))

    def test_start_and_end_are_separate_fields(self):
        """One line each, so a 300-character URL never sits beside anything
        else a reviewer has to read."""
        self.assertIn("entry 2944 of 9330", self.field("start"))
        self.assertIn("entry 3068 of 9330", self.field("end"))

    def test_each_end_carries_its_own_time_and_link(self):
        start = self.field("start")
        self.assertIn("2024-02-26 16:54:07", start)
        self.assertIn("https://clue.example/?a=1&b=2", start)
        self.assertIn("2024-02-26 16:57:01", self.field("end"))

    def test_the_document_id_is_shown(self):
        self.assertIn("-NrGclyWd2sBVJyJ7Zhj", self.field("document"))

    def test_a_missing_offering_says_so_rather_than_linking(self):
        """A URL cannot be built without an offering id, and a link that fails
        on arrival is worse than none."""
        line = self.field("start", replay_url=None)
        self.assertIn("no offering id", line)
        self.assertNotIn("](", line)

    def test_an_unknown_position_is_marked_rather_than_guessed(self):
        self.assertIn("entry ? of ?",
                      self.field("start", first_entry_idx=None, n_entries=None))

    def test_verdict_and_note_come_first(self):
        """They are the only fields typed into, so nothing is scrolled past."""
        fields = [l for l in self.section() if l.startswith("- **")]
        self.assertTrue(fields[0].startswith("- **verdict:**"))
        self.assertTrue(fields[1].startswith("- **note:**"))

    def test_an_empty_field_has_no_trailing_space(self):
        """Editors that strip trailing whitespace on save would otherwise
        rewrite every unreviewed episode and bury the real edits."""
        self.assertIn("- **verdict:**", self.section())

    def test_a_written_section_reads_back(self):
        """The writer and the parser have drifted apart twice. This pins them
        together: whatever is rendered here must survive parse_sheet()."""
        rendered = build_candidates._episode_section(
            self.EP, {"ep0001": ("confirmed", "clear enough")}, {})
        text = "## systematic — strong (1 shown)\n\n" + "\n".join(rendered)
        row = build_candidates.parse_sheet(text)[0]
        self.assertEqual(row["episode_id"], "ep0001")
        self.assertEqual(row["verdict"], "confirmed")
        self.assertEqual(row["note"], "clear enough")
        self.assertEqual(row["kind"], "systematic")


class TestParseSheet(unittest.TestCase):
    """One parser serves the writer, the carry-forward, and apply_verdicts.py.

    Three separate views of the format is how they drifted: apply_verdicts.py
    read the replay link as a verdict when a column moved, then matched
    nothing at all once episode ids stopped being decimal.
    """

    def rows(self, text=None):
        return {r["episode_id"]: r
                for r in build_candidates.parse_sheet(text or SHEET)}

    def test_reads_verdict_and_note(self):
        row = self.rows()["ep0001"]
        self.assertEqual((row["verdict"], row["note"]),
                         ("confirmed", "looks right"))

    def test_carries_the_stratum_heading_onto_its_episodes(self):
        rows = self.rows()
        self.assertEqual((rows["ep0001"]["kind"], rows["ep0001"]["stratum"]),
                         ("systematic", "strong"))
        self.assertEqual((rows["ep0003"]["kind"], rows["ep0003"]["stratum"]),
                         ("trial_and_error", "control"))

    def test_an_unreviewed_episode_is_present_with_empty_fields(self):
        """apply_verdicts.py counts reviewed against total, so an untouched
        episode has to appear rather than be skipped."""
        self.assertEqual(self.rows()["ep0002"]["verdict"], "")
        self.assertEqual(self.rows()["ep0002"]["note"], "")

    def test_a_note_with_no_verdict_is_kept(self):
        """The overwrite that prompted the carry-forward checked verdicts
        only, and dropped four notes."""
        row = self.rows()["ep0003"]
        self.assertEqual((row["verdict"], row["note"]), ("", "history error"))

    def test_fields_are_found_by_name_not_position(self):
        """A field inserted above `verdict` must not shift what is read."""
        moved = SHEET.replace("### ep0001\n\n- **verdict:**",
                              "### ep0001\n\n- **kind:** systematic\n- **verdict:**")
        self.assertEqual(self.rows(moved)["ep0001"]["verdict"], "confirmed")

    def test_the_link_definition_block_is_not_read_as_an_episode(self):
        self.assertNotIn("[ep0001-start]:", self.rows())
        self.assertEqual(len(self.rows()), 3)

    def test_a_hex_episode_id_is_matched(self):
        """Ids became content-derived hex. The old `ep\\d+` regex silently
        matched none of them, and apply_verdicts.py reported no reviews at
        all rather than failing."""
        hexed = SHEET.replace("ep0001", "epb15d6f25a1")
        self.assertIn("epb15d6f25a1", self.rows(hexed))

    def test_the_cycle_block_is_not_read_as_fields(self):
        """Cycle text is fenced and may contain anything; it must not be able
        to forge a verdict."""
        forged = SHEET.replace(
            "1. set Control.controlOperator = Hold Prior",
            "- **verdict:** rejected")
        self.assertEqual(self.rows(forged)["ep0001"]["verdict"], "confirmed")


class TestExistingReviews(unittest.TestCase):
    """A rebuild must not discard review work already entered."""

    def _write(self, body):
        path = os.path.join(tempfile.mkdtemp(), "review.md")
        with open(path, "w") as handle:
            handle.write(body)
        return path

    def test_keeps_verdicts_and_notes(self):
        kept = build_candidates._existing_reviews(self._write(SHEET))
        self.assertEqual(kept["ep0001"], ("confirmed", "looks right"))

    def test_keeps_a_note_with_no_verdict(self):
        kept = build_candidates._existing_reviews(self._write(SHEET))
        self.assertEqual(kept["ep0003"], ("", "history error"))

    def test_ignores_untouched_episodes(self):
        kept = build_candidates._existing_reviews(self._write(SHEET))
        self.assertNotIn("ep0002", kept)

    def test_a_missing_sheet_is_not_an_error(self):
        self.assertEqual(
            build_candidates._existing_reviews("/nonexistent/review.md"), {})


class TestEpisodeId(unittest.TestCase):
    """Ids are derived from what the episode is, not from where it sits."""

    BASE = {"doc_id": "d1", "tile_id": "tileA",
            "started": "2024-03-01 16:11:08.447",
            "ended": "2024-03-01 16:15:04.810"}

    def _id(self, **over):
        return build_candidates.episode_id({**self.BASE, **over})

    def test_the_same_episode_always_gets_the_same_id(self):
        self.assertEqual(self._id(), self._id())

    def test_entry_ids_do_not_affect_it(self):
        """The point of the scheme: entry ids can shift upstream, and an id
        built on them would churn for reasons unrelated to the episode."""
        a = build_candidates.episode_id({**self.BASE, "first_entry_id": "x",
                                         "last_entry_id": "y"})
        b = build_candidates.episode_id({**self.BASE, "first_entry_id": "p",
                                         "last_entry_id": "q"})
        self.assertEqual(a, b)

    def test_sub_second_jitter_does_not_affect_it(self):
        self.assertEqual(
            self._id(),
            self._id(started="2024-03-01 16:11:08.999",
                     ended="2024-03-01 16:15:04.001"))

    def test_a_different_second_is_a_different_episode(self):
        self.assertNotEqual(self._id(), self._id(started="2024-03-01 16:11:09.447"))

    def test_a_different_tile_is_a_different_episode(self):
        """Two tiles in one document can be edited in the same window, so the
        tile has to be in the key or they collide."""
        self.assertNotEqual(self._id(), self._id(tile_id="tileB"))

    def test_a_different_document_is_a_different_episode(self):
        self.assertNotEqual(self._id(), self._id(doc_id="d2"))

    def test_the_end_time_is_part_of_the_key(self):
        """Two episodes on one tile can start at the same second after a
        rebuild changes where they are cut."""
        self.assertNotEqual(self._id(), self._id(ended="2024-03-01 16:20:00.000"))


class TestResponseLine(unittest.TestCase):
    """What the simulation did while the student watched.

    Not evidence of a trial -- these variables are the program's own output and
    the simulation's reply to it. It answers whether the pause was long enough
    to see anything, which for a simulation with no slider is the only
    evidence there is.
    """

    def test_movement_is_reported_with_its_range_and_count(self):
        line = build_descriptions._response_line(
            [("output", "Gripper", 12, 31.0, 93.0, None)])
        self.assertEqual(line, "sim: Gripper 31..93 x12")

    def test_no_variable_written_is_not_the_same_as_no_response(self):
        """`not running` means there is no evidence either way. Reporting it
        as `no change` would claim the student waited and nothing happened."""
        self.assertEqual(build_descriptions._response_line(None),
                         "sim: not running")

    def test_written_but_unchanged_is_a_finding(self):
        """The student drove the input and the program produced nothing --
        which is the whole point of showing this line."""
        self.assertEqual(
            build_descriptions._response_line(
                [("sensor", "Temperature", 34, 21.0, 21.0, None)]),
            "sim: no change (Temperature 21)")

    def test_nothing_kept_moved_still_reads_as_no_change(self):
        """An empty list means variables were written in the pause but no
        RESPONSE variable was among them. MST records only changes, so a
        response that held still emits no patch at all."""
        self.assertEqual(build_descriptions._response_line([]), "sim: no change")

    def test_a_swing_too_small_to_print_is_not_a_change(self):
        """Comparing raw doubles reported `Temperature 21..21 x34` -- less
        than a tenth of a degree, shown as movement because 20.96 and 21.04
        are not equal. Values are compared at the precision they print at."""
        line = build_descriptions._response_line(
            [("sensor", "Temperature", 34, 20.96, 21.04, None)])
        self.assertIn("no change", line)
        self.assertNotIn("..", line)


def cycle_lines(**over):
    """One rendered cycle, as a list of lines."""
    base = {"burst_started": "2024-03-21 13:08:05",
            "burst_duration_s": 4.0, "pause_after_s": 29.0,
            "first_entry_idx": 4655, "pause_type": "present_unknown",
            "n_distinct_targets": 1, "sim_response": None}
    trials = over.pop("_trials", [])
    ops = over.pop("_ops", [])
    return build_descriptions._cycle_lines(2, {**base, **over}, ops, trials)


class TestCycleLines(unittest.TestCase):
    """A cycle reads as two phases of an interaction log.

    The split is exact: every operation in the corpus falls inside some
    burst's window, so nothing a student typed lands under `Outside Program`.
    """

    def test_the_header_carries_time_position_and_total(self):
        """The history index is what lets a reviewer open the replay at this
        cycle rather than at the episode and scrub."""
        head = cycle_lines()[0]
        self.assertIn("2024-03-21 13:08:05", head)
        self.assertIn("history start 4655", head)
        self.assertIn("duration 33s", head)

    def test_the_two_phases_sum_to_the_total(self):
        """A misattributed event shows up as arithmetic that does not add."""
        lines = cycle_lines()
        self.assertIn("duration 33s", lines[0])
        self.assertIn("Program Changes (4s)", lines[1])
        self.assertTrue(any("Outside Program (29s" in l for l in lines))

    def test_edits_sit_under_program_changes(self):
        lines = cycle_lines(_ops=[{"kind": "node", "op": "add",
                                   "node_type": "Timer", "node_id": "n1",
                                   "param": None, "raw_value": None,
                                   "socket": None, "source_type": None}])
        self.assertEqual(lines[1], "  Program Changes (4s)")
        self.assertIn("Timer", lines[2])

    def test_a_burst_with_no_captured_change_says_so(self):
        self.assertIn("(no program change captured)", cycle_lines()[2])

    def test_several_targets_are_noted_on_the_burst(self):
        """It is a property of the edits, not of the pause."""
        self.assertIn("Program Changes (4s, 3 targets)",
                      cycle_lines(n_distinct_targets=3)[1])

    def test_the_pause_type_qualifies_the_span(self):
        """It is the presence channels' verdict on the gap, so it belongs to
        the span rather than to any one event inside it."""
        self.assertTrue(any("Outside Program (29s, present unknown)" in l
                            for l in cycle_lines()))

    def test_every_input_driven_is_named(self):
        """"tested Target EMG x3" says what was varied; a bare count only says
        that something was."""
        line = next(l for l in cycle_lines(
            _trials=[("Gripper", 6), ("Surface Pressure", 2)])
            if l.startswith("    tested"))
        self.assertIn("Gripper x6", line)
        self.assertIn("Surface Pressure x2", line)

    def test_a_trial_outside_the_pause_is_not_claimed(self):
        """`trial_after` on the cycle is set over a fixed 120s window, so it
        can be true for a trial run after the student went back to editing.
        This block is the cycle's own log entry, so it reports only the
        pause; the episode's label still comes from that flag."""
        lines = cycle_lines(trial_after=True, trial_changes=6, _trials=[])
        self.assertFalse(any(l.startswith("    tested") for l in lines))

    def test_a_trial_and_a_switch_sit_under_outside_program(self):
        lines = cycle_lines(controls_after=["Temperature"],
                            _trials=[("Target EMG", 3)])
        i = next(n for n, l in enumerate(lines) if l.startswith("  Outside"))
        self.assertEqual(lines[i + 1], "    switched to Temperature")
        self.assertEqual(lines[i + 2], "    tested Target EMG x3")

    def test_a_last_cycle_has_no_total_to_report(self):
        """No later burst is a fact about the document ending, not a pause of
        unknown length."""
        lines = cycle_lines(pause_after_s=None)
        self.assertIn("then no further edits", lines[0])
        self.assertIn("no further edits, showing 120s",
                      next(l for l in lines if "Outside Program" in l))

    def test_a_long_pause_is_not_printed_as_raw_seconds(self):
        """Pauses in this corpus run to days; 343500s is not a legible
        number."""
        self.assertIn("Outside Program (2h 30m",
                      next(l for l in cycle_lines(pause_after_s=9000.0)
                           if "Outside Program" in l))


class TestSimControls(unittest.TestCase):
    """Clicks on the Simulator tile's own controls.

    The mode buttons write their variable with `setValue`, the same action a
    Live Output node uses to drive an actuator, so the control is identified by
    variable rather than by action.
    """

    def test_repeated_clicks_on_one_mode_are_named_once(self):
        """Deduplicated by label, first seen first, so a student toggling back
        and forth reads as the two modes rather than a list of clicks."""
        cycle = {"doc_id": "d1", "pause_after_s": 120.0,
                 "burst_ended": datetime(2025, 1, 1, 10, 0, 0)}
        controls = [
            {"doc_id": "d1", "started": "2025-01-01 10:00:05",
             "label": "Temperature"},
            {"doc_id": "d1", "started": "2025-01-01 10:00:20",
             "label": "Pressure"},
            {"doc_id": "d1", "started": "2025-01-01 10:00:40",
             "label": "Temperature"},
        ]
        self.assertEqual(build_descriptions._controls_after(controls, cycle),
                         ["Temperature", "Pressure"])

    def test_clicks_outside_the_pause_are_not_counted(self):
        """Bounded by the pause, like everything else on the cycle."""
        cycle = {"doc_id": "d1", "pause_after_s": 20.0,
                 "burst_ended": datetime(2025, 1, 1, 10, 0, 0)}
        late = [{"doc_id": "d1", "started": "2025-01-01 10:00:45",
                 "label": "Temperature"}]
        self.assertEqual(build_descriptions._controls_after(late, cycle), [])

    def test_another_document_is_not_counted(self):
        cycle = {"doc_id": "d1", "pause_after_s": 120.0,
                 "burst_ended": datetime(2025, 1, 1, 10, 0, 0)}
        other = [{"doc_id": "d2", "started": "2025-01-01 10:00:05",
                  "label": "Temperature"}]
        self.assertEqual(build_descriptions._controls_after(other, cycle), [])


class TestSelfDriven(unittest.TestCase):
    """Some variables move with nobody driving them.

    brainwaves-gripper's pan runs a canned boil stream on a loop, and
    Temperature reports it whenever the gripper is closed past a threshold --
    so a gripper held closed shows a changing temperature with no student and
    no program involved. terrarium's humidity falls 10% every ten minutes
    whatever is running. Neither can be read as the program having done
    something, so the line says so.
    """

    def test_the_marker_names_why(self):
        line = build_descriptions._response_line(
            [("sensor", "Humidity", 34, 19.4, 20.0, "falls 1%/min on its own")])
        self.assertIn("Humidity 19.4..20 x34", line)
        self.assertIn("falls 1%/min on its own", line)

    def test_an_ordinary_response_carries_no_marker(self):
        line = build_descriptions._response_line(
            [("output", "Gripper", 12, 31.0, 93.0, None)])
        self.assertEqual(line, "sim: Gripper 31..93 x12")

    def test_the_companion_variable_picks_the_simulation(self):
        """Both simulations have a variable called Temperature, and only the
        gripper's follows a pan. Heat Lamp and Pan Temperature are what say
        which document this is."""
        self.assertEqual(
            build_descriptions._self_driven("Temperature",
                                            {"Pan Temperature", "Gripper"}),
            "pan's own boil cycle")
        self.assertIsNone(
            build_descriptions._self_driven("Temperature",
                                            {"Heat Lamp", "Humidity"}))

    def test_terrarium_temperature_is_honest_evidence(self):
        """Only the fan and the heat lamp move it -- there is no base drift
        term -- so it does stand as evidence the program drove an output."""
        self.assertIsNone(
            build_descriptions._self_driven("Temperature", {"Heat Lamp"}))


class TestRespondingVars(unittest.TestCase):
    """Which variables count as the simulation responding."""

    META = {
        ("d1", "0"): ("Target EMG", []),
        ("d1", "1"): ("EMG", ["input", "sensor:emg-reading"]),
        ("d1", "2"): ("Surface Pressure", ["input", "sensor:fsr-reading"]),
        ("d1", "3"): ("Gripper", ["output", "live-output:Grabber"]),
        ("d1", "4"): ("Pan Temperature", []),
        ("d1", "5"): ("Pin", ["input", "reading", "sensor:pin-reading"]),
        # A slider on a sensor-labelled variable. None of the three shipped
        # simulations does this -- Target EMG carries no labels and
        # Potentiometer has no `sensor:` one -- so this pins the slider rule,
        # which nothing else in the data reaches.
        ("d2", "0"): ("Dial", ["input", "sensor:dial-reading"]),
        ("d2", "1"): ("Servo", ["output", "live-output:Servo"]),
    }

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.history = os.path.join(self.dir, "history.parquet")
        write_parquet([
            history_entry(
                doc, "e%s" % doc, 0, "2025-01-01 10:00:00",
                "/content/sharedModelMap/{sharedModel}/sharedModel"
                "/variables/0/commitTemporaryValue",
                [{"op": "replace",
                  "path": "/content/sharedModelMap/sm1/sharedModel"
                          "/variables/0/value", "value": 200}])
            for doc in ("d1", "d2")
        ], self.history, HISTORY_COLUMNS)

    def keep(self):
        return build_descriptions._responding_vars(
            self.history, ["d1", "d2"], self.META)

    def test_the_slider_is_not_a_response(self):
        """It is the student's own hand, already reported as the trial. Uses
        the d2 slider, which carries a `sensor:` label and so would survive
        every other rule here."""
        keep = self.keep()
        self.assertNotIn(("d2", "0"), keep)
        self.assertEqual(keep.get(("d2", "1")), "output")

    def test_the_muscle_signal_is_not_a_response(self):
        """EMG is the simulation's input, recomputed with fresh noise every
        frame. Reporting it would claim a response on every cycle of every
        gripper document."""
        self.assertNotIn(("d1", "1"), self.keep())

    def test_a_reading_derived_from_the_slider_is_not_a_response(self):
        """potentiometer-servo's step() sets Pin from the slider's angle, so
        it reports the student's hand. It is `sensor:`-labelled like a real
        reading, so only naming it keeps it out."""
        self.assertNotIn(("d1", "5"), self.keep())

    def test_a_sensor_reading_is_a_response(self):
        self.assertEqual(self.keep().get(("d1", "2")), "sensor")

    def test_a_live_output_is_a_response(self):
        self.assertEqual(self.keep().get(("d1", "3")), "output")

    def test_an_unlabelled_internal_is_left_out(self):
        """Pan Temperature belongs to the simulation's own animation and moves
        regardless of the program."""
        self.assertNotIn(("d1", "4"), self.keep())


class TestTrialsAfter(unittest.TestCase):
    """Trials are attributed to the pause they happened in."""

    def _cycle(self, ended="2024-03-01 16:11:08", pause=None):
        return {"doc_id": "d1", "pause_after_s": pause,
                "burst_ended": datetime.fromisoformat(ended)}

    def test_the_window_is_the_pause_not_a_fixed_horizon(self):
        """A trial 45s after a 20s pause happened while the student was back
        at work, so it belongs to a later cycle, not this one. Under the fixed
        120s window both cycles claimed it."""
        trial = self._trial("2024-03-01 16:11:53")   # 45s after the burst
        self.assertEqual(
            build_descriptions._trials_after([trial], self._cycle(pause=20)), [])
        self.assertEqual(
            build_descriptions._trials_after([trial], self._cycle(pause=60)),
            [("Gripper", 3)])

    def test_a_last_cycle_falls_back_to_the_fixed_horizon(self):
        """With no following burst there is no pause to bound."""
        self.assertEqual(
            build_descriptions._trials_after(
                [self._trial("2024-03-01 16:12:48")], self._cycle()),
            [("Gripper", 3)])

    def _trial(self, started, label="Gripper", n=3, doc="d1"):
        return {"doc_id": doc, "started": started, "label": label, "n": n}

    def test_a_trial_inside_the_window_counts(self):
        hits = build_descriptions._trials_after(
            [self._trial("2024-03-01 16:11:38")], self._cycle())
        self.assertEqual(hits, [("Gripper", 3)])

    def test_a_trial_beyond_the_window_does_not(self):
        late = "2024-03-01 16:14:00"   # 172s > TRIAL_WINDOW_S of 120
        self.assertEqual(
            build_descriptions._trials_after([self._trial(late)], self._cycle()),
            [])

    def test_a_trial_before_the_burst_ended_does_not(self):
        self.assertEqual(
            build_descriptions._trials_after(
                [self._trial("2024-03-01 16:10:00")], self._cycle()),
            [])

    def test_another_document_is_not_counted(self):
        self.assertEqual(
            build_descriptions._trials_after(
                [self._trial("2024-03-01 16:11:38", doc="d2")], self._cycle()),
            [])

    def test_repeats_of_one_input_merge(self):
        hits = build_descriptions._trials_after(
            [self._trial("2024-03-01 16:11:20", n=4),
             self._trial("2024-03-01 16:11:40", n=2)], self._cycle())
        self.assertEqual(hits, [("Gripper", 6)])
