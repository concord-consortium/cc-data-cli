#!/usr/bin/node

// READ-ONLY. Downloads CLUE document history entries into local JSONL.
//
// Canonical copy lives in cc-data-cli/local-data/clue-documents/. It must be run
// from the CLUE repo's scripts/ directory, because that is where
// serviceAccountKey.json and firebase-admin live:
//
//   cp download-history.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx download-history.ts --limit 8      # test on a handful
//   npx tsx download-history.ts                # the full pull
//
// Per document it writes:
//   <OUT>/<doc_id>.jsonl       one JSON object per history entry
//   <OUT>/<doc_id>.meta.json   integrity facts for that document
// Both are written to a .tmp name and renamed, so a partial file is never
// mistaken for a complete one. Re-running skips documents that already have a
// .meta.json, so an interrupted run resumes.
//
// No entry is ever dropped. Index gaps, duplicates and ranges that do not start
// at zero are expected; they are recorded in the meta file and left for query
// time to interpret.

import fs from "node:fs";
import path from "node:path";
import admin from "firebase-admin";
import { getFirestoreBasePath, getScriptRootFilePath } from "./lib/script-utils.js";

const PORTAL = "learn.concord.org";
const BASE = process.env.CC_DATA_LOCAL ? `${process.env.CC_DATA_LOCAL}/clue-documents` : "../../../local-data/clue-documents";
const HIST_JSON = `${BASE}/clue_dataflow_history_all.json`;
const OUT = `${BASE}/history`;
const CONCURRENCY = 20;

// Classes whose only assignment was the standalone Dataflow app or the
// /branch/dataflow build. All 861 of their documents have zero history entries.
const DATAFLOW_APP_CLASSES = new Set([
  "20769", "20770", "20771", "20772", "28", "8011", "20717", "21479", "21518", "21004",
]);

const args = process.argv.slice(2);
const limitArg = args.indexOf("--limit");
const LIMIT = limitArg >= 0 ? parseInt(args[limitArg + 1], 10) : undefined;
const docArg = args.indexOf("--doc");
const ONLY_DOC = docArg >= 0 ? args[docArg + 1] : undefined;

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const firestore = admin.firestore();
const docsPath = getFirestoreBasePath(PORTAL, false);

/**
 * Splits ids out of an MST action path so actions can be grouped.
 *   /content/tileMap/abc123/content/setSlate
 *     -> action "/content/tileMap/{tile}/content/setSlate", tile_id "abc123"
 */
function normalizeAction(raw: string | undefined) {
  if (!raw) return { action: null, tile_id: null, shared_model_id: null };
  let tile_id: string | null = null;
  let shared_model_id: string | null = null;
  let action = raw;

  action = action.replace(/\/tileMap\/([^/]+)/, (_m, id) => { tile_id = id; return "/tileMap/{tile}"; });
  action = action.replace(/\/sharedModelMap\/([^/]+)/, (_m, id) => {
    shared_model_id = id; return "/sharedModelMap/{sharedModel}";
  });
  action = action.replace(/\/rowMap\/([^/]+)/, "/rowMap/{row}");

  return { action, tile_id, shared_model_id };
}

function toIso(v: any): string | null {
  if (v == null) return null;
  if (typeof v === "number") return new Date(v).toISOString();
  if (typeof v === "string") { const d = new Date(v); return isNaN(+d) ? null : d.toISOString(); }
  if (typeof v?.toDate === "function") return v.toDate().toISOString();
  return null;
}

async function downloadDoc(meta: any) {
  const docId = meta.id;
  const jsonlPath = path.join(OUT, `${docId}.jsonl`);
  const metaPath = path.join(OUT, `${docId}.meta.json`);
  if (fs.existsSync(metaPath)) return { docId, skipped: true };

  const snap = await firestore.collection(`${docsPath}/${docId}/history`).orderBy("index").get();

  // Write incrementally. Building the whole file as one string and calling
  // writeFileSync fails with "Invalid string length" on documents whose history
  // exceeds V8's maximum string size — which real documents do.
  const fd = fs.openSync(`${jsonlPath}.tmp`, "w");
  let pending: string[] = [];
  let written = 0;
  let unparsed = 0;
  const flush = () => {
    if (pending.length) { fs.writeSync(fd, pending.join("")); pending = []; }
  };
  const emit = (obj: any) => {
    pending.push(JSON.stringify(obj) + "\n");
    written++;
    if (pending.length >= 500) flush();
  };

  const idxs: number[] = [];
  for (const d of snap.docs) {
    const data: any = d.data();
    let e: any = null;
    try { e = data.entry ? JSON.parse(data.entry) : null; } catch { e = null; }
    const { action, tile_id, shared_model_id } = normalizeAction(e?.action);
    if (typeof data.index === "number") idxs.push(data.index);

    if (e === null) unparsed++;
    emit({
      doc_id: docId,
      doc_uid: meta.uid ?? null,
      portal_class_id: meta.portal_class_id ?? null,
      unit: meta.unit ?? null,
      investigation: meta.investigation ?? null,
      problem: meta.problem ?? null,

      entry_id: d.id,
      idx: typeof data.index === "number" ? data.index : null,
      prev_entry_id: data.previousEntryId ?? null,
      created: toIso(e?.created),
      server_created: toIso(data.created),

      model: e?.model ?? null,
      action_raw: e?.action ?? null,
      action,
      tile_id,
      shared_model_id,

      n_records: Array.isArray(e?.records) ? e.records.length : null,
      undoable: e?.undoable ?? null,
      is_revert: e?.isRevert ?? null,
      entry_uid: e?.uid ?? null,
      state: e?.state ?? null,
      parse_ok: e !== null,

      entry_json: data.entry ?? null,
    });
  }

  flush();
  fs.closeSync(fd);
  fs.renameSync(`${jsonlPath}.tmp`, jsonlPath);

  const record = {
    doc_id: docId,
    expected_max_idx: meta.entries ?? null,
    entries_fetched: snap.size,
    min_idx: idxs.length ? Math.min(...idxs) : null,
    max_idx: idxs.length ? Math.max(...idxs) : null,
    distinct_idx: new Set(idxs).size,
    unparsed_entries: unparsed,
    fetched_at: new Date().toISOString(),
  };
  fs.writeFileSync(`${metaPath}.tmp`, JSON.stringify(record));
  fs.renameSync(`${metaPath}.tmp`, metaPath);
  return { docId, skipped: false, ...record };
}

