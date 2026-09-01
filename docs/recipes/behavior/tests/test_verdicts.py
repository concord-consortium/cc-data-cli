import unittest

import apply_verdicts

# The sheet as build_candidates.py writes it today: episode, date,
# unit/problem, cycles, start, end, verdict, note.
REVIEW = """# Review sheet

## systematic — strong (2 shown)

| episode | date | unit/problem | cycles | start | end | verdict | note |
|---|---|---|---|---|---|---|---|
| ep000001 | 2024-03-01 | brain 1.4 | 6 | [start](http://x) | [end](http://y) | confirmed | clear |
| ep000002 | 2024-03-02 | brain 1.4 | 5 | [start](http://x) | [end](http://y) | rejected | just idle |

## trial_and_error — boundary (1 shown)

| episode | date | unit/problem | cycles | start | end | verdict | note |
|---|---|---|---|---|---|---|---|
| ep000003 | 2024-03-03 | brain 1.5 | 2 | [start](http://x) | [end](http://y) |  |  |
"""

# The six-column sheet this script was originally written against. Kept so a
# sheet saved before the date and end columns existed still parses.
REVIEW_LEGACY = """# Review sheet

## systematic — strong (1 shown)

| episode | unit/problem | cycles | replay | verdict | note |
|---|---|---|---|---|---|
| ep000001 | brain 1.4 | 6 | [replay](http://x) | confirmed | clear |
"""

# The seven-column shape that shipped when `date` was added. Position-indexed
# parsing read the replay link here as the verdict, so every review looked
# unfilled; this pins that it no longer does.
REVIEW_SEVEN_COLUMN = """# Review sheet

## systematic — strong (1 shown)

| episode | date | unit/problem | cycles | replay | verdict | note |
|---|---|---|---|---|---|---|
| ep000001 | 2024-03-01 | brain 1.4 | 6 | [replay](http://x) | confirmed | clear |
"""


class TestParseReview(unittest.TestCase):
    def test_reads_verdicts_and_their_section(self):
        rows = apply_verdicts.parse_review(REVIEW)
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0]["episode_id"], "ep000001")
        self.assertEqual(rows[0]["kind"], "systematic")
        self.assertEqual(rows[0]["stratum"], "strong")
        self.assertEqual(rows[0]["verdict"], "confirmed")
        self.assertEqual(rows[0]["note"], "clear")

    def test_an_unreviewed_row_has_an_empty_verdict(self):
        rows = apply_verdicts.parse_review(REVIEW)
        self.assertEqual(rows[2]["verdict"], "")

    def test_ignores_the_header_separator_rows(self):
        rows = apply_verdicts.parse_review(REVIEW)
        self.assertTrue(all(r["episode_id"].startswith("ep") for r in rows))


class TestPrecision(unittest.TestCase):
    def test_scores_only_reviewed_rows(self):
        scored = apply_verdicts.precision([
            {"kind": "systematic", "verdict": "confirmed"},
            {"kind": "systematic", "verdict": "rejected"},
            {"kind": "systematic", "verdict": ""},
            {"kind": "trial_and_error", "verdict": "confirmed"},
        ])
        self.assertAlmostEqual(scored["systematic"], 0.5)
        self.assertAlmostEqual(scored["trial_and_error"], 1.0)

    def test_a_kind_with_no_reviews_is_absent_rather_than_zero(self):
        scored = apply_verdicts.precision([{"kind": "systematic", "verdict": ""}])
        self.assertNotIn("systematic", scored)


if __name__ == "__main__":
    unittest.main()


class TestColumnLayoutChanges(unittest.TestCase):
    """Cells are found by header name, so the sheet can grow columns."""

    def test_a_sheet_written_before_the_date_column_still_parses(self):
        rows = apply_verdicts.parse_review(REVIEW_LEGACY)
        self.assertEqual(rows[0]["verdict"], "confirmed")
        self.assertEqual(rows[0]["note"], "clear")

    def test_the_seven_column_sheet_no_longer_reads_the_link_as_a_verdict(self):
        rows = apply_verdicts.parse_review(REVIEW_SEVEN_COLUMN)
        self.assertEqual(rows[0]["verdict"], "confirmed")
        self.assertNotIn("http", rows[0]["verdict"])

    def test_a_row_before_any_header_is_an_error_rather_than_a_guess(self):
        orphan = "## systematic — strong (1 shown)\n\n| ep000001 | x |\n"
        with self.assertRaises(ValueError):
            apply_verdicts.parse_review(orphan)
