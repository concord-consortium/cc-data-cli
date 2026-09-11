#!/usr/bin/node

// READ-ONLY. Downloads CLUE document *metadata* from Firestore into local JSONL.
//
// Canonical copy lives in cc-data-cli/local-data/clue-documents/. Run it from
// the CLUE repo's scripts/ directory, where serviceAccountKey.json and
// firebase-admin live:
//
//   cp download-metadata.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx download-metadata.ts --limit 40   # test on a handful
//   npx tsx download-metadata.ts              # the full pull
//
// Why this exists: the replay URLs in the behaviour review sheet need the
// portal offering id, and the document *content* node does not carry it.
// Two metadata stores do, and this pulls both:
//
//   RTDB       .../classes/{contextId}/users/{uid}/documentMetadata/{key}
//              carries offeringId for 3,784 of 4,674 documents -- effectively
//              every document that belongs to an offering. This is the one
//              worth having.
//   Firestore  authed/learn_concord_org/documents carries the same field, but
//              it was added late: only 177 documents have it. Pulled anyway,
//              as a cross-check and for investigation/problem/visibility/kind.
//
// Note that the `DocumentDocument` interface in src/lib/firestore-schema.ts
// does NOT list `offeringId`; the interface is incomplete rather than
// authoritative. DocumentMetadataModel in
// src/models/document/document-metadata-model.ts is the accurate list.
//
// The collection is `/authed/{escaped portal}/documents` (getRootFolder in
// src/lib/firestore.ts, getRootId in src/lib/root-id.ts). Document ids there
// are `{network}_{key}` for network-scoped copies, so this queries on the
// `key` FIELD rather than reading by document id, and keeps every match: the
// same document can appear once per network it was shared into.
//
// Writes <OUT>/metadata.jsonl via a .tmp name and a rename, so a partial file
// is never mistaken for a complete one.

import fs from "node:fs";
import admin from "firebase-admin";
import { getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ? `${process.env.CC_DATA_LOCAL}/clue-documents` : "../../../local-data/clue-documents";
const DOCS = `${BASE}/clue_dataflow_docs_all.json`;
const OUT = `${BASE}/metadata.jsonl`;
const OFFERINGS_OUT = `${BASE}/offerings.jsonl`;
const COLLECTION = "authed/learn_concord_org/documents";
const OFFERINGS_COLLECTION = "authed/learn_concord_org/offerings";
// Firestore caps an `in` filter at 30 values.
const BATCH = 30;
const CONCURRENCY = 8;

const args = process.argv.slice(2);
const limitArg = args.indexOf("--limit");
const LIMIT = limitArg >= 0 ? parseInt(args[limitArg + 1], 10) : undefined;

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const fs_ = admin.firestore();
const rtdb = admin.database();

// The RTDB carries its own per-document metadata node, separate from the
// content node, and it holds offeringId far more completely than Firestore
// does -- Firestore's `offeringId` was added late, the RTDB's was written
// when the document was created. Path per
// find-documents-missing-metadata.ts in the CLUE repo's scripts/.
const RTDB_CLASSES = "/authed/portals/learn_concord_org/classes";

async function fetchRtdbMetadata(contextId: string, uid: string, key: string) {
  if (!contextId || !uid || !key) return null;
  const path = `${RTDB_CLASSES}/${contextId}/users/${uid}/documentMetadata/${key}`;
  const snap = await rtdb.ref(path).get();
  return snap.exists() ? snap.val() : null;
}

/** The metadata fields worth keeping, flattened for a Parquet row. */
function row(key: string, docId: string | null, d: any) {
  return {
    doc_key: key,
    fs_doc_id: docId,
    offering_id: d?.offeringId ?? null,
    fs_unit: d?.unit ?? null,
    fs_investigation: d?.investigation ?? null,
    fs_problem: d?.problem ?? null,
    fs_context_id: d?.context_id ?? null,
    fs_uid: d?.uid ?? null,
    fs_type: d?.type ?? null,
    network: d?.network ?? null,
    visibility: d?.visibility ?? null,
    kind: d?.kind ?? null,
    strategies: Array.isArray(d?.strategies) ? d.strategies : null,
    // A document can be listed under several networks; this records how many
    // Firestore rows matched so a later reader can see the fan-out rather
    // than silently trusting the one that was kept.
    n_matches: 0,
    // Filled in by addRtdbOffering.
    rtdb_offering_id: null as string | null,
    rtdb_metadata_found: false,
  };
}

async function fetchBatch(keys: string[]) {
  const snap = await fs_.collection(COLLECTION).where("key", "in", keys).get();
  const byKey = new Map<string, any[]>();
  snap.forEach(doc => {
    const d = doc.data();
    const k = d?.key;
    if (!k) return;
    if (!byKey.has(k)) byKey.set(k, []);
    byKey.get(k)!.push({ id: doc.id, data: d });
  });

  return keys.map(k => {
    const matches = byKey.get(k) ?? [];
    if (!matches.length) return { ...row(k, null, null), found: false };
    // Prefer a match that actually carries an offering id, then the one with
    // no network (the class-scoped original) over network-shared copies.
    const best = matches.find(m => m.data?.offeringId)
      ?? matches.find(m => !m.data?.network)
      ?? matches[0];
    return {
      ...row(k, best.id, best.data),
      n_matches: matches.length,
      found: true,
    };
  });
}

/** Adds the RTDB metadata node's offering id to an already-built row. */
async function addRtdbOffering(r: any, meta: any) {
  const contextId = r.fs_context_id ?? meta?.context_id;
  const uid = r.fs_uid ?? meta?.uid;
  const md = await fetchRtdbMetadata(contextId, uid, r.doc_key);
  return {
    ...r,
    rtdb_offering_id: md?.offeringId ?? null,
    rtdb_metadata_found: !!md,
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
      if (done % 10 === 0 || done === items.length) {
        const rate = done / ((Date.now() - t0) / 1000);
        process.stderr.write(`\r  ${done}/${items.length} batches  ${rate.toFixed(1)}/s   `);
      }
    }
  }));
  process.stderr.write("\n");
  return out;
}

