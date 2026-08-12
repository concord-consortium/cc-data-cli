import unittest

import apply_verdicts

REVIEW = """# Review sheet

## systematic — strong (2 shown)

| episode | unit/problem | cycles | replay | verdict | note |
|---|---|---|---|---|---|
| ep000001 | brain 1.4 | 6 | [replay](http://x) | confirmed | clear |
| ep000002 | brain 1.4 | 5 | [replay](http://x) | rejected | just idle |

## trial_and_error — boundary (1 shown)

| episode | unit/problem | cycles | replay | verdict | note |
|---|---|---|---|---|---|
| ep000003 | brain 1.5 | 2 | [replay](http://x) |  |  |
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
