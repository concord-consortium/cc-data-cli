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

Each cycle also reports what the SIMULATION did during the pause that followed,
on a `sim:` line. It answers one question only: was there anything to see while
the student watched?

It does NOT say who caused the movement, and must not be read that way. The
student may well have driven it -- directly, by moving the slider during the
pause, or indirectly, where that slider move ran through their program, closed
the gripper and raised the surface pressure. Or nothing of the sort: a wave
generator, a timer, or a feedback loop in which the simulation's own reading
re-triggers the program will all move these variables with the student sitting
still.

The `-> ` line on the same cycle is what separates those. A trial reported
there means the student was driving the input while this moved; no trial, and
the movement was the program running on its own.

That question is the only one available for terrarium, which has no slider and
so can never show a trial (see build_trials.py). A 60-second pause in which
Temperature never moved and one in which it climbed two degrees are different
pauses, and nothing else in the description distinguishes them.

Three outcomes are distinguished, because they mean different things:

  sim: Gripper 31..93 x12     the simulation ran and responded
  sim: no change (Temp 21)    it ran and did not respond -- the pause was
                              real, and nothing came of it
  sim: not running            no variable was written at all, so there is no
                              evidence either way. Not the same as no response.

The episodes the review sheet samples are already described inside review.md,
which build_candidates.py writes. Run this directly to look at an episode the
sheet did not sample -- cheap enough to do on demand, one episode being about
a fifth of a second:

    ./build_descriptions.py epb15d6f25a1 ep838fd6850d

It prints to stdout and writes no file. There is deliberately no whole-corpus
output: a second file describing every episode would only go stale beside the
sheet.
"""

import json
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
                                     '$.displayName')) AS display_name,
       any_value(CAST(json_extract(json_extract(patch, '$.value'),
                                   '$.labels') AS VARCHAR)) AS labels
FROM p
WHERE json_extract_string(patch, '$.op') = 'add'
  AND regexp_matches(json_extract_string(patch, '$.path'), '/variables/[0-9]+$')
  AND json_extract_string(json_extract(patch, '$.value'), '$.displayName') IS NOT NULL
GROUP BY doc_id, var_id;
"""

# Which variable the student drives by hand, so it can be left out of the
# simulation's response. One per document at most; terrarium has none.
SLIDER_VARS_SQL = """
SELECT DISTINCT doc_id,
       regexp_extract(json_extract_string(p, '$.path'),
                      '/variables/([0-9]+)/value$', 1) AS var_id
FROM (SELECT doc_id, unnest(json_extract(entry_json, '$.records[*].patches[*]')) AS p
      FROM read_parquet('{history}')
      WHERE doc_id IN ({docs}) AND action LIKE '%commitTemporaryValue')
WHERE regexp_matches(json_extract_string(p, '$.path'),
                     '/sharedModel/variables/[0-9]+/value$');
"""

