#!/usr/bin/env python3
"""Say, in words, what a student did during an episode.

Watching a replay is a poor way to see what changed: the tick stream
interleaves machine output with student actions, and a parameter edit looks
identical to the value it produced. This reconstructs the student's operations
as sentences instead, one group per cycle, so an episode can be read rather
than watched.

Everything here comes from patch VALUES, which build_edits.py deliberately
discards -- it keeps `op` and `path`, which is all the classification needs.
The values carry the meaning:

  node add         `value.name` is the type: Number, Sensor, Logic, Math,
                   Timer, Generator, Transform, Control, Demo Output, Live
                   Output. It appears ONLY on the add, so a node touched later
                   is named by looking up the add that created it.
  node remove      no value; the node id in the path resolves against that map.
  connection add   `value.node` and `value.output` are the source and its
                   socket; the path gives the target and its input socket.
  connection remove  no value. The path still names the target and input, so
                   the sentence says which input was disconnected rather than
                   inventing a source.
  parameter        both the name (from the path) and the new value are
                   present. The OLD value is not, which is why a revisit
                   cannot be diffed -- only the sequence of new values is
                   recoverable. See build_cycles.py on oscillation.

The episodes the review sheet samples are already described inside review.md,
which build_candidates.py writes. Run this directly to look at an episode the
sheet did not sample -- cheap enough to do on demand, one episode being about
a fifth of a second:

    ./build_descriptions.py epb15d6f25a1 ep838fd6850d

It prints to stdout and writes no file. There is deliberately no whole-corpus
output: a second file describing every episode would only go stale beside the
sheet.
"""

import os
import sys
from datetime import datetime, timedelta

import build_cycles
import lib

# Repeats of one gesture inside this window are one operation, matching
# build_edits.COALESCE_MS. Without it a single drag emits a dozen sentences.
COALESCE_S = 2

# Runtime output, not student edits. `demoOutput` is the Demo Output node's
# live display (median 0.08s between rewrites); `orderedDisplayName` is
# derived. build_edits.CLASSIFY still counts the first as a parameter edit --
# a known defect recorded in the design -- but a description that repeated it
# would be unreadable, so it is excluded here regardless.
RUNTIME_PARAMS = ("demoOutput", "orderedDisplayName")

# Tick values live under each node at `/data/tickEntries/{id}/...`. Excluding
# tick ACTIONS is not enough -- they also arrive inside `setProgram` entries
# (14,648 patches, 6,310 operations after coalescing, corpus-wide) -- so they
# are excluded by path as well. Matched as a prefix, not an exact name.
RUNTIME_PARAM_PREFIX = "tickEntries/"

# Simulator variables are addressed by array INDEX in the action path, so
# trials.parquet carries `var_id` = "3" rather than a name. The name is on the
# `add` patch that created the variable. Resolves for 514 of the 516
# (document, index) pairs that carry trials, with no pair mapping to two
# different names.
VAR_NAMES_SQL = """
WITH rec AS (
  SELECT doc_id, unnest(json_extract(entry_json, '$.records[*]')) AS r
  FROM read_parquet('{history}')
  WHERE doc_id IN ({docs})
),
p AS (
  SELECT doc_id, unnest(json_extract(r, '$.patches[*]')) AS patch FROM rec
)
SELECT doc_id,
       regexp_extract(json_extract_string(patch, '$.path'),
                      '/variables/([0-9]+)$', 1) AS var_id,
       any_value(json_extract_string(json_extract(patch, '$.value'),
                                     '$.displayName')) AS display_name
FROM p
WHERE json_extract_string(patch, '$.op') = 'add'
  AND regexp_matches(json_extract_string(patch, '$.path'), '/variables/[0-9]+$')
  AND json_extract_string(json_extract(patch, '$.value'), '$.displayName') IS NOT NULL
GROUP BY doc_id, var_id;
"""

