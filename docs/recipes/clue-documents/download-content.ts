#!/usr/bin/node

// READ-ONLY. Downloads CLUE document *content* from the RTDB into local JSONL.
//
// Canonical copy lives in cc-data-cli/local-data/clue-documents/. Run it from
// the CLUE repo's scripts/ directory, where serviceAccountKey.json and
// firebase-admin live:
//
//   cp download-content.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx download-content.ts --limit 40    # test on a handful
//   npx tsx download-content.ts               # the full pull (~43 MB)
//
// Content for every document type -- including publications -- lives at
// classes/{classHash}/users/{uid}/documents/{key} (src/lib/firebase.ts
// getUserDocumentPath). The publications/personalPublications paths hold only
// metadata pointing back at that, so this one path covers the whole corpus.
//
// Writes <OUT>/content.jsonl, one object per document, via a .tmp name and a
// rename so a partial file is never mistaken for a complete one. The raw
// content string is kept verbatim alongside the derived columns -- deriving is
// lossy and we cannot re-derive what we did not keep.

import fs from "node:fs";
import admin from "firebase-admin";
import { getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ? `${process.env.CC_DATA_LOCAL}/clue-documents` : "../../../local-data/clue-documents";
const DOCS = `${BASE}/clue_dataflow_docs_all.json`;
const CLASS_MAP = `${BASE}/clue_dataflow_class_map.json`;
const OUT = `${BASE}/content.jsonl`;
const PORTAL_PATH = "/authed/portals/learn_concord_org";
const CONCURRENCY = 20;

const args = process.argv.slice(2);
const limitArg = args.indexOf("--limit");
const LIMIT = limitArg >= 0 ? parseInt(args[limitArg + 1], 10) : undefined;

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const db = admin.database();

/** Summarises a parsed content blob without discarding the original. */
function summarise(content: any) {
  const tileMap = content?.tileMap ?? {};
  const tiles = Object.values(tileMap) as any[];
  const tileTypes = tiles
    .map(t => t?.content?.type ?? null)
    .filter((t): t is string => typeof t === "string");
  const counts: Record<string, number> = {};
  for (const t of tileTypes) counts[t] = (counts[t] ?? 0) + 1;
  return {
    n_tiles: tiles.length,
    tile_types: [...new Set(tileTypes)].sort(),
    tile_type_counts: counts,
    n_dataflow_tiles: counts["Dataflow"] ?? 0,
    n_rows: Object.keys(content?.rowMap ?? {}).length,
    n_shared_models: Object.keys(content?.sharedModelMap ?? {}).length,
    n_annotations: Object.keys(content?.annotations ?? {}).length,
  };
}

async function fetchDoc(meta: any, ctxToPortalId: Record<string, string>) {
  const path = `${PORTAL_PATH}/classes/${meta.context_id}/users/${meta.uid}/documents/${meta.key}`;
  const snap = await db.ref(path).get();
  const val = snap.val();

  const base = {
    doc_id: meta.id,
    doc_key: meta.key,
    uid: meta.uid,
    context_id: meta.context_id,
    portal_class_id: ctxToPortalId[meta.context_id] ?? null,
    type: meta.type ?? null,
    unit: meta.unit ?? null,
    problem: meta.problem ?? null,
    // the tools array from Firestore metadata, for comparison against what the
    // content actually contains -- they are populated by different code paths
    meta_tools: Array.isArray(meta.tools) ? meta.tools : null,
    // how this document entered the corpus: "tools" is the current-state
    // Firestore query; "log-events" means its Dataflow tile was deleted and
    // only the logs could reveal it
    discovery: meta.discovery ?? "tools",
    dataflow_tile_deleted: meta.dataflow_tile_deleted ?? false,
  };

  if (!val) return { ...base, found: false };

  let parsed: any = null;
  let parse_ok = true;
  try {
    parsed = typeof val.content === "string" ? JSON.parse(val.content) : val.content ?? null;
  } catch { parse_ok = false; }

  return {
    ...base,
    found: true,
    change_count: typeof val.changeCount === "number" ? val.changeCount : null,
    version: val.version ?? null,
    self_uid: val.self?.uid ?? null,
    self_doc_key: val.self?.documentKey ?? null,
    self_class_hash: val.self?.classHash ?? null,
    parse_ok,
    ...(parsed ? summarise(parsed) : {
      n_tiles: null, tile_types: null, tile_type_counts: null, n_dataflow_tiles: null,
      n_rows: null, n_shared_models: null, n_annotations: null,
    }),
    content_json: typeof val.content === "string" ? val.content : JSON.stringify(val.content ?? null),
  };
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
      if (done % 100 === 0 || done === items.length) {
        const rate = done / ((Date.now() - t0) / 1000);
        process.stderr.write(`\r  ${done}/${items.length} docs  ${rate.toFixed(1)}/s   `);
      }
    }
  }));
  process.stderr.write("\n");
  return out;
}