# What the simulation did during each pause: every shared-model variable
# written between the burst ending and the pause ending, with its range.
#
# Aggregated in SQL rather than pulled into Python -- the value stream is the
# largest thing in the corpus (3.9M writes across the documents that carry
# episodes) and only the range and count per cycle are wanted.
#
# The variables belong to the document's shared model, not to a tile, so two
# Dataflow tiles in one document see the same simulation. Each tile's cycles
# get the same response, which is the truth rather than an approximation.
SIM_RESPONSE_SQL = """
WITH cyc AS (
  SELECT doc_id, tile_id, cycle_id, burst_ended,
         burst_ended + to_seconds(CAST(coalesce(pause_after_s, {window}) AS BIGINT))
           AS pause_ended
  FROM read_parquet('{cycles}')
  WHERE doc_id IN ({docs})
),
v AS (
  SELECT doc_id, created,
         regexp_extract(json_extract_string(p, '$.path'),
                        '/variables/([0-9]+)/value$', 1) AS var_id,
         TRY_CAST(json_extract_string(p, '$.value') AS DOUBLE) AS val
  FROM (SELECT doc_id, created,
               unnest(json_extract(entry_json, '$.records[*].patches[*]')) AS p
        FROM read_parquet('{history}') WHERE doc_id IN ({docs}))
  WHERE regexp_matches(json_extract_string(p, '$.path'),
                       '/sharedModel/variables/[0-9]+/value$')
)
SELECT c.doc_id, c.tile_id, c.cycle_id, v.var_id,
       count(*) AS n, min(v.val) AS lo, max(v.val) AS hi
FROM cyc c
JOIN v ON v.doc_id = c.doc_id
      AND v.created > c.burst_ended AND v.created <= c.pause_ended
WHERE v.val IS NOT NULL
GROUP BY c.doc_id, c.tile_id, c.cycle_id, v.var_id;
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
    # Clicks on the Simulator tile's own controls lead, because they change
    # what the simulation is doing and so change how everything after them
    # reads -- a mode switch is why a temperature suddenly starts moving.
    switched = ", ".join("switched to %s" % c
                         for c in cycle.get("controls_after") or ())
    if switched:
        switched += ", "
    if cycle.get("trial_after"):
        if trials:
            named = ", ".join("%s x%d" % (label, n) for label, n in trials)
            return "%stested %s, %s" % (switched, named, tail)
        # trial_after came from cycles.parquet, so a trial is known to exist;
        # saying so without a name beats implying none happened.
        return "%stested (%s input changes), %s" % (
            switched, cycle.get("trial_changes") or 0, tail)
    ptype = cycle.get("pause_type") or "unknown"
    return "%s%s, %s" % (switched, ptype.replace("_", " "), tail)


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


# `EMG` is not a response: brainwaves-gripper's step() computes it as
# `Target EMG` minus a random fraction of itself, EVERY FRAME. It moves
# constantly whatever the student does, so reporting it would claim the
# simulation responded on every cycle of every gripper document -- one such
# pause showed `EMG 36..439 x3731` and meant nothing at all.
#
# Named outright rather than derived from the slider, after two narrower rules
# each missed cases. Keying on the slider missed documents where the student
# never touched it, and keying on `Target EMG` being present missed an older
# version of the simulation that has no such variable (its EMG runs 40..490 and
# its Surface Pressure 0..3800). The reason is simpler than either rule: the
# EMG is the muscle signal, which is upstream of the program in every version
# of the simulation. It is never the simulation replying to what the program
# did, so it is never a response.
#
# `Pin` is the same shape in potentiometer-servo: its step() sets it to
# `round(Potentiometer / maxPotAngle * maxResistReading)`, a pure function of
# the slider. Like EMG it is labelled `sensor:` -- the program reads it through
# a Sensor node -- but it carries no information the `-> ` line does not
# already carry, since it is the slider's own value arithmetically restated. terrarium has neither: it has no slider, so every
# labelled variable it carries is a genuine response.
#
# The simulation's own name lives in the content snapshot, which this module
# does not read; these variable names are in the history it already reads.
STIMULUS_VARS = ("Target EMG", "EMG", "Pin")


# Variables that move with nothing driving them, and the companion variable
# that says which simulation this document is running (the simulation's own
# name is in the content snapshot, which this module does not read).
#
# These are still worth reporting -- the student may also be driving them --
# but they cannot be read as the program having done something, so the line
# says so where it appears.
#
#   Temperature, in brainwaves-gripper, is the pan's temperature whenever the
#   gripper is closed past a threshold and the simulation is in temperature
#   mode, and `baseTemperature` otherwise. The pan itself runs
#   `demoStreams.fastBoil` indexed by `frame % length`: it ramps and loops
#   forever regardless of anyone. A gripper held closed therefore reports a
#   changing temperature with no student and no program involved.
#
#   Humidity, in terrarium, has `baseHumidityImpactPerStep` applied every step
#   whatever else is running -- "-10% every 10 minutes" in the source. Over a
#   two-minute pause that is a 2% fall on its own. Terrarium's Temperature has
#   no such term: only the fan and the heat lamp move it, so it stays honest
#   evidence that the program drove an output.
SELF_DRIVEN = {
    ("Temperature", "Pan Temperature"): "pan's own boil cycle",
    ("Humidity", "Heat Lamp"): "falls 1%/min on its own",
}


def _self_driven(name, doc_names):
    """The note for a variable that moves on its own, or None."""
    for (var, companion), note in SELF_DRIVEN.items():
        if name == var and companion in doc_names:
            return note
    return None


# The Simulator tile's own controls, which the student clicks directly. In
# brainwaves-gripper's component these are two buttons, Pressure and
# Temperature, and selectMode() writes the variable with `setValue` -- the same
# action a Live Output node uses, so the control is identified by VARIABLE
# rather than by action. Nothing else can write this one: it carries no
# `live-output:` label, so no output node can bind to it.
#
# Reported on the `-> ` line because it is the student operating the
# simulation, alongside the slider. It is deliberately NOT fed to
# build_trials.py: a mode switch changes what the simulation is doing, not the
# value of the program's input, and counting it as a trial would mix the two.
#
# It matters for reading the `sim:` line. Switching to Temperature is what
# connects the pan's own boil cycle to the reported Temperature, so a mode
# change and a temperature swing in the same cycle are one event, not two.
SIM_CONTROLS = {
    "Simulation Mode": {0: "Pressure", 1: "Temperature"},
}

SIM_CONTROL_SQL = """
SELECT doc_id, created,
       regexp_extract(json_extract_string(p, '$.path'),
                      '/variables/([0-9]+)/value$', 1) AS var_id,
       TRY_CAST(json_extract_string(p, '$.value') AS DOUBLE) AS val