OPS_SQL = """
WITH rec AS (
  SELECT doc_id, created,
         unnest(json_extract(entry_json, '$.records[*]')) AS r
  FROM read_parquet('{history}')
  WHERE doc_id IN ({docs})
    -- The same action filter build_edits.py applies before classifying.
    -- Without it the tick stream arrives as parameter edits: ticks are stored
    -- under each node at `/data/tickEntries/{{id}}`, so the node-data branch
    -- would sweep them in. One 5-cycle episode read as 1,356 parameter
    -- changes before this line existed.
    AND action NOT LIKE '%/content/step'
    AND action NOT LIKE '%tickAndProcess'
),
pat AS (
  SELECT doc_id, created, unnest(json_extract(r, '$.patches[*]')) AS patch
  FROM rec
),
p AS (
  SELECT doc_id, created,
         json_extract_string(patch, '$.op')   AS op,
         json_extract_string(patch, '$.path') AS path,
         json_extract(patch, '$.value')       AS val
  FROM pat
),
-- A node's type is recorded only when it is added, so every add in the
-- document's whole history is needed, not just those inside the episode.
-- `created` is kept because a node id can be REUSED for a different type
-- after the first is deleted (63 such ids in this corpus), so the type must
-- be resolved as of the operation's own time rather than per document.
node_types AS (
  SELECT doc_id, created,
         regexp_extract(path, '/program/nodes/([^/]+)$', 1) AS node_id,
         json_extract_string(val, '$.name') AS node_type
  FROM p
  WHERE op = 'add' AND regexp_matches(path, '/program/nodes/[^/]+$')
),
ops AS (
  SELECT doc_id, created, op, val,
    coalesce(nullif(regexp_extract(path, '/tileMap/([^/]+)', 1), ''), '') AS tile_id,
    regexp_extract(path, '/program/nodes/([^/]+)', 1) AS node_id,
    CASE
      WHEN regexp_matches(path, '/program/nodes/[^/]+$')        THEN 'node'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/inputs/') THEN 'connection'
      WHEN regexp_matches(path, '/program/nodes/[^/]+/data/')   THEN 'parameter'
    END AS kind,
    regexp_extract(path, '/inputs/([^/]+)/', 1) AS socket,
    regexp_extract(path, '/data/(.*)$', 1)      AS param
  FROM p
  WHERE regexp_matches(path, '/program/nodes/')
),
named AS (
  SELECT o.*,
    (SELECT s.node_type FROM node_types s
      WHERE s.doc_id = o.doc_id AND s.node_id = o.node_id
        AND s.created <= o.created
      ORDER BY s.created DESC LIMIT 1) AS node_type,
    (SELECT s.node_type FROM node_types s
      WHERE s.doc_id = o.doc_id
        AND s.node_id = json_extract_string(o.val, '$.node')
        AND s.created <= o.created
      ORDER BY s.created DESC LIMIT 1) AS source_type,
    json_extract_string(o.val, '$.output') AS source_socket,
    json_extract_string(o.val, '$') AS raw_value
  FROM ops o
  WHERE o.kind IS NOT NULL
    AND (o.param IS NULL OR o.param = ''
         OR (o.param NOT IN ({runtime})
             AND o.param NOT LIKE 'tickEntries/%'))
),
-- Collapse a repeated gesture the way build_edits.py does, so one drag is one
-- sentence. Keyed on the same tuple it uses: target, kind and op.
marked AS (
  SELECT *, CASE WHEN epoch(created) - epoch(lag(created) OVER (
              PARTITION BY doc_id, tile_id, node_id, kind, op, param
              ORDER BY created)) <= {coalesce} THEN 0 ELSE 1 END AS is_new
  FROM named
),
grouped AS (
  SELECT *, sum(is_new) OVER (PARTITION BY doc_id, tile_id, node_id, kind, op, param
                              ORDER BY created) AS grp
  FROM marked
)
SELECT doc_id, tile_id, min(created) AS created, kind, op,
       any_value(node_type) AS node_type, any_value(source_type) AS source_type,
       any_value(source_socket) AS source_socket, any_value(socket) AS socket,
       any_value(param) AS param,
       -- The last value of a coalesced run is the one that stuck.
       arg_max(raw_value, created) AS value
FROM grouped
GROUP BY doc_id, tile_id, node_id, kind, op, param, grp
ORDER BY doc_id, tile_id, created;
"""


