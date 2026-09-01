#!/usr/bin/env python3
"""Rank episodes along the axis and write a review sheet you can verify.

  ./build_candidates.py

An episode is a maximal run of consecutive same-kind cycles. A document yields
a sequence of episodes, not a label -- the same student can work one way and
then the other in the same document.

Sampling is stratified rather than top-ranked. Ranking purely by strength shows
only the clearest cases and teaches nothing about the boundary, so the sheet
carries strong examples of each pole, a band straddling the threshold, and some
unclassified cycles as a check that the phenomenon is not being missed.
"""
import os
import re
from datetime import datetime
from urllib.parse import urlencode

import build_descriptions
import lib
# The burst gap these cycles were built at, recorded in the review sheet
# because composition rates move with it. Imported rather than restated: two
# copies of the same constant drift, and the sheet would then report a gap the
# cycles were not built at.
from build_cycles import BURST_GAP_S

CLUE_BASE = os.environ.get(
    "CC_CLUE_BASE", "https://collaborative-learning.concord.org/branch/master/")
# The portal these documents were authored through. `authed/learn_concord_org`
# is the Firestore namespace the documents live in, which is derived from this
# host, so it is not a guess.
PORTAL = os.environ.get("CC_CLUE_PORTAL", "https://learn.concord.org")

# A trial-and-error burst touches at least this many distinct targets.
TE_TARGETS = 3
STRONG_CYCLES = 4      # episodes at least this long are "strong"
BOUNDARY_CYCLES = 2    # episodes this short sit on the boundary
PER_STRATUM = 15
MAX_GAP_CYCLES = 1     # an episode survives one unclassified cycle in the middle

CANDIDATE_COLUMNS = {
    "episode_id": "VARCHAR", "doc_id": "VARCHAR", "doc_key": "VARCHAR",
    "uid": "VARCHAR", "unit": "VARCHAR", "problem": "VARCHAR",
    "tile_id": "VARCHAR",
    "kind": "VARCHAR", "n_cycles": "BIGINT", "purity": "DOUBLE",
    "span_s": "DOUBLE", "started": "TIMESTAMP", "ended": "TIMESTAMP",
    "first_entry_id": "VARCHAR", "last_entry_id": "VARCHAR",
    "replay_url": "VARCHAR", "end_url": "VARCHAR", "stratum": "VARCHAR",
    "offering_source": "VARCHAR",
}


def classify_cycle(row):
    """Label one cycle, or decline to. Most cycles decline.

    Composition carries the axis, because Task 4 established that pause length
    does not separate: 118,054 gaps decay smoothly with no second peak. This is
    also closer to CLUE-575, which defines trial and error as changing blocks
    rapidly "without systematicity (one change at a time)".

    Oscillation and undo are tested before the target count because they are
    direct evidence rather than a proxy: putting something in and taking it
    back out is trial and error even when only one target was touched, which
    the count alone would read as systematic. That ordering makes oscillation
    the most common route to a trial-and-error label -- see build_cycles.py on
    what it does and does not detect.
    """
    targets = row.get("n_distinct_targets") or 0
    if row.get("oscillation") or (row.get("undo_in_burst") or 0) > 0:
        return "trial_and_error"
    if targets >= TE_TARGETS:
        return "trial_and_error"
    # One change, then evidence the student checked it -- either by exercising
    # the program, by watching it afterward (the presence data covers the
    # pause with no other activity), or by writing something down. Composition
    # alone is too weak to call systematic: a single-target burst may just be
    # an interrupted one.
    if targets == 1 and (row.get("trial_after")
                         or row.get("pause_type") in ("documenting", "watching")):
        return "systematic"
    return "unclassified"