FROM (SELECT doc_id, created,
             unnest(json_extract(entry_json, '$.records[*].patches[*]')) AS p
      FROM read_parquet('{history}') WHERE doc_id IN ({docs}))
WHERE regexp_matches(json_extract_string(p, '$.path'),
                     '/sharedModel/variables/[0-9]+/value$')
  AND ({wanted})
ORDER BY doc_id, created;
"""


def _sim_control_changes(history, docs, meta):
    """Every click on a Simulator tile control, as [{doc_id, started, label}].

    Restricted in SQL to the variables that are controls, which is why `meta`
    is resolved first: `setValue` writes 230k values across these documents and
    all but a few hundred of them are a Live Output node driving an actuator.
    """
    wanted = [(doc_id, var_id) for (doc_id, var_id), (name, _l) in meta.items()
              if name in SIM_CONTROLS]
    if not wanted:
        return []
    clause = " OR ".join(
        "(doc_id = '%s' AND regexp_extract(json_extract_string(p, '$.path'),"
        " '/variables/([0-9]+)/value$', 1) = '%s')"
        % (doc_id.replace("'", "''"), var_id) for doc_id, var_id in wanted)
    out = []
    for r in lib.query(SIM_CONTROL_SQL.format(
            history=history,
            docs=", ".join("'%s'" % d.replace("'", "''") for d in docs),
            wanted=clause)):
        name = meta[(r["doc_id"], r["var_id"])][0]
        label = SIM_CONTROLS[name].get(
            int(r["val"]) if r["val"] is not None else None)
        out.append({"doc_id": r["doc_id"], "started": r["created"],
                    # An unrecognised value is still a click; naming the raw
                    # value beats dropping the event or inventing a mode.
                    "label": label or "%s %g" % (name, r["val"] or 0)})
    return out


def _controls_after(controls, cycle):
    """Control clicks in the same window `trial_after` uses."""
    end = str(cycle["burst_ended"])
    horizon = str(cycle["burst_ended"] + timedelta(
        seconds=build_cycles.TRIAL_WINDOW_S))
    seen = []
    for c in controls:
        if c["doc_id"] != cycle["doc_id"]:
            continue
        if end <= str(c["started"]) <= horizon and c["label"] not in seen:
            seen.append(c["label"])
    return seen


def _var_meta(history, docs):
    """(doc_id, var_id) -> (display name, labels), for naming the response."""
    doc_list = ", ".join("'%s'" % d.replace("'", "''") for d in docs)
    meta = {}
    for r in lib.query(VAR_NAMES_SQL.format(history=history, docs=doc_list)):
        labels = json.loads(r["labels"]) if r["labels"] else []
        meta[(r["doc_id"], r["var_id"])] = (r["display_name"], labels)
    return meta


def _responding_vars(history, docs, meta):
    """Which variables count as the simulation responding, per document.

    Two are excluded, both because they restate the trial rather than because
    the student did not cause them. The slider variable is written by hand and
    is already reported on the `-> ` line. Whatever the simulation derives
    arithmetically from it says the same thing a second time, and says it on
    every frame whether the student moved or not -- see STIMULUS_VARS.

    What remains is not "things the student did not drive". A slider move
    during the pause reaches these variables through the program, and that is
    the student driving them; so is the gripper closing and the pressure
    rising as a result. It is only that nothing here is written by hand.

    The slider exclusion is defensive rather than load-bearing today: both
    sliders that exist (Target EMG, Potentiometer) are already dropped, the
    first by STIMULUS_VARS and the second by carrying no `sensor:` label. It
    stays because a slider on a sensor-labelled variable would otherwise be
    read as the simulation responding to the program.

    What remains is split by the labels the simulation declares: `output` is
    what the student's program drove (Gripper, Fan, Heat Lamp, Humidifier), and
    `sensor:` is what the simulation returned in response (Surface Pressure,
    Temperature, Humidity). Variables carrying neither -- Pan Temperature, Raw
    Temperature, Simulation Mode -- are simulation internals that move on their
    own, and are left out.
    """
    doc_list = ", ".join("'%s'" % d.replace("'", "''") for d in docs)
    sliders = {}
    for r in lib.query(SLIDER_VARS_SQL.format(history=history, docs=doc_list)):
        sliders.setdefault(r["doc_id"], set()).add(r["var_id"])

    keep = {}
    for (doc_id, var_id), (name, labels) in meta.items():
        if var_id in sliders.get(doc_id, ()) or name in STIMULUS_VARS:
            continue
        role = ("output" if "output" in labels
                else "sensor" if any(l.startswith("sensor:") for l in labels)
                else None)
        if role:
            keep[(doc_id, var_id)] = role
    return keep


def _sim_responses(history, cycles_path, docs):
    """(doc_id, tile_id, cycle_id) -> [(role, name, n, lo, hi)], ordered with
    the program's own outputs first."""
    doc_list = ", ".join("'%s'" % d.replace("'", "''") for d in docs)
    meta = _var_meta(history, docs)
    keep = _responding_vars(history, docs, meta)

    names_by_doc = {}
    for (doc_id, _var_id), (name, _labels) in meta.items():
        names_by_doc.setdefault(doc_id, set()).add(name)

    out = {}
    for r in lib.query(SIM_RESPONSE_SQL.format(
            history=history, cycles=cycles_path, docs=doc_list,
            window=build_cycles.TRIAL_WINDOW_S)):
        key = (r["doc_id"], r["tile_id"], r["cycle_id"])
        # Every cycle with any variable write at all gets an entry, even when
        # nothing kept moved: "the simulation ran and did not respond" and "the
        # simulation was not running" are different findings, and the caller
        # cannot tell them apart from an empty list alone.
        out.setdefault(key, [])
        role = keep.get((r["doc_id"], r["var_id"]))
        if not role:
            continue
        name = (meta.get((r["doc_id"], r["var_id"])) or (None,))[0]
        name = name or ("#" + r["var_id"])
        out[key].append((role, name, r["n"], r["lo"], r["hi"],
                         _self_driven(name, names_by_doc.get(r["doc_id"], ()))))
    for key in out:
        out[key].sort(key=lambda t: (t[0] != "output", t[1]))
    return out


