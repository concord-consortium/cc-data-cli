import unittest

import apply_verdicts
import build_candidates

# The sheet as build_candidates.py writes it: one `###` section per episode,
# under a `##` stratum heading.
REVIEW = """# Review sheet

## systematic — strong (2 shown)

### ep000001

- **verdict:** confirmed
- **note:** clear
- **date:** 2024-03-01
- **replay:** [start][ep000001-start]

### ep000002

- **verdict:** rejected
- **note:** just idle
- **date:** 2024-03-02

## trial_and_error — boundary (1 shown)

### ep000003

- **verdict:**
- **note:**
- **date:** 2024-03-03

[ep000001-start]: http://x
"""

# The table sheet this script was written against, before episodes became
# sections. It must yield nothing rather than half-parse: apply_verdicts.main()
# turns an empty result into an error naming the format, which is the failure
# this file's predecessor could not produce.
REVIEW_TABLE = """# Review sheet

## systematic — strong (1 shown)

| episode | date | unit/problem | cycles | start | end | verdict | note |
|---|---|---|---|---|---|---|---|
| ep000001 | 2024-03-01 | brain 1.4 | 6 | [start](http://x) | [end](http://y) | confirmed | clear |
"""


class TestParseSheetFromApplyVerdicts(unittest.TestCase):
    """apply_verdicts.py reads the sheet through the module that writes it.

    Owning a second parser is what let the two drift: this script read the
    replay link as a verdict when a column moved, and later matched nothing at
    all once episode ids stopped being decimal -- reporting "0 reviewed" in
    both cases rather than failing.
    """

    def test_uses_the_writers_parser(self):
        self.assertIs(apply_verdicts.parse_sheet, build_candidates.parse_sheet)

    def test_reads_verdicts_and_their_section(self):
        rows = apply_verdicts.parse_sheet(REVIEW)
        self.assertEqual(len(rows), 3)
        self.assertEqual(rows[0]["episode_id"], "ep000001")
        self.assertEqual(rows[0]["kind"], "systematic")
        self.assertEqual(rows[0]["stratum"], "strong")
        self.assertEqual(rows[0]["verdict"], "confirmed")
        self.assertEqual(rows[0]["note"], "clear")

    def test_an_unreviewed_episode_has_an_empty_verdict(self):
        rows = apply_verdicts.parse_sheet(REVIEW)
        self.assertEqual(rows[2]["verdict"], "")

    def test_the_old_table_sheet_yields_nothing_rather_than_half_parsing(self):
        """Nothing is better than something wrong here: main() turns an empty
        result into an error that names the sheet, so a stale file cannot be
        mistaken for an unreviewed one."""
        self.assertEqual(apply_verdicts.parse_sheet(REVIEW_TABLE), [])


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
