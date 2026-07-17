# Live PKCE end-to-end check

`pkce-live.mjs` proves the real `cc-data login` handshake against a locally
running report-service and a staging-portal test account. It is a dev-machine
test, never a CI gate. Subtask (a)'s definition of done includes this script
existing, being documented, and having passed once; it re-runs as each owed
server dependency lands, so the primary auth paths are eventually exercised
against a real server rather than certified only via the fake-server tests.

## Why this exists

The in-repo `internal/auth` integration tests drive the full login → auth status
--check → logout path against an `httptest` fake whose shapes are pinned to
[wire-captures.md](../../specs/REPORT-77-cc-data-cli/wire-captures.md). Only a
live handshake proves the CLI and the real controller agree on param encoding,
redirect timing, and browser behavior. Neither deployed report server serves
`/auth/cli` or `/api/v1/*` yet, so this script is the only pre-deploy
verification of the real flow.

## Prerequisites

1. The standard report-service dev environment: asdf toolchain, MySQL container,
   `.env` secrets from 1Password (see the report-service README). `dev.exs`
   already targets the staging portal with the `research-report-server` OAuth
   client, so no new portal-side setup is needed.
2. A staging-portal account with report access.
3. Node with Playwright installed (`npm i -D playwright && npx playwright install
   chromium`).
4. A built `cc-data` binary (`go build -o cc-data .`).

## Run

```bash
export CC_DATA_TEST_USER=<staging portal login>
export CC_DATA_TEST_PASSWORD=<staging portal password>
# optional overrides:
# export CC_DATA_BIN=./cc-data
# export CC_DATA_SERVER=http://localhost:4000
# export CC_DATA_PORTAL=learn.portal.staging.concord.org

node test/e2e/pkce-live.mjs
```

The script sets `CC_DATA_NO_BROWSER=1` so the CLI prints the auth URL without
opening a browser, then drives that URL with Playwright (portal login form, OAuth
approve), waits for the CLI to complete the token exchange, and asserts
`auth status --check`, `reports list`, and `logout` against the live server. It
uses a throwaway `HOME` so it never touches your real credentials.

On success it prints `LIVE PKCE CHECK PASSED` and exits 0.
