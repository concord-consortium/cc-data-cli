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
import hashlib
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
    "first_entry_idx": "BIGINT", "last_entry_idx": "BIGINT",
    "n_entries": "BIGINT", "outputs": "VARCHAR",
    "replay_url": "VARCHAR", "end_url": "VARCHAR", "stratum": "VARCHAR",
    "offering_source": "VARCHAR",
}

# What the student's program could actually drive, resolved as of the episode.
#
# Read from history rather than from the content snapshot, which is the
# document's FINAL state: a student can rebuild a program completely, and one
# episode here ends with a Timer driving a Light Bulb having spent the episode
# on a Sensor driving a Grabber.
#
# `Live Output` drives a device; `hubSelect` says which, and CLUE writes the
# literal string `⚠️ connect device` when a node wants one and none is
# attached. That is the most common value in the corpus (451 documents), and it
# means the program produced no visible effect anywhere -- a strong reason for a
# pause to be idle, and worth telling apart from a student ignoring a working
# gripper.
#
# `Demo Output` never touches hardware, but it is not nothing: it animates on
# screen, which is more watchable than a number changing, so it is reported too
# with whatever it was set to display.
DEMO_OUTPUT_DEFAULT = "Light Bulb"

OUTPUT_EVENTS_SQL = """
WITH pat AS (
  SELECT h.doc_id, h.created,
         unnest(json_extract(h.entry_json, '$.records[*].patches[*]')) AS p
  FROM read_parquet('{history}') h
  WHERE h.doc_id IN (SELECT DISTINCT doc_id FROM read_parquet('{cycles}'))
),
e AS (
  SELECT doc_id, created,
         regexp_extract(json_extract_string(p, '$.path'), '/tileMap/([^/]+)', 1) AS tile_id,
         regexp_extract(json_extract_string(p, '$.path'),
                        '/program/nodes/([^/]+)', 1) AS node_id,
         json_extract_string(p, '$.op') AS op,
         json_extract_string(p, '$.path') AS path,
         json_extract_string(json_extract(p, '$.value'), '$.name') AS node_name,
         json_extract_string(p, '$.value') AS raw_value
  FROM pat
  WHERE regexp_matches(json_extract_string(p, '$.path'), '/program/nodes/')
)
SELECT doc_id, tile_id, node_id, created,
       CASE
         WHEN regexp_matches(path, '/program/nodes/[^/]+$') AND op = 'add' THEN 'add'
         WHEN regexp_matches(path, '/program/nodes/[^/]+$') AND op = 'remove' THEN 'remove'
         WHEN regexp_matches(path, '/data/hubSelect$') THEN 'hubSelect'
         WHEN regexp_matches(path, '/data/liveOutputType$') THEN 'liveOutputType'
         WHEN regexp_matches(path, '/data/outputType$') THEN 'outputType'
       END AS kind,
       coalesce(node_name, raw_value) AS value
FROM e
WHERE (regexp_matches(path, '/program/nodes/[^/]+$') AND op IN ('add', 'remove'))
   OR regexp_matches(path, '/data/(hubSelect|liveOutputType|outputType)$')
ORDER BY doc_id, tile_id, node_id, created
"""

# Where an episode's first and last entries sit in the document's history, and
# how long that history is -- so a reviewer can see whether an episode is early
# work or a final pass, and how much of the document it covers.
#
# `idx` is CLUE's own `index` field on the entry, not a position this pipeline
# assigns, so it is the number the history slider counts in. It is not always
# unique: 15 of the 2,294 documents carrying episodes repeat an index across
# two distinct entries, and 19 hold a different number of entries than the
# document's own metadata claims (worst case 298). Both are recorded upstream
# in history_documents.parquet as `distinct_idx` and `expected_max_idx`. The
# index is reported as CLUE stores it rather than renumbered, because a
# renumbered position would not match what the reviewer sees.
#
# Semi-join against cycles.parquet rather than an IN list of ~9,000 literal
# entry ids, which would be a quarter-megabyte of SQL text.
ENTRY_POSITIONS_SQL = """
WITH want AS (
  SELECT first_entry_id AS entry_id FROM read_parquet('{cycles}')
  UNION
  SELECT last_entry_id FROM read_parquet('{cycles}')
),
totals AS (
  SELECT doc_id, count(*) AS n_entries
  FROM read_parquet('{history}') GROUP BY doc_id
)
SELECT h.doc_id, h.entry_id, h.idx, t.n_entries
FROM read_parquet('{history}') h
JOIN want w USING (entry_id)
JOIN totals t USING (doc_id)
"""


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


