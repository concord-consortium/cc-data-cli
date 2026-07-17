# Implementation Plan: cc-data CLI

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-77
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

Covers all requirements subtasks: (a) foundation and auth, (b) datasets/stores/summaries, (d/d2) the fetch commands, (c) the DuckDB query layer, (e) Claude integration, (f) sensitive-data documentation, and (g) the release pipeline. All wire shapes below were re-verified against the report-service working tree (branch `REPORT-77-cli-server-support`) on 2026-07-16; where a shape is specced-but-not-landed server work, it is marked as such.

Code in this plan is presented at three levels: exact Go structs for anything persisted to disk or read off the wire (schemas are contracts), full algorithms for the tricky parts (the merge engine, the sandbox), and signatures plus prose for straightforward plumbing. Test code is summarized per step; every step lands with its tests in the same commit.

## Implementation Plan

### Module scaffold, output contract, and version command

**Summary**: The repo skeleton and the two contracts every later step leans on: the exit-code classes and the stdout/stderr stream discipline, centralized in one package so no command can improvise.

**Files affected**:
- `go.mod`, `main.go` — module `github.com/concord-consortium/cc-data-cli`, Go 1.25.0 (the MCP go-sdk's `go 1.25.0` directive is the binding floor; 1.24 is also past support), binary `cc-data`
- `.tool-versions` — `golang 1.25.12`: the exact asdf toolchain for dev machines (go.mod's `go 1.25.0` remains the floor; bump the patch here as new 1.25.x releases land)
- `cmd/root.go` — cobra root, global flags, silence-usage wiring
- `cmd/version.go` — prints the ldflags-injected version
- `internal/output/output.go` — exit codes, error envelope, result lines, stderr progress

**Estimated diff size**: ~350 lines

`internal/output` defines the coarse classes as constants matching the requirements table (0 success, 1 internal, 2 usage, 3 `NOT_AUTHENTICATED`, 4 not-ready, 5 contract, 6 transient-after-retries) and one error type:

```go
type CLIError struct {
    ExitCode int               // the coarse class
    Code     string            // specific error code, e.g. "NOT_AUTHENTICATED"
    Message  string
    Action   string            // e.g. "A human must run: cc-data login"
    Extra    map[string]any    // e.g. athena_query_state on NOT_READY
}
```

`main()` is the single exit point: it runs the root command, and on a `CLIError` prints exactly one single-line JSON envelope (`{"error", "message", "action", ...Extra}`, omitting empty fields) to **stdout** and exits with `ExitCode`; any other error becomes exit 1 with a `SERVER_ERROR`-shaped envelope carrying `"error":"INTERNAL"`. Cobra usage errors map to exit 2. `ResultLine(v any)` marshals the standard `get` result line to stdout; `Progressf` and friends write only to stderr. The root command help text carries the exit-code table (asserted by test in the documentation step below).

`cc-data version` prints the version injected via `-ldflags "-X main.version=..."` (dev builds print `dev`). The same value drives the skill freshness stamp (Claude integration step below).

Tests: envelope shape goldens (failure emits exactly one JSON object on stdout, nothing else), exit-code mapping matrix.

---

### Config and portal resolution

**Summary**: `~/.config/cc-data/config.json` and the portal/server addressing rules used by every command.

**Files affected**:
- `internal/config/config.go` — load/save, defaults, env overrides
- `internal/config/portal.go` — portal normalization
- `internal/fsutil/fsutil.go` — `WriteFileAtomic0600`, the shared sensitive-file atomic-write helper

**Estimated diff size**: ~280 lines

```go
type Config struct {
    Version       int    `json:"version"`                  // 1
    DefaultPortal string `json:"default_portal,omitempty"` // hostname form
    DataRoot      string `json:"data_root,omitempty"`      // default ~/cc-data
    ServerURL     string `json:"server_url,omitempty"`     // report server origin
}
```

Written via a shared `internal/fsutil` helper, `WriteFileAtomic0600` (`os.OpenFile(tmp, O_CREATE|O_WRONLY|O_EXCL, 0600)` → write → fsync → rename), so a sensitive file is never created with a broader mode even transiently (the natural create-then-chmod leaves a world-readable window under the default umask); `credentials.json` uses the same helper, and `repl_history` is pre-created by it before readline opens the file. On Windows the files rely on user-profile ACLs, documented.

**Windows rename/read robustness**: the atomic-rename-onto-a-fixed-name pattern (this helper, `WriteManifest`, and cursor writes) is not concurrency-safe on Windows as written, because Go opens files with `FILE_SHARE_READ | FILE_SHARE_WRITE` and no `FILE_SHARE_DELETE` (verified, `syscall_windows.go`), so `MoveFileEx(REPLACE_EXISTING)` onto a name any process holds open fails with a sharing violation, and a reader opening a file mid-replace can see a transient error. `internal/fsutil` therefore provides robustio-style wrappers (modeled on Go's own `cmd/internal/robustio`, which exists for exactly this): on Windows only, `RenameAtomic` and the openers of replace-target files (manifest, config, credentials, cursor) retry `ERROR_SHARING_VIOLATION` / `ERROR_ACCESS_DENIED` / transient `ERROR_FILE_NOT_FOUND` with a short bounded backoff (reader windows are milliseconds); on POSIX they are plain `os.Rename`/`os.Open`. Every fixed-name durable write and every lockless read of one goes through these wrappers.

**Path base and `~` expansion**: `~` means `os.UserHomeDir()` on every platform (one code path, one documented location), so config/credentials live at `%USERPROFILE%\.config\cc-data` on Windows deliberately (the user-profile ACL story holds there), not `%AppData%`. Resolution order for the data root: `CC_DATA_ROOT` env, then `data_root`, then `~/cc-data` (`os.UserHomeDir()`-based, same rule).

Portal values normalize to hostname form (`https://learn.concord.org/` → `learn.concord.org`, port preserved for dev portals); the hostname keys credentials, dataset folders, and the firebase source mapping (production portal → `report-service-pro`, all others → `report-service-dev`). When the server needs the portal as a parameter (the `/auth/cli` `portal` query param requires an https origin), the client re-expands the hostname to `https://<host[:port]>`. Because a dev/staging portal host carries a port (`localhost:8080`) and `:` is illegal in a Windows path component, the on-disk folder name for a portal uses a filesystem encoding (`:` → `_`, so `localhost_8080`) applied on every platform for portability; the credentials and manifest record the real host, and the encoding is a pure folder-name transform (the only characters a normalized host can carry beyond the DNS alphabet are the `:` and digits of a port, so a single substitution is total). This is the dev/staging path the rollout note calls the only usable one until the server deploys land, so it must work on the Windows CI leg.

The report server itself is a separate origin from the portals it serves: `ServerURL` defaults to the built-in production constant `https://report-server.concord.org` (verified live and as the `PHX_HOST` default in report-service `runtime.exs`) and is overridable in config plus a `--server` flag on `login` for staging/local dev (`--server` help text names the staging server, `https://report-server.concordqa.org`, as the example). The server origin is recorded per portal in the credentials metadata at login time, so later commands talk to the same server that minted the token. Server origins are validated wherever one is read (the `--server` flag and the config field alike, so a hand-edited `config.json` cannot bypass it): the host must be `concord.org` or `concordqa.org` or a subdomain of either (dot-boundary suffix match, so `evil-concord.org` fails), or a loopback host (`localhost`/`127.0.0.1`, the only hosts where http is accepted; all others require https). Anything else is a usage error naming the rule; widening the allowlist is deliberately a code change, since a `--server` value receives the user's login flow and bearer token.

Tests: normalization table (scheme/case/port/trailing slash), the portal folder-encoding case (`localhost:8080` → `localhost_8080`, real host preserved in credentials), the server-origin allowlist matrix (concord.org/concordqa.org subdomains pass, `evil-concord.org` and bare lookalikes fail, http accepted for loopback only), precedence of env over config, `~`-expansion via `os.UserHomeDir()`, and the fsutil helper's unit test asserting mode `0600` on the temp file immediately at creation as well as on the final file (the mode assertion gated `runtime.GOOS != "windows"`, where stat mode is always 0444/0666; the Windows leg instead asserts the file is not world-accessible via the ACL check). A robustio-wrapper test drives a concurrent open during a rename-replace on Windows and asserts the retry succeeds.

---

### Credential storage

**Summary**: Per-portal token storage: OS keychain first via `zalando/go-keyring`, `credentials.json` fallback, with the local metadata (`stored_at`, backend, server origin) that offline `auth status` renders.

**Files affected**:
- `internal/creds/creds.go` — `Store` API and metadata file
- `internal/creds/keyring.go` — keychain backend
- `go.mod` — add `github.com/zalando/go-keyring`

**Estimated diff size**: ~250 lines

Layout: metadata always lives in `~/.config/cc-data/credentials.json` (`0600`); the secret lives in the keychain (service `cc-data`, account = portal host) when available, else inline in the same file:

```go
type CredFile struct {
    Version int                    `json:"version"` // 1
    Portals map[string]PortalCred  `json:"portals"` // key: portal host
}
type PortalCred struct {
    Token    string    `json:"token,omitempty"`  // only for backend "file"
    Backend  string    `json:"backend"`          // "keyring" | "file"
    Server   string    `json:"server"`           // report server origin that minted it
    StoredAt time.Time `json:"stored_at"`
}
```

Backend selection probes the keyring once per save (a Linux box without Secret Service falls back cleanly with a one-line stderr note). API: `Save(portal, token, server)`, `Token(portal)`, `Delete(portal)`, `List()`. `go-keyring`'s `MockInit()` backs the tests; a real-keychain smoke test is a documented manual step since CI runners have no Secret Service.

Tests: fallback path, round-trip, delete, `List()` ordering, file permission assertion (the `0600` mode check gated `runtime.GOOS != "windows"`, where stat mode is always 0444/0666; the Windows leg asserts the ACL instead).

---

### API client: retry policy and error classification

**Summary**: `internal/api`: the typed HTTP client every command shares, implementing the requirements' retry policy, the landed error vocabulary, pagination, and the presigned-S3 second-call rule.

**Files affected**:
- `internal/api/client.go` — client, auth header, retry loop
- `internal/api/errors.go` — envelope decode + classification
- `internal/api/types.go` — wire structs
- `internal/api/s3.go` — presigned-URL streaming with envelope re-request

**Estimated diff size**: ~450 lines

Wire structs, exact field names verified in report-service:

```go
type ReportRun struct {
    ID                 int             `json:"id"`
    ReportSlug         string          `json:"report_slug"`
    ReportType         *string         `json:"report_type"`         // answers|usage|log; ReportJSON computes it from the slug at render (not a stored column), so nil only on a deployed build predating that view
    ReportFilter       json.RawMessage `json:"report_filter"`        // filters, dates, hide_names, per-dimension ids; always a full object (ReportJSON normalizes a NULL filter from programmatic runs to the empty ReportFilter, never JSON null)
    ReportFilterValues map[string]any  `json:"report_filter_values"` // resolved labels
    AthenaQueryState   *string         `json:"athena_query_state"`   // null|queued|running|succeeded|failed|cancelled
    InsertedAt         time.Time       `json:"inserted_at"`
    UpdatedAt          time.Time       `json:"updated_at"`
}
type Page[T any] struct {
    Items         []T     `json:"items"`
    NextPageToken *string `json:"next_page_token"`
}
type BulkPage struct {
    Items          []json.RawMessage `json:"items"`
    NextPageToken  *string           `json:"next_page_token"`
    TotalEndpoints *int              `json:"total_endpoints"` // owed server field; nil on older servers
}
type DownloadEnvelope struct {
    DownloadURL      string `json:"download_url"`
    Filename         string `json:"filename"`
    ExpiresInSeconds int    `json:"expires_in_seconds"` // 600
}
type Job struct {
    ID        int             `json:"id"` // integer on the wire (job_server.ex mints length(jobs)+1)
    Steps     json.RawMessage `json:"steps"`
    Status    string          `json:"status"` // "completed" is the ready state
    HasResult bool            `json:"has_result"`
}
type AttachmentResult struct {
    DocID string `json:"doc_id"`
    Name  string `json:"name"`
    URL   string `json:"url,omitempty"`
    Error string `json:"error,omitempty"` // "not_found" | "not_authorized"
}
type AttachmentResults struct {
    Results          []AttachmentResult `json:"results"`
    ExpiresInSeconds int                `json:"expires_in_seconds"`
}
type TokenInfo struct { // owed introspection endpoint
    Label        *string    `json:"label"`
    CreatedAt    time.Time  `json:"created_at"`
    LastUsedAt   *time.Time `json:"last_used_at"`
    ReportAccess bool       `json:"report_access"`
}
```

Pagination facts baked into the helpers: `GET /reports` takes `limit` (default 50, max 200) and an opaque `page_token`; the bulk endpoints take `limit` (max 500) and their own opaque token. Tokens are never constructed client-side, only echoed.

Origin rule: the client's base URL for every authenticated call is the portal credential's recorded `Server` (the origin that minted the token); `config.ServerURL` is consulted only by `login`, as the default when `--server` is absent. A bearer token is therefore never sent to an origin other than the one that issued it, even if the config's global field has been edited since.

Error handling: non-2xx bodies decode as `{error, message, ...extras}`. Classification per the requirements retry policy: transport errors, 429, and 5xx are transient (exponential backoff, 500 ms base doubling to a 30 s cap, full jitter, ~6 attempts within a bounded budget); every 4xx-class coded error (`BAD_REQUEST` 400, `NOT_FOUND` 404, `NOT_READY` 409, `EXPIRED_CURSOR` 410, plus any unrecognized code) is a contract error, never blind-retried. `NOT_AUTHENTICATED` (401) short-circuits into the exit-3 `CLIError` with the canonical `action` string. `NOT_READY` carries its extra field (`athena_query_state` on run downloads, `status` on job downloads) through `CLIError.Extra`. `EXPIRED_CURSOR` surfaces as a typed error the paged fetcher catches for its restart rule.

`s3.go`: `StreamToFile(envelopeFn, path)` requests an envelope, immediately GETs `download_url`, and streams to `path` (caller handles `.tmp`/rename). Any S3 failure, including expiry, discards the response unparsed (it is XML, outside the JSON error contract) and re-invokes `envelopeFn` for a fresh URL within the same retry budget. URLs never touch logs or disk.

Tests: classification matrix over an `httptest` server (each landed code, unrecognized 4xx code, 429/5xx retry counts, jitter bounds); the origin rule (a data command with `config.ServerURL` pointing at a different origin than the credential's recorded `Server` sends the request and bearer to the credential's origin, never the config's); S3 expiry → fresh envelope; pagination drain.

---

### login, logout, and auth status

**Summary**: The full auth command set: PKCE loopback login with manual `--token` fallback, revoke-then-delete logout with the older-server degradation, and offline-plus-`--check` status.

**Files affected**:
- `cmd/login.go`, `cmd/logout.go`, `cmd/auth.go`
- `internal/auth/pkce.go` — verifier/challenge/state generation
- `internal/auth/loopback.go` — the callback listener
- `go.mod` — add `github.com/pkg/browser`

**Estimated diff size**: ~500 lines

PKCE: verifier = 43-char base64url of 32 random bytes; challenge = unpadded base64url SHA-256 of the verifier string (the server validates the challenge against `^[A-Za-z0-9_-]{43}$`); `state` = base64url of 16 random bytes. The verifier, the state nonce, and any other client-minted security token MUST come from `crypto/rand` (`crypto/rand.Read`; a read failure is a hard error, never a fallback to a weaker source). A `math/rand` implementation passes every functional test, including the PKCE vector tests, while collapsing the security property, so the import is a review point and a source-level test asserts `math/rand` is not imported anywhere in `internal/auth`.

Loopback: `net.Listen("tcp", "127.0.0.1:0")`, handler mounted at exactly `/callback` (the server rejects any other `redirect_uri` shape: http scheme, literal `127.0.0.1`, integer port, path `/callback`, no query/fragment). The browser opens

```
<server>/auth/cli?portal=https://<portal-host>&redirect_uri=http://127.0.0.1:<port>/callback
    &state=<state>&code_challenge=<challenge>&code_challenge_method=S256
```

via `pkg/browser`, whose package-level `browser.Stdout`/`browser.Stderr` defaults the opener seam first redirects to the CLI's stderr (they default to `os.Stdout`, and `xdg-open` chatter on stdout would break the one-JSON-envelope stream contract), with the URL also printed to stderr for copy/paste. The callback receives `code` and `state` query params; `state` is compared constant-time. The listener keeps serving until a callback whose `state` matches (or the 5-minute overall timeout fires): a mismatch is answered with a static error page and neither stops the listener nor consumes anything, so a local process racing junk to the ephemeral port cannot break the login (the landed controller renders errors server-side and never redirects an error to the loopback, so a legitimate callback always carries `code` + `state`). On a matching callback the listener answers a small self-contained HTML "you can return to your terminal" page and shuts down; neither response page reflects query values into the HTML. The 5-minute timeout matches the server's grant TTL; timeout exits 1 with a clear message.

Exchange: `POST <server>/auth/cli/token` with JSON body `{"code", "code_verifier", "label": "CLI login (<hostname>)"}` (secrets in the body only; the server rejects them as query params; `label` is the additive owed param, ignored by older servers which then label the token "CLI login"). Success is `{"token": "ccd_..."}`; failure is a single `BAD_REQUEST` ("Invalid code or verifier.", one-time codes burn on first try, so no retry). The token stores via `creds.Save` with the server origin and stored-at timestamp. `--token` skips the flow and stores directly; `--token -` reads the secret from stdin (piped for scripts, or an echo-off prompt when stdin is a TTY) and is the form the help text recommends, with the bare `--token <value>` form kept for compatibility but documented as discouraged because flag values land in shell history and process lists.

`logout`: `DELETE <server>/api/v1/tokens/current` (owed endpoint; success is 200 `{"revoked": true}`). Responses map per the requirements: 200 → remove local, exit 0; 401 → stderr "nothing needed revoking" warning, remove local, exit 0 (the one 401 exemption); contract 404 (older server: the route falls through to the API catch-all) → remove local, warn the token may still be active with the token-management UI URL, exit 0.

`auth status`: offline render of `creds.List()` (portal, backend, stored-at) plus `default_portal`, no network. `--check` adds a per-portal call to `GET /api/v1/tokens/current` (owed endpoint) rendering label (null → "(no label)"; `--json` carries the raw null), `created_at`, `last_used_at`, `report_access`; a contract 404 falls back to `GET /api/v1/reports?limit=1` for validity-only with metadata marked unknown. Exit 0 whenever the command completes, regardless of per-portal validity, which travels in the output.

Tests: PKCE vector tests (RFC 7636 appendix vectors); the `math/rand`-not-imported source assertion for `internal/auth`; state-mismatch rejection (and the listener still accepting the genuine callback afterward); timeout; exchange body placement; login stream discipline (browser-launcher output never reaches stdout); `--token -` stdin and TTY-prompt paths; logout tri-state matrix; status `--json` goldens with null label and unknown metadata.

---

### Auth integration tests: fake-server end-to-end plus the on-demand live PKCE check

**Summary**: The subtask (a) acceptance harness: an `httptest` fake report server driving the real `login` → `auth status --check` → `logout` command path in-process, plus a documented, on-demand Playwright script proving the real handshake against a locally running report-service (per the resolved live-test question).

**Files affected**:
- `internal/auth/integration_test.go` — the fake server + flow test
- `internal/auth/browser.go` — browser-opener seam (`var openBrowser = browser.OpenURL`) so tests can follow the auth URL itself
- `test/e2e/pkce-live.mjs` — Playwright script driving the real flow against a local report-service
- `test/e2e/README.md` — setup and run instructions

**Estimated diff size**: ~400 lines

The fake server implements `/auth/cli` (validates every query param exactly as the real controller does, including `portal` being optional-with-default-fallback and validated only when present, then 302-redirects to the `redirect_uri` with `code` + echoed `state`), `/auth/cli/token` (verifies the S256 challenge relationship and body-only secrets, mints a `ccd_` token), `/api/v1/tokens/current` (GET and DELETE), and `/api/v1/reports`. The test stubs the opener to fetch the URL like a browser, runs the real commands, and asserts stored credentials, `--check` output, and revocation. A second variant plays an older server (404 on the tokens routes) to pin the degradation paths.

Fake-server behavior is pinned, not guessed: every fake-served surface was captured live and recorded with its source commit in [wire-captures.md](wire-captures.md), which fake fixtures cite. The 401 envelope, token introspection and revoke including the post-revoke 401, run list/show including `report_type`, and the `BAD_REQUEST` envelope came from a locally running report-service; the download envelope from a follow-up session against a real Athena run; and the bulk answers/history pages and attachment presign responses (initially uncapturable) from a third session against the Node functions deployed to staging (`report-service-dev`). The one behavior not captured live is the `EXPIRED_CURSOR` 410 (provoking it requires the one-hour scratch TTL to lapse); its exact body is code-derived from the server source at the same pinned commit and recorded in wire-captures.md alongside the controller-test citation and the live observation that a malformed token is a 400, not a 410. Re-capture when the pinned commit moves.

The live check (`test/e2e/pkce-live.mjs`, ~150 lines): runs `cc-data login --server http://localhost:4000 --portal learn.portal.staging.concord.org` with the browser opener suppressed, captures the printed auth URL, drives it with Playwright (staging-portal login form via test-account credentials from env vars, OAuth approve), waits for the CLI to complete the exchange, then asserts `auth status --check`, `reports list`, and `logout` against the live server. Prerequisites are the standard report-service dev environment (asdf toolchain, MySQL container, `.env` secrets from 1Password, per the server README) and a staging-portal account with report access; report-service's `dev.exs` already targets the staging portal with the `research-report-server` OAuth client, so no new portal-side setup is needed. It is a dev-machine test, never CI. Subtask (a)'s definition of done includes this script existing, being documented, and having passed once; it is not a CI gate, but it re-runs after each owed server dependency lands (part of that dependency's definition of done), so the primary paths the fakes encode are eventually all exercised against a real server rather than certified once via their fallbacks.

Rollout note (verified live 2026-07-16): neither deployed report server, production `report-server.concord.org` nor staging `report-server.concordqa.org`, currently serves `/api/v1/*` or `/auth/cli`; both run builds predating the landed CLI-support code. The live check above is therefore the only pre-deploy verification of the real handshake; first deployed dogfooding additionally requires a report-server release (staging first, per its normal `-pre.N` release flow) plus the owed endpoints tracked in the requirements, **and separately a Firebase functions deploy**: the bulk-read and attachment endpoints live in the report-service Node functions, which (verified 2026-07-17 by live probe) are not yet deployed to `report-service-dev` or `report-service-pro`, so the answers/history/attachments endpoints fail against every real environment until that deploy happens even where the Elixir server is current.

---

### Dataset references, manifest schema, and dataset CRUD

**Summary**: The dataset layer everything else builds on: ref resolution, the on-disk layout, the manifest as typed Go structs with versioned migration, and the `dataset create|list|show|rename|edit|delete|purge` commands (`show`/`list` details and `reindex` come in a later step, after stores exist).

**Files affected**:
- `internal/dataset/ref.go` — dataset ref parsing/resolution
- `internal/dataset/manifest.go` — manifest schema, read/migrate/write
- `internal/dataset/dataset.go` — open/create/delete/purge operations
- `internal/store/lock.go` — the `gofrs/flock` wrapper and the process-wide per-dataset / whole-fetch-activity guards (lands here so the mutating commands below can lock; the merge-engine step consumes the same primitives, and its "Lock files and the whole-fetch activity lock" paragraph defines their semantics)
- `cmd/dataset.go` — cobra `dataset` command tree

**Estimated diff size**: ~560 lines

Ref resolution: `<portal>/<name>` splits on the first `/`; a bare `<name>` uses `config.DefaultPortal` (usage error, exit 2, if unset). Portal is normalized to a hostname (scheme stripped, lowercased). Dataset names match `^[a-z0-9][a-z0-9_-]{0,62}$`; `create` rejects others, and this same alphabet feeds the SQL-identifier sanitization in the query-layer step below. `create` and `rename` additionally reject DuckDB's reserved/built-in schema names (`main`, `temp`, `system`, `information_schema`, `pg_catalog`) with a clear message, since each dataset becomes a schema in multi-dataset queries. Every command echoes the resolved ref to stderr. Dataset path: `<data_root>/<portal-host>/datasets/<name>/` where `data_root` is `CC_DATA_ROOT` env, else `config.json` `data_root`, else `~/cc-data`.

Manifest structs (`manifest.json`, schema `version: 1`; all file paths relative to the dataset folder):

```go
type Manifest struct {
    Version     int               `json:"version"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    CreatedAt   time.Time         `json:"created_at"`
    Stores      map[string]Store         `json:"stores"`      // keys: "answers", "history"
    Membership  map[string]MembershipRef `json:"membership"`  // key "<type>/<run>": current membership version, repointed by the merge
    Downloads   []Download               `json:"downloads"`
    Attachments []AttachmentFile         `json:"attachments"` // rebuilt index, see get attachments
}

type Store struct {
    File    string            `json:"file"`    // e.g. "answers.v3.jsonl" (final names only)
    Version int               `json:"version"` // 3
    Count   int               `json:"count"`   // records in this version
    Columns map[string]string `json:"columns"` // DuckDB type per field, derived from every record during the merge
}

type MembershipRef struct {
    File    string `json:"file"`    // e.g. "members_answers_584.v3.jsonl" (final names only)
    Version int    `json:"version"` // the store version whose merge wrote it
}

type Download struct {
    Type         string          `json:"type"` // report | report_job | answers | history | attachments
    RunID        int             `json:"run_id"`
    JobID        *int            `json:"job_id,omitempty"`
    Slug         string          `json:"slug,omitempty"`
    ReportType   string          `json:"report_type,omitempty"` // answers|usage|log, or an unknown value recorded verbatim
    SourceKey    string          `json:"source_key,omitempty"`
    Filters      json.RawMessage `json:"filters,omitempty"`       // raw filter selection from the run
    FilterLabels []string        `json:"filter_labels,omitempty"` // resolved human labels
    Files        []string        `json:"files,omitempty"`         // relative paths this download owns
    Coverage     *Coverage       `json:"coverage,omitempty"`
    MergeCounts  *MergeCounts    `json:"merge_counts,omitempty"`
    HistoryMode  string          `json:"history_mode,omitempty"` // constant "full" for history
    Complete     bool            `json:"complete"` // false from fetch start until the merge lands; resume state lives in the cursor file, never here
    FetchedAt    time.Time       `json:"fetched_at"`
    Scanned      []string        `json:"scanned,omitempty"`  // attachments: sources scanned
    RowCount     *int            `json:"row_count,omitempty"` // CSVs: data rows, pseudo-header rows excluded
    Columns      map[string]string `json:"columns,omitempty"` // CSVs: DuckDB type per column, full-file detection at download (the CSV sibling of Store.Columns)
    CSVDialect   *CSVDialect       `json:"csv_dialect,omitempty"` // CSVs: the fixed dialect the detection pass and the views agree on
    Recovered    bool              `json:"recovered,omitempty"` // set by reindex when this entry was rebuilt without the original fetch's provenance (filters, exact report_type)
}

type CSVDialect struct { // report CSVs are RFC4180-shaped; recorded so views never re-sniff
    Delim  string `json:"delim"`  // ","
    Quote  string `json:"quote"`  // "\""
    Escape string `json:"escape"` // "\""
    Header bool   `json:"header"` // true
}

type Coverage struct {
    Queried  *int          `json:"queried"`  // null when total_endpoints absent (older server)
    WithData int           `json:"with_data"`
    Empty    *int          `json:"empty"`    // null when queried unknown
    Missing  []MissingItem `json:"missing,omitempty"` // attachments: per-item not_found/not_authorized
}

type MergeCounts struct {
    Fetched int `json:"fetched"`
    New     int `json:"new"`
    Updated int `json:"updated"`
    Removed int `json:"removed"`
}

type MissingItem struct { // attachments coverage: per-item presign failures, never fatal
    DocID string `json:"doc_id"`
    Name  string `json:"name"`
    Error string `json:"error"` // "not_found" | "not_authorized"
}

type AttachmentFile struct { // one row per unique downloaded file; feeds the attachment_files view
    ID12        string          `json:"id12"`         // first 12 hex of sha256("<source>|<publicPath>")
    Name        string          `json:"name"`
    Source      string          `json:"source"`
    PublicPath  string          `json:"public_path"`
    ContentType string          `json:"content_type,omitempty"` // from the attachments map when present
    Size        int64           `json:"size"`
    File        string          `json:"file"` // relative path: attachments/<id12>_<safename>
    State       bool            `json:"state,omitempty"` // offloaded interactive state (referenced by an __attachment__ marker); the attachment_states view's selection predicate
    Refs        []AttachmentRef `json:"refs"` // referencing records; rebuilt at get attachments / reindex
}

type AttachmentRef struct {
    Type           string `json:"type"` // answers | history
    SourceKey      string `json:"source_key"`
    RemoteEndpoint string `json:"remote_endpoint"`
    QuestionID     string `json:"question_id"`
    HistoryID      string `json:"history_id,omitempty"`
}
```

`ReadManifest` unmarshals `version` first, migrates forward through a `switch` (v1 is a no-op today; the switch is the extension point), and returns the "please upgrade cc-data" error (exit 1) for versions above `CurrentManifestVersion`. `WriteManifest` goes `.tmp` → fsync → rename (via the `internal/fsutil` robustio wrapper, so a concurrent Windows reader does not fail the rename; see the config step), like every durable write in the CLI.

Command behaviors, per the requirements CRUD bullet: `create` auto-names `{date}_{slug}` (slug kebab-cased from `--description`, else `{date}_{n}` counter scanning existing names); `delete` removes the whole folder after confirmation (`--force` skips); `purge` deletes stores, segments, membership files, CSVs, and `attachments/`, clears `Stores`, `Membership`, `Downloads`, and `Attachments` in the manifest, keeps name/description/identity. `rename` renames the folder and manifest name; `edit --description` updates the manifest.

Locking: every mutating dataset command (`rename`, `edit`, `delete`, `purge`, and `reindex` in its later step) first acquires the exclusive whole-fetch activity lock, then the per-dataset lock, both **non-blocking** (lock files and semantics are defined in the merge-engine step); if another cc-data process holds either (a live fetch holds the shared activity lock for its whole lifetime), the command fails fast with exit 1 and "dataset is busy: another cc-data command is writing to it", never waiting silently. Read-only commands (`show`, `list`, `query`, `repl`) deliberately do not lock: they read only manifest-current, complete artifacts, which the atomic-rename discipline makes safe to read concurrently, and blocking analysis during a long fetch would be a real regression.

Tests: table-driven ref parsing; name-validator rejection of DuckDB reserved schema names and regex boundary rows (63-char max accepted, 64 rejected, leading digit accepted, leading hyphen/underscore rejected); manifest round-trip + future-version refusal; CRUD against a `t.TempDir()` data root via `CC_DATA_ROOT`; purge acceptance (zero holdings, still listed); mutate-under-fetch matrix (each mutating command against a real fetch stalled between pages, via the crash harness's env-var stall point, fails fast with the busy error and the fetch then completes unharmed; runs on the Linux and Windows CI legs).

---

### Per-dataset locking, segments, and the identity merge engine

**Summary**: The heart of the never-duplicate model: identity keys, the per-download segment lifecycle, and the merge-compact that produces each new store version. This step is pure storage engine with no network dependency, so it lands with exhaustive unit tests including the two concurrency acceptance criteria.

**Files affected**:
- `internal/store/identity.go` — identity tuples and ordering
- `internal/store/segment.go` — segment append + cursor persistence
- `internal/store/merge.go` — merge-compact + rebase loop
- `internal/store/membership.go` — membership file read/write
- `internal/store/lock.go` — extended here (the wrapper and per-dataset/activity guards were introduced in the dataset-CRUD step); this step adds the per-download lock and the merge critical-section usage

**Estimated diff size**: ~550 lines (plus ~450 lines of tests)

Identity: answers = `(source_key, remote_endpoint, question_id)`, history = same + `history_id`. Keys encode length-prefixed: each field as `<decimal byte length>:<raw bytes>`, concatenated. This is injective for arbitrary field contents by construction (the identity fields come from client-written Firestore docs, so no character set can be assumed; a crafted `question_id` must not be able to collide two identities), and byte comparison of encoded keys gives a total order. The order is an internal sort key only (it differs from raw lexicographic order of the fields when lengths differ, which is harmless: the merge needs consistency, not meaning), and the encoding never leaves memory; stores, membership files, and query surfaces all carry the raw fields.

**Stores are kept sorted by identity key.** This is the load-bearing design decision of this step: with both inputs ordered, merge-compact is a streaming two-way merge with O(1) memory over the store, so dataset size is disk-bound, not RAM-bound. The segment (one run's fetch) is never materialized whole: at merge time the CLI sorts the segment's `(identity key, _fetched_at, _run_id, byte offset)` tuples in memory, the same identity-set bound the design doc states for the merge, and streams records back from the segment by offset in sorted order (random reads within one file, traded against never holding full records; history snapshots can reach the 1 MiB Firestore doc limit and a history segment is a run's entire snapshot series, so record-proportional memory would be unbounded). DuckDB `read_json` does not care about physical order, so queries are unaffected.

Record stamping: every record written to a segment gains `_fetched_at` (RFC3339, one timestamp per download) and `_run_id`. Answer records also get `report_state` double-decoded at ingest (see the answers/history step).

Segment lifecycle per download, under `<dataset>/segments/`:
- `seg_<type>_<run>.jsonl` — page items appended as fetched, then fsynced before the cursor that records the page is persisted (the order is the invariant: the cursor's `next_page_token` must never durably name pages whose segment bytes a crash could lose, or resume would skip them and the merge would land a `complete: true` store silently missing records; process-kill crash tests never expose this because the OS retains buffered writes across process death, so it is a stated rule rather than a test-discovered one)
- `seg_<type>_<run>.cursor.json` — `{next_page_token, pages, items, total_endpoints, merged_as}` written (`.tmp` + rename) after each page, under the per-download lock only (already held for the command's lifetime; cursor writes take no per-dataset lock, so page loops never stall behind another download's merge); `merged_as` is set to the landed store version immediately after a successful manifest repoint (the resume short-circuit, see crash safety)
- `seg_<type>_<run>.lock` — the per-download lock file (a dedicated file, never the cursor, which is replaced by rename)

**Per-download lock**: every `get` of a `(type, run)` acquires an exclusive non-blocking flock on `seg_<type>_<run>.lock` for the lifetime of the command; a second command fetching the same download fails fast with exit 1 and "download busy: another cc-data command is fetching run <run> <type>". The rule covers every download type, not only the store-building fetches: `get report` locks `seg_report_<run>.lock`, `get report --job <id>` locks `seg_report_job_<run>_<id>.lock` (job-qualified, so two different jobs of one run never spuriously conflict), and `get attachments` locks `seg_attachments_<run>.lock`; the lock file lives under `segments/` in every case. For the store-building fetches, this is what makes the wholesale membership replacement safe: no merge can run against a segment that a concurrent same-run command is appending to, deleting (`--refresh`), or restarting (`EXPIRED_CURSOR`), because all of those happen only while holding this lock. Different-run downloads stay fully concurrent; the per-dataset lock alone serializes their manifest writes and merges.

**Lock semantics (in-process and cross-process)**: gofrs/flock's `Lock()` short-circuits without a syscall when the same `Flock` instance is already held, while separate instances on the same path contend even within one process (flock attaches to the open file description). Both the per-dataset and per-download locks therefore follow one rule: each lock is a process-wide guard per path, a `sync.Mutex` for goroutine exclusion layered over a single flock for cross-process exclusion, acquired exactly once per critical section; inner helpers never re-acquire and instead assert the guard is held (`WriteManifest` asserts the per-dataset guard; cursor writes assert the per-download guard). This closes both failure modes: a shared bare `Flock` would let two in-process merges (the MCP server is one long-lived process) enter the critical section together and clobber each other's store version, and a fresh instance inside a helper would deadlock against the outer hold.

**Lock files and the whole-fetch activity lock**: every lock is taken on a dedicated file that is never renamed and never read as data, for the same reason the per-download rule excludes the cursor: a rename silently detaches flocks onto a dead inode, and on Windows `LockFileEx` is mandatory, so flocking a file that lockless readers read would fail their reads with `ERROR_LOCK_VIOLATION`. The per-dataset lock file is `<dataset>/.dataset.lock`. A second file, `<dataset>/.activity.lock`, provides whole-fetch exclusion: every `get` takes a shared flock on it (`TryRLock`, non-blocking) for the command's lifetime, and every mutating dataset command (`rename`, `edit`, `delete`, `purge`, `reindex`) takes an exclusive flock on it (`TryLock`, non-blocking) before taking `.dataset.lock` for its own work, failing fast with the dataset-busy error while any fetch is live. Two files rather than one because a merging fetch already holds the shared lock and a shared-to-exclusive upgrade on a single file would self-deadlock; separate files avoid upgrade semantics entirely. In-process, the activity lock's guard is a `sync.RWMutex` layered over the single flock with a reader count: the first in-process reader takes the shared flock, the last releases it, and exclusive acquisition uses the write half, so concurrent fetches inside the MCP server share correctly. Lifecycle: no lock file (`.dataset.lock`, `.activity.lock`, `segments/*.lock`) is ever unlinked by cleanup, `--refresh`, or `purge` (`purge` enumerates its deletions and excludes `*.lock`), because unlinking a held flock file detaches it from its inode and hands the next acquirer a second "held" lock; `delete` acquires the exclusive activity lock, closes its own handles, then removes the folder, an accepted microscopic window in which a newly starting fetch fails on the missing manifest rather than losing data.

**Write amplification and the lock-free backlog sweep**: each merge rewrites the whole store version, so N sequential single-run fetches into one dataset cost O(N^2) store IO (the K-th merge rewrites a store holding K-1 runs' records to add the K-th). This is inherent to the immutable-sorted-store design and is accepted; concurrent multi-run pulls into one dataset pay the same O(N^2), because each live fetch merges its own segment itself under the per-download lock it holds for the whole command lifetime, and one merge cannot fold in a still-live sibling's segment without violating that lock (see the boundary below). It is stated here rather than left implicit (the merge's other cost claim, "disk-bound, not RAM-bound", addresses memory only). The **backlog** case is mitigated: when a merge acquires the dataset lock it additionally sweeps other finished-but-unmerged segments of the type into the same compact, but **only those whose per-download lock it can itself acquire non-blocking** (`seg_<type>_<run>.lock` free, i.e. the fetch that wrote the segment has exited or crashed between finishing its pages and merging, rather than a live command still holding it), taking that lock for the duration of the sweep-write so the per-download-lock contract (only the lock holder reads the segment or writes its cursor/membership) is never broken. Sweep candidates are the cursor state the resume path already reads (null `next_page_token`, no `merged_as` at or below the current store version); `MembershipUnion` accepts the multiple per-run membership replacements a multi-segment sweep produces. This collapses a *backlog* of finished-and-abandoned segments (a crashed fetch, or fetches that exited before their merge landed) into one store rewrite; a **live** concurrent pull is never swept and always merges its own segment and reports its own counts, so no result line ever misstates a live fetch's counts and no live segment is touched by another process. Each swept run still gets its own membership version, its own `Download`-entry counts (computed against the pre-sweep store; a per-run count against a sibling segment in the same sweep is the one accepted minor skew), and its own `merged_as`, each written under that run's briefly-held per-download lock; one manifest write repoints the store and all swept memberships together. **Boundary:** because a live fetch holds its per-download lock until its command exits (which is after its own merge), the sweep by construction never folds in a live concurrent pull; it is a crash/exit-backlog optimization, not a live-concurrency one, and the O(N^2) cost of genuinely concurrent multi-run pulls stands.

**Sequencing note:** the backlog sweep is a pure write-amplification optimization with no correctness role. An abandoned finished-but-unmerged segment is already correctly merged by an ordinary single-segment resume, one at a time; the sweep only saves store rewrites when several such segments have piled up. So it can land as a separate commit *after* the single-segment merge and its concurrency acceptance tests (never-duplicate, crash-injection matrix, same-run exclusion, `EXPIRED_CURSOR` restart) are proven green, keeping the spec's most delicate subsystem's correctness core independent of this optimization's extra logic (discover lock-free finished siblings, acquire each per-download lock non-blocking, multi-run `MembershipUnion`/`streamMerge`, per-sibling membership/counts/`merged_as`/cleanup, release).

Merge-compact (runs when the final page lands, or on resume of a finished-but-unmerged segment; additionally sweeps the lock-free finished-but-unmerged segments of the type per above, the sketch showing the single-segment spine for readability, with `seg`/`runID` standing for the swept set):

```go
// mergeCompact holds the dataset lock for the critical section only.
// Segment appends from other downloads proceed concurrently.
// It additionally sweeps the finished-but-unmerged segments of `typ`
// whose per-download lock it can acquire non-blocking (see above);
// the single-segment form below is the readable spine.
func mergeCompact(ds *Dataset, typ string, runID int, seg *Segment) (MergeCounts, error) {
    lock := ds.Lock()                       // process-wide per-dataset guard (mutex + flock; see lock semantics)
    if err := lock.Lock(); err != nil { return MergeCounts{}, err }
    defer lock.Unlock()

    for {
        m, err := ds.ReadManifest()         // re-read INSIDE the lock; checked: a zero-value
        if err != nil {                     // write below would wipe the manifest and bypass
            return MergeCounts{}, err       // the future-version upgrade gate
        }
        cur := m.Stores[typ]                // may be newer than we prepared against; the zero
                                            // value (Version 0, File "") on the first merge of
                                            // a type reads as an empty old-store stream
        next := cur.Version + 1
        tmp := ds.Path(fmt.Sprintf("%s.v%d.jsonl.tmp", typ, next))

        // Load every membership set for this type; the new segment's identity
        // set REPLACES this run's previous membership wholesale.
        covered := ds.MembershipUnion(typ, runID, seg.Identities())

        counts, total, cols, err := streamMerge(ds.Path(cur.File), seg.Sorted(), tmp, covered)
        // streamMerge: two-way merge on identity key; segment beats store;
        // equal keys within one input break ties on (_fetched_at, _run_id);
        // identities not in covered are dropped and counted as removed;
        // total = records written to the new store version;
        // cols = the DuckDB column map derived while streaming (see below);
        // an empty old-store path (first merge) streams zero records;
        // output is fsynced before return.
        if err != nil { return counts, err }

        cur2, err := ds.CurrentStore(typ)
        if err != nil { return counts, err } // checked: a failed read must abort, not spin the
                                             // rebase loop forever or false-pass on version 0
        if cur2.Version != cur.Version {
            os.Remove(tmp)                  // a concurrent merge won; rebase
            continue
        }
        // Durable-write ORDER is load-bearing (see crash safety below):
        // 1. store rename  2. membership  3. manifest repoint  4. cursor merged_as  5. segment removal LAST.
        final := ds.Path(fmt.Sprintf("%s.v%d.jsonl", typ, next))
        if err := os.Rename(tmp, final); err != nil { return counts, err }
        if err := ds.WriteMembership(typ, runID, next, seg.Identities()); err != nil { // members_<type>_<run>.v<next>.jsonl, .tmp + rename
            return counts, err // abort BEFORE the repoint: segment survives, resume re-merges
        }
        m.Stores[typ] = Store{File: filepath.Base(final), Version: next, Count: total, Columns: cols}
        m.SetMembershipRef(typ, runID, next)  // one manifest write repoints store AND membership
        if err := ds.WriteManifest(m); err != nil { return counts, err }
        ds.CleanupOldVersions(typ, next)    // best-effort, idempotent; old stores AND old membership versions
        if err := seg.MarkMerged(next); err != nil { // cursor merged_as = next, written only after the repoint landed
            return counts, err // segment survives; resume re-merges (content-idempotent)
        }
        seg.Remove()                        // ONLY after all four prior writes succeeded
        return counts, nil
    }
}
```

(The in-lock rebase loop is belt and braces: because the whole merge runs under the lock, the pointer cannot actually move mid-merge; the re-read-and-retry guards the day someone shortens the critical section.)

The merge also derives the store's DuckDB column map while streaming (it touches every record anyway): contract fields (identities, `_fetched_at`, `_run_id`, `_decode_error`, the raw sibling fields, `report_state`/`answer`) get pinned types (`_fetched_at` is TIMESTAMP, written uniform UTC `Z` so RFC3339 rendering correctly assumes UTC; `report_state`/`answer` are JSON), and every other observed field widens over the values seen (int → BIGINT, adding double → DOUBLE, bool → BOOLEAN, string → VARCHAR, object/array/mixed → JSON; plain-string fields like the server's `created_at` therefore map to VARCHAR deterministically, castable in queries when needed). The map lands in the manifest's `Store.Columns`, and the query layer builds `read_json(..., columns = {...})` from it, replacing sampling-based inference entirely (DuckDB's `columns` parameter is all-or-nothing, so the full map is recorded, not just the contract fields).

Merge counts: `fetched` = segment items; `new` = segment identities absent from the old store; `updated` = present and overwritten; `removed` = old-store identities dropped because no membership covers them anymore (the shrunk-`--refresh` case). Merge counts describe the download (they feed the result line and the manifest `Download` entry); the separately returned store total describes the store and feeds `Store.Count`, which `dataset show` renders without scanning files.

Crash safety, matching the requirements bullet exactly: before merge, segment + cursor resume; a `.tmp` store is discarded on sight; a final-named store is complete by construction (rename discipline); after the manifest repoint, `CleanupOldVersions` is idempotent. The write order above adds the invariant that makes every crash window recoverable: **the segment and cursor are deleted last, only after the store rename, membership write, manifest repoint, and `merged_as` write have all succeeded.** A failed membership write aborts before the manifest repoint (checked like every other durable write): the manifest still names the old store/membership pairing, the segment survives, and resume re-runs the merge, renaming over the orphan final-named `v(N+1)`. A surviving segment means resume re-runs the merge, which is idempotent by construction (the same segment merged against whatever store is current yields a correct next version), so a crash in any window converges on resume. Reads feed the same rule as writes: the in-lock manifest re-read and the rebase check's store-pointer read are error-checked and abort the merge, since proceeding on a zero value would durably write an empty manifest (wiping provenance and bypassing the version gate) or spin the rebase loop under the lock. Merge counts stay honest across crashes too: a resume that finds `merged_as` set and at or below the manifest's current store version short-circuits (the merge already landed; versions only grow, and `merged_as` is written only after this merge's own repoint, so the check can never false-positive off another run's merge), cleaning up segment + cursor and emitting an already-merged result line with zero counts, exit 0. Two residual count skews are accepted and named: a crash in the instant between the repoint and the `merged_as` write makes the resume re-merge report overlap counts for work it did not redo, and a crashed shrunk `--refresh` leaves its dropped identities to be removed by the next run's merge, which reports them in its own `removed`. The order also protects readers, now fully: membership files are versioned like stores and both are repointed by the same manifest write, so queries (which resolve stores and membership through the manifest) see a consistent store/membership pairing at all times; the worst post-crash skew (renamed store and membership, old manifest) is invisible to them, and no rename of a store or membership file ever lands on a path a concurrent reader holds open (the Windows constraint that motivated store versioning applies identically to membership). The claim is scoped to the versioned artifacts: the fixed-name files that are replaced in place (manifest, config, credentials, cursor, and the `--refresh` targets `report_<run>.csv` and attachment files) can be held open by a concurrent Windows reader during their rename, which is why those writes go through the fsutil robustio wrapper (config step) and the `--refresh` overwrites are a documented accepted race (get report / get attachments), not a versioning guarantee.

Tests: identity-encoding goldens (fields containing 0x1F, colons, and empty strings; distinct tuples encode distinctly), golden merges (overlap, shrink, tie-break), the never-duplicate acceptance (fetch A, overlapping B, re-fetch A with `--refresh`, assert `count(*) == count(DISTINCT identity)` by scanning the store), the concurrency acceptance (two separate cc-data processes, via test re-exec of the binary against a shared `CC_DATA_ROOT`, merging different runs into one dataset, assert no lost records), an in-process lock regression (two goroutine acquisitions of the same dataset guard actually block each other), an activity-lock matrix (two concurrent fetches hold the shared lock together; a mutator's exclusive `TryLock` fails while either is held; a fetch's `TryRLock` fails while a mutator holds the exclusive lock; the in-process reader count releases the flock only when the last reader exits), the same-run exclusion (a second `get` of the same run+type against a held per-download lock fails fast with the busy error and loses nothing), and crash injection at each write boundary via an explicit `testHookAfterWrite(n)` seam in `mergeCompact` (a no-op in production builds: abort after each of the five durable writes, resume, assert content convergence with no lost records, and count convergence from the `merged_as` write onward), plus one coarse real-process case reusing the concurrency acceptance's test re-exec harness (SIGKILL the binary at an env-var-driven stall point between page append and cursor write, assert resume converges), plus a fault-injection case where the membership write itself fails (merge aborts before the repoint, segment survives, resume converges with no lost records), and a large-segment memory case (synthetic segment with oversized records: the merge's working set is identity tuples plus a single-record read buffer, never the full record set); a first-merge bootstrap golden (no prior store: the zero `Store` value reads as an empty old-store stream and `v1` lands with the derived column map); a corrupt-manifest abort case (an unreadable or future-version manifest aborts the merge with no manifest write and the segment intact); and a lock-free backlog sweep case (two runs' segments left finished-but-unmerged with their per-download locks *free*, one merge collapses both into a single new store version with both memberships repointed and each run's counts and `merged_as` recorded, and the store is rewritten once, not twice), plus its negative (a finished-but-unmerged segment whose per-download lock is *held* by a live command is NOT swept by another run's merge, and that live command merges its own segment and reports its own counts).

---

### Listing commands: reports list and reports jobs

**Summary**: The two read-only listing commands over `GET /api/v1/reports` and `GET /api/v1/reports/:id/jobs`, draining keyset pagination. First consumers of the paged-envelope helper in `internal/api`.

**Files affected**:
- `cmd/reports.go` — `reports list`, `reports jobs`
- `internal/api/reports.go` — typed list/jobs calls

**Estimated diff size**: ~200 lines

`reports list --portal <portal>` drains all pages and renders run id, slug, state, and resolved filter labels. Filter rendering tolerates a filter-less run: a programmatically created run stores a `NULL` report filter, but the server's `ReportJSON` view normalizes `nil` to the empty filter object (`report_json.ex`: `report_filter_json(nil)` → `report_filter_json(%ReportFilter{})`), so the wire always carries a full `report_filter` object and such a run renders with zero filter labels rather than requiring a nil branch. Listing output is the command's product, so the human table goes to stdout (the stream-discipline rule reserves stdout for machine output on `get`/`query`; for pure listings the table is the output, and `--json` swaps it for the machine form). `reports jobs <run-id>` is the same shape over the jobs endpoint.

Tests: `httptest` server with multi-page fixtures; golden table and `--json` output.

---

### get report: polling, presigned envelope, atomic CSV download

**Summary**: `cc-data get report <run-id> --dataset <ref>` with `--job`, `--no-wait`, and `--refresh`, implementing the poll → envelope → prompt S3 stream → atomic rename flow.

**Files affected**:
- `internal/fetch/report.go` — the state machine
- `cmd/get_report.go` — flags and wiring

**Estimated diff size**: ~350 lines

The command holds its per-download lock for its lifetime (`seg_report_<run>.lock`; with `--job <id>`, `seg_report_job_<run>_<id>.lock`), failing fast with the busy error per the per-download lock rule in the merge-engine step.

Flow (states verified in report-service: `null`, `"queued"`, `"running"`, `"succeeded"`, `"failed"`, `"cancelled"`): call `GET /:id/download` directly and branch on the response (verified in `report_controller.ex`: the download action calls `AthenaRunOps.ensure_current` exactly like `GET /reports/:id`, so polling it advances an unstarted run; the requirements' "checks `athena_query_state` first" is satisfied through the `NOT_READY` envelope's carried state, and audit rows are written only at envelope issuance, never by a not-ready poll). 200 → stream immediately. `NOT_READY` (409, carrying `athena_query_state` as an envelope extra): a terminal failure state → error envelope, exit 5, write nothing; otherwise with `--no-wait` → standard result line (`complete: false`, the carried state, no file), exit 4; otherwise poll with backoff (2 s doubling to a 30 s cap, jitter, stderr progress line per poll) until a terminal state or the polling budget elapses. Terminal-state handling is a load-bearing detail, verified against report-service: for runs, `failed`/`cancelled` are terminal; for jobs, the ready state is `"completed"` and `"failed"` is terminal (the job controller renders every non-completed status, `"failed"` included, as `NOT_READY` 409, so the CLI must recognize `status: "failed"` as terminal rather than poll it forever — `report_job_controller.ex`). A terminal failure exits 5 with the carried state; write nothing. Two further termination guards, both from verified server behavior: (1) an overall polling budget, default 30 minutes (sized against Athena's ~30-minute DML timeout) and overridable with `--poll-timeout <dur>` (`--no-wait` is the zero case), on expiry exits 4 with the last observed state and a "still not ready after <budget>" message, so a headless or MCP caller never hangs; (2) oscillation detection — `AthenaRunOps.ensure_current` releases a run's state back to `null` when a self-start persistently fails (`athena_run_ops.ex`), so the download action drives `null`→`queued`→`null` and re-submits an Athena query every poll; on observing this cycle repeat, the CLI stops early with "the server repeatedly failed to start this query" (exit 5) instead of burning the budget and re-triggering Athena submissions. On 200, stream `download_url` to `report_<run>.csv.tmp`, fsync, rename to `report_<run>.csv` (the URL is 600 s and audit-logged at issuance, so the download starts promptly and the URL is never persisted or logged). A failed/expired S3 GET re-requests a fresh envelope within the same bounded retry budget and never parses the S3 XML body against the JSON error vocabulary. `--job <id>` targets the job endpoints and `report_<run>_job_<id>.csv`; its `NOT_READY` extra field is `status`. Re-pull guard: existing CSV without `--refresh` → error, exit 2.

On success the download records `report_type` in the manifest `Download` entry (written under the per-dataset lock, like every manifest write): the run's server-reported value when present; against an older server, derived from the known slugs (`student-answers` → `answers`, `student-assignment-usage` → `usage`, `student-actions`/`student-actions-with-metadata`/`teacher-actions` → `log`); anything else recorded verbatim with a stderr warning that the type is unknown to this cc-data version (quarantined at query time; upgrade suggested). The CSV's data `row_count` is counted during the stream; for `answers`-type downloads the two pseudo-header rows (`student_id` in `Prompt`/`Correct answer`) are excluded, other types count all rows. The same single streaming pass (fields split with `encoding/csv`) detects each column's DuckDB type by full-file widening (every non-empty value: all-integer → BIGINT, adding a decimal → DOUBLE, all-boolean → BOOLEAN, else VARCHAR; the same widening rule the merge uses for `Store.Columns`) and records the result plus the fixed dialect (`,` / `"` / `"` / header) in the `Download` entry's `Columns`/`CSVDialect`. This is what lets the query layer build the report views with `auto_detect=false` (see the engine step): full-file detection is order- and size-independent, so it neither depends on the server ordering the pseudo-header rows first (a REPORT-58 behavior the CLI must not silently rely on) nor breaks past DuckDB's 20,480-row sniff sample. The detection runs over the CSV as DuckDB will read it (pseudo-header rows included, since they are real rows in the file), so an answers CSV's `_answer` columns detect VARCHAR from the Prompt row's text exactly as before, and a question column whose prompt text is SQL NULL (rendered as the literal string `"null"`) detects VARCHAR rather than a numeric type that would later fail mid-scan. The result line is emitted last.

Tests: `httptest` for both API and a fake S3 endpoint; expired-URL path (S3 403 XML → fresh envelope); `--no-wait` acceptance (queued run exits 4 with parseable state); terminal-failure cases (a run `failed`/`cancelled` and a job `status: "failed"` each exit 5 without polling); polling-budget expiry (a run stuck `running` past `--poll-timeout` exits 4 with the last state); oscillation detection (a fake server cycling `null`→`queued`→`null` stops early with the specific message, exit 5, not a re-submit loop); partial-write crash leaves only `.tmp`; `row_count` fixtures (an answers-shaped CSV with N data rows plus the two pseudo-header rows records `row_count` N under `report_type: answers`; a usage-shaped CSV counts all rows); column-detection fixtures (the same stream records `Columns`/`CSVDialect`: an answers CSV detects `_answer` columns as VARCHAR from the Prompt row and score columns as BIGINT/DOUBLE; a usage CSV detects `student_id` as BIGINT; a CSV whose numeric column carries a text value only in a row past 20,480 still detects VARCHAR, since detection is full-file, not sampled); success-path stream discipline via a shared capture-streams helper (run the command core with both streams captured, assert stdout is exactly one parseable JSON result line; the same helper is reused by the answers/history and attachments test suites); same-run exclusion (a second `get report` of the same run against a held per-download lock fails fast with the busy error and leaves the existing file set unchanged, and a `--job` fetch of a different job proceeds).

---

### get answers and get history: paged fetch, resume, coverage

**Summary**: The paged store-building fetches: segment append per page, cursor persistence, resume, `EXPIRED_CURSOR` restart, double-decoding, membership, merge, and coverage.

**Files affected**:
- `internal/fetch/paged.go` — shared paged-fetch loop (answers + history are one code path parameterized by type)
- `cmd/get_answers.go`, `cmd/get_history.go`

**Estimated diff size**: ~400 lines

Item shapes (verified in the Node bulk-read source that produces them, and live-captured against staging 2026-07-17; see wire-captures.md): answer items are the stored answer document plus `id` (the doc id), carrying `remote_endpoint`, `question_id`, `source_key`, `report_state` (JSON string), `answer`, `type`, `question_type`, and an `attachments` map when present; history items are the state document spread plus `history_id`, `created_at` (ISO 8601), `answer_id`, and `question_id`, and carry `remote_endpoint`. Records are stored whole; identity fields are read from the record. History records carry `source_key` (state docs are full answer-doc copies; verified in the Node seed helpers and the parquet answer schema), so the download-context stamp is a defensive fallback only. One guard rail: a record's `source_key` is LARA-derived and can differ from the Firestore `source` segment (the `answersSourceKey` override), so it is never used as the presign `source`, which always comes from the portal mapping. Page size: `limit` max 500 (also the default); an over-cap `limit` is silently accepted by the server (live-observed, not a contract error), so the cap is a CLI-side discipline, not a negotiated one.

At fetch start the command records (or, on resume, refreshes) the `Download` entry with `Complete: false` under the dataset lock; this is what gives `dataset show` its resumable/incomplete statuses, and the merge's manifest write later flips it to `Complete: true` with the counts. Loop: if a segment + cursor exist for `(type, run)`, resume from `next_page_token`. Finished-ness is the cursor: a cursor with `next_page_token` null marks a finished segment, and resume then re-runs the merge only, unless the cursor's `merged_as` is set and at or below the manifest's current store version, in which case the merge already landed and the command cleans up and emits an already-merged result line with zero counts, exit 0. A segment with no cursor file is stale and discarded. `--refresh` deletes segment + cursor while holding the per-download lock and restarts from a null cursor; its membership-replace consequences (identities no longer covered dropped and reported in `removed`) are the merge's normal semantics, not special-cased. Each page: append items to the segment, then persist the cursor under the per-download lock (held for the command's lifetime; no per-dataset lock). On `EXPIRED_CURSOR` (HTTP 410; the scratch TTL server-side is one sliding hour; exit-class 5 only if the automatic restart also fails): delete segment + cursor (already holding the per-download lock, so no concurrent same-run command can race the deletion), restart from a null cursor, once. Only the 410 triggers this restart: a malformed or corrupted `page_token` instead yields HTTP 400 `BAD_REQUEST` ("page_token is not valid", live-observed), which classifies as a plain contract error like any other 400, so a cursor file corrupted client-side surfaces as a fatal error, not a silent restart. On the final page (`next_page_token` null): run merge-compact (which writes the run's new membership version inside its critical section), update the fetch-start `Download` entry (`Complete: true`, merge counts, `HistoryMode: "full"` for history), emit the result line.

Double-decode at ingest: `report_state` arrives as a JSON string whose `interactiveState` is itself a JSON string; both are parsed and stored as real JSON objects in the record (fields keep their names). A record that fails to double-decode leaves the object field null (`report_state`, or the inner `interactiveState` for an inner-only failure) and stores the failing raw string in a sibling field (`report_state_raw` / `interactive_state_raw`) with a `_decode_error` marker set: data is never dropped, and no field ever mixes object and string types (load-bearing for the query layer, where a type conflict past `read_json`'s inference sample fails the whole view scan).

Coverage: `total_endpoints` from the envelope (when present) is captured into the cursor file per page (constant across pages) and becomes `Coverage.Queried`; `WithData` = distinct `remote_endpoint` in the run's new membership; `Empty` = the difference; absent field → `Queried`/`Empty` null, per requirements.

Tests: multi-page fixtures with resume (kill between pages, rerun); `EXPIRED_CURSOR` acceptance (restart ends `complete: true`); `--refresh` restart and stale-segment (no cursor) discard; success-path stream discipline via the shared capture-streams helper; double-decode goldens including the decode-error path (object field null, raw string in the sibling field, `_decode_error` set); coverage with and without `total_endpoints`.

---

### get attachments: scan, batch presign, download, GC

**Summary**: `cc-data get attachments <run-id>` scanning the run's stored records for attachment refs, presigning in chunks, downloading with existence-driven resume, plus selectors, `--url`/`--inline`, and attachment garbage collection.

**Files affected**:
- `internal/fetch/attachments.go` — scan + presign + download + GC
- `cmd/get_attachments.go` — flags, `attachment` alias (cobra `Aliases`)

**Estimated diff size**: ~450 lines

The command holds its per-download lock (`seg_attachments_<run>.lock`) for its lifetime, failing fast with the busy error per the per-download lock rule in the merge-engine step; its manifest writes (the `Download` entry and the rebuilt attachment index) happen under the per-dataset lock.

Scan (mechanics corrected against the landed wire, 2026-07-16; see the requirements round-6 note): each stored record's `attachments` map is the ref source, keyed by attachment name with values carrying `publicPath` and optional `contentType`; live staging capture (2026-07-17) also showed a `folder` object (`{id, ownerId}`) in each value, which the scan tolerates and ignores (decode leniently: read the named fields, never reject on extras). There is no dedicated `audioFile` field and no `__attachment__` sentinel on the wire: `audioFile` is a conventional attachment *name*, and `__attachment__` markers inside the double-decoded interactive state reference entries of the map by name (the scan sets `AttachmentFile.State = true` on those entries, and that flag is the `attachment_states` view's selection predicate; `reindex` re-derives it during its store rescan the same way). The scan streams the run's records (store joined to the run's membership for whichever of answers/history exist; error only when neither does), yielding per ref the presign coordinates `(collection, source, doc_id, name)` (collection from the store type; source = the portal's firebase source; doc_id = `id` for answers, `history_id` for history: a history record's embedded `id` is the copied answer doc's id, not its own doc id, so it must never be used as the presign coordinate) plus the identity/filename tuple `(source, publicPath)`. Dedup on `(source, publicPath)`. The result line and `Download.Scanned` name the sources scanned; a missing source prints the "history not fetched; history-referenced attachments not included" stderr note.

Selectors (`--answer`, `--history`, `--question`, `--name`) filter the scan set before presigning; targeted downloads print resulting local paths. `--url` prints presigned URLs instead (single → bare URL on stdout; multiple → JSONL with `expires_at`) and writes nothing.

Presign + download: chunk refs ~100 per `POST /:id/attachments` call with body `{"attachments": [{collection, source, doc_id, name}, ...], "disposition": "attachment"|"inline"}` (server cap 500 items, live-confirmed as a whole-call 400 `BAD_REQUEST` "too many attachments (max 500)" rather than per-item errors, so the CLI's ~100-ref chunks must never exceed it; response `{"results": [...], "expires_in_seconds": 600}`, so URLs must outlive a chunk); per-item results carry exactly one of `url` or `error` (`"not_found"` | `"not_authorized"`), and errored items land in `Coverage.Missing`, never fatal (a wrong `source` also reads as `not_found`, live-observed). Files download to `attachments/<id12>_<safename>` where `id12` = first 12 hex chars of `sha256("<source>|<publicPath>")`, `.tmp` → fsync → rename. `<safename>` is the attachment name run through a deterministic cross-platform sanitizer (applied on every OS, not just Windows, so paths are portable and reproducible): `filepath.Base` first (strip any `/` or `\` path segments), then keep `[A-Za-z0-9._-]` and replace every other rune with `_`, strip trailing dots and spaces, and prefix a `_` to a Windows reserved device basename (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`, case-insensitive). This is required because the name is a client-written `attachments` map key with unconstrained content (the same Firestore-rules reality the identity-encoding finding cites): a raw `<` `>` `:` `"` `|` `?` `*` fails `os.OpenFile` on Windows, and a `:` in particular can silently write an NTFS alternate data stream so the bytes land where the manifest path does not point. `id12` already guarantees per-file uniqueness, so sanitization can never collide two distinct files. The sanitized name is what `AttachmentFile.File` records and what the `attachment_files` view exposes. Resume skips existing final files and only presigns what is missing; `--refresh` re-downloads all (its rename over an existing `attachments/<id12>_<safename>` a concurrent reader may hold open is a documented accepted race on Windows, handled by the fsutil robustio wrapper, like the CSV `--refresh` case). URLs are never persisted.

GC (also invoked by `reindex`): delete files in `attachments/` that no current record references, except files still referenced by retained history snapshots. The manifest `Attachments` index (id12, name, source, publicPath, size, referencing identities) is rebuilt here and feeds the `attachment_files` view. Accepted race from the read-only-commands-do-not-lock rule: a long-lived open `repl` session whose views were registered from an older manifest can reference a file GC has since deleted; the failure is a self-explanatory read error in that session (re-open to refresh), never data loss.

Tests: scan goldens over crafted stores (including a history ref asserting `doc_id` equals the record's `history_id`, never its embedded `id`); a filename-sanitization golden over hostile attachment names (`../etc/passwd`, `a:b`, `CON`, a name with `?*|` and trailing dots) asserting the on-disk path stays inside `attachments/`, is Windows-legal, and round-trips through `AttachmentFile.File`; selector filtering goldens (each of `--answer`/`--history`/`--question`/`--name`, plus a multi-match `--name` downloading all matches); `--url` output goldens (single ref → bare URL on stdout, multiple → JSONL with `expires_at`) with the dataset directory and manifest asserted byte-unchanged afterward; a presign-body assertion that `--inline` sends `disposition: "inline"`; chunking boundaries; partial-failure coverage; resume and `--refresh`; success-path stream discipline via the shared capture-streams helper; GC keep/delete cases including the history-retention exception; same-run exclusion (a second `get attachments` of the same run against a held per-download lock fails fast with the busy error).

---

### dataset show, dataset list, and reindex

**Summary**: The manifest-only summary surfaces with `--json`, and the recovery command that rebuilds a manifest from the filesystem.

**Files affected**:
- `cmd/dataset_show.go`, `cmd/dataset_list.go` — rendering + `--json`/`--full`
- `internal/dataset/reindex.go` — manifest rebuild + attachment GC hook

**Estimated diff size**: ~450 lines

`show`: per-type totals, per-download table (run id, type, slug, labels, fetched_at, merge counts, status), warnings from cheap stat-level drift checks (manifest file missing on disk, final-named files not in the manifest, `Complete: false` entries, downloads whose `report_type` is unknown to this binary and therefore excluded from `reports`, with the upgrade note). Coverage always renders as the split ("2,895 queried, 2,610 with data"), with "coverage unknown" when `Queried` is null. `list`: one row per dataset across all portal folders (name, description, age, counts by type, size). Neither reads data files nor starts DuckDB; `--full` adds per-download/per-file detail.

The `--json` schema is a contract (consumed by the skill's orientation step and the MCP payload-parity tests), so it is defined as Go structs rather than left to the renderer, in `internal/dataset/summary.go`:

```go
type ShowJSON struct {
    Ref         string            `json:"ref"`          // resolved <portal>/<name>
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Portal      string            `json:"portal"`       // real host (not the folder encoding)
    CreatedAt   time.Time         `json:"created_at"`
    Totals      map[string]int    `json:"totals"`       // record/row count per type: answers, history, report, attachments
    SizeBytes   int64             `json:"size_bytes"`
    Downloads   []DownloadJSON    `json:"downloads"`    // one per manifest Download; --full adds per-file detail
    Warnings    []string          `json:"warnings"`     // the drift/incomplete/unknown-type notes, machine-stable codes plus text
}
type DownloadJSON struct {
    Type        string       `json:"type"`
    RunID       int          `json:"run_id"`
    JobID       *int         `json:"job_id,omitempty"`
    Slug        string       `json:"slug,omitempty"`
    ReportType  string       `json:"report_type,omitempty"` // includes the reindex "recovered" sentinel
    FilterLabels []string    `json:"filter_labels,omitempty"`
    FetchedAt   time.Time    `json:"fetched_at"`
    Complete    bool         `json:"complete"`
    MergeCounts *MergeCounts `json:"merge_counts,omitempty"`
    Coverage    *Coverage    `json:"coverage,omitempty"`  // Queried null → "coverage unknown"
    RowCount    *int         `json:"row_count,omitempty"`
    Recovered   bool         `json:"recovered,omitempty"` // mirrors Download.Recovered (provenance rebuilt by reindex)
    Files       []string     `json:"files,omitempty"`     // --full only
}
type ListJSON struct {
    Datasets []ListRowJSON `json:"datasets"`
}
type ListRowJSON struct {
    Ref         string         `json:"ref"`
    Name        string         `json:"name"`
    Description string         `json:"description,omitempty"`
    Portal      string         `json:"portal"`
    AgeSeconds  int64          `json:"age_seconds"`
    Totals      map[string]int `json:"totals"`
    SizeBytes   int64          `json:"size_bytes"`
}
```

All fields come from the manifest (no data-file scan, no DuckDB); the MCP `dataset_show`/`dataset_list` tools return these exact structs, which the payload-parity tests assert equal the CLI `--json`.

`reindex`: runs under the per-dataset lock (non-blocking, busy error if held, per the CRUD step's locking rule); adopt the newest final-named `answers.v<N>.jsonl`/`history.v<N>.jsonl` per type and the newest final-named membership version per `(type, run)` into the `membership` map (discard `.tmp` always), count records and re-derive each adopted store's `Columns` map in the same single scan, rebuild `Downloads` from the adopted membership files + CSVs present (re-deriving CSV `row_count`, the full-file `Columns`/`CSVDialect` map, and `report_type` in one rescan of each CSV) (provenance fields that only the original fetch knew, like filters, are marked recovered-without-provenance). `report_type` recovery is only partial, because the filename encodes neither the slug nor the type: the CSV shape recovers the two determinable cases exactly (no `student_id` column → `log`, since both no-`student_id` reports are log-type; `student_id` plus the two pseudo-header rows → `answers`) and records a distinguished `recovered` value for the ambiguous remainder (`student_id`, no pseudo-header rows: usage, student-actions-with-metadata, or an unknown type are indistinguishable post hoc). `recovered` is treated as in-allowlist for the `reports` union (unfiltered, like usage/log, so a manifest-loss reindex never spuriously quarantines healthy usage/log data) but excluded from `report_prompts`, and `dataset show` flags such downloads as "type recovered without provenance; re-fetch to restore the exact `report_type`" rather than an upgrade warning. The one accepted residual: a run whose original `report_type` was a genuinely unknown (quarantined) value re-enters `reports` as `recovered` after a reindex, visibly flagged rather than silently, and a re-fetch restores the exact type; this keeps the "a new server report type never *silently* corrupts aggregates" guarantee (the flag makes it non-silent) while favoring recovery of real data over spurious exclusion, rebuild the `Attachments` index by rescanning current stores, run attachment GC, write the manifest. One documented caveat from the merge write order: run against a dataset that crashed mid-merge and has not yet resumed, `reindex` may adopt a store version newer than the membership files describe; that is drift, not data loss (per-run views under-report until the resume or a re-fetch), and the surviving segment corrects it on the next resume.

Tests: golden `--json` documents (schema-stability tests against the defined `dataset show`/`list --json` structs); drift-warning cases; reindex from a manifest-less folder reproduces holdings; a `report_type`-recovery case (a log CSV without `student_id` recovers `log`, an answers CSV recovers `answers`, a usage CSV recovers `recovered` and stays in `reports` with the recovered-without-provenance flag); purge acceptance re-checked through `show`.

---

### DuckDB engine: view registration and the sandbox

**Summary**: The `internal/duck` package: open an ephemeral in-memory DuckDB, register every view from the manifest's explicit file lists, then lock the sandbox. This step is engine-only (no CLI command yet) so the sandbox acceptance test lands with it.

**Files affected**:
- `internal/duck/engine.go` — open, register, sandbox, close
- `internal/duck/views.go` — per-view SQL builders
- `go.mod` — add `github.com/duckdb/duckdb-go/v2` (v2.10504.0, bundles DuckDB 1.5.4; versioning scheme `v2.MAJOR_MINOR_PATCH.x`; the former `marcboeker/go-duckdb` path is deprecated as of v2.5.0)

**Estimated diff size**: ~650 lines

Connection: `sql.Open("duckdb", "")` (in-memory), then a single pinned `*sql.Conn` for the session. The sandbox settings (`enable_external_access`, `allowed_directories`, `lock_configuration`) are database-scoped in DuckDB, not connection-scoped, so pooling cannot bypass them; pinning one connection is for deterministic `SET` ordering, not security.

Registration order (semantics verified with a throwaway test on DuckDB 1.3.0, 2026-07-16; the Go acceptance test re-verifies on the bundled engine, 1.5.4 at pin time):

1. `CREATE SCHEMA` per dataset when more than one `--dataset` is given (sanitized names, alias form `pre=<ref>`, hard error on collision without aliases; DuckDB's reserved/built-in schema names count as collisions and require the alias, covering datasets created before the name validator excluded them). A single dataset uses the default schema and unqualified view names.
2. Create all views (below), one at a time. Views are not lazy at CREATE: DuckDB binds each view when it is created (file existence plus schema resolution; verified) and re-binds at every query, so registration touches the dataset's own files before the sandbox engages while the post-lock allowlist still governs every read user SQL performs. A view whose bind fails degrades, never aborts: store/membership-backed views fall back to their typed empty form (schema from `Store.Columns` / the membership contract), and CSV-backed views likewise fall back to a typed empty form from the download's recorded `Columns` (the full-file detection at download records each CSV's schema in the manifest, so a missing or unreadable CSV file has a typed stand-in just like a store; the scan contributes zero rows to the `reports`/`report_prompts` unions), each with a stderr warning naming the view and file. One broken artifact costs its rows, never the session or the union's schema.
3. `SET allowed_directories = [<each dataset folder>, <each --allow-dir>]`
4. `SET enable_external_access = false`
5. `SET autoinstall_known_extensions = false; SET autoload_known_extensions = false; SET allow_community_extensions = false`
6. `SET lock_configuration = true`

After step 6 the lock is irreversible for the process (verified: unlocking, widening `allowed_directories`, and `../` traversal all fail).

**Path discipline**: every dataset folder and `--allow-dir` is canonicalized (`filepath.EvalSymlinks` + `filepath.Abs`) before use, and that same canonical form feeds both the `SET allowed_directories` list and every embedded view-path literal (the manifest's dataset-relative paths joined to the canonical folder), so prefixes are byte-identical. On the pinned engine (DuckDB 1.5.4) this is defensive consistency rather than load-bearing: 1.5.4 canonicalizes both sides of the allowlist itself (`DBConfig::SanitizeAllowedPath` runs `FileSystem::CanonicalizePath` — `realpath` on POSIX, `GetFinalPathNameByHandleW` on Windows — and normalizes separators to `/`, applied to both `AddAllowedDirectory` and every `CanAccessFile` check), so a symlinked `data_root` resolves correctly with or without the client-side step, and the client canonicalization simply keeps the recorded literals stable. The both-sides forward-slash normalization is also what makes Go's backslash-separated Windows paths match the allowlist. Crucially, the symlink boundary is the **opposite** of the earlier 1.3.0-based claim, verified empirically against real 1.3.0, 1.4.1, and 1.5.4 CLIs: on 1.5.4 an interior symlink pointing outside an allowed directory is **denied** (its real path is resolved and checked), and an allowlisted symlink alias **admits** the real path — where 1.3.0/1.4.1 did the reverse (byte-lexical, no canonicalization). So on the shipped engine the sandbox does *not* leak through interior symlinks. The residual trust boundary is narrower and still real: anyone who can write into a dataset folder can place actual files (not just links) that the researcher's queries then read as trusted, so the sensitive-data documentation still states that a dataset folder inherits the trust of whoever can write to it, but no longer claims a symlink-escape mechanism.

View SQL builders (paths embedded as escaped string literals from the manifest's explicit file lists, never globs; identifiers double-quoted; escaping is one general rule: every embedded literal, view paths and `SET` statements alike, is single-quote-doubled, since `data_root` may contain a quote even though dataset names and hostnames cannot):

- `reports`: `UNION ALL BY NAME` over per-run CSV scans, each `SELECT <run_id> AS run_id, * FROM read_csv('<file>', auto_detect=false, header=true, delim=',', quote='"', escape='"', columns={...})` with the `columns` map and dialect taken verbatim from the download's recorded `Columns`/`CSVDialect` (no sniffing: the types were detected full-file at download, so binding neither reads the file for inference nor depends on the server ordering the pseudo-header rows into a sample window), with the pseudo-header filter `WHERE student_id::VARCHAR NOT IN ('Prompt', 'Correct answer')` applied only to scans whose download entry records `report_type: answers`. The five current report shapes explain the conditional: answers = `student_id` plus the two pseudo-header rows; usage and student-actions-with-metadata = `student_id`, no such rows (usage `student_id` is BIGINT, which the cast guards); student-actions and teacher-actions = log columns with no `student_id` at all, where an unconditional filter is a binder error. Runs whose `report_type` is outside the allowlist (`answers`, `usage`, `log`, plus the reindex-only `recovered`, which is unfiltered like usage/log but flagged as recovered-without-provenance) are excluded from the union with a stderr warning naming the type and suggesting an upgrade; their per-run views remain available, unfiltered. Job CSVs excluded. Column types are the recorded detected types; `_answer` columns are VARCHAR (Prompt-row text) and the skill teaches `TRY_CAST`, per the amended requirements.
- `report_prompts`: the `answers`-type scans only (same `auto_detect=false` recorded-columns read), with the inverted filter, exposing the two pseudo-header rows as metadata.
- `answers`, `history`: `read_json('<store file>', format = 'newline_delimited', columns = {...})` over the current store version, the column map coming verbatim from the manifest's `Store.Columns` (derived from every record during the merge). No sampling-based inference: `read_json` samples only the first 20,480 records, so a column first appearing later (a late decode error, a late `attachments` map) would silently vanish from an inferred schema, and a type conflict past the sample fails the whole scan. No dedup needed by construction.
- `run_membership`: `UNION ALL BY NAME` over the manifest's current membership versions (the `membership` map, final names only), each read with the fixed CLI-owned membership `columns=` (identity fields plus `history_id`) and stamped with constant `run_id` and `type` columns; answers rows NULL-fill `history_id`.
- `downloads`: a `VALUES`-built dimension table rendered from the manifest.
- `attachment_files`: `VALUES`-built from the manifest attachment index (id12, name, source, publicPath, size, local path).
- `attachment_states`: `read_text` over the downloaded state files the attachment index marks `State: true` (the `__attachment__`-referenced offloaded state, not every `.json` attachment), exposed as a stable five-column shape: `filename`, `id12`, `name`, `content` (raw text), and `state` (`TRY_CAST(content AS JSON)`: the bytes are tool-authored and not CLI-validated, so one malformed file degrades to one NULL-`state` row, inspectable via `content`, instead of failing every `state`-touching query; verified). Deliberately NOT `read_json` schema inference: verified by throwaway test 2026-07-16 that inference over schema-divergent CODAP/SageModeler docs unions every tool's top-level keys into typed columns and bakes data-dependent object keys (e.g. node IDs) into the inferred STRUCT types, so schemas explode and diverge per file; the single JSON column with path extraction (`->>`, `from_json` + `unnest`) works under the locked sandbox and stays stable across tools and pulls.
- Per-download views: `report_<run>`, `answers_<run>` (store joined to that run's membership, type-scoped by construction), likewise `history_<run>`, and `report_<run>_job_<id>`.

**Empty inputs**: every view always registers with its full schema, even with nothing to read. Zero-input unions and empty `VALUES` sources (fresh dataset: `downloads`, `attachment_files`; no CSVs: `reports`, `report_prompts`) render as typed empty relations (`SELECT CAST(NULL AS <type>) AS <col>, ... WHERE false` over the recorded schema; zero-row `VALUES` is a parser error, verified). An absent store registers `answers`/`history` as a typed empty view (schema from the last recorded `Store.Columns`, else the contract fields alone). Membership scans always pass the fixed CLI-owned membership `columns=` (an empty JSONL otherwise infers a single `json` column, breaking `run_membership` and the per-download joins at CREATE, verified). Empty `read_text` lists are cast (`[]::VARCHAR[]`). Net rule: a fresh dataset and a zero-item fetch leave every view queryable with zero rows, never a registration error.

Tests: golden view SQL (asserting `reports`/`report_prompts`/per-run report scans emit `auto_detect=false` with the recorded `columns=`, never a bare `read_csv`); a fixture dataset queried end-to-end through the engine (one CSV per report shape: answers with pseudo-header rows, usage with numeric `student_id`, log-based without `student_id`, each with its recorded `Columns` map, plus an unknown-type run asserting `reports`-view exclusion with the warning, plus an answers CSV exceeding 20,480 data rows with a NULL-prompt numeric column, asserting the `reports` view scans and aggregates without the mid-scan conversion error the old sniff-based design would hit; and a generated store with a decode-error record and an attachments-bearing record past the 20,480th row, asserting the view scans cleanly and both columns exist via the manifest column map; a fresh-dataset fixture where every view is queryable with zero rows; and a zero-item-fetch fixture where the empty store and membership register and the per-download join returns zero rows; a broken-artifact fixture with one missing store file and one missing CSV, asserting the session opens, the store view and the CSV-backed views are both typed-empty (the CSV from its recorded `Columns`) contributing zero rows to `reports`, and the warnings name the files; an `attachment_states` fixture with one malformed `.json` file, one NULL-`state` row, view still scans); the sandbox acceptance (`read_csv` of a path outside the named datasets fails from a locked session; `--allow-dir` admits exactly the granted path; symlink behavior pinned to the bundled 1.5.4 and verified empirically: an interior symlink to an outside file is denied — 1.5.4 canonicalizes and resolves it — and an allowlisted symlink alias admits the real path, the reverse of the retired 1.3.0 behavior, with the assertions written so a future engine change flips them loudly; a symlinked `data_root` still queries via canonicalization; a `data_root` containing a single quote, escaped literals in views and SETs both parsing); the JSON reader and JSON functions working with extension autoinstall/autoload disabled on the bundled engine (verified on the 1.3.0 CLI; re-run on the bundled 1.5.4); multi-dataset schema qualification (including a legacy dataset named `main`: hard error without an alias, registers via `pre=`; a sanitization golden for an auto-generated-style name like `2026-07-16_wildfire`, the common case since default names lead with a digit and carry hyphens; and the `a-b`/`a_b` collision: hard error without aliases, resolved via `pre=`).

---

### query and repl commands

**Summary**: `cc-data query` with the four output formats and repeatable `--dataset`/`--allow-dir`, plus the interactive `repl`.

**Files affected**:
- `cmd/query.go`, `cmd/repl.go`
- `internal/duck/format.go` — table/csv/json/jsonl renderers

**Estimated diff size**: ~400 lines

`query --dataset <ref> "SELECT ..." [--format table|csv|json|jsonl]`: results go to stdout in the chosen format (default `table` via `text/tabwriter`; `csv` via `encoding/csv`; `json` an array; `jsonl` one object per row); all prose stays on stderr, and a failing query emits the single-line JSON error envelope as the only stdout output, per the stream-discipline requirement. DuckDB values map to JSON with `TIMESTAMP`/`DATE` rendered RFC3339 and `HUGEINT`/`DECIMAL` as strings when they exceed float64-safe range.

`repl`: `github.com/ergochat/readline` (the maintained near-API-compatible fork; upstream `chzyer/readline` has been frozen since 2022 with 86 open issues, several of them Windows terminal handling) with history at `~/.config/cc-data/repl_history` (0600, pre-created by the fsutil helper before readline opens it), multi-line statements terminated by `;`, `.tables`/`.schema` conveniences, same engine and sandbox as `query`. The line accumulator and dot-command handlers live behind reader/writer seams so they unit-test without a terminal. Interactive-only; the MCP `query` tool covers the capability over MCP.

Tests: format goldens including type-edge rows; error-envelope-on-stdout acceptance (`--format csv` with stderr discarded is clean CSV; failure yields exactly one JSON object); repl statement-splitting cases (multi-line, `;` inside string literals), `.tables`/`.schema` goldens against the fixture dataset, and a history-file `0600` assertion after a simulated session (verifying the fsutil pre-creation actually engages; the mode check gated `runtime.GOOS != "windows"` like the other permission assertions).

---

### Claude skill, init, uninstall, and the version-stamped lifecycle

**Summary**: The embedded Claude Code skill, the `init`/`uninstall` installer commands, and the every-invocation freshness check that replaces installer hooks.

**Files affected**:
- `internal/claude/skill.go` — embedded skill content (`go:embed skill/SKILL.md`), stamp compare/rewrite
- `internal/claude/pointer.go` — `~/.claude/CLAUDE.md` one-line pointer add/remove
- `cmd/init.go`, `cmd/uninstall.go`
- `cmd/root.go` — freshness check in `PersistentPreRun`

**Estimated diff size**: ~450 lines (skill prose included)

Skill: `~/.claude/skills/cc-data/SKILL.md` with YAML frontmatter (`name: cc-data`, description tuned for auto-invocation on researcher data questions), body per the requirements skill bullet: delegate detail to `--help`; teach the views including `run_membership` type-qualified joins, the reports-to-stores join keys, the per-run positional semantics of `res_<N>` prefixes (N is the resource's index in that run's list, so across runs the same prefix can be different activities and one activity can shift position: `res_N` reads are run-scoped, `res_<N>_name` identifies the resource, and cross-run question-level analysis uses the `answers`/`history` stores plus `report_prompts`), the `TRY_CAST` pattern for `_answer` columns, the `attachment_states` JSON extraction pattern (`state->>'$.path'`, `unnest(from_json(...))` for arrays; `state IS NULL AND content IS NOT NULL` marks a file that failed to parse), and schema-qualified multi-dataset queries; orient via `dataset show <ref> --json`; document the `NOT_AUTHENTICATED` contract (relay "run cc-data login", never drive the browser); prefer download-to-dataset over `--url`; carry the sensitive-data guidance (what Claude may auto-read). Stamped with `<!-- cc-data skill version: <binary semver> -->`.

Freshness check: on every invocation (skipped when `~/.claude/skills/cc-data/` does not exist, so non-init users never get files written), read the stamp; if the binary is newer, rewrite skill + pointer silently. Cost is one stat + small read.

`init`: write skill + pointer, print what was written, then prompt for `login` (skippable). `uninstall`: remove skill + pointer; `--credentials` (or interactive prompt) first revokes each portal's token via the logout path, then deletes locally, warning with the token-UI URL on revoke failure; always prints where datasets remain. README documents `brew uninstall` removes only the binary.

Tests: stamp compare matrix (older/equal/newer/missing); pointer idempotency (add twice, remove); uninstall ordering with a fake server (revoke called before local delete; failure still deletes with warning).

---

### MCP server

**Summary**: `cc-data mcp`, a stdio MCP server exposing the pinned data-and-analysis tool surface with honest annotations and the server-enforced `confirm: true` guard.

**Files affected**:
- `internal/mcpserver/server.go` — server assembly, tool registration
- `internal/mcpserver/tools.go` — per-tool handlers delegating to the shared command cores
- `cmd/mcp.go` — `mcp` command with `--allow-dir` launch flag
- `go.mod` — add `github.com/modelcontextprotocol/go-sdk` (official SDK, GA v1.6.x, stdio transport, typed input schemas)

**Estimated diff size**: ~500 lines

Tools, exactly the requirements surface: `auth_status`, `version`, `reports_list`, `reports_jobs`, `get_report`, `get_answers`, `get_history`, `get_attachments`, `dataset_create`, `dataset_list`, `dataset_show`, `dataset_rename`, `dataset_edit`, `dataset_delete`, `dataset_purge`, `dataset_reindex`, `query`. `auth_status` exposes a `check` boolean argument (default `false`, the offline render): when `true` it runs the per-portal network introspection the CLI's `--check` does, so the model opts into the network call deliberately rather than every `auth_status` call touching each portal's server. `version` takes no arguments and needs no credentials. Handlers call the same internal command cores as the CLI (no self-exec subprocess) and return the `--json` payloads verbatim, so CLI and MCP can never drift. Annotations: `readOnlyHint` on listings/show/query/status/version; `destructiveHint` on `dataset_delete`/`dataset_purge`, which also reject calls without a literal `confirm: true` argument in the handler (the MCP mirror of `--force`, enforced server-side because host confirmation is only a SHOULD). Long-running fetch tools (`get_report`, `get_answers`, `get_history`, `get_attachments`) forward the handler's `context.Context` into the fetch core, making poll/page/chunk loops cancellable, and emit MCP progress notifications (`ServerSession.NotifyProgress`, verified in the SDK along with context cancellation) per poll/page/chunk whenever the client sent a progress token, mirroring the CLI's stderr progress from one shared code path. A cancelled fetch exits cleanly mid-download and leaves the normal resumable segment state. The `query` tool takes `max_rows` (default 1,000): rows beyond it are dropped and the payload carries `"truncated": true` with the total row count, and the tool description tells the model to refine the query or raise `max_rows` deliberately.

Tool arguments are an enumerated, curated subset of each command's CLI flags, not a mirror: flags that mint capabilities or widen the sandbox are excluded by design, with the exclusion and its reason stated in the tool description. Concretely: `get_attachments` is download-to-dataset only (no `url`/`inline` arguments; a presigned URL returned to the model would put a credential-free capability to a student's file into persistent conversation context, and the model reaches downloaded content through the `attachment_files` view's local paths instead), and the `query` tool takes a `datasets` list and no allow-dir argument; the allowlist extends over MCP only via `cc-data mcp --allow-dir` launch args in the client config. Excluded, with reasons documented in the tool listing source: `login`/`logout` (credential management is a terminal act), `repl` (interactive), `mcp` (recursive), `init`/`uninstall` (host-machine installer acts).

Tests: in-process client over the SDK's transport pair; guard rejection without `confirm`; annotation snapshot; argument-schema snapshot asserting the excluded arguments (`url`, `inline`, allow-dir) do not exist on any tool; cancellation mid-page leaves resumable segment state; `query` truncation-marker golden; payload-parity tests asserting tool output equals the CLI `--json` output for the same fixture dataset.

---

### Sensitive-data documentation and help polish

**Summary**: Subtask (f): align README, skill, and `--help` on the sensitive-data classification, retention guidance, and the exit-code/stream contract.

**Files affected**:
- `README.md` — retention/purge guidance, `0600`/ACL reality, Claude auto-read boundary, `--url` caveat, the dataset-folder trust boundary (anyone who can write into a dataset folder can plant files the researcher's queries read as trusted; the bundled DuckDB resolves symlinks, so this is a writable-files boundary, not a symlink-escape), the `--server` trust boundary (a server origin receives your login and token; the CLI enforces the concord.org/concordqa.org/loopback allowlist), uninstall notes
- `cmd/*.go` — long help text per command; root help carries the exit-code table and stream discipline

**Estimated diff size**: ~250 lines

The README already carries most of this (verified against the current file); this step reconciles it with the final command surface, adds the documented exit-code table to `cc-data --help`, and cross-checks the skill text so all three sources state the same classification and boundaries, including the `--token -` stdin recommendation for manual token pastes. Acceptance: the exit-code table appears in `--help` output and matches `internal/output` constants (asserted by a test that renders help and greps the table).

---

### Release pipeline: goreleaser, CI matrix, signing, Homebrew tap

**Summary**: Subtask (g): tagged builds produce signed/notarized macOS (arm64 + amd64) and Linux amd64 artifacts plus a Homebrew formula in `concord-consortium/homebrew-tap`; every push builds and tests all five targets.

**Files affected**:
- `.goreleaser.base.yaml` + `scripts/gen-goreleaser.sh` (renders the three thin per-platform configs), `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- `scripts/notarize.sh` — codesign + notarytool wrapper with the unsigned dev-build fallback

**Estimated diff size**: ~400 lines

First action of this step, per the requirements' external-dependency note: confirm the Apple Developer ID Application certificate and App Store Connect API key exist (or create them; Admin/Account Holder access required) and land them as GitHub Actions secrets before any pipeline work starts.

CI (every push): matrix over `ubuntu-24.04` (amd64), `ubuntu-24.04-arm` (arm64), `macos-15` (arm64), `macos-15-intel` (amd64), `windows-2022` (amd64); each runs `go build ./...` and `go test ./...` (cgo + bundled DuckDB static libs, no cross-compilation). Runner-label maintenance note (verified against actions/runner-images 2026-07-16: `macos-13` is already retired; Intel images now carry `-intel` labels and macOS 14 is deprecation-badged): GitHub supports only the last two macOS versions, so the macOS labels here are expected to be bumped routinely. If Intel macOS images sunset entirely, the documented fallback is building both darwin arches on the arm64 runner via same-OS cross-arch compilation (`GOARCH=amd64 CGO_ENABLED=1` with Apple clang `-arch x86_64`; go-duckdb ships the darwin/amd64 static lib) and signing/notarizing both there, at the cost of no longer running `go test` on real amd64 hardware.

Release (on tag), per the resolved orchestration question (free goreleaser + scripted assembly; split/merge is Pro-only, verified 2026-07-16): each release-matrix job (macos-15 arm64, macos-15-intel amd64, ubuntu-24.04) runs free `goreleaser release --skip=publish` against its own thin per-platform config, rendered in CI from `.goreleaser.base.yaml` by `scripts/gen-goreleaser.sh` filling in the single `builds.goos`/`goarch` block (`goreleaser release` has no free single-target mode; that is exactly the Pro split/merge feature, so the shared-config-per-runner shape would build all targets on every runner and fail the cgo cross-links), producing the build, archive, and per-file checksum (`checksum: {split: true}`, a free feature) for its own platform, with the macOS jobs signing and notarizing via `scripts/notarize.sh`, which runs as a goreleaser build post-hook in the per-platform config, before archiving (the ordering is load-bearing: sign-after-archive ships an unsigned binary inside a signed-looking release): `codesign --options runtime --timestamp` with the Developer ID Application certificate → zip a submission copy (`notarytool` accepts only ZIP/DMG/PKG, never a tar.gz or bare binary) → `notarytool submit --wait` with the App Store Connect API key → discard the zip. No stapling: a ticket cannot be stapled to a standalone Mach-O (stapler error 73), so the shipped binary is notarized-but-unstapled, the standard state for CLI tools (brew's curl downloads carry no quarantine so Gatekeeper never assesses; a direct browser download does an online Gatekeeper check on first run, documented in the README). `notarytool submit --wait` adds minutes of wall time per macOS job. Both credentials are GitHub Actions secrets; the script degrades to unsigned dev-build mode with a loud log line when they are absent, but the release workflow fails the tag if signing was skipped. Each job uploads its archives as workflow artifacts; a final publish job downloads them all, verifies checksums and that the signed path ran, creates the GitHub release with `gh release create`, and renders the Homebrew formula from a ~40-line template (per-platform URLs + sha256) pushed to `concord-consortium/homebrew-tap` (created during this step; verified today no tap repo exists yet) with a repo-scoped push token secret. Install path: `brew install concord-consortium/tap/cc-data`. The formula template and artifact naming live side by side in the repo with a test asserting they agree.

Definition of done, from requirements: a tagged build produces the signed/notarized macOS + Linux amd64 artifacts and the formula installs them.

---

## Open Questions

### RESOLVED: How should the multi-runner release be orchestrated: goreleaser Pro, free goreleaser + scripted assembly, or single-runner zig cross-compile?
**Context**: cgo/DuckDB forces native builds per platform, but one tagged release needs artifacts from three runners plus one Homebrew formula. goreleaser's split/merge feature that solves exactly this is Pro (paid; verified against the goreleaser docs 2026-07-16). The requirements name "goreleaser + GitHub Actions building on native runners" and "zig cc is the fallback approach" without settling orchestration.
**Options considered**:
- A) goreleaser Pro with split/merge: cleanest pipeline, one config, adds a paid license dependency for the org.
- B) Free goreleaser per-runner in single-target mode, then a final publish job assembling the GitHub release and rendering the formula from a small template script: all free, ~100 lines of bespoke release scripting to own.
- C) Single Linux runner cross-compiling all targets with `zig cc` and signing/notarizing mach-o binaries with `quill`: one job, all free, but the most unproven toolchain risk (cgo + DuckDB static libs under zig is undocumented upstream, verified against the go-duckdb README; notarization without Xcode tooling), and it inverts the requirements' stated native-runners-primary approach.

**Decision**: B. No procurement dependency, no unproven toolchain on the story's critical path (the signed macOS artifacts), and the bespoke half is small, boring, inspectable scripting (download artifacts, `gh release create`, render a formula template). The release-pipeline step was updated to describe the B pipeline concretely.
*(Amended by round-2 review: `goreleaser release` has no free single-target flag, that being the Pro split/merge feature, so B is realized as thin per-platform configs rendered from one base config; see the release step.)*

### RESOLVED: What is the production report server URL, and is one `server_url` config default enough?
**Context**: The requirements and design doc key everything by portal but never state how the CLI locates the report server itself (a separate origin serving multiple portals). The config step specs a `server_url` config field with a built-in production default plus a `--server` login flag for dev, and records the minting server per portal in the credentials metadata. The built-in default's value needed confirming, and the single-default assumption breaks if different portals are ever served by different report servers.
**Options considered**:
- A) Single built-in production default + config override + `--server` on login; per-portal server recorded at login time. Matches the one-multi-portal-server reality today.
- B) A per-portal `servers` map in config with no built-in default; `login` requires `--server` the first time. More explicit, more friction.

**Decision**: A, with the constant `https://report-server.concord.org`. Verified 2026-07-16: that host is live and is the `PHX_HOST` default in report-service `runtime.exs`; staging is `https://report-server.concordqa.org` (CloudFormation `report-server` stack in the QA account), documented as the `--server` example; one deployment serves all portals via per-portal DB connections, so the multi-server scenario B guards against does not exist today, and A's per-portal `Server` credentials field already carries the association after login if it ever does. Also verified: neither deployed environment serves the v1 API yet (both 404 on `/api/v1/reports` and `/auth/cli`), recorded as a rollout note in the auth integration step.

### RESOLVED: Does subtask (a) include a live end-to-end PKCE test against a locally running report-service, or is a fake-server integration test enough?
**Context**: The auth implementation will carry an `httptest` fake-server integration test of the full loopback exchange either way (cheap, runs in CI). The landed server behavior was code-verified, but only a live handshake proves the two implementations agree (param encoding, redirect timing, browser behavior). A live test means standing up the Phoenix dev server locally and driving the browser step with Playwright; it would be a documented dev-machine test, not CI.
**Options considered**:
- A) Fake-server integration test only; the first live verification is manual dogfooding against the staging portal when subtask (a) lands.
- B) A plus a scripted Playwright check (documented in the repo, run on demand) against a local report-service; part of subtask (a)'s definition of done.

**Decision**: B. Deciding facts (verified 2026-07-16): report-service's `dev.exs` already authenticates a local server against the staging portal with the existing `research-report-server` OAuth client, so the expensive prerequisite is just the standard report-service dev environment; and neither deployed server carries the CLI-support code yet, so this script is the only way to prove the real handshake before a server deploy enters the debugging loop. Scoped as "script exists, is documented, and has passed once on a dev machine", never a CI gate. The auth integration step was updated with the script's shape and prerequisites.

## Self-Review

### Senior Engineer

#### RESOLVED: The `mergeCompact` sketch uses a `counts.Total` field that `MergeCounts` does not define
The merge pseudocode sets the manifest store pointer with `Count: counts.Total`, but the `MergeCounts` struct (and the manifest/result-line schema it feeds) defines only `fetched`/`new`/`updated`/`removed`. The merged-store record total is a real output of `streamMerge` that has to live somewhere; as written the plan's own code cannot compile and the schema question (is the total part of merge counts or separate?) is silently unanswered.

**Resolution**: `streamMerge` returns `(MergeCounts, total int, err)`; the result-line/merge-counts schema stays exactly as the requirements define it, and `Store.Count` takes the separate total. The sketch and the merge-counts paragraph were updated with the distinction (merge counts describe the download; the store total describes the store).
*(Amended by round-3 review: the signature also carries the derived column map, `(MergeCounts, total int, cols map[string]string, err error)`, so the repoint can persist `Store.Columns`; see the round-3 Go findings.)*

---

### Security Engineer

#### RESOLVED: `login --token <paste>` puts the secret in shell history and process lists
The manual fallback takes the raw `ccd_` token as a flag value, so it lands in `~/.bash_history` and is momentarily visible in `ps`. The requirements treat these tokens as long-lived credentials whose safety story is visibility+revocation; leaking them into history undermines that quietly.

**Resolution**: `--token -` reads the token from stdin (piped for scripts, echo-off prompt on a TTY), the help text recommends that form, and the bare `--token <value>` form remains for compatibility but is documented as discouraged with the reason stated. Login step, its tests, and the sensitive-data documentation step updated so README, skill, and `--help` agree.

#### RESOLVED: The MCP `get_attachments` tool must not expose the `--url` mode
The CLI's `--url` prints presigned URLs, which the requirements call "a short-lived, credential-free capability to one student's file" and tell Claude to avoid. The MCP step pinned the tool surface but never said whether `get_attachments` accepts a `url` argument; if it did, the model could mint capability URLs into its own context, and a Claude Desktop transcript is exactly the persistent channel the requirements' caveat warns about. No capability is lost: download-to-dataset serves the analysis purpose inside the dataset boundary, and a human who wants a URL still has the terminal.

**Resolution**: generalized into a stated principle in the MCP step: tool arguments are a curated, enumerated subset of CLI flags, and flags that mint capabilities or widen the sandbox (`url`, `inline`, allow-dir) are excluded by design with the reason in the tool description. An argument-schema snapshot test asserts the exclusions.

---

### QA Engineer

#### RESOLVED: Mutating dataset commands do not state their locking behavior
`rename`, `delete`, `purge`, and `reindex` mutate the dataset folder while a concurrent fetch may hold the per-dataset lock mid-merge; `rename` in particular changes the path under a merge's feet. The CRUD step never mentioned the lock.

**Resolution**: every mutating dataset command (`rename`, `edit`, `delete`, `purge`, `reindex`) acquires the per-dataset lock non-blocking and fails fast with exit 1 and "dataset is busy: another cc-data command is writing to it". Read-only commands (`show`, `list`, `query`, `repl`) deliberately do not lock: they read only manifest-current, complete artifacts (safe under the atomic-rename discipline), and blocking analysis during a long fetch would be a regression. Mutate-under-fetch test matrix added.

---

### Data/Storage Engineer

#### RESOLVED: The merge's durable-write order across store rename, membership, manifest, and segment removal is unspecified
The merge step specified each artifact's atomicity but not their sequence, and each crash window between them leaves a different skew (store renamed but membership stale; membership new but store old; both new but manifest old). `reindex` "adopts the newest final-named store", which can adopt a store whose membership files predate it. None of these lose records if the segment still exists, but the plan needed to pin the order and the recovery story per window.

**Resolution**: order pinned as store rename → membership write → manifest repoint → segment+cursor removal, with the stated invariant that the segment is deleted last and only after all three writes succeed, so every crash window converges via the idempotent resume re-merge; readers are protected because queries resolve stores through the manifest. The reindex step carries the one honest caveat (pre-resume reindex can adopt a store newer than its membership: drift, not data loss, corrected on resume). Crash-injection tests added at every write boundary.

#### RESOLVED: `attachment_states` via `read_json` schema inference will fight real CODAP/SageModeler documents
Offloaded state files are large, deeply nested, and schema-divergent across tools; `read_json` with `union_by_name` over them risks column explosion, inference failure, or per-file type conflicts, and the view exists mainly so researchers can poke into the JSON anyway. Confirmed by throwaway DuckDB test (2026-07-16): inference on two toy CODAP/SageModeler-shaped files unioned every top-level key into deep typed STRUCT columns and baked a data value (a node ID used as an object key) into the column type, so real documents guarantee per-file and per-pull schema divergence; meanwhile `read_text` + `content::JSON` with `->>`/`from_json`/`unnest` extraction worked fully under the locked sandbox, also proving the JSON functions are statically available with autoload disabled.

**Resolution**: the view is `read_text`-based with the stable shape (`filename`, `id12`, `name`, raw `content`, and `state = TRY_CAST(content AS JSON)` — five columns; see the engine step); the engine step records the verified rationale, its test list gains the JSON-functions-under-sandbox check on the bundled engine, and the skill teaches the extraction pattern.
*(Amended by round-3 review: the shape is the five columns above, not the earlier four-column `filename/id12/name/state`; the `content`/`TRY_CAST` split is what keeps one malformed file from poisoning the view. The requirements views bullet, which still said `read_json`, was corrected to this shape at the same time.)*

---

### DevOps Engineer

#### RESOLVED: The release matrix depends on the `macos-13` Intel runner, which GitHub is retiring
darwin/amd64 builds were assigned to `macos-13`; GitHub has been phasing out Intel macOS runners, and a matrix pinned to one is a time bomb for a pipeline whose definition of done includes signed amd64 artifacts. Verification (actions/runner-images, 2026-07-16) showed it worse than filed: `macos-13` is already retired and the plan would have failed on first push; Intel images now live under `-intel` labels (`macos-15-intel`, `macos-26-intel`) with macOS 14 already deprecation-badged.

**Resolution**: both matrices updated to `macos-15` (arm64) + `macos-15-intel` (amd64), with a maintenance note that the last-two-versions policy makes label bumps routine, and the same-OS cross-arch fallback (both darwin arches built, signed, and notarized on the arm64 runner) documented for the day Intel images sunset entirely; native Intel stays preferred while it exists because it also runs `go test` on real amd64 hardware.

---

### LLM-Agent UX

#### RESOLVED: Long-running MCP tools need progress and cancellation, and the MCP `query` tool needs a row cap
`get_report` can poll for minutes and `get_answers`/`get_history` page through large exports; over MCP these are silent until done unless the tools emit progress notifications and honor client cancellation. Separately, the MCP `query` tool returned result rows into model context with no bound; one broad `SELECT` floods the conversation with student data (the same transcript-boundary logic as the `--url` exclusion). Verified in the official SDK: `ServerSession.NotifyProgress` + progress tokens, and client cancellation propagating as `context.Context` cancellation into tool handlers.

**Resolution**: fetch tools thread the handler context into the fetch core (cancel-safe by construction: interruption leaves the normal resumable segment state) and emit progress per poll/page/chunk when a progress token is present; the `query` tool gains `max_rows` (default 1,000) with an explicit `"truncated": true` marker, total row count, and description guidance to refine or raise deliberately. MCP step and tests updated.

---

### Concurrency/Storage Engineer (round 2, code-verified 2026-07-17)

#### RESOLVED: `WriteMembership` failure is ignored mid-critical-section; a later merge then silently deletes a completed fetch's records
**Severity: high.** In the `mergeCompact` sketch every durable write checks its error except `ds.WriteMembership(...)`, which is called as a bare statement between the store rename and the manifest repoint. If it fails (disk full; or on Windows, rename-over-open-file while a concurrent query holds the membership file, the exact hazard the design doc versions stores to avoid), execution proceeds to `WriteManifest` and `seg.Remove()`. Result on disk: the new store contains the fetch's new identities, the stale membership file does not list them, and the segment (the only other copy) is gone; the next merge of any run computes membership union, finds those identities covered by nothing, and drops them as `removed`. A `complete: true` fetch's records vanish with no error near the fetch that loses them.

**Resolution**: the sketch now checks `WriteMembership`'s error and returns before the manifest repoint, so the manifest keeps naming the old store/membership pairing and the surviving segment makes resume re-run the merge (renaming over the orphan final-named `v(N+1)`). The crash-safety paragraph names the abort path, and the test list gains a fault-injection case for a failing membership write.

#### RESOLVED: No mutual exclusion for two commands fetching the same `(type, run)`; membership replace semantics turn the race into data loss
**Severity: high.** Segments and cursors live at fixed paths and nothing acquires per-download exclusivity; the MCP server makes same-run concurrency realistic (terminal command plus Claude tool call). Concrete loss paths: two plain fetches of run 584 clobber each other's cursor and one removes the segment mid-append of the other; a merge running against a partial or vanished segment replaces run 584's membership wholesale with that subset, dropping every run-only record outside it as `removed` (the shrunk-refresh code path firing on data that did not shrink); two concurrent resumes of a finished-but-unmerged segment let the second merge an empty identity set, dropping all of the run's records. The existing acceptance only covers different runs.

**Resolution**: a per-download lock, added to the segment lifecycle: every `get` of a `(type, run)` holds an exclusive non-blocking flock on a dedicated `seg_<type>_<run>.lock` file (never the cursor, which is replaced by rename) for the command's lifetime, failing fast with "download busy"; `--refresh` and the `EXPIRED_CURSOR` restart delete segment + cursor only while holding it. The requirements merge-serialization bullet carries the matching sentence and a same-run acceptance criterion, and the test list gains the same-run exclusion case.

#### RESOLVED: The per-dataset lock's in-process semantics are unpinned; the goroutine acceptance test can pass vacuously and the MCP server can lose records in production *(with QA Engineer)*
**Severity: high.** Verified in gofrs/flock source: `Lock()` short-circuits (`if *locked { return nil }`) before any syscall when the same `Flock` instance is already held, while separate instances (separate fds) do contend even within one process because flock(2) attaches to the open file description. The spec never says whether `ds.Lock()` mints a fresh instance. Two consequences: (1) the specced "two goroutines merging different runs" acceptance test is vacuous with a cached per-`Dataset` instance, and the same shape in the long-lived MCP server process lets two concurrent merges both pass the version check, both rename `v(N+1)`, and lose the first fetch's records in production; (2) the inverse trap: if inner helpers (`WriteManifest`, cursor writes) self-lock with a fresh instance per the requirements sentence "only cursor/manifest writes and the merge hold the lock", two fds on the same path block each other and every merge self-deadlocks.

**Resolution**: a "Lock semantics" paragraph in the merge-engine step pins the rule: each lock (per-dataset and per-download) is one process-wide guard per path, a `sync.Mutex` layered over a single flock, acquired exactly once per critical section; inner helpers never re-acquire and assert the guard is held. The concurrency acceptance now runs as two separate processes (test re-exec against a shared `CC_DATA_ROOT`) and an in-process regression asserts two goroutine acquisitions actually block.

#### RESOLVED: Three documents disagree on when membership is written, and the reader-consistency claim is false for re-fetches
**Severity: medium.** `mergeCompact` writes membership inside the merge after the store rename; the get answers/history step says "write membership, run merge-compact" (before); the design doc says "rename the membership file into place, then merge-compact" (before). Separately, membership files are unversioned, so on a re-fetch the same path is replaced before the manifest repoint: a concurrent `query`/`repl` session registered from the old manifest lazily reads new membership joined to the old store (shrunk refresh under-reports; grown fetch yields membership rows with no store row), and after a crash in that window the skew persists until resume. The unversioned rename also has the Windows rename-over-open-file exposure that feeds the `WriteMembership` finding above.

**Resolution**: the merge-internal order is now stated consistently in the fetch step and the design doc (the merge writes the membership version whole from the segment's identity set inside its critical section; a consistency re-pass removed a leftover per-page-scratch description from the design doc), and membership files are versioned like stores: `members_<type>_<run>.v<N>.jsonl`, current version carried in a new manifest `membership` map that the same manifest write repoints alongside the store pointer. `run_membership` and `MembershipUnion` read the manifest-named versions, cleanup and `reindex` handle old membership versions by the same final-name rule, `purge` clears the map, and the reader-protection sentence now states the restored (true) claim. Requirements naming, write-path, reader, and reindex bullets updated to match.

#### RESOLVED: In-memory segment sort contradicts the design doc's memory bound and is unbounded for history runs
**Severity: medium.** The merge step says the segment "is sorted in memory at merge time; a single run's pages are the natural working-set bound", but the design doc promises "the merge streams with memory bounded by the identity sets, not record sizes". A history segment is one run's entire snapshot series; the server pages by byte budget (8 MiB per page, items up to the 1 MiB Firestore doc limit), so classroom-scale history exports reach hundreds of MB to GBs, inflated further by at-least-once resume duplicates. The failure presents as OOM at the very end of a long fetch, and since the segment survives, retrying dies the same way.

**Resolution**: the merge step now sorts only the segment's `(identity key, _fetched_at, _run_id, byte offset)` tuples in memory (the identity-set bound the design doc already states) and streams records back from the segment by offset in sorted order, never materializing the record set; the trade-off (random reads within one file) is stated, and the test list gains a large-segment memory case asserting the working set is tuples plus a single-record buffer. No design-doc change needed; the implementation now satisfies its stated bound.

#### RESOLVED: The identity-key premise "server-issued identifiers that never contain control bytes" is not a server guarantee
**Severity: low.** The identity fields come from client-written Firestore docs: `firestore.rules` allows any authenticated learner to create/update answer and history docs with only ownership fields validated, `question_id` content is unconstrained, `history_id` is a client-choosable doc id, and the bulk endpoints pass values through verbatim. A crafted `question_id` containing 0x1F breaks key injectivity, so the merge treats distinct records as one identity and silently swallows one.

**Resolution**: keys now encode length-prefixed (`<decimal byte length>:<raw bytes>` per field, concatenated), injective for arbitrary field contents with a stated total byte order; the false premise is replaced with the actual client-written-data reality, the paragraph notes the encoding never leaves memory (confirmed user-invisible: stores, membership files, and query surfaces carry raw fields), and identity-encoding goldens with hostile field contents lead the test list.

#### RESOLVED: Merge counts are not crash-idempotent even though store content is
**Severity: low.** A crash between the manifest repoint and segment removal makes resume re-run the merge against the already-merged store, recording `new=0, updated=all, removed=0` into the Download entry and result line, misstating a first fetch as pure overlap; symmetrically, after a crash in the rename-to-membership window of a shrunk `--refresh`, the next run's merge claims removals it did not cause. The requirements' "honest merge counts" acceptance cannot hold across these windows as designed.

**Resolution**: the cursor file gains `merged_as`, written as durable write #4 immediately after the repoint (safe against false positives: versions only grow and the field is written only after this merge's own repoint landed); resume short-circuits on it with an already-merged zero-count result line, exit 0. The crash-safety paragraph names the two accepted residual skews (the instant between repoint and `merged_as`; a crashed shrunk refresh's removals attributed to the next merge), and the crash-injection tests now assert count convergence from the `merged_as` write onward.

#### RESOLVED: `--refresh` and finished-ness detection are unspecified for `get answers`/`get history`
**Severity: low.** The step defines resume and the `EXPIRED_CURSOR` restart but never mentions `--refresh` (the headline never-duplicate acceptance depends on it, and the design doc defines it), and "a finished segment with no merge recorded re-runs the merge only" never states the detection rule.

**Resolution**: the fetch step now pins finished-ness (cursor present with `next_page_token` null; segment with no cursor is stale and discarded), defines `--refresh` (delete segment + cursor under the per-download lock, restart from a null cursor, membership-replace consequences being the merge's normal semantics), and the test list gains the `--refresh` restart and stale-segment discard cases.

---

### DuckDB Query Engineer (round 2, verified by test on DuckDB 1.3.0; re-verify on the bundled engine)

#### RESOLVED: The pseudo-header filter hard-errors the `reports` view when any usage-type CSV is present
**Severity: high.** A usage CSV has no Prompt row, so `student_id` sniffs BIGINT and the filter literal comparison fails at scan time: `WHERE student_id NOT IN ('Prompt','Correct answer')` raises `Conversion Error: Could not convert string 'Prompt' to INT64` (reproduced). `DESCRIBE` succeeds (the union unifies to VARCHAR) but every row-producing query over `reports` fails as soon as a dataset mixes answers and usage runs, and the requirements claim "usage-type CSVs ... are unaffected by the filter" is false as written. Verified fix: `student_id::VARCHAR NOT IN (...)`.

**Resolution** (amended during processing; verification against the Athena query generation showed the filed finding understated the problem, and three user decisions shaped the fix): the five report shapes are answers (`student_id` + pseudo-header rows), usage and student-actions-with-metadata (`student_id`, no rows), and student-actions/teacher-actions (log columns, no `student_id` at all), so the unconditional filter was a binder error for the last two, not just a conversion error, and a cast alone could not fix it. The fix is metadata-driven with an allowlist: `report_type` (`answers` | `usage` | `log`) becomes the fourth owed server dependency on run metadata (specced via an amendment to report-service's closed `specs/REPORT-77-cli-server-support.md`), recorded per download with slug-derived fallback against older servers; the builders filter (cast defensively) only `answers`-type scans; `report_prompts` unions answers-type scans only; and types outside the allowlist are quarantined: downloaded and recorded verbatim, excluded from `reports`/`report_prompts` with an upgrade warning (fetch stderr and `dataset show`), per-run views available unfiltered, so a future server report type can never silently corrupt aggregates. Requirements (Background, fetch bullet, views bullet, Technical Notes owed list plus its stale spec path, generation-readiness note), the get report step, the engine step and its fixtures, `dataset show` warnings, and the design doc all updated.

#### RESOLVED: `read_json`'s 20,480-record sampling breaks the `_decode_error` mixed-type design and silently drops late-appearing columns
**Severity: high.** `read_json` infers schema from the first 20,480 records. A raw-string `report_state` (the `_decode_error` path) past that point fails the entire view scan (`Expected OBJECT, but got VARCHAR`, reproduced at line 20,601); scalar type drift (`answer` numeric early, string late) fails the same way; and columns first appearing past the sample (`_decode_error` itself, or `attachments` when the first attachment-bearing record is late) are silently absent from the schema, so `WHERE _decode_error` is a binder error. The spec's "never dropping data" promise inverts at query time into dropping the view.

**Resolution** (one refinement over the approved sketch: DuckDB's `columns` parameter is all-or-nothing, with no partial-plus-sniffed mode, so "explicit contract columns, rest sniffed" is not implementable as such): both fixes land, composed. (1) Decode failures never mix types: the object field stays null and the raw string goes to a sibling `report_state_raw`/`interactive_state_raw` field with `_decode_error` set. (2) The merge derives the store's complete DuckDB column map while streaming (contract fields pinned; all other fields widened over every observed value: BIGINT/DOUBLE/BOOLEAN/VARCHAR, object/array/mixed → JSON), records it in the manifest's new `Store.Columns`, and the view builders pass it verbatim as `read_json(..., columns = {...})`, eliminating sampling-based inference; `reindex` re-derives the map in its adoption scan. Fetch-step goldens and an over-sample-boundary engine fixture added; the requirements views bullet updated.

#### RESOLVED: Zero-holdings and zero-item edge cases crash view registration, and registration is all-or-nothing
**Severity: high.** Reproduced: zero-row `VALUES` is a parser error (a fresh dataset has no renderable `downloads`/`attachment_files` view); `UNION ALL BY NAME` over zero scans has no SQL form; `read_json` on a missing store file fails at CREATE VIEW (an answers-only dataset kills registration of `history`); an empty JSONL file infers a single `json` column, giving `run_membership` a phantom column and making the per-download join fail at CREATE (`Column "source_key" does not exist on right side of join!`); `read_text([])` needs a typed cast. One zero-item fetch or a fresh dataset makes the whole dataset unqueryable. Verified fixes: `columns={...}` on membership/store scans makes empty files return typed zero rows and the join register; typed zero-row views via `SELECT CAST(NULL AS ...) ... WHERE false` cover the empty-`VALUES` cases.

**Resolution**: the engine step gains an explicit "Empty inputs" rule: every view always registers with its full schema; zero-input unions and empty `VALUES` sources render as typed empty relations; absent stores register as typed empty views (schema from `Store.Columns` when known, else the contract fields); membership scans always pass the fixed CLI-owned `columns=`; empty `read_text` lists are cast. Net rule stated in the spec: a fresh dataset and a zero-item fetch leave every view queryable with zero rows, never a registration error. Fresh-dataset and zero-item-fetch fixtures added to the engine tests.

#### RESOLVED: "Views are lazy; nothing is read yet" is false: CREATE VIEW binds and reads the files
**Severity: medium.** Reproduced: CREATE VIEW over a missing file fails immediately, and a view whose file disappears fails again at SELECT, so views bind at CREATE and re-bind per query. Registration therefore opens and schema-sniffs every file named by every view before the first query, which (a) makes any drift (the documented reindex caveat, the GC-under-open-repl race) fail the whole session at startup rather than one view, (b) scales registration cost with holdings, and (c) means files are read during step 2, before `allowed_directories` is set (the dataset's own files, and re-bind at query time keeps the allowlist load-bearing, so the security claim survives; the requirements' "views are lazy" wording needs the same correction).

**Resolution** (refined during processing): registration step 2 now states the bind-at-CREATE/re-bind-at-query truth and the degradation rule: per-view bind errors warn on stderr naming the view and file; store/membership-backed views fall back to typed empty form (`Store.Columns` / the membership contract), while CSV-backed views are omitted from registration and the unions, because a report CSV's schema is question-dependent (per-run positional `res_<N>` columns) and recorded nowhere, so no typed stand-in exists. The requirements sandbox bullet is reworded to the true claim (views re-bind at query time, so the allowlist governs every read user SQL performs), and a broken-artifact fixture asserts one lost file costs one view, never the session.

#### RESOLVED: `res_<N>` report columns are positional per run; cross-run reads of them silently mix activities *(raised by user review during round-2 processing)*
**Severity: medium (teaching/documentation).** Verified in `shared_queries.ex` (`Enum.with_index(1)`): `res_<N>` is the resource's 1-based position within that run's resource list. In a dataset mixing runs of different activities, `union_by_name` keeps rows correct (each row carries its own run's values plus `run_id`, so the taught reports-to-stores join stays row-correct), but selecting or aggregating `res_1_*` across runs mixes activities under one column name, and the same activity can occupy different prefixes across runs, so cross-run reads of `res_N` columns produce plausible-looking wrong results. Restricting datasets to one activity was considered and rejected: it would defeat the combine-classes-and-activities purpose and would not even fix position drift between same-activity runs.

**Resolution**: taught, not restricted. The skill (implementation and requirements bullets) and the design doc's union claim now state the positional semantics: `res_N` reads are run-scoped (per-run views or `WHERE run_id`, with `res_<N>_name` identifying the resource), and cross-run question-level analysis belongs on the `answers`/`history` stores plus `report_prompts`, which key by position-independent `remote_endpoint`/`question_id`.

#### RESOLVED: `allowed_directories` is lexical prefix matching: symlinks inside an allowed directory escape the locked sandbox
**Severity: medium.** Reproduced under the full locked sandbox: `read_csv` through a symlinked file or symlinked directory inside the allowlisted dataset folder reads an outside file; conversely an allowlisted symlink alias denies the real path (no canonicalization on either side). The official securing docs promise nothing about symlinks. The CLI never writes symlinks, so escape needs an outside actor, but over MCP this turns planted links into arbitrary file reads; and the alias flip side is a correctness bug if `data_root` is itself a symlink and allowlist entries and view literals mix resolved and unresolved forms. Related correction to the Go/Library finding below: relative allowlist entries are not rejected outright; consistent relative forms work and only mixed relative/absolute forms fail.

**Resolution**: a "Path discipline" rule in the engine step: every dataset folder and `--allow-dir` is canonicalized (`filepath.EvalSymlinks` + `filepath.Abs`) and that single canonical form feeds both the SET list and every embedded view literal, byte-identical prefixes guaranteed (this subsumes the Go/Library absolutize-manifest-paths finding below, resolved by reference). The interior-symlink boundary is documented in the engine step, README bullet, and requirements sensitive-data section ("a dataset folder inherits the trust of whoever can write to it"), and the sandbox acceptance tests pin the symlink behavior on the bundled engine so a future matcher change surfaces loudly.
*(Amended by round-3 review, verified empirically against real 1.3.0/1.4.1/1.5.4 CLIs: the byte-lexical/no-canonicalization behavior above is a 1.3.0 property that the pinned engine 1.5.4 reverses — it canonicalizes both allowlist sides, so an interior symlink is denied and an allowlisted alias admits the real path. The client-side `EvalSymlinks`+`Abs` path discipline stays (harmless consistency), but the interior-symlink-escape boundary does not exist on the shipped engine; the documentation now frames the residual boundary as writable files, not a symlink escape. See the round-3 Cross-Platform finding.)*

#### RESOLVED: A dataset legally named `main` (or `information_schema`, `pg_catalog`) breaks multi-dataset registration
**Severity: low-medium.** The name regex admits `main`; `CREATE SCHEMA "main"` fails with `Catalog Error: Schema with name "main" already exists!` (reproduced; `information_schema`/`pg_catalog` same; `temp`/`system` create but shadow built-ins). `dataset create main` succeeds today, and the collision rule only covers inter-dataset collisions.

**Resolution**: two layers: `create`/`rename` reject the reserved names (`main`, `temp`, `system`, `information_schema`, `pg_catalog`) with a clear message, and multi-dataset registration treats them as collisions requiring the `pre=` alias, covering datasets created by older binaries. Validator-rejection and legacy-`main` alias tests added.

#### RESOLVED: `attachment_states`' `content::JSON` cast lets one malformed file poison the whole view
**Severity: low.** Reproduced: with one non-JSON file in the list, any query materializing `state` fails with a conversion error for all rows. The bytes are tool-authored, not CLI-validated, so a server-side-corrupt offloaded state kills the view.

**Resolution**: the view is now the five-column shape `filename`, `id12`, `name`, `content` (raw), `state` (`TRY_CAST(content AS JSON)`), so one bad file degrades to one NULL-`state` row with the raw bytes inspectable; the skill teaches `state IS NULL AND content IS NOT NULL` as the parse-failure signal, and a malformed-file fixture joins the engine tests.

#### RESOLVED: Small gaps: allowlist literal escaping, and data-dependent timestamp sniffing
**Severity: low.** (1) The escaping rule is stated only for view builders, but `SET allowed_directories = ['<path>']` embeds the same literals and `data_root` can carry a quote (`/home/o'brien`); unescaped is a parser error, doubling works (reproduced). State the escaping rule once for every embedded literal, SET statements included. (2) `read_json` sniffs server `created_at` as VARCHAR whenever timezone offsets are mixed within the sample, while `_fetched_at` (uniform Z) sniffs as naive TIMESTAMP; the JSON renderer must handle both, and RFC3339 rendering of the naive TIMESTAMP must assume UTC (correct, the CLI writes Z). HUGEINT/DECIMAL render losslessly as claimed.

**Resolution**: (1) the escaping rule is now stated once for every embedded literal, view paths and SETs alike, with a quote-bearing `data_root` sandbox test. (2) Superseded in the main by the manifest column map (the sampling finding above): types no longer depend on data order; the map now pins `_fetched_at` as TIMESTAMP (uniform UTC `Z`) and plain-string fields like `created_at` map to VARCHAR deterministically, so the renderer's job is fixed per type, not per dataset.

---

### API Contract Engineer (round 2, code-verified 2026-07-17)

#### RESOLVED: `Job.ID` is typed `string`; the server serializes an integer
**Severity: major.** Verified: job ids are minted as integers (`job_server.ex`: `id: length(jobs) + 1`) and rendered unchanged (`report_job_json.ex`: `id: job["id"]`). `encoding/json` refuses a JSON number into a Go `string`, so `reports jobs` and `get report --job` fail against every real response.

**Resolution**: `ID int` in the `Job` wire struct, consistent with the manifest's `JobID *int` and the integer-parsed route param.

#### RESOLVED: The history presign `doc_id` is `history_id`, and the record's embedded `id` field is a trap
**Severity: major.** Verified: history attachment refs resolve at `/sources/{source}/interactive_state_history_states/{doc_id}` where the doc id is the history id, but the state doc is a full answer-doc copy whose embedded `id` field is the answer id. The spec says "doc_id = the record's doc id, `id` for answers" and leaves history implicit; the natural reading sends the answer id, so every history-referenced attachment presign returns `not_found` and silently lands in `coverage.missing`.

**Resolution**: the scan paragraph now pins doc_id = `id` for answers, `history_id` for history, with the trap named in place, and a scan-golden case asserts the history ref uses `history_id`, never the embedded `id`.

#### RESOLVED: The history `source_key` hedge can be resolved now: history records do carry `source_key`
**Severity: minor.** State docs are full answer-doc copies including `source_key` (verified in the seeding helpers this repo treats as faithful, and the parquet answer schema lists it required). Tighten the hedge to "history records carry `source_key`; the download-context stamp is a defensive fallback only" (the authoritative writer is the activity-player repo, so keep the stamp). One nuance worth a parenthetical: a record's `source_key` is LARA-derived and can differ from the Firestore `source` segment, so it must never substitute for `source` in presign coordinates (the spec already sources `source` from the portal mapping).

**Resolution**: the fetch step's hedge is replaced with the verified statement plus the defensive-fallback framing, and the guard-rail sentence (never use `source_key` as the presign `source`) is stated in place.

#### RESOLVED: `/auth/cli`'s `portal` param is optional on the landed controller, not required
**Severity: minor.** Verified: an absent `portal` falls back to the server's configured default portal rather than being rejected (validated as https origin with a known DB connection only when present). The round-3 requirements wording "the landed controller also requires `portal`" is factually off, and a fake server built to require it would be stricter than the real one. The CLI must still always send it to select the portal.

**Resolution**: the requirements login bullet now states the optional-with-fallback behavior in place, the round-3 decision log carries a matching amendment, and the fake-server description pins the same validated-only-when-present behavior.

---

### Go/Library Engineer (round 2, verified against upstream 2026-07-17)

#### RESOLVED: `marcboeker/go-duckdb/v2` is deprecated; the project moved to `github.com/duckdb/duckdb-go`
**Severity: major.** pkg.go.dev carries "Deprecated: This module has moved to github.com/duckdb/duckdb-go" (starting v2.5.0; migration guide published); the last marcboeker tag is v2.4.3, and the successor's latest is v2.10504.0 bundling DuckDB 1.5.4, with all five prebuilt static-lib targets intact and the same cgo/`sql.Open("duckdb", "")` facts. Pinning the deprecated path freezes DuckDB at 1.4.1 with no future fixes.

**Resolution**: the engine step's go.mod line now names `github.com/duckdb/duckdb-go/v2` (v2.10504.0, DuckDB 1.5.4) with the deprecation noted; "bundled 1.4.1" references updated to the bundled engine at pin time; requirements key-libraries line renamed.

#### RESOLVED: The declared Go 1.24 module cannot require MCP go-sdk v1.6.x
**Severity: major.** Verified: go-sdk v1.6.1's go.mod declares `go 1.25.0`, so a `go 1.24` module fails at `go get`; Go 1.24 is also past support (1.25/1.26 current). The scaffold step and the MCP step contradict each other.

**Resolution**: the scaffold step declares `go 1.25.0` with the SDK floor noted as the binding constraint, and a `.tool-versions` file (`golang 1.25.12`, installed via asdf) pins the dev-machine toolchain.

#### RESOLVED: `chzyer/readline` has been unmaintained since 2022
**Severity: minor.** Last release April 2022, no commits since, 86 open issues (several Windows terminal-handling); maintained near-compatible fork exists (`ergochat/readline`). The spec presents the choice as settled with no risk note.

**Resolution**: switched to `github.com/ergochat/readline` in the repl step, with the upstream-frozen rationale noted in place.

#### RESOLVED: `pkg/browser` writes the launched command's output to the CLI's stdout, colliding with stream discipline
**Severity: minor.** The library exposes `var Stdout io.Writer = os.Stdout` for the browser-launcher subprocess, so `xdg-open` chatter lands on the CLI's stdout unless redirected, breaking the exactly-one-JSON-envelope contract on `login` failures. One-line fix, but only if the implementer knows the default exists (the library is also dormant, last activity Jan 2024, acceptably small).

**Resolution**: the login step now states the opener seam redirects `browser.Stdout`/`browser.Stderr` to the CLI's stderr before `OpenURL`, with the reason in place, and the login test list gains a stream-discipline assertion.

#### RESOLVED: View builders must absolutize the manifest's dataset-relative paths before embedding
**Severity: minor.** The manifest stores paths relative to the dataset folder and the engine step embeds "paths ... from the manifest's explicit file lists" verbatim; under the sandbox, path forms must be consistent with the allowlist entries (verified: mixing relative and absolute forms is denied in either direction, surfacing as a misleading "file system operations are disabled" error). Combined with the canonicalization finding above, the rule is: one canonical absolute form everywhere.

**Resolution**: resolved by reference; the "Path discipline" rule added for the symlink finding above covers this fully (canonical absolute form for allowlist entries and embedded literals alike, with the sandbox acceptance test exercising the symlinked-`data_root` case).

---

### Security Engineer (round 2, code-verified 2026-07-17)

#### RESOLVED: The randomness source for the PKCE verifier and state nonce is never specified
**Severity: medium.** The login step says only "random bytes"; `crypto/rand` appears nowhere in either spec. The verifier is the sole secret protecting the one-time code and `state` is the callback-forgery defense; a `math/rand` implementation passes every functional test (including the planned PKCE vector tests, which only check derivation) while collapsing the security property.

**Resolution**: the PKCE paragraph mandates `crypto/rand` for all client-minted security tokens (read failure = hard error, never a fallback), names the why, and the test list gains a source-level assertion that `math/rand` is not imported in `internal/auth`. The paragraph also picked up a free clarity fix: the challenge hashes the 43-char verifier string, not the underlying raw bytes.

#### RESOLVED: Sensitive `.tmp` files must be created `0600`, not chmodded after
**Severity: medium.** The spec states `0600` on the final `credentials.json`/`config.json`/`repl_history` but never that the `.tmp` is created with that mode; the natural `os.Create` (0644 under default umask) then chmod leaves the plaintext `ccd_` token group/other-readable during the write window.

**Resolution**: a shared `internal/fsutil.WriteFileAtomic0600` helper (`O_CREATE|O_WRONLY|O_EXCL, 0600` at open) now owns the rule in one place; config, credentials, and the pre-created repl history all route through it, and its unit test asserts `0600` on the temp file immediately at creation as well as on the final file.

#### RESOLVED: Pin non-login commands' target origin to the per-portal recorded server in the API-client step
**Severity: low.** The config step already states the rule ("later commands talk to the same server that minted the token"), but the API-client step never says its base URL comes from `PortalCred.Server` rather than the global `config.ServerURL`, and no test asserts it. An implementation reading the global field would send portal A's bearer token to whatever origin config currently names.

**Resolution**: an "Origin rule" paragraph in the API-client step pins the enforcement point (base URL = the credential's recorded `Server`; `config.ServerURL` is login-only), and the client test list gains the mismatched-config assertion.

#### RESOLVED: Listener behavior on a rejected (bad-state) callback is unspecified
**Severity: low.** "The listener answers a small ... page and shuts down" does not say whether a state-mismatch request stops the listener. If it does, any local process that races junk to the ephemeral port breaks the login (nuisance DoS). Verified server-side: errors render on the server and never redirect to the loopback, so the callback only ever legitimately carries `code` + `state`.

**Resolution**: the loopback paragraph now states the serve-until-match rule (mismatch answered with a static error page, listener continues, nothing consumed), the no-reflection rule for both response pages, and the test extends to assert the genuine callback still succeeds after a mismatch.

#### RESOLVED: `--server` is a trust decision and should say so
**Severity: low.** `login --server <x>` drives the entire PKCE flow and token exchange against `<x>`; unlike `--url`, no doc flags the trust boundary. A social-engineered setup snippet with a malicious `--server` captures the login.

**Resolution** (strengthened during processing by user decision, from documentation to enforcement): server origins are validated wherever read (flag and config field): the host must be `concord.org`/`concordqa.org` or a subdomain of either (dot-boundary suffix match) or loopback (the only hosts accepting http); anything else is a usage error, and widening the allowlist is deliberately a code change. Allowlist matrix test added, and the README still carries the trust-boundary sentence.

---

### QA Engineer (round 2)

#### RESOLVED: "Crash injection at each write boundary" has no specified mechanism
**Severity: medium.** "Kill between every adjacent pair of the four durable writes" cannot mean killing a goroutine, and the `mergeCompact` sketch exposes no injection seam or subprocess entry point. The crash-safety story is the spec's most-argued invariant; its test is the vaguest line in the plan.

**Resolution**: the test list now names the mechanism: a `testHookAfterWrite(n)` seam in `mergeCompact` (no-op in production) drives the five-boundary matrix, and one coarse subprocess SIGKILL case reuses the concurrency acceptance's re-exec harness with an env-var stall point.

#### RESOLVED: Attachments selectors, `--url`, and `--inline` have no planned tests
**Severity: medium.** The step's test list covers scan, chunking, partial failure, resume, and GC, but nothing covers selector filtering (including multi-match `--name`), the `--url` single-vs-multiple stdout contract, the writes-nothing guarantee, or `--inline` flipping the presign `disposition`. The `--url` gaps are security-adjacent stated requirements; the MCP side is tested (argument-schema snapshot), making the CLI-side omission conspicuous.

**Resolution**: the step's test list gains selector goldens (all four selectors plus multi-match `--name`), `--url` output goldens with a dataset-and-manifest byte-unchanged assertion, and the `--inline` presign-body disposition assertion.

#### RESOLVED: Fakes encode unlanded server behavior with no pinning, and the one-shot live check can never validate the owed endpoints
**Severity: medium.** Every test of the owed surfaces (revoke, introspection, `total_endpoints`) asserts against a hand-written guess, and the live Playwright check, scoped as "passed once", will exercise the contract-404 fallback paths for exactly those features until the server work lands, then never run again. Drift in the owed endpoints lands silently with all tests green.

**Resolution** (strengthened during processing: the owed server work had landed on the report-service branch by processing time, so pinning went from "record a commit" to "capture the real bytes"): the capturable surfaces were captured live from a locally running report-service (seeded user/token/runs, then cleaned up) and recorded with their source commit in [wire-captures.md](wire-captures.md), which the fake fixtures cite; uncapturable surfaces cite the server's own controller tests. The live PKCE check's scope changed from one-time stamp to re-running as each owed dependency lands (part of that dependency's definition of done), stated in both spec files.

#### RESOLVED: SQL identifier sanitization is untested exactly where the auto-generated dataset name lands
**Severity: medium.** The default name `{date}_{slug}` (for example `2026-07-16_wildfire`) leads with a digit and contains hyphens, so sanitization is the common case, yet no test covers sanitizing such a name, the collision hard error (`a-b` vs `a_b`), `pre=<ref>` alias resolution, or the name-regex boundaries; "table-driven ref parsing" covers only ref splitting.

**Resolution**: the engine tests gain the auto-generated-name sanitization golden and the `a-b`/`a_b` collision + alias cases, and the CRUD name-validator tests gain the regex boundary rows (63 accepted / 64 rejected, leading digit accepted, leading hyphen/underscore rejected).

#### RESOLVED: The success-path half of the stream-discipline acceptance has no planned test
**Severity: low-medium.** The failure leg and the query leg are tested, but no step asserts that a successful `get` writes exactly one JSON result line and nothing else to stdout; a stray print in a fetch loop breaks the documented `2>/dev/null | jq .` acceptance while every planned test stays green.

**Resolution**: a shared capture-streams helper is specced in the get report step and reused by the answers/history and attachments suites; each fetch success path asserts stdout is exactly one parseable JSON result line.

#### RESOLVED: CSV `row_count` pseudo-header exclusion has no regression test
**Severity: low.** The off-by-2 count was a code-verified round-3 finding; the get report tests never include an answers-shaped fixture CSV with the two rows and an expected manifest `row_count` (the view-side filter is covered; the manifest count is not).

**Resolution**: the get report tests gain both fixtures: answers-shaped (N data rows + the two pseudo-header rows, recorded `row_count` N) and usage-shaped (all rows counted), also pinning the `report_type`-conditional exclusion from the round-2 filter finding.

#### RESOLVED: `repl` has zero planned tests
**Severity: low.** Interactivity excuses the readline loop, not the unit-testable parts: statement accumulation on `;`, `.tables`/`.schema` output, and the `0600` history-file permission (a stated sensitive-data control).

**Resolution**: the step now extracts the accumulator and dot-command handlers behind reader/writer seams, and the test list gains statement-splitting cases (including `;` inside string literals), `.tables`/`.schema` goldens, and the history-file permission assertion.

---

### DevOps/Release Engineer (round 2, verified against upstream 2026-07-17)

#### RESOLVED: "Free goreleaser in single-target mode" does not exist for `goreleaser release`; the pipeline fails on its first tag
**Severity: high.** Verified in goreleaser's `cmd/release.go`: the `release` command has no `--single-target` flag (that flag exists only on `goreleaser build`, which produces bare binaries, no archives/checksums); per-platform partial releases are exactly the Pro split/merge feature the resolved orchestration question cites as Pro-only. As specced, each runner's `release --skip=publish` against the shared config tries to build all targets, and the cgo linux build fails to link on the macOS runners.

**Resolution**: variant (a) chosen: thin per-platform configs rendered in CI from `.goreleaser.base.yaml` by `scripts/gen-goreleaser.sh` (only the `builds.goos`/`goarch` block differs), each job running `goreleaser release --skip=publish -f` its own config with free per-file checksums (`checksum: {split: true}`); the release step and the resolved Open Question's decision log both updated.

#### RESOLVED: Notarization cannot run on a tar.gz, stapling a bare binary is impossible, and the sign-before-archive ordering is unstated
**Severity: medium.** `notarytool submit` accepts only ZIP/DMG/PKG, and a notarization ticket cannot be stapled to a standalone Mach-O (stapler error 73), so the shipped binary is notarized-but-unstapled: fine for brew (no quarantine on curl downloads), an online Gatekeeper check for direct downloads. Signing and notarization must run as a goreleaser build post-hook (codesign with hardened runtime + timestamp, zip a submission copy, `notarytool submit --wait`, discard the zip) before archiving, or the tar.gz ships an unsigned binary; a literal "codesign + notarytool" script pointed at the produced artifact gets a file-type rejection. `--wait` also adds minutes per macOS job.

**Resolution**: the release step now specs the script's full flow with the build post-hook placement, the ZIP-submission-copy mechanics, the no-staple reality, the wall-time note, and the README's brew-vs-direct-download Gatekeeper behavior.

---

### Consistency Re-pass (round-2 close-out, 2026-07-17)

#### RESOLVED: Fourteen consistency findings from a fresh-eyes re-pass over all four documents after the round's 38 edits
A dedicated consistency review (contradictions and staleness only, no new design review) found: (1) the design doc's generation-readiness bullet defined `report_type` with an incompatible run-kind vocabulary (`athena`/`portal`); (2) cursor persistence was three-way inconsistent (design doc said manifest-entry cursor; implementation uses cursor files; manifest `LastCursor` had no writer, and `dataset show`'s incomplete statuses had no producer); (3) the design doc still described attachment refs as `audioFile`/`__attachment__` fields in two places; (4) membership versioning missed three design-doc spots; (5) the design doc's `attachment_states` bullet still said `read_json` + lazy DDL; (6) implementation still listed download envelopes as uncapturable after wire-captures captured one; (7) wire-captures swapped two `report_type` values; (8) the design doc's error vocabulary still taught the phantom `FORBIDDEN`; (9) the design doc described per-page membership-scratch accumulation while the sketch writes membership whole at merge time, and a round-2 resolution wrongly claimed they agreed; (10) `seg.MarkMerged` was unchecked while prose claimed every durable write is checked, and the deletion invariant listed only three of the four writes; (11) the design doc's `attachment_files` taught a join on columns the implemented view lacks, and described `downloads` as `read_json_auto` over the manifest; (12) three diff estimates were stale; (13) lock-busy conditions exit 1 while the exit-code table labeled class 1 "unexpected/internal error"; (14) minor: `MissingItem`/`AttachmentFile` structs undefined, the design doc's "Why Go" named the deprecated module, and a stale inference hedge.

**Resolution** (all fourteen applied in one approved batch): the design doc's `report_type`, attachments-ref, membership-naming, `attachment_states`, error-vocabulary, membership-mechanism, `attachment_files`/`downloads`, module-name, and inference-hedge passages all aligned to the current normative text; the cursor model settled as fetch-start `Complete: false` Download entries (updated by the merge) with all resume state in the cursor file, `LastCursor`/`last_cursor` deleted from both specs; `MarkMerged` error-checked and the deletion invariant now names all four writes; the uncapturable list and wire-captures value order corrected; merge-test/engine/config estimates bumped (~450/~650/~280); the exit-code table's class 1 widened to name lock-busy; and `MissingItem`/`AttachmentFile`/`AttachmentRef` structs defined. The `attachment_files` join is now taught through the record's `attachments` map (`publicPath` extraction) rather than a nonexistent `doc_id` column.

---

### Concurrency/Lifecycle Engineer (round 3, code-verified 2026-07-17)

#### RESOLVED: The per-download lock does not cover `get report` or `get attachments`; concurrent same-run fetches corrupt shared `.tmp` paths
**Severity: high.** The lock's definition claims universal coverage ("every `get` of a `(type, run)` acquires an exclusive non-blocking flock"), and the manifest's own type vocabulary includes `report`, `report_job`, and `attachments`, but the mechanism is defined entirely in segment terms and the get report and get attachments steps acquire no lock of any kind (verified by grep: zero lock mentions in either step). Consequences if skipped: two concurrent `get report <run>` stream to the same fixed `report_<run>.csv.tmp` (interleaved corruption plus a double rename; the re-pull guard does not help mid-download), two concurrent `get attachments` race per-file `.tmp` writes and both rebuild the manifest attachment index, and both steps write the manifest without stating they hold the dataset lock, which the lock-semantics rule (`WriteManifest` asserts the guard is held) requires. The requirements acceptance ("a second `get` of the same run+type while one is running exits non-zero with the busy error") already claims this coverage.

**Resolution**: the per-download lock rule now names the non-store lock keys (`seg_report_<run>.lock`, `seg_report_job_<run>_<id>.lock` job-qualified, `seg_attachments_<run>.lock`, all under `segments/`); the get report and get attachments steps acquire their lock for the command lifetime and place their manifest writes under the per-dataset lock; both steps' test lists gain the same-run exclusion case (get report's also asserting a different `--job` of the same run proceeds). No requirements change was needed: the acceptance there already states the general same-run+type rule, which the implementation now delivers.

#### RESOLVED: Mutating dataset commands can run mid-fetch; `purge`/`delete` destroy a live fetch's segment and lock files
**Severity: high.** The round-2 resolution has mutating commands acquire the per-dataset lock non-blocking, but fetches hold that lock only during cursor/manifest writes and merges; during page downloads and S3 streams (most of a fetch's wall time) it is free, so `purge`, `delete`, or `rename` acquire it and proceed against an in-flight fetch. `purge` deletes the fetch's segment mid-append (the fetch's later merge then wholesale-replaces the run's membership from whatever subset survived, exactly the shrunk-membership data-loss shape the round-2 same-run finding closed); `delete` removes the whole folder; `rename` moves the path under the fetch (the next cursor write recreates the old path on Linux, or fails on Windows). The specced mutate-under-fetch matrix tests "against a held lock", an artificially held one, so it passes while the production interleaving is unprotected. Compounding: the `seg_<type>_<run>.lock` files live under `segments/` and nothing states their lifecycle. On Linux, unlinking a held lock file detaches the flock from its inode, so a purge-then-refetch sequence gives two processes the same per-download lock simultaneously; on Windows the deletion instead fails with a sharing violation (Go opens files with `FILE_SHARE_READ | FILE_SHARE_WRITE` only, no `FILE_SHARE_DELETE`; verified in Go 1.25.12 `syscall_windows.go:396`), leaving a partial purge of data the spec classifies as sensitive, reported with raw OS errors.

**Resolution**: whole-fetch exclusion added as a second dedicated lock file: every `get` holds a shared flock on `<dataset>/.activity.lock` for its lifetime; mutating commands take it exclusively, non-blocking, before the per-dataset lock, so a mutation can never interleave with a live fetch (two files rather than one because a merging fetch already holds the shared lock and a single-file upgrade would self-deadlock; in-process the guard is a reader-counted `sync.RWMutex` over one flock). The lock-file lifecycle is pinned: no lock file is ever unlinked by cleanup, `--refresh`, or `purge` (`purge` enumerates its deletions and excludes `*.lock`); `delete` acquires the exclusive activity lock, closes its handles, then removes the folder, with the microscopic window named as accepted. The mutate-under-fetch matrix now runs against a real fetch stalled between pages via the crash harness's stall point, on the Linux and Windows CI legs, and an activity-lock matrix joins the merge-engine tests. The requirements merge-serialization bullet gained the activity-lock sentence and a purge-during-fetch acceptance criterion.

#### RESOLVED: The per-dataset lock's file is never named, and the obvious candidate is a trap on every platform
**Severity: medium-high.** No sentence in either spec file names the path the per-dataset flock is taken on. The natural choice, `manifest.json`, is disqualified twice over: it is replaced by rename (`WriteManifest` goes `.tmp`, fsync, rename), which silently detaches every held or future flock onto a dead inode, voiding the merge serialization three round-2 resolutions depend on; and on Windows `LockFileEx` locks are mandatory, so flocking a file that lockless readers read makes concurrent `show`/`query` reads fail with `ERROR_LOCK_VIOLATION` (verified in gofrs/flock `flock_windows.go`: exclusive lock on byte range [0,1)). The spec states exactly this rule for the per-download lock ("a dedicated file, never the cursor, which is replaced by rename") one paragraph away and leaves the more contended lock unpinned.

**Resolution**: a "Lock files and the whole-fetch activity lock" paragraph in the merge-engine step now names both files (`<dataset>/.dataset.lock` for the per-dataset lock, `<dataset>/.activity.lock` for whole-fetch exclusion), states the dedicated-file rule (never renamed, never read as data, never unlinked) with the rename-detach and Windows mandatory-locking rationale recorded in place.

---

### Go Implementation Reviewer (round 3)

#### RESOLVED: `mergeCompact` ignores the `ReadManifest` error; a failed read durably wipes the manifest and bypasses the version gate
**Severity: high.** The sketch reads `m, _ := ds.ReadManifest()` inside the critical section and later calls `WriteManifest(m)`. On any read error (corrupt manifest, transient I/O failure, or the deliberate future-version "please upgrade cc-data" refusal) `m` is the zero-value `Manifest`; the loop proceeds and the repoint durably replaces the real manifest with one containing only this merge's store/membership pointer: all other types' stores, the `Downloads` provenance, and the `Attachments` index are wiped, and provenance is unrecoverable by the spec's own admission (reindex marks it recovered-without-provenance). It also defeats the version gate: an old binary merging against a newer-schema manifest gets the refusal error, ignores it, and overwrites the newer manifest. This is the same failure class as the resolved `WriteMembership` finding, one line earlier in the same sketch.

**Resolution**: the sketch now checks the re-read (`m, err := ds.ReadManifest(); if err != nil { return MergeCounts{}, err }`), and the crash-safety paragraph states the reads-feed-the-same-rule-as-writes invariant. A corrupt-manifest abort case joins the merge test list.

#### RESOLVED: The rebase check ignores the `CurrentStore` error; a persistent read failure spins forever holding the dataset lock
**Severity: medium.** `cur2, _ := ds.CurrentStore(typ)`: on a read error `cur2` is the zero `Store` with `Version` 0. When `cur.Version >= 1`, a persistent error (permissions, corruption) makes the inequality true every iteration, so the loop re-merges and deletes `tmp` forever, holding the per-dataset lock, surfacing nothing; on a first merge (`cur.Version == 0`) a failed read false-passes the check instead.

**Resolution**: the sketch checks the `CurrentStore` read and aborts on error; the belt-and-braces re-read is kept (the parenthetical defends it) but can no longer spin or false-pass on version 0.

#### RESOLVED: The manifest repoint drops `Store.Columns`, and `streamMerge`'s signature cannot deliver the column map
**Severity: medium.** The sketch assigns `m.Stores[typ] = Store{File: ..., Version: next, Count: total}`, zero-valuing `Columns` on every merge, while the same step's prose makes the merge-derived column map the query layer's replacement for sampling inference, and `streamMerge` returns `(counts, total, err)` with no path for the map to reach the assignment. Transcribed literally (and the spec's stated convention is that the merge is presented at full-algorithm fidelity), every store view degrades to the empty-store fallback's contract-fields-only schema and all data columns silently vanish from queries.

**Resolution**: `streamMerge` now returns `(MergeCounts, total int, cols map[string]string, err error)`, the repoint carries `Columns: cols`, and the round-2 finding that introduced the map was amended to record the corrected signature.

#### RESOLVED: The first-merge bootstrap is absent: `cur.File` is empty when no store exists
**Severity: low.** On the first fetch of a type, `m.Stores[typ]` is the zero `Store`, so `ds.Path(cur.File)` is the dataset directory itself and `streamMerge` would open a directory (or nothing) as the old store. The empty-store case is the first thing every dataset does and neither the sketch nor its prose names the branch (the engine step handles absent stores for views, not for the merge input).

**Resolution**: the sketch comments the zero-`Store` case as an empty old-store stream (`streamMerge` streams zero records for an empty old-store path), and a first-merge bootstrap golden joins the merge test list.

---

### Performance Engineer (round 3, verified by measurement and server code)

#### RESOLVED: The CSV type-sniffing model is wrong: DuckDB samples 20,480 rows, correctness rides on an unstated REPORT-58 server ORDER BY, and a NULL-prompt column can still hard-fail the whole `reports` view
**Severity: medium-high.** The claim "`read_csv` infers column types from the whole file" is false. Verified on DuckDB 1.3.0 with a 30,002-row CSV whose Prompt/Correct-answer rows were placed at the end: all columns sniff BIGINT (`sample_size = 20480` shown in the error dump), and both the raw scan and the spec's exact filter view (`WHERE student_id::VARCHAR NOT IN (...)`) fail mid-scan with a conversion error; the WHERE cannot prevent a parse-stage failure, and `count(*)` succeeds while column-materializing queries fail, making the breakage query-dependent. The spec's conclusion holds today only because the server pins the two pseudo-header rows first: `shared_queries.ex:307`, `ORDER BY CASE student_id WHEN 'Prompt' THEN -2 WHEN 'Correct answer' THEN -1 ELSE 0 END, ...`, commit `4e01de3` (REPORT-58), a load-bearing dependency recorded nowhere in this repo. Residual failure even with today's ordering: pseudo-header cells render `Map.get(column, :header) || "null"`, so a question column whose prompt/correct-answer text is SQL NULL gets no header text; with more than 20,480 data rows and numeric-then-text drift past the sample window, every row-producing `reports` query hard-fails. The spec is also internally inconsistent: it states the 20,480-record sample rule for `read_json` while claiming whole-file inference for `read_csv`.

**Resolution** (shared with the registration-cost finding; applied together): the get report stream now detects each CSV's column types full-file (the same widening rule the merge uses for `Store.Columns`) and records `Columns`/`CSVDialect` in the `Download` entry; the `reports`/`report_prompts`/per-run report view builders read with `auto_detect=false, header=true, columns={...}` from that record, so binding neither sniffs nor depends on row order or size. The false "whole file" claim is corrected in both spec files, the REPORT-58 ordering is recorded as a pinned dependency in wire-captures.md, and `reindex` re-derives the map in its CSV rescan. Verified by throwaway DuckDB test: with the two pseudo-header rows appended last and past the 20,480-row sample (including a NULL-prompt column rendering the literal `"null"`), `auto_detect=false` + explicit `columns=` reads all rows and `TRY_CAST`-aggregates cleanly, while the old bare-`read_csv` design fails with the predicted conversion error. The `_answer`-columns-are-VARCHAR reality and the skill's `TRY_CAST` teaching are unchanged.

#### RESOLVED: Ephemeral engine plus bind-time sniffing costs about a second per query at 30 runs, tripled at registration, linear in run count
**Severity: medium.** Measured on a spec-shaped fixture (30 answers-type CSVs of ~2 MB each, 136 MB answers store, 595 MB history store, full 65-statement view set, NVMe): registration 2.5 to 3.0 s, dominated by CSV sniffing (`reports` union 0.92 s, `report_prompts` 0.85 s, ~30 ms per per-run view; each CSV is dialect-and-type-sniffed three times, once per view naming it), while the store views with explicit `columns=` bind in ~1 ms and contribute nothing (confirming the `Store.Columns` design). Re-bind repeats the sniff per query: `EXPLAIN SELECT count(*) FROM reports` alone costs 0.93 s, paid by every query, `DESCRIBE`, or `.schema` touching `reports` in every invocation, and every `cc-data query` and MCP `query` call is a fresh invocation paying registration first. At 100 runs this is ~8 to 10 s registration plus ~3 s per `reports` query. The existing round-2 acknowledgment stops at "scales registration cost with holdings" without numbers and misses the per-query half.

**Resolution**: resolved by the same manifest-recorded CSV schema as the sniffing finding above (`auto_detect=false, columns={...}` reads bind in ~1 ms and read the file for neither dialect nor type inference, per the measurement), so registration and per-query re-bind no longer pay a per-CSV sniff. The engine test list gains a golden asserting the builders emit `auto_detect=false` rather than a bare `read_csv`.

#### RESOLVED: Per-page cursor writes take the per-dataset lock, so every concurrent download stalls behind any in-flight merge; the cursor needs only the per-download lock
**Severity: medium.** The merge holds the dataset lock across the whole `streamMerge` (measured floor: 4.36 s for a single merge-shaped pass over a 595 MB store on fast NVMe; the real merge adds random segment reads, and researcher laptops are slower), during which every other download blocks at its next per-page cursor write, contradicting "different-run downloads stay fully concurrent"; downloads that finish also queue their merges behind it. The dataset lock buys no correctness for cursors: every cursor writer (page loop, `--refresh` deletion, `EXPIRED_CURSOR` restart, `MarkMerged`) already holds the exclusive per-download lock for the command's lifetime, so no second process can touch that cursor.

**Resolution**: per-page cursor writes now run under the per-download lock only (already held for the command's lifetime); the per-dataset lock is reserved for manifest writes and the merge critical section. The segment-lifecycle bullet, the lock-semantics helper rule, the fetch step's page-loop sentence, and both requirements sentences (cursor under the per-download lock; "only manifest writes and the merge hold the per-dataset lock") were updated to match.

#### RESOLVED: Building a K-run dataset is O(K^2) store IO, stated and accepted nowhere
**Severity: medium.** Each merge rewrites the complete store version; a dataset built by sequentially fetching 30 runs whose store grows to 600 MB performs ~30 full rewrites, roughly 9 GB read plus 9 GB written to land 600 MB of data (~15x write amplification), and a 2 GB store across 40 runs is ~40 GB each way, on researcher laptops. Because merges serialize under the dataset lock, concurrent multi-run pulls queue these rewrites end to end, so each successive `get` ends with a longer merge tail. The plan's only merge cost claim ("disk-bound, not RAM-bound") addresses RAM only.

**Resolution**: a "Write amplification and the lock-free backlog sweep" paragraph in the merge-engine step now states and accepts the sequential O(N^2) cost, and the merge additionally sweeps other finished-but-unmerged segments of the type into a single compact (reusing the existing finished-cursor state and `MembershipUnion`-over-multiple-replacements). Each swept run keeps its own membership, counts, and `merged_as`, repointed by one manifest write; a backlog sweep test joins the merge list.
*(Amended by round-7 self-review: the original text claimed "concurrent and backlogged multi-run pulls collapse to one store rewrite," which contradicted the per-download-lock contract from the Concurrency round-3 findings. To sweep a live sibling's segment, the merging process would have to read that segment and write its cursor/membership without holding that run's per-download lock, which a live command holds for its whole lifetime; the invariant "only the per-download-lock holder reads the segment or writes its cursor" (and "no merge runs against a segment a live same-run command holds") forbids it, and doing it anyway produces a redundant second full store rewrite plus misstated per-run counts. Superseding decision (option A): the sweep collapses **only lock-free** finished-but-unmerged segments (a crashed or already-exited fetch), acquiring each swept segment's per-download lock non-blocking for the sweep-write, so the invariant is preserved. A **live** concurrent pull is never swept and merges its own segment itself, so genuinely concurrent multi-run pulls still pay O(N^2); the sweep is a crash/exit-backlog optimization, not a live-concurrency one. The paragraph, the `mergeCompact` sweep comment, and the sweep test (now a lock-free backlog case plus a live-lock-held negative) were updated to match.)*

#### RESOLVED: Polling never terminates on two verified server states, and one of them re-submits an Athena query every poll
**Severity: medium.** Verified in report-service: (1) `ensure_current` on a run whose `start_query` persistently fails (bad filter, Athena/IAM error) releases `athena_query_state` back to `nil` and only logs (`athena_run_ops.ex`); the download action calls `ensure_current` on every request, so the CLI's poll loop drives `nil` to `queued` to `nil` forever, never reaching `failed`, and every poll submits a fresh Athena `StartQueryExecution` (real AWS cost) indefinitely. (2) Job statuses include `"failed"` (`job_server.ex`), and the job controller renders every non-completed status, `"failed"` included, as `NOT_READY` 409 (`report_job_controller.ex`), which the spec classifies as poll-again; `get report --job` on a failed job polls at 30 s forever (terminal handling is defined only for run `failed`/`cancelled`). No overall polling budget exists anywhere; `--no-wait` is opt-in, and the tool's headless/MCP consumers hang.

**Resolution**: the get report flow now treats job `status: "failed"` as terminal (exit 5, mirroring run `failed`/`cancelled`), adds a `--poll-timeout` overall budget (default 30 min, on expiry exits 4 with the last state), and detects the `null`→`queued`→`null` self-start oscillation to stop early with a specific message instead of re-submitting Athena queries forever. The requirements fetch bullet gained the budget and the job-failed terminal rule; the get report test list gained terminal-failure, budget-expiry, and oscillation cases.

#### RESOLVED: Segment appends are the one durable write with no fsync rule; the fsynced cursor can claim pages the segment lost
**Severity: low.** The segment file is the only artifact in the lifecycle with no fsync language, while the cursor that indexes it is renamed durably per page. After a power loss or kernel crash, the cursor can claim page N while the unfsynced segment tail for pages up to N is gone; resume trusts `next_page_token` and continues, and the merge lands a store silently missing those items, `complete: true`, honest-looking counts. The crash-injection matrix kills processes, which never exposes this (the OS retains buffered writes across process death). The fix costs one fsync per page on an already-open file, negligible next to the ~8 MiB page fetch.

**Resolution**: the segment-lifecycle bullet now states the fsync-segment-before-cursor rule and the invariant (the cursor must never durably name pages whose segment bytes a crash could lose), with the note that process-kill tests cannot surface it.

---

### Cross-Platform (Windows) Engineer (round 3, verified against Go, DuckDB, and library sources)

#### RESOLVED: The lockless-reader safety premise is false on Windows for `manifest.json`, config, credentials, and cursor files
**Severity: medium-high.** Go's Windows file open passes `FILE_SHARE_READ | FILE_SHARE_WRITE` with no `FILE_SHARE_DELETE` (Go 1.25.12 `syscall_windows.go:396`), and `os.Rename` is `MoveFileEx(REPLACE_EXISTING)`, which requires DELETE access to the destination. While any cc-data process has `manifest.json` open for reading, a concurrent `WriteManifest` rename fails with a sharing violation, and readers can see spurious open errors during a replace; the Go project ships `cmd/internal/robustio` for exactly this pattern (retrying `ERROR_SHARING_VIOLATION`, `ERROR_ACCESS_DENIED`, transient `ERROR_FILE_NOT_FOUND` on both the rename and the reader side). The claim "no rename ever lands on a path a concurrent reader holds open" is achieved for stores and membership by versioning but is false for the fixed-name files: manifest (read by every command and the long-lived MCP server, replaced by every merge), `config.json`/`credentials.json` (login vs MCP-server reads), and cursor files. Concrete failure: a merge's manifest write fails at the end of a long fetch because a `dataset show` happened to be reading.

**Resolution**: a "Windows rename/read robustness" paragraph in the config step specs the `internal/fsutil` robustio-style wrappers (Windows-only bounded retry on `ERROR_SHARING_VIOLATION`/`ERROR_ACCESS_DENIED`/transient `ERROR_FILE_NOT_FOUND`, plain `os.Rename`/`os.Open` on POSIX) applied to rename and to opening the replace-target files (manifest, config, credentials, cursor); `WriteManifest` routes through it, the no-rename-over-open-path claim is scoped to the versioned store/membership artifacts, and a concurrent-rename robustio test joins the config test list.

#### RESOLVED: The specced 0600 permission assertions fail unconditionally on the Windows CI leg
**Severity: medium.** On Windows a file's stat mode derives solely from `FILE_ATTRIBUTE_READONLY` and is always 0444 or 0666 (Go `os/types_windows.go`), so the three planned assertions (fsutil temp-and-final mode, credentials file permission, repl history 0600) are guaranteed red on `windows-2022`, a leg the requirements commit to keeping green on every push. The spec already acknowledges the ACL reality in prose but did not carry it into the test plan.

**Resolution**: the three permission assertions (fsutil, credentials, repl history) are gated `runtime.GOOS != "windows"`, with the Windows leg asserting the ACL instead; noted in each test list.

#### RESOLVED: Attachment filenames embed client-controlled names with no sanitization rule; Windows-reserved characters error out or silently write NTFS alternate data streams
**Severity: medium.** Attachment names come from the client-written `attachments` map keys, and the spec itself records that Firestore rules allow arbitrary content in these docs. Windows forbids `< > : " / \\ | ? *`, reserved device basenames (`CON`, `NUL`, `COM1`, ...), and trailing dots/spaces. Most reserved characters fail the `os.OpenFile` with a raw OS error on Windows only; a `:` can instead succeed by writing an NTFS alternate data stream, so the bytes land where the manifest's `File` path does not point and the GC/`attachment_files` logic sees drift. The two spec files also disagree on the component (`<basename>` vs `<name>`), so even the slash-stripping rule is inconsistent.

**Resolution**: the get attachments step pins the deterministic cross-platform sanitizer (`filepath.Base`, keep `[A-Za-z0-9._-]` else `_`, strip trailing dots/spaces, guard Windows device basenames), the file lands at `attachments/<id12>_<safename>` recorded in `AttachmentFile.File`, the requirements bullet is aligned (resolving the `<name>`/`<basename>` inconsistency too), and a hostile-name golden joins the attachments tests.

#### RESOLVED: The pinned DuckDB 1.5.4 canonicalizes both sides of the allowlist; the spec's symlink-boundary claims and documentation are wrong for the shipped engine
**Severity: medium.** The path-discipline paragraph and three documentation surfaces (README step, requirements sensitive-data bullet, skill) state a boundary verified on DuckDB 1.3.0: allowlist matching is byte-lexical with no canonicalization, an interior symlink escapes the sandbox, and an allowlisted symlink alias denies the real path. Verified in the v1.5.4 sources: `DBConfig::SanitizeAllowedPath` calls `FileSystem::CanonicalizePath` (realpath on POSIX, `GetFinalPathNameByHandleW(..., FILE_NAME_NORMALIZED)` on Windows) and normalizes separators to `/`, and it is applied to both `AddAllowedDirectory` and `CanAccessFile`. On the bundled engine the behavior is therefore the opposite of the documented one: an interior symlink pointing outside is denied, an allowlisted symlink alias admits the real path, and the "byte-lexical" load-bearing rationale is stale (client-side `EvalSymlinks` + `Abs` remains correct as harmless consistency, and the both-sides forward-slash normalization is also what makes Go's backslash paths match on Windows). The planned tripwire test asserting the 1.3.0 behavior fails on day one of the engine step.

**Resolution**: re-verified empirically against real 1.3.0, 1.4.1, and 1.5.4 CLIs (the 1.5.4 binary confirmed: interior symlink to an outside file denied, allowlisted alias admits the real path — the reverse of 1.3.0). The path-discipline paragraph now states the 1.5.4 canonicalize-both-sides reality (client-side `EvalSymlinks`+`Abs` kept as harmless consistency); the symlink-boundary sentences in the README step and requirements sensitive-data bullet are reframed as a writable-files trust boundary; the round-2 resolved finding that stated the 1.3.0 behavior carries an amendment; and the sandbox acceptance test asserts the 1.5.4 outcomes with a flip-loudly note.

#### RESOLVED: `--refresh` renames over CSV and attachment paths a concurrent reader can hold open, violating the spec's own no-rename-over-open-path invariant
**Severity: low-medium.** Read-only sessions do not lock and views re-bind per query, so a `repl` scan can hold `report_<run>.csv` open exactly when `get report --refresh` renames onto it, and likewise attachment files. DuckDB 1.4.x opens files on Windows without `FILE_SHARE_DELETE` (deterministic rename failure); 1.5.0+ adds `FILE_SHARE_DELETE` (verified in `local_file_system.cpp` per tag), which permits deletion but still leaves `MoveFileEx(REPLACE_EXISTING)` onto an open name unreliable. No data loss (the `.tmp` survives and the old CSV is intact), but the invariant "no rename ever lands on a path a concurrent reader holds open" is overbroad and no error surface is defined for the Windows failure.

**Resolution**: the no-rename-over-open-path claim is now scoped to the versioned store/membership artifacts, and the `--refresh` overwrites of `report_<run>.csv` and attachment files are documented accepted races handled by the same fsutil robustio wrapper (noted in the get report and get attachments steps and the crash-safety reader paragraph).

#### RESOLVED: Port-bearing dev-portal hostnames are illegal directory names on Windows
**Severity: low-medium.** Portal hostnames key dataset folders with "port preserved for dev portals", so a dev portal yields `<data_root>/localhost:8080/datasets/...`; `:` is reserved in Windows path components, so `os.MkdirAll` fails. The dev/staging workflow (the only usable one until the server deploys land, per the rollout note) is unusable on Windows, and any Windows CI fixture using a port-bearing portal fails.

**Resolution**: the config step defines the portal folder encoding (`:` → `_`, applied on every platform) with the real host kept in credentials and manifest, and the config test list gains the `localhost:8080` → `localhost_8080` case.

#### RESOLVED: The config/credentials location on Windows and the `~` expansion rule are unstated
**Severity: low.** Go performs no `~` expansion, and the two reasonable mechanisms diverge on Windows: `os.UserHomeDir` yields `%USERPROFILE%\\.config\\cc-data` while `os.UserConfigDir` yields `%AppData%`. Windows is a supported CI target promotable to release by config alone, so the credential-file location there is a decision the plan currently makes implicitly; it also affects `uninstall`'s credential removal and the sensitive-file documentation.

**Resolution**: the config step's "Path base and `~` expansion" paragraph pins `~` = `os.UserHomeDir()` on every platform (Windows config/credentials at `%USERPROFILE%\.config\cc-data`, deliberately not `%AppData%`, since the ACL story holds there), and the data-root default `~/cc-data` uses the same rule.

---

### Fresh-Eyes Coherence (round 3)

#### RESOLVED: requirements.md still specifies `attachment_states` as `read_json`, and two implementation passages disagree on the view's columns
**Severity: medium.** The requirements views bullet says "`attachment_states` (`read_json` over downloaded offloaded CODAP/SageModeler `.json` state)", the design the implementation explicitly rejects with a verified test ("Deliberately NOT `read_json`"). The round-2 close-out fixed this staleness in the design doc but not in requirements. Additionally, the round-2 resolution text names a four-column shape (`filename`, `id12`, `name`, `state JSON`) while the normative view builder names five (adding `content`).

**Resolution**: the requirements views bullet now states the `read_text` five-column shape (`filename`, `id12`, `name`, `content`, `state`), and the round-2 resolution text carries an amendment naming the five columns.

#### RESOLVED: The listing step's stdout carve-out contradicts the requirements stream-discipline bullet, and `dataset show`/`list` and `auth status` have no assigned stream
**Severity: medium.** The requirements bullet enumerates stdout as machine-consumable only (result line, `--json` documents, `query` output, failure envelope) with all "human prose" on stderr and no listing carve-out; the listing step asserts "the stream-discipline rule reserves stdout for machine output on `get`/`query`", a scope the requirements never state, and puts the human table on stdout. No step assigns the human renderings of `dataset show`, `dataset list`, or `auth status` to a stream at all; under the requirements as written they are forbidden from stdout.

**Resolution**: the requirements stream-discipline bullet now scopes the stdout-is-machine-only rule to `get`/`query` and adds the pure-listing/summary carve-out (reports list/jobs, dataset list/show, auth status, version: the human table is the product and goes to stdout, `--json` replaces it there, warnings still to stderr, a failure still emits the single JSON error envelope), with a `dataset list --json` acceptance line. The implementation listing step's wording now matches.

#### RESOLVED: `reindex` cannot recover `report_type` for non-answers CSVs, and the unstated choice changes quarantine behavior
**Severity: medium.** Reindex re-derives "the pseudo-header presence for `report_type` recovery", but presence is a boolean while `report_type` is `answers | usage | log | verbatim-unknown`; the filename `report_<run>.csv` encodes neither slug nor type, so the slug-derivation fallback is unavailable post hoc, usage and log are indistinguishable by shape (student-actions-with-metadata is log-typed with a `student_id` column), and neither is distinguishable from a quarantined unknown type. Both plausible implementations misbehave: recording an in-allowlist guess silently un-quarantines a previously unknown-typed run after a reindex (defeating "a new server report type can therefore never silently corrupt aggregates"), while recording unknown spuriously quarantines every healthy usage/log run with a bogus upgrade warning, breaking reindex's recovery equivalence. Also stale: "Filenames encode exactly what recovery needs" (Technical Notes).

**Resolution**: the reindex step pins the partial-recovery rule (no `student_id` column → `log`; `student_id` + pseudo-header rows → `answers`; ambiguous remainder → a distinguished `recovered` value, in-allowlist for `reports` but excluded from `report_prompts` and flagged by `dataset show` as recovered-without-provenance) with the accepted residual documented; the `reports` view allowlist and the requirements reindex/Technical-Notes text are updated, and a reindex `report_type`-recovery test is added.

#### RESOLVED: The offloaded-state tag feeding `attachment_states` has no manifest field, so the view's selection predicate is undefined
**Severity: medium-low.** The attachments scan "uses [the `__attachment__` markers] to tag which attachments are offloaded state, feeding the `attachment_states` view", and the engine step selects "the downloaded `.json` state files listed in the attachment index", but `AttachmentFile` has no such field and no other artifact persists the tag. Each plausible predicate (name extension, `ContentType`, a flag that does not exist) yields a different view population, and `reindex` rebuilds the index by rescanning stores, so whatever carries the tag must be re-derivable there.

**Resolution**: `AttachmentFile` gains `State bool`, set by the scan from the `__attachment__` markers and re-derived by reindex; the `attachment_states` view selects on it (not on file extension), stated in both the scan paragraph and the view builder.

#### RESOLVED: The dataset-CRUD step forward-depends on the per-dataset lock defined in the following step
**Severity: low.** The CRUD step's mutating commands and its mutate-under-fetch test matrix require the per-dataset lock, but `internal/store/lock.go`, the flock wrapper, and the lock-semantics rule are introduced only in the next step, so the CRUD commit as scoped cannot land with its stated tests.

**Resolution**: `internal/store/lock.go` (the flock wrapper and the per-dataset/activity guards) moves into the CRUD step's file list so the lock primitive lands with the first commit that needs it; the merge-engine step's file entry notes it extends rather than introduces the file, and its lock-semantics paragraph still defines the full contract.

#### RESOLVED: Minor drift batch: undefined `dataset show --json` schema, `NOT_READY` missing from the requirements exit-5 enumeration, the skill's `attachment_states` pattern missing from requirements, and the `auth_status` MCP tool's check semantics unstated
**Severity: low.** (1) Both files call the `show`/`list` `--json` output a "stable schema" and it is a contract consumed by the skill (orientation step) and the MCP payload-parity tests, but no field list exists anywhere, against the spec's own schemas-are-contracts convention; the goldens will pin whatever the implementer invents. (2) The requirements class-5 enumeration omits `NOT_READY`, which the implementation classifies as a contract error (a `failed`/`cancelled` run exits 5 with `error: NOT_READY`). (3) The requirements skill bullet enumerates every taught pattern except the `attachment_states` extraction pattern the resolutions added. (4) Whether the `auth_status` MCP tool exposes the network `--check` is unstated, a real behavioral difference (offline read vs per-portal network calls) for a tool an LLM calls freely.

**Resolution**: all four applied. `ShowJSON`/`DownloadJSON`/`ListJSON`/`ListRowJSON` structs (`internal/dataset/summary.go`) now define the `dataset show`/`list --json` contract, which the MCP tools return and the payload-parity tests assert; the requirements exit-5 enumeration gains the terminal `NOT_READY` failure state; the requirements skill bullet gains the `attachment_states` extraction pattern; and the MCP step states `auth_status`'s `check` argument (default `false`, offline) and that `version` takes no arguments.

---

### Ground-Truth Verification (round 7, 2026-07-17)

This round re-ran the spec's load-bearing external claims against three sources of truth rather than re-reading them: the report-service repo on the exact `REPORT-77-cli-server-support` branch, the pinned **DuckDB 1.5.4** binary (`duckdb-go/v2 v2.10504.0`, confirmed `@latest` stable, with only a `v2.20000.0-*.preview` 2.0.0 above it), and the Go module cache. It also re-examined the merge/lock/crash-safety pseudocode for internal-consistency defects.

#### RESOLVED: All cited server, DuckDB-1.5.4, and Go-library facts re-verified; no functional defect found
Confirmed exact against the branch: auth params and `redirect_uri` validation, the `^[A-Za-z0-9_-]{43}$` challenge regex, portal-optional-with-fallback (`auth_cli_controller.ex`); `exchange_auth_grant/3` (+ additive `label`), `revoke_api_token/2`, `create_api_token(user, "CLI login")` (`accounts.ex`); token introspection/revoke shapes and the already-revoked-as-success path (`token_controller.ex`); the six-code error vocabulary with no `FORBIDDEN` and unmapped→`SERVER_ERROR` (`error_helpers.ex`); integer `Job.ID` = `length(jobs)+1` and non-`completed`→`NOT_READY 409` with the `status` extra (`job_server.ex`, `report_job_controller.ex`); the REPORT-58 `ORDER BY CASE student_id...` and the pseudo-header `Map.get(column,:header) || "null"` rendering, plus a full trace of `get_columns_for_question` confirming the CSV type model (only `_answer`-family columns carry the prompt expression and detect VARCHAR; numeric/boolean columns like `res_N_total_*` and `_submitted` are header-less, so their pseudo-header cells are SQL NULL and they keep their types) (`shared_queries.ex`); the `null→queued→null` self-start oscillation (`athena_run_ops.ex`); `total_endpoints` emission and the public attachment 500-cap (`bulk_export_controller.ex`, `bulk_params.ex`); the 600 s download TTL (`athena_db.ex`). Confirmed on the **DuckDB 1.5.4 binary**: interior symlink denied / allowlisted alias admits the real path / `lock_configuration` irreversible; `read_json(..., columns={...})` tolerates both sparse (missing→NULL) and extra (unknown-field ignored) records; empty-file/zero-row-`VALUES`/`read_text([])` empty-input forms; `UNION ALL BY NAME` unifies `BIGINT`+`VARCHAR`→`VARCHAR`; and the `json` extension is `STATICALLY_LINKED` so `TRY_CAST(... AS JSON)`/`->>`/`from_json`/`read_text` all work under the fully locked sandbox with autoload disabled. Confirmed in the Go module cache: `sql.Register("duckdb", ...)`, `ServerSession.NotifyProgress(ctx, *ProgressNotificationParams)`, and `ToolHandler` receiving a `context.Context`. No spec text changed for these; the verification itself is the deliverable, and it re-pins `wire-captures.md` to HEAD `7a8c550`.

#### RESOLVED: The multi-segment sweep contradicted the per-download-lock contract it was built beside
**Severity: medium.** Two round-3 resolutions collided. The Concurrency finding's per-download lock is held for a fetch's whole command lifetime and guarantees only its holder reads the segment or writes its cursor/membership; the Performance finding's sweep claimed to collapse "concurrent multi-run pulls that queued behind the lock" into one store rewrite. But to fold a *live* sibling's segment, the merging process must read that segment and write its cursor `merged_as`/membership without holding that run's per-download lock (which the live command still holds), breaking the invariant; and because the `mergeCompact` sketch never checks `merged_as` at its top, the swept sibling's own queued merge then redundantly re-merges the same segment (a second full store rewrite, defeating the optimization) and records `updated=all` instead of `new=all`. Respecting the lock (sweeping only lock-free segments) cannot collapse live pulls at all, since a live pull holds its lock until after its own merge, so the stated benefit was unreachable as written.

**Resolution** (option A): the sweep collapses **only lock-free** finished-but-unmerged segments (a crashed or already-exited fetch's backlog), acquiring each swept segment's per-download lock non-blocking for the sweep-write, so the invariant holds. A live concurrent pull is never swept and merges its own segment and reports its own counts; genuinely concurrent multi-run pulls still pay O(N^2), which the spec already accepts for the sequential case. The merge-engine paragraph (renamed "Write amplification and the lock-free backlog sweep"), the `mergeCompact` sweep comment, the sweep test (now a lock-free backlog case plus a live-lock-held negative), and the round-3 Performance resolution's amendment were all updated to match.

#### RESOLVED: Two low-severity accuracy/coverage notes from the re-verified branch
**Severity: low.** (1) `wire-captures.md`'s pinned commit had drifted five commits behind report-service HEAD; the captured behaviors were re-verified at HEAD and a re-pin note added. (2) Commit `2cefdee` (post-dating the spec's 2026-07-16 verification) confirmed programmatically created runs store a `NULL report_filter` and appear in the run list; `report_json.ex` normalizes `nil` to the empty filter, so the CLI is safe, but the spec never named this run class. (3) `report_type` is server-computed from the slug (`report_json.ex` `Tree.find_report`), not a stored column, making the CLI's slug-fallback identical to the server's own derivation.

**Resolution**: `wire-captures.md` re-pinned to `7a8c550` with a re-verification note; the requirements Listing bullet, the get-report / reports-list steps, and the `ReportRun` wire struct now note that `report_filter` is always a full object (nil normalized server-side) so filter-less runs render with zero labels; and the requirements `report_type` bullet plus the `ReportRun.ReportType` comment now state it is a render-time slug derivation, so "owed by this story" means a deployed build predating the view, not a persisted field.