def sentence(row):
    """One operation as a phrase. Unknown node types read `?` rather than
    being dropped: a gap the reader can see beats a silent omission."""
    kind, op = row["kind"], row["op"]
    node = row.get("node_type") or "?"
    if kind == "node":
        return ("added a %s node" % node if op == "add"
                else "deleted the %s node" % node)
    if kind == "connection":
        if op == "add":
            return "connected %s -> %s (%s)" % (
                row.get("source_type") or "?", node, row.get("socket") or "?")
        return "disconnected the %s input of %s" % (row.get("socket") or "?", node)
    return "set %s.%s = %s" % (node, row.get("param") or "?",
                               row.get("value") if row.get("value") is not None else "?")


def summarise(rows):
    """The compact `changes` cell for the review sheet.

    Counts by operation kind, naming node types only when there are one or
    two -- more than that and the cell stops being scannable, which is the
    only thing it is for.
    """
    if not rows:
        return "-"
    counts, types = {}, []
    for r in rows:
        key = {"node": "node", "connection": "conn", "parameter": "param"}[r["kind"]]
        counts[key] = counts.get(key, 0) + 1
        t = r.get("node_type")
        if t and t not in types:
            types.append(t)
    parts = ["%d %s" % (counts[k], k) for k in ("node", "conn", "param") if k in counts]
    out = ", ".join(parts)
    if 0 < len(types) <= 2:
        out += " (%s)" % ", ".join(types)
    return out


def _pause(cycle, trials):
    """What followed the burst. `trial_after` means the student drove the
    program's input, which is stronger evidence of checking than any pause
    length -- so it is reported ahead of the pause type. For a simulation with
    no slider there is no such evidence to have, and the pause type is all
    there is; see build_trials.py on terrarium.

    Naming the input matters: "tested Target EMG x3" says what the student was
    varying, where "tested (3 input changes)" only says that they were.
    Counts are per input here, whereas cycles.trial_changes is the single
    largest trial in the window -- so the two can differ when a student drove
    more than one input.

    This line used to read "tested Gripper x12", which was backwards: the
    Gripper is what the program DRIVES. The label is right now because
    build_trials.py was rewritten onto the slider, not because anything here
    changed -- the name comes from whichever variable trials.parquet points at.
    """
    secs = cycle.get("pause_after_s")
    tail = "%ds" % round(secs) if secs is not None else "?"
    if cycle.get("trial_after"):
        if trials:
            named = ", ".join("%s x%d" % (label, n) for label, n in trials)
            return "tested %s, %s" % (named, tail)
        # trial_after came from cycles.parquet, so a trial is known to exist;
        # saying so without a name beats implying none happened.
        return "tested (%s input changes), %s" % (cycle.get("trial_changes") or 0, tail)
    ptype = cycle.get("pause_type") or "unknown"
    return "%s, %s" % (ptype.replace("_", " "), tail)


def group_by_cycle(ops, cycles):
    """Assign each operation to the cycle it happened in.

    An operation belongs to the latest cycle that had started by then. Cycles
    arrive ordered, so a linear walk is enough and no interval join is needed.
    """
    out = [(c, []) for c in cycles]
    for op in ops:
        placed = None
        for i, c in enumerate(cycles):
            if str(op["created"]) >= str(c["burst_started"]):
                placed = i
            else:
                break
        if placed is not None:
            out[placed][1].append(op)
    return out