def episode_id(ep):
    """A stable id for an episode, derived from what the episode IS.

    Positional ids (`ep%06d` over the episode list) were the previous scheme,
    and they shift for every episode after any episode that appears or
    disappears. Review work keyed on them silently reattaches to a different
    episode on the next rebuild, which is worse than losing it.

    The inputs are deliberately the document, the tile, and the episode's
    start and end truncated to the second -- NOT the history entry ids. Entry
    ids can shift when upstream parsing changes, and an id built on them would
    churn for reasons that have nothing to do with the episode. Second
    resolution absorbs sub-second jitter in the timestamps for the same
    reason. Where the ids do change, doc_id, tile_id, started and ended are
    all carried in candidates.parquet, so old and new can still be matched.
    """
    key = "|".join([
        ep["doc_id"] or "", ep["tile_id"] or "",
        str(ep["started"])[:19], str(ep["ended"])[:19],
    ])
    return "ep" + hashlib.sha1(key.encode("utf-8")).hexdigest()[:10]


# The three places that touch review.md -- this writer, the carry-forward on
# rebuild, and apply_verdicts.py -- agree on the format through parse_sheet()
# alone. They used to hold three separate views of a markdown table, and two of
# them rotted: apply_verdicts.py read the replay link as a verdict when a
# column moved, and later matched nothing at all once episode ids stopped being
# decimal. One parser means a format change breaks loudly or not at all.
STRATUM_HEADING = re.compile(r"^##\s+(\S+)\s+—\s+(\w+)")
EPISODE_HEADING = re.compile(r"^###\s+(\S+)\s*$")
REVIEW_FIELD = re.compile(r"^-\s+\*\*(verdict|note):\*\*(.*)$")


def parse_sheet(text):
    """Every episode in the sheet, with whatever review has been filled in.

    Fields are found by key name inside the episode's own `### <id>` section,
    so adding or reordering fields cannot carry the wrong text forward.
    """
    rows, kind, stratum, row = [], "", "", None
    fenced = False
    for line in text.splitlines():
        # The cycle block is fenced because it carries parameter VALUES, which
        # are student data and may be any text at all. Skipping it means a
        # value cannot forge a `- **verdict:**` line, and it is why the block
        # is fenced rather than rendered as a list.
        if line.startswith("```"):
            fenced = not fenced
            continue
        if fenced:
            continue
        heading = STRATUM_HEADING.match(line)
        if heading:
            kind, stratum = heading.group(1), heading.group(2)
            continue
        episode = EPISODE_HEADING.match(line)
        if episode:
            row = {"episode_id": episode.group(1), "kind": kind,
                   "stratum": stratum, "verdict": "", "note": ""}
            rows.append(row)
            continue
        field = REVIEW_FIELD.match(line)
        if field and row is not None:
            row[field.group(1)] = field.group(2).strip()
    return rows


def _existing_reviews(path):
    """Verdicts and notes already filled in, keyed by episode id.

    Rebuilding the sheet must not throw away review work.
    """
    if not os.path.exists(path):
        return {}
    with open(path) as handle:
        rows = parse_sheet(handle.read())
    return {r["episode_id"]: (r["verdict"], r["note"]) for r in rows
            if r["verdict"] or r["note"]}


def _dropped_reviews(kept, sampled):
    """Reviewed episode ids that the rebuild will not write anywhere.

    Compared against the SAMPLED episodes, not every episode: only sampled
    rows are rendered into review.md, so an episode that survives the rebuild
    but falls out of the sample carries its verdict and note into nothing.
    Comparing against the full episode list reported those as carried forward
    and warned about neither -- two notes were lost that way.
    """
    shown = {e["episode_id"] for e in sampled}
    return sorted(ep for ep in kept if ep not in shown)