def replay_url(doc_key, entry_id, class_id, offering_id):
    """A CLUE URL that opens someone else's document at a point in its history.

    `studentDocument` alone is not enough. CLUE has to authenticate you against
    the portal and place you in the class the document belongs to, or
    `fetchFullDocument` has nothing to look in. Five more parameters do that,
    and CLUE refuses the launch without them (`src/models/stores/portal.ts`:
    "Missing class parameter!", "Missing offering parameter!", and "Unable to
    get classInfoUrl or offeringId"):

      authDomain    kicks off the OAuth2 redirect to the portal, which is what
                    logs you in. This is the parameter whose absence made every
                    previously generated link fail.
      researcher    authenticate as a researcher rather than needing to appear
                    in the class roster as a teacher or student
      reportType    must be exactly `offering`; CLUE rejects anything else
      class         portal class info URL
      offering      portal offering URL

    `unit` and `problem` are deliberately absent: when `offering` is present
    CLUE reads both from the offering's activity_url and ignores the params
    (`getProblemIdForAuthenticatedUser` in src/lib/portal-api.ts).

    Returns None when the offering id is unknown, rather than a URL that will
    fail on arrival.
    """
    if not class_id or not offering_id:
        return None
    params = urlencode({
        "class": "%s/api/v1/classes/%s" % (PORTAL, class_id),
        "offering": "%s/api/v1/offerings/%s" % (PORTAL, offering_id),
        "reportType": "offering",
        "authDomain": PORTAL,
        "researcher": "true",
        "studentDocument": doc_key,
        "studentDocumentHistoryId": entry_id,
    })
    return "%s?%s" % (CLUE_BASE.rstrip("/") + "/", params)


def episode_date(ep):
    """The day the episode started, YYYY-MM-DD.

    This is the client clock -- history entries carry the timestamp of the
    machine the student was working on, not the server's. Documents whose
    clocks run backwards are already excluded upstream (`clock_suspect`), so
    these dates are consistent within a document, but a device set to the
    wrong date will report the wrong day here.
    """
    started = ep.get("started")
    if not started:
        return "-"
    return str(started)[:10]


PORTAL_IDS_SQL = """
WITH pop AS (SELECT doc_id FROM read_parquet('{population}')),
doc AS (
  SELECT c.doc_id, c.doc_key, c.portal_class_id AS class_id,
         c.rtdb_offering_id, c.offering_id
  FROM read_parquet('{content}') c JOIN pop USING (doc_id)
),
-- Third source: the log events, which carry offering_id per document.
by_key AS (
  SELECT doc_key, min(offering_id) AS offering_id
  FROM read_parquet('{logs}')
  WHERE offering_id IS NOT NULL AND offering_id <> ''
  GROUP BY doc_key
)
SELECT d.doc_id, d.doc_key, d.class_id,
       coalesce(d.rtdb_offering_id, d.offering_id, k.offering_id) AS offering_id,
       CASE WHEN d.rtdb_offering_id IS NOT NULL THEN 'rtdb'
            WHEN d.offering_id IS NOT NULL THEN 'firestore'
            WHEN k.offering_id IS NOT NULL THEN 'log-document'
            ELSE 'none' END AS offering_source
FROM doc d
LEFT JOIN by_key k USING (doc_key)
"""


def _portal_ids(derived, content, logs):
    """doc_id -> (doc_key, class_id, offering_id, offering_source).

    The class id is on the document. The offering id has three sources, all
    of them per-document facts rather than inferences, and none of them ever
    disagree with each other on this corpus:

      rtdb          `offeringId` on the RTDB metadata node, written when the
                    document was created. The most complete by far: 3,784 of
                    4,674 documents. The ~890 without it are `personal`,
                    `publication` and similar documents, which belong to no
                    single offering and correctly have none.
      firestore     `offeringId` on the Firestore metadata document. The same
                    value, but the field was added late, so only 177 documents
                    carry it. Kept as a cross-check (0 conflicts with the RTDB).
      log-document  `offering_id` on the document's log events. Also agrees
                    (0 conflicts), and covers documents whose metadata nodes
                    are missing.

    An earlier version inferred the offering from other documents in the same
    class working the same problem. That is dropped: it is a guess rather than
    a fact, and once the RTDB source was found it resolved nothing extra. A
    missing link is better than a link to the wrong offering.
    """
    rows = lib.query(PORTAL_IDS_SQL.format(
        population=os.path.join(derived, "population.parquet"),
        content=content, logs=logs))
    return {r["doc_id"]: (r["doc_key"], r["class_id"], r["offering_id"],
                          r["offering_source"])
            for r in rows}