async function mapLimit<T, R>(items: T[], limit: number, fn: (t: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length);
  let next = 0, done = 0;
  const t0 = Date.now();
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    for (;;) {
      const i = next++;
      if (i >= items.length) return;
      try { out[i] = await fn(items[i]); }
      catch (err: any) { out[i] = { error: err?.message ?? String(err), item: items[i] } as any; }
      done++;
      if (done % 25 === 0 || done === items.length) {
        const rate = done / ((Date.now() - t0) / 1000);
        const eta = (items.length - done) / Math.max(rate, 0.001);
        process.stderr.write(`\r  ${done}/${items.length} docs  ${rate.toFixed(1)}/s  eta ${(eta / 60).toFixed(1)}m   `);
      }
    }
  }));
  process.stderr.write("\n");
  return out;
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });

  const all = JSON.parse(fs.readFileSync(HIST_JSON, "utf8"));
  let work = all.filter((r: any) =>
    r.has_history && !(r.portal_class_id && DATAFLOW_APP_CLASSES.has(r.portal_class_id)));

  console.log(`documents with history, excluding Dataflow-app classes: ${work.length}`);
  if (ONLY_DOC) {
    work = work.filter((r: any) => r.id === ONLY_DOC);
    console.log(`--doc ${ONLY_DOC}: ${work.length} match, expected entries ${work[0]?.entries}`);
  }
  if (LIMIT) {
    // spread the sample across the size distribution rather than taking the head
    work = [...work].sort((a: any, b: any) => a.entries - b.entries);
    const step = Math.max(1, Math.floor(work.length / LIMIT));
    work = work.filter((_: any, i: number) => i % step === 0).slice(0, LIMIT);
    console.log(`--limit ${LIMIT}: sampling ${work.length} across the size distribution`);
    console.log(`  expected entry counts: ${work.map((w: any) => w.entries).join(", ")}`);
  }

  const results = await mapLimit(work, CONCURRENCY, downloadDoc);

  const errors = results.filter((r: any) => r?.error);
  const done = results.filter((r: any) => r && !r.error && !r.skipped);
  const skipped = results.filter((r: any) => r?.skipped);
  const totalEntries = done.reduce((a: number, r: any) => a + (r.entries_fetched ?? 0), 0);

  console.log(`\ndownloaded: ${done.length}   skipped (already present): ${skipped.length}   errors: ${errors.length}`);
  console.log(`entries written: ${totalEntries.toLocaleString()}`);
  for (const e of errors.slice(0, 10)) console.log(`  ERROR ${e.item?.id}: ${e.error}`);

  // Index anomalies are expected and are kept, never dropped. `expected_max_idx`
  // is a 0-based max index, so a clean document has max_idx + 1 entries — compare
  // against that, or every document looks anomalous and the real ones hide.
  const anomalies = done.filter((r: any) => {
    const countOff = r.expected_max_idx != null && r.entries_fetched !== r.expected_max_idx + 1;
    const dupes = r.distinct_idx < r.entries_fetched;
    const gaps = r.min_idx != null && (r.max_idx - r.min_idx + 1) !== r.distinct_idx;
    return countOff || dupes || gaps;
  });
  console.log(`\nindex anomalies: ${anomalies.length}/${done.length} (entries kept regardless)`);
  for (const m of anomalies.slice(0, 12)) {
    const why = [
      m.expected_max_idx != null && m.entries_fetched !== m.expected_max_idx + 1 ? "count" : null,
      m.distinct_idx < m.entries_fetched ? "duplicate-idx" : null,
      m.min_idx != null && (m.max_idx - m.min_idx + 1) !== m.distinct_idx ? "gaps" : null,
    ].filter(Boolean).join(",");
    console.log(`  ${m.doc_id}: [${why}] fetched=${m.entries_fetched} expected_max_idx=${m.expected_max_idx} ` +
                `range=${m.min_idx}..${m.max_idx} distinct=${m.distinct_idx}`);
  }
  const unparsed = done.reduce((a: number, r: any) => a + (r.unparsed_entries ?? 0), 0);
  console.log(`unparsed entry payloads: ${unparsed}`);

  process.exit(errors.length ? 1 : 0);
}

main().catch(e => { console.error(e); process.exit(1); });