def _entry_field(label, when, idx, n_entries, url):
    """One end of the episode: when it happened, where in the history, and the
    link that opens the document there.

    Position is `entry N of M` because neither number means much alone --
    entry 400 is early in a 5,000-entry document and the whole story in a
    420-entry one.
    """
    where = "entry %s of %s" % ("?" if idx is None else idx,
                                "?" if n_entries is None else n_entries)
    # No offering id means no launchable URL, and a link that fails on arrival
    # is worse than none.
    link = "[open](%s)" % url if url else "no offering id"
    return "- **%s:** %s · %s · %s" % (label, str(when)[:19], where, link)


def _episode_section(e, kept, described):
    """One episode: its fields as a list, then what the student did.

    The fields are a bulleted list rather than a table row. A row carrying two
    ~300-character replay URLs was unreadable in the markdown source, which is
    where this file is filled in -- and `verdict` and `note`, the only two
    cells a reviewer typed into, sat at the far right of it. Here they come
    first, and each field is its own line however long its URL is.

    `verdict` and `note` are matched back by _existing_reviews() and
    apply_verdicts.py on the `- **key:** value` shape, so the marker and the
    key name are load-bearing; the rest of the list is for reading.
    """
    ep_id = e["episode_id"]
    verdict, note = kept.get(ep_id, ("", ""))
    # No trailing space on an empty field: editors that strip trailing
    # whitespace on save would otherwise rewrite every unreviewed episode and
    # bury the real edits in the diff.
    lines = ["### %s" % ep_id, "",
             ("- **verdict:** %s" % verdict).rstrip(),
             ("- **note:** %s" % note).rstrip(),
             "- **date:** %s" % episode_date(e),
             "- **unit/problem:** %s %s" % (e["unit"] or "-", e["problem"] or "-"),
             "- **document:** `%s`" % e["doc_id"],
             "- **outputs:** %s" % (e.get("outputs") or "none"),
             "- **cycles:** %d" % e["n_cycles"],
             "- **changes:** %s" % described.get(ep_id, {}).get("summary", "-"),
             _entry_field("start", e["started"], e.get("first_entry_idx"),
                          e.get("n_entries"), e["replay_url"]),
             _entry_field("end", e["ended"], e.get("last_entry_idx"),
                          e.get("n_entries"), e["end_url"]),
             ""]
    d = described.get(ep_id)
    if d:
        lines += build_descriptions.cycles_block(d)
    return lines


def _output_state(events):
    """(doc_id, tile_id) -> ordered events, for resolving outputs per episode."""
    by_tile = {}
    for r in events:
        if r["kind"]:
            by_tile.setdefault((r["doc_id"], r["tile_id"]), []).append(r)
    return by_tile


