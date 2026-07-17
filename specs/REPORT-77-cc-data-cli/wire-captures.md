# Captured wire samples: report-service v1 API

Captured live 2026-07-17 against a locally running report-service (branch `REPORT-77-cli-server-support`, commit `42f59e8` plus the then-uncommitted `report_type` implementation, verified passing its controller tests). Seeded state: one admin user, one API token labeled "spec capture", three succeeded runs (slugs `student-answers`, `teacher-actions`, `student-assignment-usage`). The fake servers in the auth and API-client test steps pin their behavior to these bodies; re-capture when the pinned commit moves.

A second capture session (same day) ran the local server against the production learn portal (SSH portal-DB tunnel, production `report-service-pro` config, a real minted-and-then-revoked API key) and captured the download envelope from a real succeeded Athena run. At that time the bulk answers/history pages and attachment presign responses were uncapturable: the Node functions code (bulk-read, attachment endpoints) was not yet deployed to any Firebase project, so those endpoints 500ed against every real environment. A third session (also 2026-07-17, after the functions deployed to `report-service-dev`) captured them live against staging; see "Bulk read and attachment presign" below.

**Re-verification note (2026-07-17, round-7 self-review):** the `REPORT-77-cli-server-support` branch has since advanced to HEAD `7a8c550` (five commits past the `79715e7` capture point: `a622f28`, `0cea222`, `6e8e8f2`, `2cefdee`, `7a8c550`). Every captured behavior below was re-verified against that HEAD and still holds byte-for-byte: the error vocabulary (`error_helpers.ex`), token introspection/revoke shapes and the post-revoke 401 (`token_controller.ex`), `total_endpoints` emission (`bulk_export_controller.ex`, including the empty-run `total_endpoints: 0` case), the attachment `not_found`/`not_authorized` strings and the whole-call `"too many attachments (max 500)"` 400 (`attachment_controller.ex` / `bulk_params.ex`, `@max_limit 500`), the download envelope TTL (`AthenaDB.download_url_ttl_seconds` = `60 * 10` = 600 with `response-content-disposition=attachment`), and the REPORT-58 pseudo-header `ORDER BY` (`shared_queries.ex:307`). One layering nuance confirmed harmless: the internal Node function rejects an over-cap attachment batch with `"too many items"` (`attachment-meta.ts`), but the public Elixir layer caps first with `"too many attachments (max 500)"`, which is what a client (and this file) observes. The captures need no content change; this note re-pins them to `7a8c550`.

## Download envelope (captured against a real Athena run)

`GET /api/v1/reports/212/download` on a succeeded `student-answers` run: HTTP 200

```json
{"download_url":"<497-char presigned S3 URL, redacted>","filename":"student-answers-run-212.csv","expires_in_seconds":600}
```

The filename pattern is `<report-slug>-run-<id>.csv`; the URL carried `X-Amz-Expires=600` and a `response-content-disposition=attachment` flavor, matching the spec's envelope and disposition notes.

## Unauthenticated request

`GET /api/v1/reports` without a bearer token: HTTP 401

```json
{"error":"NOT_AUTHENTICATED","message":"You must supply a valid API token."}
```