def _trials(history, derived, docs):
    """Every trial in these documents, labelled by the input it drove.

    Simulator trials name the variable; sensor trials name the reading and
    whether the device was physical or simulated. An unresolvable variable
    falls back to its index rather than being dropped -- the trial happened
    either way.
    """
    doc_list = ", ".join("'%s'" % d.replace("'", "''") for d in docs)
    names = {}
    for r in lib.query(VAR_NAMES_SQL.format(history=history, docs=doc_list)):
        names[(r["doc_id"], r["var_id"])] = r["display_name"]

    rows = []
    for r in lib.query(
            "SELECT doc_id, var_id, started, n_changes FROM read_parquet('%s') "
            "WHERE doc_id IN (%s)"
            % (os.path.join(derived, "trials.parquet"), doc_list)):
        label = names.get((r["doc_id"], r["var_id"]))
        rows.append({"doc_id": r["doc_id"], "started": r["started"],
                     "label": label or "Simulator input #%s" % r["var_id"],
                     "n": r["n_changes"]})
    for r in lib.query(
            "SELECT doc_id, sensor_kind, sensor_type, started, n_ticks "
            "FROM read_parquet('%s') WHERE doc_id IN (%s)"
            % (os.path.join(derived, "sensor_trials.parquet"), doc_list)):
        rows.append({"doc_id": r["doc_id"], "started": r["started"],
                     "label": "%s (%s)" % (r["sensor_type"] or "sensor",
                                           r["sensor_kind"] or "?"),
                     "n": r["n_ticks"]})
    return rows


def _trials_after(trials, cycle):
    """Trials the cycle's burst was checked by.

    Same window build_cycles.py uses to set `trial_after`: a trial starting
    at or after the burst ends, within TRIAL_WINDOW_S of it. Merged by label,
    since one input driven twice is still one input.
    """
    end = str(cycle["burst_ended"])
    horizon = str(cycle["burst_ended"] + timedelta(
        seconds=build_cycles.TRIAL_WINDOW_S))
    hits = {}
    for t in trials:
        if t["doc_id"] != cycle["doc_id"]:
            continue
        if end <= str(t["started"]) <= horizon:
            hits[t["label"]] = hits.get(t["label"], 0) + (t["n"] or 0)
    return sorted(hits.items(), key=lambda kv: (-kv[1], kv[0]))


def describe(history, cycles_path, episodes):
    """Return {episode_id: {"summary": str, "cycles": [(cycle, [ops])]}}."""
    if not episodes:
        return {}
    docs = sorted({e["doc_id"] for e in episodes})
    rows = lib.query(OPS_SQL.format(
        history=history,
        docs=", ".join("'%s'" % d.replace("'", "''") for d in docs),
        runtime=", ".join("'%s'" % p for p in RUNTIME_PARAMS),
        coalesce=COALESCE_S))

    cycles = lib.query(
        "SELECT doc_id, tile_id, burst_started, burst_ended, n_distinct_targets, "
        "pause_after_s, pause_type, trial_after, trial_changes "
        "FROM read_parquet('%s') ORDER BY doc_id, tile_id, burst_started"
        % cycles_path)
    for c in cycles:
        c["burst_ended"] = datetime.fromisoformat(str(c["burst_ended"]))
    trials = _trials(history, os.path.dirname(cycles_path), docs)

    described = {}
    for ep in episodes:
        ep_cycles = [c for c in cycles
                     if c["doc_id"] == ep["doc_id"] and c["tile_id"] == ep["tile_id"]
                     and str(ep["started"]) <= str(c["burst_started"]) <= str(ep["ended"])]
        # `ended` is the last burst's START. Cutting operations there would
        # drop everything the student did inside that final burst, so the
        # window runs to when the burst actually finished.
        window_end = max([str(c["burst_ended"]) for c in ep_cycles],
                         default=str(ep["ended"]))
        in_ep = [r for r in rows
                 if r["doc_id"] == ep["doc_id"] and r["tile_id"] == ep["tile_id"]
                 and str(ep["started"]) <= str(r["created"]) <= window_end]
        described[ep["episode_id"]] = {
            "summary": summarise(in_ep),
            "cycles": [(c, ops, _trials_after(trials, c))
                       for c, ops in group_by_cycle(in_ep, ep_cycles)],
        }
    return described