def _outputs_during(events, started, ended):
    """What the tile's output nodes were at any point during the episode.

    Presence is an interval overlap, not a snapshot. Resolving at the
    episode's end alone would drop a node the student built, watched and then
    deleted -- which is exactly the thing they were watching. Anything present
    for even part of the window is something they could have been looking at.

    Parameters take their last value at or before `ended`, since that is the
    state the node spent the episode arriving at.
    """
    nodes, live = {}, {}
    for e in events:
        # `continue`, not `break`: the events arrive grouped by node, so the
        # first one past the episode belongs to whichever node sorted first
        # and says nothing about the rest. Breaking here dropped every output
        # in an episode whose earliest node outlived it.
        if str(e["created"]) > str(ended):
            continue
        node = nodes.setdefault(e["node_id"], {"spans": []})
        if e["kind"] == "add":
            # A reused node id starts a fresh node, not a continuation, but
            # its earlier spans still happened and are kept.
            spans = node["spans"]
            nodes[e["node_id"]] = {"name": e["value"], "spans": spans}
            live[e["node_id"]] = str(e["created"])
        elif e["kind"] == "remove":
            opened = live.pop(e["node_id"], None)
            if opened is not None:
                node["spans"].append((opened, str(e["created"])))
            node["last_name"] = node.get("name") or node.get("last_name")
            node["name"] = None
        elif e["kind"]:
            node[e["kind"]] = e["value"]
    for node_id, opened in live.items():
        nodes[node_id]["spans"].append((opened, None))

    out = []
    for node in nodes.values():
        # Overlaps the window if it started before the episode ended and had
        # not already been deleted when the episode began.
        if not any(span[0] <= str(ended)
                   and (span[1] is None or span[1] >= str(started))
                   for span in node["spans"]):
            continue
        name = node.get("name") or node.get("last_name")
        if name == "Live Output":
            # hubSelect is the binding; the type alone says what it would drive
            # if anything were attached.
            out.append("Live Output %s" % (node.get("hubSelect")
                                           or node.get("liveOutputType")
                                           or "unset"))
        elif name == "Demo Output":
            # A node whose type was never written is still showing something:
            # demo-output-node.ts defaults `outputType` to "Light Bulb", and
            # MST records only changes, so silence means the default.
            out.append("Demo Output %s"
                       % (node.get("outputType") or DEMO_OUTPUT_DEFAULT))
    # Deduplicated and sorted: two identical outputs are one thing to watch,
    # and a stable order keeps the sheet diffable between rebuilds.
    return sorted(set(out))


