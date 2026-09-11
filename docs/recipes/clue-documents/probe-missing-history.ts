#!/usr/bin/node

// READ-ONLY. Do the documents whose Dataflow tile was deleted still have their
// history? If so, the deleted tile's whole life is still recoverable, which is
// exactly the trial-and-error signal the current-state corpus cannot show.
//
//   cp probe-missing-history.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx probe-missing-history.ts

import fs from "node:fs";
import admin from "firebase-admin";
import { getFirestoreBasePath, getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ?? "../../../local-data";
const PROBE = `${BASE}/log-events/missing_docs_probe.json`;
const PORTAL = "learn.concord.org";

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const firestore = admin.firestore();
const docsPath = getFirestoreBasePath(PORTAL, false);

async function main() {
  const docs = JSON.parse(fs.readFileSync(PROBE, "utf8"));
  let withHistory = 0, totalEntries = 0;
  const sizes: number[] = [];

  for (const d of docs) {
    // count without downloading: ask for ids only
    const snap = await firestore.collection(`${docsPath}/${d.doc_key}/history`)
      .select().get();
    if (snap.size > 0) { withHistory++; sizes.push(snap.size); totalEntries += snap.size; }
  }

  sizes.sort((a, b) => a - b);
  console.log(`documents probed: ${docs.length}`);
  console.log(`with history entries: ${withHistory}`);
  console.log(`total history entries: ${totalEntries.toLocaleString()}`);
  if (sizes.length) {
    console.log(`entries per document: min ${sizes[0]}, median ${sizes[Math.floor(sizes.length / 2)]}, max ${sizes[sizes.length - 1]}`);
  }
  process.exit(0);
}

main().catch(e => { console.error(e); process.exit(1); });
