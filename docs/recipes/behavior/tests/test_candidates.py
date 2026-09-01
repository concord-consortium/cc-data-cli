import os
import tempfile
import unittest
from datetime import datetime
from urllib.parse import parse_qs, urlparse

import build_candidates
import build_descriptions
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


class TestExistingReviews(unittest.TestCase):
    """A rebuild must not discard review work already entered."""

    def _write(self, body):
        path = os.path.join(tempfile.mkdtemp(), "review.md")
        with open(path, "w") as handle:
            handle.write(body)
        return path

    SHEET = (
        "# Review sheet\n\n"
        "## systematic — strong (2 shown)\n\n"
        "| episode | date | unit/problem | cycles | start | end | "
        "verdict | note |\n"
        "|---|---|---|---|---|---|---|---|\n"
        "| ep0001 | 2024-03-01 | brain 3 | 4 | [start](u) | [end](v) | "
        "confirmed | looks right |\n"
        "| ep0002 | 2024-03-02 | brain 3 | 4 | [start](u) | [end](v) |  |  |\n"
        "| ep0003 | 2024-03-03 | brain 3 | 4 | [start](u) | [end](v) |  "
        "| history error |\n"
    )

    def test_keeps_verdicts_and_notes(self):
        kept = build_candidates._existing_reviews(self._write(self.SHEET))
        self.assertEqual(kept["ep0001"], ("confirmed", "looks right"))

    def test_keeps_a_note_with_no_verdict(self):
        """The overwrite that prompted this checked verdicts only."""
        kept = build_candidates._existing_reviews(self._write(self.SHEET))
        self.assertEqual(kept["ep0003"], ("", "history error"))

    def test_ignores_untouched_rows(self):
        kept = build_candidates._existing_reviews(self._write(self.SHEET))
        self.assertNotIn("ep0002", kept)

    def test_reads_by_header_not_position(self):
        """A column inserted before `verdict` must not shift what is carried.

        Position-indexed parsing is how apply_verdicts.py came to read the
        replay link as a verdict, so this pins the header-based behaviour.
        """
        moved = self.SHEET.replace(
            "| episode | date | unit/problem | cycles | start | end | "
            "verdict | note |",
            "| episode | date | extra | unit/problem | cycles | start | end | "
            "verdict | note |"
        ).replace(
            "|---|---|---|---|---|---|---|---|",
            "|---|---|---|---|---|---|---|---|---|"
        ).replace(
            "| ep0001 | 2024-03-01 | brain 3 |",
            "| ep0001 | 2024-03-01 | x | brain 3 |")
        kept = build_candidates._existing_reviews(self._write(moved))
        self.assertEqual(kept["ep0001"], ("confirmed", "looks right"))

    def test_a_linked_episode_id_is_keyed_by_its_bare_id(self):
        """The episode cell is a markdown link to episodes.md. Keying on the
        raw cell means review work is lost the moment the link changes --
        which is exactly how five notes were dropped when it was added."""
        linked = self.SHEET.replace(
            "| ep0001 |", "| [ep0001](episodes.md#ep0001) |")
        kept = build_candidates._existing_reviews(self._write(linked))
        self.assertEqual(kept["ep0001"], ("confirmed", "looks right"))

    def test_an_unlinked_episode_id_still_works(self):
        kept = build_candidates._existing_reviews(self._write(self.SHEET))
        self.assertIn("ep0001", kept)

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


class TestPauseLine(unittest.TestCase):
    """The line after a burst says what the student did to check it."""

    CYCLE = {"pause_after_s": 99.0, "pause_type": "present_unknown",
             "trial_after": True, "trial_changes": 6}

    def test_names_the_input_that_was_driven(self):
        """'tested Gripper x6' says what was varied; 'tested (6 input
        changes)' only says that something was."""
        line = build_descriptions._pause(self.CYCLE, [("Gripper", 6)])
        self.assertIn("Gripper", line)
        self.assertIn("x6", line)

    def test_lists_every_input_driven_in_the_window(self):
        line = build_descriptions._pause(
            self.CYCLE, [("Gripper", 6), ("Surface Pressure", 2)])
        self.assertIn("Gripper", line)
        self.assertIn("Surface Pressure", line)

    def test_falls_back_when_a_trial_is_known_but_unnamed(self):
        """trial_after comes from cycles.parquet, so the trial is known to
        have happened; reporting nothing would imply it did not."""
        line = build_descriptions._pause(self.CYCLE, [])
        self.assertIn("tested", line)
        self.assertIn("6", line)

    def test_an_unchecked_burst_reports_its_pause_type(self):
        line = build_descriptions._pause(
            {"pause_after_s": 40.0, "pause_type": "present_unknown",
             "trial_after": False}, [])
        self.assertIn("present unknown", line)
        self.assertNotIn("tested", line)


class TestTrialsAfter(unittest.TestCase):
    """Trials are attributed with the same window build_cycles.py uses."""

    def _cycle(self, ended="2024-03-01 16:11:08"):
        return {"doc_id": "d1",
                "burst_ended": datetime.fromisoformat(ended)}

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