def cycles_block(d):
    """The per-cycle narrative for one episode, as markdown lines.

    Shared with build_candidates.py, which renders the same block inside the
    review sheet. Kept here because the shape of a cycle line is this module's
    business.
    """
    if not d["cycles"]:
        return ["_No program operations recorded in this window._", ""]
    # A fenced block, not a numbered list: markdown folds a list item's
    # continuation lines into one paragraph, and parameter values are data
    # that may contain markdown metacharacters.
    lines = ["```"]
    for n, (cycle, ops, trials) in enumerate(d["cycles"], start=1):
        if not ops:
            # A cycle with no describable operation still happened; saying so
            # beats renumbering and implying it did not.
            lines.append("%d. (no program change captured)" % n)
        else:
            lines.append("%d. %s" % (n, sentence(ops[0])))
            for op in ops[1:]:
                lines.append("   %s" % sentence(op))
        note = ""
        if (cycle.get("n_distinct_targets") or 0) > 1:
            note = "   [%d targets]" % cycle["n_distinct_targets"]
        lines.append("   -> %s%s" % (_pause(cycle, trials), note))
    return lines + ["```", ""]


def render(episodes, described):
    """One section per episode, for `build_descriptions.py <id>` on stdout.

    The review sheet no longer reads this: build_candidates.py folds the same
    cycles_block() into review.md so there is one file to read and edit rather
    than a sheet pointing at a companion. This stays for looking at an episode
    that the sheet did not sample.
    """
    lines = ["# Episode details", "",
             "What the student actually did, per cycle, reconstructed from the "
             "document history. Printed by `build_descriptions.py` for "
             "episodes the review sheet did not sample; the sampled ones are "
             "described inside `review.md` itself.", "",
             "`-> ` is what followed the burst: `tested` means the student "
             "drove the program's input afterwards, which is the strongest "
             "evidence in the data that they checked the change.", ""]
    for ep in episodes:
        d = described.get(ep["episode_id"])
        if not d:
            continue
        links = []
        if ep.get("replay_url"):
            links.append("[start](%s)" % ep["replay_url"])
        if ep.get("end_url"):
            links.append("[end](%s)" % ep["end_url"])
        lines += ["### %s · %s · %d cycles · %s · %s %s" % (
            ep["episode_id"], ep["kind"], ep["n_cycles"], str(ep["started"])[:10],
            ep["unit"] or "-", ep["problem"] or "-"), ""]
        if links:
            lines += [" · ".join(links), ""]
        lines += cycles_block(d)
    return "\n".join(lines) + "\n"


def main():
    derived = lib.ensure_derived()
    candidates = os.path.join(derived, "candidates.parquet")
    cycles = os.path.join(derived, "cycles.parquet")
    lib.require_file(candidates)
    lib.require_file(cycles)

    wanted = sys.argv[1:]
    where = ""
    if wanted:
        where = " WHERE episode_id IN (%s)" % ", ".join(
            "'%s'" % w.replace("'", "''") for w in wanted)
    episodes = lib.query(
        "SELECT * FROM read_parquet('%s')%s ORDER BY episode_id" % (candidates, where))
    if not episodes:
        sys.exit("no matching episodes")
    if not wanted:
        # Checked before describing rather than after: the whole corpus is
        # thousands of episodes, and describing them only to refuse to write
        # is minutes of wasted work.
        #
        # No file is written for the whole corpus. The descriptions a reviewer
        # needs are folded into review.md by build_candidates.py; a second
        # file holding every episode would only go stale beside it.
        sys.exit("pass episode ids to print their descriptions, e.g.\n"
                 "  ./build_descriptions.py %s\n"
                 "The sampled episodes are already described in review.md."
                 % episodes[0]["episode_id"])
    described = describe(lib.paths()["history"], cycles, episodes)
    sys.stdout.write(render(episodes, described))


if __name__ == "__main__":
    main()
