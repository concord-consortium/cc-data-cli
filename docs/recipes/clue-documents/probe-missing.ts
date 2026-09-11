#!/usr/bin/node

// READ-ONLY. Investigates documents that have Dataflow log events but are
// absent from the Firestore-derived document corpus.
//
//   cp probe-missing.ts ~/Development/collaborative-learning/scripts/
//   cd ~/Development/collaborative-learning/scripts
//   npx tsx probe-missing.ts
//
// Three explanations to tell apart, and they need different checks:
//
//   deleted            no RTDB content and no Firestore metadata
//   metadata-gap       RTDB content exists, no Firestore metadata document
//   tools-gap          both exist, but metadata.tools omits "Dataflow", so the
//                      array-contains query that built the corpus missed it
//
// The last one matters most: it would mean the corpus is undercounting for a
// reason that also applies to documents we never suspected.

import fs from "node:fs";
import admin from "firebase-admin";
import { getFirestoreBasePath, getScriptRootFilePath } from "./lib/script-utils.js";

const BASE = process.env.CC_DATA_LOCAL ?? "../../../local-data";
const MISSING = `${BASE}/log-events/missing_docs.csv`;
const CLASS_MAP = `${BASE}/clue-documents/clue_dataflow_class_map.json`;
const PORTAL = "learn.concord.org";
const PORTAL_PATH = "/authed/portals/learn_concord_org";

admin.initializeApp({
  credential: admin.credential.cert(getScriptRootFilePath("serviceAccountKey.json")),
  databaseURL: "https://collaborative-learning-ec215.firebaseio.com",
});
const db = admin.database();
const firestore = admin.firestore();
const docsPath = getFirestoreBasePath(PORTAL, false);

function parseCsv(text: string) {
  const [head, ...lines] = text.trim().split("\n");
  const cols = head.split(",");
  return lines.map(l => {
    // no quoted commas in this file; keep the parse honest anyway
    const parts = l.split(",");
    return Object.fromEntries(cols.map((c, i) => [c, parts[i]])) as any;
  });
}

async function main() {
  const rows = parseCsv(fs.readFileSync(MISSING, "utf8"));
  const ctxToPortalId = JSON.parse(fs.readFileSync(CLASS_MAP, "utf8")).ctxToPortalId;
  const portalIdToCtx: Record<string, string> = {};
  for (const [ctx, pid] of Object.entries(ctxToPortalId)) portalIdToCtx[pid as string] = ctx;

  const verdicts: Record<string, number> = {};
  const detail: any[] = [];

  for (const r of rows) {
    const ctx = portalIdToCtx[r.class_id];
    const path = `${PORTAL_PATH}/classes/${ctx}/users/${r.doc_uid}/documents/${r.doc_key}`;
    const snap = await db.ref(path).get();
    const content = snap.val();

    const fsSnap = await firestore.doc(`${docsPath}/${r.doc_key}`).get();
    const meta = fsSnap.exists ? fsSnap.data() : null;

    let parsed: any = null;
    if (content?.content) {
      try {
        parsed = typeof content.content === "string"
          ? JSON.parse(content.content) : content.content;
      } catch { /* leave null */ }
    }
    const tileTypes = parsed
      ? [...new Set(Object.values(parsed.tileMap ?? {})
          .map((t: any) => t?.content?.type).filter(Boolean))] as string[]
      : [];

    const verdict =
      !content && !meta ? "deleted"
      : content && !meta ? "metadata-gap"
      : content && meta && !(meta.tools ?? []).includes("Dataflow") ? "tools-gap"
      : content && meta ? "in-firestore-but-missed"
      : "content-gap";

    verdicts[verdict] = (verdicts[verdict] ?? 0) + 1;
    detail.push({
      doc_key: r.doc_key, verdict, unit: r.unit, doc_type: r.doc_type,
      n_dataflow_events: Number(r.n_dataflow_events),
      first_day: r.first_day, last_day: r.last_day,
      rtdb: !!content, firestore: !!meta,
      meta_tools: meta?.tools ?? null,
      meta_type: meta?.type ?? null,
      meta_context: meta?.context_id ?? null,
      content_tiles: tileTypes.sort(),
      has_dataflow_tile: tileTypes.includes("Dataflow"),
      change_count: content?.changeCount ?? null,
    });
  }

  console.log("verdicts:", verdicts);
  console.log();
  console.log(`${"doc_key".padEnd(22)}${"verdict".padEnd(24)}${"rtdb".padEnd(6)}${"fs".padEnd(5)}${"dfTile".padEnd(8)}tiles`);
  for (const d of detail.slice(0, 25)) {
    console.log(`${d.doc_key.padEnd(22)}${d.verdict.padEnd(24)}` +
                `${String(d.rtdb).padEnd(6)}${String(d.firestore).padEnd(5)}` +
                `${String(d.has_dataflow_tile).padEnd(8)}${d.content_tiles.join(",")}`);
  }

  fs.writeFileSync(`${BASE}/log-events/missing_docs_probe.json`,
                   JSON.stringify(detail, null, 2));
  console.log(`\nwrote ${detail.length} rows to log-events/missing_docs_probe.json`);
  process.exit(0);
}

main().catch(e => { console.error(e); process.exit(1); });