/**
 * The whole offerings collection (~4k rows). An offering is one (class,
 * activity) pair, so this maps (context_id, unit, problem) to an offering id
 * for every class -- including classes that produced no log events, which the
 * log-derived fallback can never reach.
 */
async function fetchOfferings() {
  const snap = await fs_.collection(OFFERINGS_COLLECTION).get();
  const rows: any[] = [];
  snap.forEach(doc => {
    const d = doc.data();
    rows.push({
      offering_doc_id: doc.id,
      offering_id: d?.id ?? null,
      context_id: d?.context_id ?? null,
      unit: d?.unit ?? null,
      problem: d?.problem ?? null,
      problem_path: d?.problemPath ?? null,
      network: d?.network ?? null,
      name: d?.name ?? null,
      uri: d?.uri ?? null,
    });
  });
  return rows;
}

async function main() {
  const docs = JSON.parse(fs.readFileSync(DOCS, "utf8"));
  let keys: string[] = [...new Set(docs.map((d: any) => d.key).filter(Boolean))] as string[];
  if (LIMIT) keys = keys.slice(0, LIMIT);

  const batches: string[][] = [];
  for (let i = 0; i < keys.length; i += BATCH) batches.push(keys.slice(i, i + BATCH));
  console.log(`fetching metadata for ${keys.length} documents in ${batches.length} batches`);

  const firestoreRows = (await mapLimit(batches, CONCURRENCY, fetchBatch)).flat();

  // The RTDB metadata node is a point read per document, so it runs after the
  // batched Firestore pass rather than inside it.
  const byKeyMeta = new Map<string, any>(docs.map((d: any) => [d.key, d]));
  console.log(`fetching RTDB metadata for ${firestoreRows.length} documents`);
  const results = await mapLimit(
    firestoreRows, 20,
    (r: any) => addRtdbOffering(r, byKeyMeta.get(r.doc_key)));

  const tmp = `${OUT}.tmp`;
  const fd = fs.openSync(tmp, "w");
  for (const r of results) fs.writeSync(fd, JSON.stringify(r) + "\n");
  fs.closeSync(fd);
  fs.renameSync(tmp, OUT);

  const found = results.filter((r: any) => r.found).length;
  const withOffering = results.filter((r: any) => r.offering_id).length;
  const multi = results.filter((r: any) => r.n_matches > 1).length;
  console.log(`wrote ${OUT}`);
  console.log(`  ${found}/${results.length} found in Firestore`);
  const rtdbFound = results.filter((r: any) => r.rtdb_metadata_found).length;
  const rtdbOffering = results.filter((r: any) => r.rtdb_offering_id).length;
  const conflicts = results.filter((r: any) =>
    r.offering_id && r.rtdb_offering_id
    && String(r.offering_id) !== String(r.rtdb_offering_id)).length;
  console.log(`  ${withOffering} carry an offering id (Firestore)`);
  console.log(`  ${multi} matched more than one Firestore row (network copies)`);
  console.log(`  ${rtdbFound} have an RTDB metadata node, ` +
    `${rtdbOffering} of which carry an offering id`);
  console.log(`  ${conflicts} conflict between Firestore and RTDB offering ids`);

  const offerings = await fetchOfferings();
  const otmp = `${OFFERINGS_OUT}.tmp`;
  const ofd = fs.openSync(otmp, "w");
  for (const r of offerings) fs.writeSync(ofd, JSON.stringify(r) + "\n");
  fs.closeSync(ofd);
  fs.renameSync(otmp, OFFERINGS_OUT);
  console.log(`wrote ${OFFERINGS_OUT}`);
  console.log(`  ${offerings.length} offerings across ` +
    `${new Set(offerings.map(o => o.context_id)).size} classes`);
}

main().then(() => process.exit(0)).catch(err => { console.error(err); process.exit(1); });