def _episodes(cycles_path):
    """Fold cycles into same-kind runs, tolerating one unclassified cycle in
    the middle.

    The tolerance is what makes `purity` mean anything. Maximal *strictly*
    same-kind runs are 100% pure by construction, so the number would carry no
    information -- and real episodes would fragment on a single ambiguous
    pause, which is common given how weak the tick signal is.
    """
    rows = lib.query(
        "SELECT doc_id, uid, unit, problem, tile_id, cycle_id, burst_started, "
        "n_changes, n_distinct_targets, pause_type, oscillation, undo_in_burst, "
        "trial_after, trial_changes, pause_after_s, n_nodes_after, "
        "first_entry_id, last_entry_id FROM read_parquet('%s') "
        "ORDER BY doc_id, tile_id, burst_started" % cycles_path)

    episodes = []
    current = None

    def close(ep):
        if not ep:
            return
        # Trailing tolerated cycles are not part of the episode.
        ep["n_cycles"] -= ep.pop("pending")
        ep["purity"] = ep["n_kind"] / ep["n_cycles"]
        ep["span_s"] = (datetime.fromisoformat(ep["ended"])
                        - datetime.fromisoformat(ep["started"])).total_seconds()
        ep.pop("key")
        ep.pop("n_kind")
        episodes.append(ep)

    for row in rows:
        kind = classify_cycle(row)
        key = (row["doc_id"], row["tile_id"])
        if current and current["key"] == key:
            if kind == current["kind"]:
                current["n_cycles"] += 1
                current["n_kind"] += 1
                current["pending"] = 0
                current["ended"] = row["burst_started"]
                current["last_entry_id"] = row["last_entry_id"]
                continue
            if kind == "unclassified" and current["pending"] < MAX_GAP_CYCLES:
                current["pending"] += 1
                current["n_cycles"] += 1
                continue
        close(current)
        current = None
        if kind != "unclassified":
            current = {"key": key, "kind": kind, "doc_id": row["doc_id"],
                       "uid": row["uid"], "unit": row["unit"],
                       "problem": row["problem"], "tile_id": row["tile_id"],
                       "n_cycles": 1, "n_kind": 1, "pending": 0,
                       "started": row["burst_started"],
                       "ended": row["burst_started"],
                       "first_entry_id": row["first_entry_id"],
                       "last_entry_id": row["last_entry_id"]}
    close(current)
    return episodes


def _episode_id(cell):
    """The bare id from an episode cell.

    The cell is a markdown link to episodes.md, so the raw text is
    `[ep000123](episodes.md#ep000123)`. Matching on that would key review work
    by a string that changes whenever the link does -- which silently dropped
    five notes the first time the link was added.
    """
    m = re.match(r"^\[([^\]]+)\]\(.*\)$", cell.strip())
    return (m.group(1) if m else cell).strip()


def _existing_reviews(path):
    """Read verdict and note cells already filled in, keyed by episode id.

    Rebuilding the sheet must not throw away review work. Cells are located by
    the header row rather than by position, so adding or reordering a column
    cannot silently carry the wrong text forward -- which is exactly how
    apply_verdicts.py came to read the replay link as a verdict.
    """
    if not os.path.exists(path):
        return {}
    kept, header = {}, None
    with open(path) as handle:
        for line in handle:
            if not line.startswith("|"):
                continue
            cells = [c.strip() for c in line.strip().strip("|").split("|")]
            if cells and cells[0] == "episode":
                header = cells
                continue
            # The |---|---| separator carries no data.
            if not header or set("".join(cells)) <= {"-"}:
                continue
            row = dict(zip(header, cells))
            verdict, note = row.get("verdict", ""), row.get("note", "")
            if verdict or note:
                kept[_episode_id(row.get("episode", ""))] = (verdict, note)
    return kept