# Values are compared at the precision they are printed at. Comparing raw
# doubles instead reported `Temperature 21..21 x34` -- a swing of less than a
# tenth of a degree, shown as a change because 20.96 and 21.04 are not equal.
DISPLAY_DP = 1


def _num(x):
    """Trim a float that is really an integer, so `21.0` prints as `21`."""
    return "%g" % round(x, DISPLAY_DP)


def _response_line(response):
    """What the simulation did during the pause, or why nothing is claimed.

    `None` means no variable was written at all -- the simulation was not
    running, which is not evidence that it failed to respond.
    """
    if response is None:
        return "sim: not running"
    moved = [r for r in response
             if round(r[3], DISPLAY_DP) != round(r[4], DISPLAY_DP)]
    if not moved:
        flat = ", ".join("%s %s" % (name, _num(lo))
                         for _role, name, _n, lo, _hi, _s in response)
        return "sim: no change" + (" (%s)" % flat if flat else "")
    return "sim: " + ", ".join(
        "%s %s..%s x%d%s" % (name, _num(lo), _num(hi), n,
                             " [%s]" % self_driven if self_driven else "")
        for _role, name, n, lo, hi, self_driven in moved)


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
        "SELECT doc_id, tile_id, cycle_id, burst_started, burst_ended, "
        "n_distinct_targets, pause_after_s, pause_type, trial_after, "
        "trial_changes "
        "FROM read_parquet('%s') ORDER BY doc_id, tile_id, burst_started"
        % cycles_path)
    trials = _trials(history, os.path.dirname(cycles_path), docs)
    responses = _sim_responses(history, cycles_path, docs)
    controls = _sim_control_changes(history, docs, _var_meta(history, docs))
    for c in cycles:
        c["burst_ended"] = datetime.fromisoformat(str(c["burst_ended"]))
        c["controls_after"] = _controls_after(controls, c)
        # Absent from `responses` means no variable was written in the pause at
        # all, which _response_line reports differently from "written but
        # unchanged". Carried on the cycle so cycles_block needs no extra
        # argument.
        c["sim_response"] = responses.get(
            (c["doc_id"], c["tile_id"], c["cycle_id"]))

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
        # What the simulation did while the student watched. Not evidence of
        # a trial and deliberately not used to detect one: it says something
        # moved, never who moved it. Pair it with the `-> ` line above, which
        # does say whether the student was driving the input at the time.
        lines.append("      %s" % _response_line(cycle.get("sim_response")))
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
             "evidence in the data that they checked the change, and "
             "`switched to` means they clicked one of the Simulator tile's "
             "own controls.", ""]
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