def _entry_positions(cycles, history):
    """entry_id -> (its index in the document, the document's entry count).

    Keyed on entry_id alone: entry ids are unique across the whole corpus --
    6,381,134 entries, no id repeated even within a document -- so the doc_id
    would add nothing to the key.
    """
    return {r["entry_id"]: (r["idx"], r["n_entries"])
            for r in lib.query(ENTRY_POSITIONS_SQL.format(
                cycles=cycles, history=history))}


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

    positions = _entry_positions(cycles, p["history"])
    outputs = _output_state(lib.query(OUTPUT_EVENTS_SQL.format(
        history=p["history"], cycles=cycles)))

    episodes = _episodes(cycles)
    for ep in episodes:
        ep["episode_id"] = episode_id(ep)
        ep["stratum"] = _stratum(ep)
        first = positions.get(ep["first_entry_id"], (None, None))
        last = positions.get(ep["last_entry_id"], (None, None))
        ep["first_entry_idx"], ep["n_entries"] = first
        ep["last_entry_idx"] = last[0]
        ep["outputs"] = ", ".join(_outputs_during(
            outputs.get((ep["doc_id"], ep["tile_id"]), []),
            ep["started"], ep["ended"]))
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
             "One `###` section per episode. Fill in **verdict** and **note** "
             "on the section itself -- they are the first two fields so you "
             "are not scrolling past anything to reach them.", "",
             "Verdicts: `confirmed`, `rejected`, `ambiguous`. Save this file, "
             "then run `apply_verdicts.py`. Everything else in a section is "
             "regenerated on the next build, so only those two fields "
             "survive; write anything longer in **note** rather than as loose "
             "prose, which would be lost.", "",
             "The fenced block under each episode says in words what the "
             "student did in each cycle -- easier to read than the replay, "
             "where tick output is interleaved with the edits. It is often "
             "enough on its own.", "",
             "Each cycle is two phases. **Program Changes** is the burst of "
             "edits and how long it took. **Outside Program** is the gap from "
             "that burst's last edit to the next burst's first, and "
             "everything that happened inside it. The two sum to the duration "
             "in the cycle's header, which also carries the wall-clock time "
             "and the history index of the burst's first entry -- so you can "
             "open the replay at a cycle rather than at the episode.", "",
             "`tested` means the student drove the program's input. `switched "
             "to` means they clicked one of the Simulator tile's own "
             "controls. `sim:` is what moved in the simulation while they "
             "watched: `no change` means it ran and nothing happened, `not "
             "running` means it was not running, which is no evidence either "
             "way.", "",
             "`sim:` says something moved, not who moved it. The student may "
             "have driven it by moving the slider, or indirectly -- that move "
             "running through their program to close the gripper and raise "
             "the pressure. Or a generator, a timer or a feedback loop moved "
             "it with the student sitting still. A `tested` line in the same "
             "cycle is what says they were driving the input at the time.", "",
             "A `[...]` marker means the variable moves on its own. The "
             "gripper simulation's pan boils on a loop, and Temperature "
             "reports it whenever the gripper is closed far enough -- so a "
             "gripper held closed shows a changing temperature with nobody "
             "driving it. The terrarium's humidity falls 1%/min whatever is "
             "running. Do not read a marked change as the program having done "
             "something. The terrarium's Temperature is unmarked and moves "
             "only for the fan or the heat lamp, so it is honest evidence.",
             "",
             "**start** and **end** are the two ends of the episode: when it "
             "happened, where the entry sits in the document's history, and a "
             "link that opens the document there. They are separate fields "
             "because CLUE cannot yet show a range on the slider (CLUE-635), "
             "so opening both is how you see where the episode begins and "
             "ends.", "",
             "**outputs** is what the program could drive during the "
             "episode, and so what the student could have been watching. "
             "`Live Output` drives a device and names its binding; "
             "`⚠️ connect device` is CLUE's own words for a node that wants "
             "one with none attached -- that program produced no visible "
             "effect anywhere, which is a strong reason for a pause to be "
             "idle. `Demo Output` never touches hardware but animates on "
             "screen, which is more watchable than a number changing, so it "
             "counts too. A node the student built and later deleted is still "
             "listed: it was there to watch at the time.", "",
             "`entry 412 of 5,003` is CLUE's own index for that entry and the "
             "number of entries the document holds -- how far into the "
             "student's work this episode sits, and how much of it the "
             "episode covers.", "",
             "Strata: **strong** = a long run, **boundary** = a short run near "
             "the threshold, **control** = a single cycle. Boundary and control "
             "episodes matter most -- they are where the thresholds are wrong.", "",
             "`date` is when the episode started, on the student's own device "
             "clock -- history entries carry no server timestamp.", "",

             "## What these episodes are", "",
             "An episode is a run of consecutive edit-and-pause cycles on one "
             "tile in one document, all carrying the same label. A cycle is a "
             "burst of edits plus the pause that follows it.", "",
             "**systematic** -- each cycle changed exactly one thing, and "
             "there is evidence the student then checked it: they moved the "
             "simulation's slider, or the pause after the edit shows them "
             "present and watching, or they wrote something down.", "",
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
             "For a **systematic** episode, check that the student really changed "
             "one thing at a time and really checked between changes. Reject "
             "it if the pause was the student leaving or idling rather than "
             "attending to the program, or if they changed several things and "
             "only one was visible to the detector.", "",
             "One asymmetry to allow for: the strongest evidence of checking "
             "is the student driving the simulation's input, and the "
             "terrarium simulation has no control to drive -- it is a closed "
             "loop fed by the student's own outputs. Those episodes can only "
             "ever be evidenced by pausing or writing something down, so a "
             "thinner case there is a limit of the data rather than a weaker "
             "student.", "",
             "For a **trial_and_error** episode, check that the student changed "
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
            lines += ["## %s — %s (%d shown)" % (kind, stratum, len(picked)), ""]
            for e in picked:
                lines += _episode_section(e, kept, described)

    with open(review_path, "w") as handle:
        handle.write("\n".join(lines))
    print("wrote review.md (%d episodes)" % len(described))
    if kept:
        dropped = _dropped_reviews(kept, sampled)
        print("  carried forward %d reviewed row(s)" % (len(kept) - len(dropped)))
        # A reviewed episode the rebuild no longer samples would vanish
        # silently, which is the failure this whole function exists to stop.
        if dropped:
            print("  WARNING: %d reviewed row(s) no longer in the sheet: %s"
                  % (len(dropped), ", ".join(sorted(dropped))))


if __name__ == "__main__":
    main()