async function main() {
  const docs = JSON.parse(fs.readFileSync(DOCS, "utf8"));
  const ctxToPortalId = JSON.parse(fs.readFileSync(CLASS_MAP, "utf8")).ctxToPortalId ?? {};

  let work = docs;
  if (LIMIT) {
    const step = Math.max(1, Math.floor(work.length / LIMIT));
    work = work.filter((_: any, i: number) => i % step === 0).slice(0, LIMIT);
    console.log(`--limit ${LIMIT}: sampling ${work.length} across the corpus`);
  }
  console.log(`fetching content for ${work.length} documents`);

  const results = await mapLimit(work, CONCURRENCY, (m: any) => fetchDoc(m, ctxToPortalId));

  // Write in chunks through a file descriptor. Joining into one string fails
  // with "Invalid string length" once the corpus is large enough, as the
  // history download found the hard way.
  const fd = fs.openSync(`${OUT}.tmp`, "w");
  let pending: string[] = [];
  let written = 0;
  for (const r of results) {
    if ((r as any)?.error) continue;
    pending.push(JSON.stringify(r) + "\n");
    written++;
    if (pending.length >= 200) { fs.writeSync(fd, pending.join("")); pending = []; }
  }
  if (pending.length) fs.writeSync(fd, pending.join(""));
  fs.closeSync(fd);
  fs.renameSync(`${OUT}.tmp`, OUT);

  const errors = results.filter((r: any) => r?.error);
  const found = results.filter((r: any) => r?.found);
  const missing = results.filter((r: any) => r && !r.error && !r.found);
  const unparsed = found.filter((r: any) => !r.parse_ok);
  const withDataflow = found.filter((r: any) => (r.n_dataflow_tiles ?? 0) > 0);

  console.log(`\nwrote ${written} rows to ${OUT}`);
  console.log(`found ${found.length}   missing ${missing.length}   errors ${errors.length}`);
  console.log(`content failed to parse: ${unparsed.length}`);
  console.log(`documents with at least one Dataflow tile: ${withDataflow.length}`);
  for (const e of errors.slice(0, 10)) console.log(`  ERROR ${e.item?.id}: ${e.error}`);

  // The Firestore `tools` metadata and the actual content are written by
  // different code paths; disagreement is a real signal, not a bug to hide.
  const disagree = found.filter((r: any) =>
    Array.isArray(r.meta_tools) &&
    r.meta_tools.includes("Dataflow") !== ((r.n_dataflow_tiles ?? 0) > 0));
  console.log(`metadata/content disagree on Dataflow presence: ${disagree.length}`);
  for (const d of disagree.slice(0, 5)) {
    console.log(`  ${d.doc_id}: tools=${JSON.stringify(d.meta_tools)} n_dataflow_tiles=${d.n_dataflow_tiles}`);
  }

  process.exit(errors.length ? 1 : 0);
}

main().catch(e => { console.error(e); process.exit(1); });
