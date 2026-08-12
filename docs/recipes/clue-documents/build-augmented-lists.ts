#!/usr/bin/node

// READ-ONLY (Firestore). Extends the document corpus with documents that once
// held a Dataflow tile but no longer do.
//
//   cp build-augmented-lists.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx build-augmented-lists.ts
//
// The original corpus was found with a Firestore query on the `tools` array,
// which describes a document's CURRENT tiles. A student who added a Dataflow
// tile, worked in it and deleted it leaves a document that the query cannot
// see -- exactly the trial-and-error case the research is about.
//
// Log events record what happened rather than what remains, so the additional
// documents come from DATAFLOW_TOOL_CHANGE events (log-events/missing_docs.csv).
// History entries would also reveal them, but Firestore history lives in a
// per-document subcollection, so finding them that way means enumerating every
// document in a class and scanning each one -- far more work than scanning the
// class's log events.
//
// Writes, alongside the originals rather than over them:
//   clue_dataflow_docs_all.json      corpus + additions
//   clue_dataflow_history_all.json   same, in the history-list shape
// Both gain a `discovery` field: "tools" (current-state query) or "log-events".

import fs from "node:fs";
import admin from "firebase-admin";
import { getFirestoreBasePath, getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ?? "../../../local-data";
const DOCS = `${BASE}/clue-documents/clue_dataflow_docs.json`;
const HIST = `${BASE}/clue-documents/clue_dataflow_history.json`;
const CLASS_MAP = `${BASE}/clue-documents/clue_dataflow_class_map.json`;
const MISSING = `${BASE}/log-events/missing_docs.csv`;
const OUT_DOCS = `${BASE}/clue-documents/clue_dataflow_docs_all.json`;
const OUT_HIST = `${BASE}/clue-documents/clue_dataflow_history_all.json`;
const PORTAL = "learn.concord.org";

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const firestore = admin.firestore();
const docsPath = getFirestoreBasePath(PORTAL, false);

function parseCsv(text: string) {
  const [head, ...lines] = text.trim().split("\n");
  const cols = head.split(",");
  return lines.map(l => Object.fromEntries(
    l.split(",").map((v, i) => [cols[i], v])) as any);
}

async function main() {
  const docs = JSON.parse(fs.readFileSync(DOCS, "utf8"));
  const hist = JSON.parse(fs.readFileSync(HIST, "utf8"));
  const ctxToPortalId = JSON.parse(fs.readFileSync(CLASS_MAP, "utf8")).ctxToPortalId;
  const known = new Set(docs.map((d: any) => d.key));

  const extra = parseCsv(fs.readFileSync(MISSING, "utf8"))
    .filter(r => !known.has(r.doc_key));
  console.log(`corpus ${docs.length}, log-only candidates ${extra.length}`);

  const addedDocs: any[] = [];
  const addedHist: any[] = [];

  for (const r of extra) {
    const snap = await firestore.doc(`${docsPath}/${r.doc_key}`).get();
    if (!snap.exists) {
      console.log(`  no Firestore metadata for ${r.doc_key} — skipping`);
      continue;
    }
    const m: any = snap.data();
    const context_id = m.context_id ?? null;

    addedDocs.push({
      context_id,
      id: r.doc_key,
      key: r.doc_key,
      tools: m.tools ?? [],
      type: m.type ?? r.doc_type ?? null,
      uid: m.uid ?? r.doc_uid ?? null,
      unit: m.unit ?? null,
      problem: m.problem ?? null,
      discovery: "log-events",
      // the tile is gone from the content; this is why the document is here
      dataflow_tile_deleted: true,
      n_dataflow_events: Number(r.n_dataflow_events),
    });

    const histSnap = await firestore.collection(`${docsPath}/${r.doc_key}/history`)
      .select().get();
    addedHist.push({
      id: r.doc_key,
      type: m.type ?? r.doc_type ?? null,
      unit: m.unit ?? null,
      context_id,
      uid: m.uid ?? r.doc_uid ?? null,
      portal_class_id: context_id ? (ctxToPortalId[context_id] ?? null) : null,
      has_history: histSnap.size > 0,
      // the history list stores a 0-based max index, not a count
      entries: histSnap.size > 0 ? histSnap.size - 1 : null,
      discovery: "log-events",
    });
  }

  const allDocs = [...docs.map((d: any) => ({ ...d, discovery: "tools" })), ...addedDocs];
  const allHist = [...hist.map((h: any) => ({ ...h, discovery: "tools" })), ...addedHist];

  fs.writeFileSync(OUT_DOCS, JSON.stringify(allDocs));
  fs.writeFileSync(OUT_HIST, JSON.stringify(allHist));

  console.log(`\nadded ${addedDocs.length} documents`);
  console.log(`  with history: ${addedHist.filter(h => h.has_history).length}`);
  console.log(`  total entries to fetch: ${addedHist.reduce((a, h) => a + ((h.entries ?? -1) + 1), 0).toLocaleString()}`);
  console.log(`wrote ${allDocs.length} -> ${OUT_DOCS}`);
  console.log(`wrote ${allHist.length} -> ${OUT_HIST}`);
  process.exit(0);
}

main().catch(e => { console.error(e); process.exit(1); });