(The `action` field in the CLI's error line is CLI-added, not server-sent.)

## Token introspection

`GET /api/v1/tokens/current`: HTTP 200. Before any data call (`last_used_at` untouched by introspection itself):

```json
{"label":"spec capture","last_used_at":null,"created_at":"2026-07-17T13:16:32Z","report_access":true}
```

After a data call touched the token:

```json
{"label":"spec capture","last_used_at":"2026-07-17T13:16:58Z","created_at":"2026-07-17T13:16:32Z","report_access":true}
```

## Run list

`GET /api/v1/reports`: HTTP 200. Envelope `{items, next_page_token}`, keyset-descending by id, `next_page_token` null on the last page. One item, formatted (all three items share this shape; `report_type` was `"answers"`, `"log"`, and `"usage"` for the three seeded slugs respectively):

```json
{
  "id": 216,
  "inserted_at": "2026-07-17T13:16:32Z",
  "updated_at": "2026-07-17T13:16:32Z",
  "report_filter": {"state": null, "filters": [], "class": null, "cohort": null, "school": null, "teacher": null, "assignment": null, "permission_form": null, "student": null, "country": null, "subject_area": null, "start_date": null, "exclude_internal": false, "end_date": null, "hide_names": false},
  "report_slug": "student-answers",
  "athena_query_state": "succeeded",
  "report_filter_values": {},
  "report_type": "answers"
}
```

## Run show

`GET /api/v1/reports/216`: HTTP 200, identical shape to the list item above (no `athena_result_url` leak).

## Pinned server behavior: answers-CSV pseudo-header row ordering

Not a v1 API response but a report-CSV content dependency the CLI relies on. `generate_resource_sql` in report-service `server/lib/report_server/reports/athena/shared_queries.ex` (line 307 at commit `4e01de3`, "fix: Guarantee Prompt and Correct answer header row ordering [REPORT-58]") emits, for `report_type == :answers` results only:

```sql
ORDER BY CASE student_id WHEN 'Prompt' THEN -2 WHEN 'Correct answer' THEN -1 ELSE 0 END, class NULLS FIRST, username
```

so the two pseudo-header rows are always the first two data rows of an answers CSV. The CLI does NOT depend on this ordering for correctness (it detects CSV column types full-file at download and reads views with `auto_detect=false`), but the behavior is recorded here because it was the load-bearing reason the earlier sniff-based design happened to work, and a change to this ordering or to the header-row rendering (`Map.get(column, :header) || "null"`, so a NULL prompt renders the literal string `"null"`) is the kind of server change that would otherwise resurface as a CLI type-inference surprise. Re-verify when the pinned commit moves.

## Contract error

`GET /api/v1/reports?limit=bogus`: HTTP 400

```json
{"error":"BAD_REQUEST","message":"limit must be an integer"}
```

## Revoke, then post-revoke

`DELETE /api/v1/tokens/current`: HTTP 200

```json
{"revoked":true}
```

Same token afterward, `GET /api/v1/tokens/current`: HTTP 401 with the `NOT_AUTHENTICATED` body above. This is the live proof of the CLI's logout exemption path: a 401 from the revoke or introspection endpoint after an admin-side revoke means nothing needed revoking.

## Bulk read and attachment presign (captured against staging, 2026-07-17)

Captured live against the local server (branch `REPORT-77-cli-server-support`, commit `79715e7`) running with the `.env` "development" block: staging `REPORT_SERVICE_URL` at `us-central1-report-service-dev.cloudfunctions.net` with the Node functions (bulk-read, attachment endpoints) newly deployed to `report-service-dev`, `REPORT_SERVICE_FIREBASE_APP=report-service-dev`, staging portal per `config/dev.exs`. A fresh capture-only API key was minted for the session and revoked afterward. Target: run 220, a succeeded `student-answers` run whose class has real answer data in the staging Firestore. This is real student data, so item values, identifiers, and URLs are reduced to shapes throughout, per this file's conventions.

### Bulk answers page

`GET /api/v1/reports/220/answers?limit=1`: HTTP 200. Envelope keys exactly `items`, `next_page_token`, `total_endpoints`. This is the first live observation of `total_endpoints`: it was `2` and constant across both fetched pages, as owed. `next_page_token` was an opaque 147-character base64url-safe string; passing it back as `page_token` returned the next page (HTTP 200) with a further token, so the token round-trips.

First item, reduced to a field-to-type map:

```json
{
  "answer": "string",
  "context_id": "string",
  "created": "string",
  "id": "string",
  "interactive_state_history_id": "string",
  "platform_id": "string",
  "platform_user_id": "string",
  "question_id": "string",
  "question_type": "string",
  "remote_endpoint": "string",
  "report_state": "string",
  "resource_link_id": "string",
  "resource_url": "string",
  "run_key": "string",
  "source_key": "string",
  "submitted": "null",
  "tool_id": "string",
  "type": "string",
  "version": "number"
}
```

All the spec-named fields are present (`id`, `remote_endpoint`, `question_id`, `source_key`, `report_state` as a JSON string, `answer`, `type`, `question_type`), plus the rest of the stored answer document, matching implementation.md's "the stored answer document plus `id`". The page-2 item additionally carried `answer_text` (string) and an `attachments` map, confirming `attachments` is present only on items that have them. The `attachments` map shape (attachment names were machine-generated audio filenames, e.g. `audio<epoch-millis>.mp3`):

```json
{
  "<name>": {
    "contentType": "string",
    "folder": {"id": "string", "ownerId": "string"},
    "publicPath": "string"
  }
}
```

Note the `folder` object: an extra wire field beyond `publicPath` and optional `contentType`; the scan tolerates and ignores it (implementation.md's scan step records this).

### Bulk history page

`GET /api/v1/reports/220/history?limit=1`: HTTP 200. Same envelope keys, `total_endpoints` again `2` and constant across both fetched pages, `next_page_token` an opaque 187-character string that round-tripped for page 2 (page-2 item keys identical to page 1). First item, reduced to a field-to-type map:

```json
{
  "answer": "string",
  "answer_id": "string",
  "answer_text": "string",
  "attachments": "object",
  "context_id": "string",
  "created": "string",
  "created_at": "string",
  "history_id": "string",
  "id": "string",
  "interactive_state_history_id": "string",
  "platform_id": "string",
  "platform_user_id": "string",
  "question_id": "string",
  "question_type": "string",
  "remote_endpoint": "string",
  "report_state": "string",
  "resource_link_id": "string",
  "resource_url": "string",
  "run_key": "string",
  "source_key": "string",
  "submitted": "null",
  "tool_id": "string",
  "type": "string",
  "version": "number"
}
```

This is the state-doc spread (the full answer-doc copy, including `source_key` and the embedded `id` trap) plus `history_id`, `created_at` (verified ISO 8601), and `answer_id`, exactly as implementation.md describes.

### Bulk endpoint parameter probes

`GET /api/v1/reports/220/answers?limit=501`: HTTP 200. An over-cap limit is silently accepted, not a contract error (the run holds only 5 answers, so clamp-to-500 versus passthrough is indistinguishable here; either way the CLI's max-500 requests are safe). `limit=bogus`: HTTP 400 with the same body as the run-list capture (`{"error":"BAD_REQUEST","message":"limit must be an integer"}`). `GET /api/v1/reports/999999/answers?limit=1`: HTTP 404

```json
{"error":"NOT_FOUND","message":"Not found."}
```

### Corrupted page token

`GET /api/v1/reports/220/answers?limit=1&page_token=<corrupted>`: HTTP 400

```json
{"error":"BAD_REQUEST","message":"page_token is not valid"}
```

A malformed token is a 400 contract error, not `EXPIRED_CURSOR`; the 410 is reserved for a well-formed token whose server-side scratch has passed the one-hour sliding TTL. Provoking that live would have meant waiting out the TTL, so the 410 body below is code-derived, not live-captured: it is fully determined by `bulk_export_controller.ex` and `error_helpers.ex` at the same commit `79715e7` (`render_error` merges an empty context, so exactly these two fields), and the expired-scratch path is asserted by the controller test "an expired scratch -> 410 EXPIRED_CURSOR".

```json
{"error":"EXPIRED_CURSOR","message":"The export cursor has expired; restart the export from a null page_token."}
```

Two adjacent code-verified facts: the TTL is `@ttl_seconds` 3600 with an atomic absolute bump on each active fetch (truly sliding), and an expired scratch is deleted on lookup, so a replay after the 410 is a 404-or-410 situation, never a resurrection.

### Attachment presign, not_found item

`POST /api/v1/reports/220/attachments` with a fabricated ref (`{"attachments":[{"collection":"answers","source":"<staging source, redacted>","doc_id":"nonexistent","name":"audioFile"}],"disposition":"attachment"}`): HTTP 200 (per-item errors do not fail the call)

```json
{"results":[{"doc_id":"nonexistent","name":"audioFile","error":"not_found"}],"expires_in_seconds":600}
```

### Attachment presign, success item

Same POST with a real ref taken from the page-2 answers item's `attachments` map (its `doc_id` = the item's `id`, `name` = an attachment map key, `disposition` omitted): HTTP 200

```json
{"results":[{"doc_id":"<redacted>","name":"<redacted>","url":"<508-char presigned S3 URL, redacted>"}],"expires_in_seconds":600}
```

The success item carried exactly `doc_id`, `name`, `url` and no `error`, so each result carries exactly one of `url` or `error`, as specced. The URL was an AWS SigV4 presigned `s3.amazonaws.com` URL with `X-Amz-Expires=600` (matching `expires_in_seconds`) and `response-content-disposition=attachment` even though `disposition` was omitted from the request, so `attachment` is confirmed as the server default.

### Attachment presign edge probes

The same real ref with `"disposition":"inline"`: HTTP 200, and the returned URL's `response-content-disposition` parameter flipped to `inline`, confirming the request field drives the presigned disposition. A real `doc_id`/`name` with a fabricated `source`: HTTP 200 with a `not_found` error item, so a wrong source reads as a missing doc; `not_authorized` was not provoked in this session (it presumably requires a real doc the token's user cannot access). 501 fabricated refs in one call: HTTP 400

```json
{"error":"BAD_REQUEST","message":"too many attachments (max 500)"}
```

This is the first wire confirmation of the 500-item server cap; it is a whole-call contract error, not a per-item one.

### Session key revoke (staging re-confirmation)

The session's capture key was retired through the API itself, re-confirming the revoke surface from the first capture session against this staging-configured server. `DELETE /api/v1/tokens/current`: HTTP 200 with `{"revoked":true}`. The same token afterward got HTTP 401 with the standard `NOT_AUTHENTICATED` body from both `GET /api/v1/tokens/current` and a bulk data call (`GET /api/v1/reports/220/answers?limit=1`), so revocation takes effect immediately across surface types, including the Node-functions-backed bulk endpoints.
