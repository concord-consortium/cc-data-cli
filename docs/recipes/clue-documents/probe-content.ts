#!/usr/bin/node

// READ-ONLY. Samples CLUE document *content* from the RTDB to size the job
// before committing to a full download.
//
//   cp probe-content.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx probe-content.ts
//
// Content for every document type -- including publications -- lives at
// classes/{classHash}/users/{uid}/documents/{key} (src/lib/firebase.ts
// getUserDocumentPath). The publications/personalPublications paths hold only
// metadata pointing back at that, so one path pattern covers the whole corpus.

import fs from "node:fs";
import admin from "firebase-admin";
import { getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ? `${process.env.CC_DATA_LOCAL}/clue-documents` : "../../../local-data/clue-documents";
const DOCS = `${BASE}/clue_dataflow_docs.json`;
const PORTAL_PATH = "/authed/portals/learn_concord_org";
const SAMPLE = 40;

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const db = admin.database();

async function main() {
  const docs = JSON.parse(fs.readFileSync(DOCS, "utf8"));

  // spread the sample across document types rather than taking the head
  const byType = new Map<string, any[]>();
  for (const d of docs) {
    if (!byType.has(d.type)) byType.set(d.type, []);
    byType.get(d.type)!.push(d);
  }
  const sample: any[] = [];
  for (const [, list] of byType) {
    const step = Math.max(1, Math.floor(list.length / Math.ceil(SAMPLE / byType.size)));
    for (let i = 0; i < list.length && sample.length < SAMPLE; i += step) sample.push(list[i]);
  }

  const t0 = Date.now();
  let found = 0, missing = 0, bytes = 0;
  const perType = new Map<string, { n: number; found: number; bytes: number }>();

  for (const d of sample) {
    const path = `${PORTAL_PATH}/classes/${d.context_id}/users/${d.uid}/documents/${d.key}`;
    const snap = await db.ref(path).get();
    const val = snap.val();
    const size = val ? JSON.stringify(val).length : 0;
    const t = perType.get(d.type) ?? { n: 0, found: 0, bytes: 0 };
    t.n++; if (val) { t.found++; t.bytes += size; }
    perType.set(d.type, t);
    if (val) { found++; bytes += size; } else { missing++; }
  }

  const secs = (Date.now() - t0) / 1000;
  console.log(`sampled ${sample.length} documents in ${secs.toFixed(1)}s ` +
              `(${(sample.length / secs).toFixed(1)}/s)`);
  console.log(`found ${found}, missing ${missing}`);
  console.log(`mean size of found: ${found ? Math.round(bytes / found).toLocaleString() : 0} bytes\n`);

  console.log(`${"type".padEnd(22)}${"sampled".padStart(8)}${"found".padStart(7)}${"mean bytes".padStart(12)}`);
  for (const [type, t] of perType) {
    console.log(`${type.padEnd(22)}${String(t.n).padStart(8)}${String(t.found).padStart(7)}` +
                `${(t.found ? Math.round(t.bytes / t.found) : 0).toLocaleString().padStart(12)}`);
  }

  const total = 4602;
  console.log(`\nprojected for all ${total}: ` +
              `${((bytes / Math.max(found, 1)) * total / 1e6).toFixed(0)} MB, ` +
              `${(total / (sample.length / secs) / 60).toFixed(1)} min at this rate (serial)`);

  // show one document's shape without dumping student work
  const one = sample.find(d => d.type === "problem") ?? sample[0];
  const v = await db.ref(
    `${PORTAL_PATH}/classes/${one.context_id}/users/${one.uid}/documents/${one.key}`).get();
  if (v.val()) {
    console.log("\ntop-level keys on a document:", Object.keys(v.val()).sort().join(", "));
    const c = v.val().content;
    if (typeof c === "string") {
      const parsed = JSON.parse(c);
      console.log("content is a JSON string; parsed keys:", Object.keys(parsed).sort().join(", "));
    }
  }
  process.exit(0);
}

main().catch(e => { console.error(e); process.exit(1); });