def _stratum(ep):
    if ep["n_cycles"] >= STRONG_CYCLES:
        return "strong"
    if ep["n_cycles"] >= BOUNDARY_CYCLES:
        return "boundary"
    return "control"


def main():
    derived = lib.ensure_derived()
    cycles = os.path.join(derived, "cycles.parquet")
    lib.require_file(cycles)

    p = lib.paths()
    ids = _portal_ids(derived, p["content"], p["logs"])

    episodes = _episodes(cycles)
    for i, ep in enumerate(episodes):
        ep["episode_id"] = "ep%06d" % i
        ep["stratum"] = _stratum(ep)
        doc_key, class_id, offering_id, source = ids.get(
            ep["doc_id"], (ep["doc_id"], None, None, "none"))
        ep["doc_key"] = doc_key
        ep["offering_source"] = source
        ep["replay_url"] = replay_url(doc_key, ep["first_entry_id"],
                                      class_id, offering_id)
        ep["end_url"] = replay_url(doc_key, ep["last_entry_id"],
                                   class_id, offering_id)

    out = os.path.join(derived, "candidates.parquet")
    lib.write_parquet(
        [{k: ep.get(k) for k in CANDIDATE_COLUMNS} for ep in episodes],
        out, CANDIDATE_COLUMNS)

    print("episodes: %d (%d systematic, %d trial-and-error)"
          % (len(episodes),
             sum(1 for e in episodes if e["kind"] == "systematic"),
             sum(1 for e in episodes if e["kind"] == "trial_and_error")))
    linkable = sum(1 for e in episodes if e["replay_url"])
    by_source = {}
    for e in episodes:
        by_source[e["offering_source"]] = by_source.get(e["offering_source"], 0) + 1
    print("replay links: %d of %d episodes" % (linkable, len(episodes)))
    for src in ("rtdb", "firestore", "log-document", "none"):
        if by_source.get(src):
            print("  offering id from %-18s %5d" % (src, by_source[src]))
    print("wrote candidates.parquet")

    review_path = os.path.join(derived, "review.md")
    kept = _existing_reviews(review_path)

    sampled = [e for kind in ("systematic", "trial_and_error")
               for stratum in ("strong", "boundary", "control")
               for e in [x for x in episodes
                         if x["kind"] == kind and x["stratum"] == stratum][:PER_STRATUM]]
    described = build_descriptions.describe(
        lib.paths()["history"], cycles, sampled)

    lines = ["# Review sheet", "",
             "Each row is one episode. Open the replay link, watch what the "
             "student actually did, and fill in the verdict column.", "",
             "The episode id links to `episodes.md`, which says in words "
             "what the student did in each cycle -- easier to read than the "
             "replay, where tick output is interleaved with the edits.", "",
             "`start` opens the episode's first history entry and `end` "
             "its last. Both open the same document at different points: CLUE "
             "cannot yet show a range on the slider (CLUE-635), so the two "
             "links are how you see where the episode begins and ends.", "",
             "Verdicts: `confirmed`, `rejected`, `ambiguous`. Save this file, "
             "then run `apply_verdicts.py`.", "",
             "Strata: **strong** = a long run, **boundary** = a short run near "
             "the threshold, **control** = a single cycle. Boundary and control "
             "rows matter most -- they are where the thresholds are wrong.", "",
             "`date` is when the episode started, on the student's own device "
             "clock -- history entries carry no server timestamp.", "",

             "## What these episodes are", "",
             "An episode is a run of consecutive edit-and-pause cycles on one "
             "tile in one document, all carrying the same label. A cycle is a "
             "burst of edits plus the pause that follows it.", "",
             "**systematic** -- each cycle changed exactly one thing, and "
             "there is evidence the student then checked it: they ran the "
             "program, or the pause after the edit shows them present and "
             "watching, or they wrote something down.", "",
             "**trial_and_error** -- a cycle changed three or more distinct "
             "things before any check, or it undid something, or it "
             "oscillated: a value put in and taken back out within the same "
             "burst. Oscillation is the most common route to this label, so "
             "it is the one most worth checking.", "",
             "Cycles matching neither rule are unclassified, which is most of "
             "them. An episode tolerates one unclassified cycle in the middle "
             "without breaking; `purity` in candidates.parquet records what "
             "fraction of the cycles actually carried the label.", "",

             "## What to look for", "",
             "The label is inferred from the shape of the edits. You are "
             "judging whether someone watching the student would agree.", "",
             "For a **systematic** row, check that the student really changed "
             "one thing at a time and really checked between changes. Reject "
             "it if the pause was the student leaving or idling rather than "
             "attending to the program, or if they changed several things and "
             "only one was visible to the detector.", "",
             "For a **trial_and_error** row, check that the student changed "
             "several things before observing any result. Reject it if a "
             "repeated change was a deliberate comparison rather than "
             "flailing, or if the edits were housekeeping -- renaming, moving "
             "or resizing tiles -- rather than trying things out.", "",
             "Use `ambiguous` when the replay does not settle it. That is a "
             "useful answer, not a failure to decide.", "",
             "Built at a burst gap of %.1fs, calibrated against trial-bounded "
             "cycles (see `build_cycles.py`). Composition rates still move with "
             "that number, and it is calibrated on the documents that have "
             "detectable trials rather than the whole corpus, so treat the "
             "rates as approximate and compare students against each other."
             % BURST_GAP_S, ""]

    for kind in ("systematic", "trial_and_error"):
        for stratum in ("strong", "boundary", "control"):
            picked = [e for e in episodes
                      if e["kind"] == kind and e["stratum"] == stratum][:PER_STRATUM]
            if not picked:
                continue
            lines += ["## %s — %s (%d shown)" % (kind, stratum, len(picked)), "",
                      "| episode | date | unit/problem | cycles | changes | "
                      "start | end | verdict | note |",
                      "|---|---|---|---|---|---|---|---|---|"]
            for e in picked:
                # No offering id means no launchable URL. Show the document key
                # instead of a link that would fail on arrival.
                start = ("[start](%s)" % e["replay_url"] if e["replay_url"]
                         else "no offering id (`%s`)" % e["doc_key"])
                end = "[end](%s)" % e["end_url"] if e["end_url"] else "-"
                verdict, note = kept.get(e["episode_id"], ("", ""))
                summary = described.get(e["episode_id"], {}).get("summary", "-")
                lines.append("| [%s](episodes.md#%s) | %s | %s %s | %d | %s "
                             "| %s | %s | %s | %s |" % (
                    e["episode_id"], e["episode_id"], episode_date(e),
                    e["unit"] or "-", e["problem"] or "-",
                    e["n_cycles"], summary, start, end, verdict, note))
            lines.append("")

    with open(review_path, "w") as handle:
        handle.write("\n".join(lines))
    print("wrote review.md")

    with open(os.path.join(derived, "episodes.md"), "w") as handle:
        handle.write(build_descriptions.render(sampled, described))
    print("wrote episodes.md (%d episodes)" % len(described))
    if kept:
        shown = {e["episode_id"] for e in episodes}
        dropped = [ep for ep in kept if ep not in shown]
        print("  carried forward %d reviewed row(s)" % (len(kept) - len(dropped)))
        # A reviewed episode the rebuild no longer samples would vanish
        # silently, which is the failure this whole function exists to stop.
        if dropped:
            print("  WARNING: %d reviewed row(s) no longer in the sheet: %s"
                  % (len(dropped), ", ".join(sorted(dropped))))


if __name__ == "__main__":
    main()
