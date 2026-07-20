// Live PKCE end-to-end check: drives the real cc-data login handshake against a
// locally running report-service and a staging-portal test account, then asserts
// auth status --check, reports list, and logout against the live server.
//
// This is a dev-machine test, never CI. See README.md for prerequisites.
//
// Run:  node test/e2e/pkce-live.mjs
//
// Required env:
//   CC_DATA_BIN            path to the built cc-data binary (default: ./cc-data)
//   CC_DATA_SERVER         report-service origin (default: http://localhost:4000)
//   CC_DATA_PORTAL         staging portal host (default: learn.portal.staging.concord.org)
//   CC_DATA_TEST_USER      staging portal login (email/username)
//   CC_DATA_TEST_PASSWORD  staging portal password
//   CC_DATA_HOME           throwaway HOME that isolates credentials.json (default: a
//                          temp dir). NOTE: HOME does NOT isolate the OS keyring; the
//                          keyring entry is saved and restored separately (see below).

import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright";

const BIN = process.env.CC_DATA_BIN || "./cc-data";
const SERVER = process.env.CC_DATA_SERVER || "http://localhost:4000";
const PORTAL = process.env.CC_DATA_PORTAL || "learn.portal.staging.concord.org";
const USER = required("CC_DATA_TEST_USER");
const PASSWORD = required("CC_DATA_TEST_PASSWORD");
const HOME = process.env.CC_DATA_HOME || mkdtempSync(join(tmpdir(), "cc-data-e2e-"));

function required(name) {
  const v = process.env[name];
  if (!v) {
    console.error(`missing required env ${name}`);
    process.exit(2);
  }
  return v;
}

// Keyring isolation for the login step.
//
// The cc-data keyring is keyed by a hardcoded service name ("cc-data") plus the
// portal host as the account (internal/creds/keyring.go); HOME never enters the
// key. So the throwaway CC_DATA_HOME above isolates only credentials.json, NOT the
// OS keyring: a login for PORTAL overwrites the developer's real keychain entry for
// that same portal, and this test's final logout would then delete it. The service
// name is a Go const with no env override, and this test does not own that file, so
// rather than namespace the service we save any pre-existing keychain entry for
// (cc-data, PORTAL) before logging in and restore it in a finally block, leaving the
// developer's real keychain exactly as we found it. Supports macOS (security) and
// Linux (secret-tool); on other platforms or missing tools it degrades to a warning.
const KEYRING_SERVICE = "cc-data";

function keyringRead(account) {
  try {
    if (process.platform === "darwin") {
      const out = execFileSync(
        "/usr/bin/security",
        ["find-generic-password", "-s", KEYRING_SERVICE, "-a", account, "-w"],
        { encoding: "utf8" },
      );
      return { found: true, secret: out.replace(/\n$/, "") };
    }
    const out = execFileSync(
      "secret-tool",
      ["lookup", "service", KEYRING_SERVICE, "username", account],
      { encoding: "utf8" },
    );
    return { found: true, secret: out };
  } catch {
    // Non-zero exit means no entry (or the tool is unavailable); nothing to save.
    return { found: false, secret: "" };
  }
}

function keyringDelete(account) {
  try {
    if (process.platform === "darwin") {
      execFileSync(
        "/usr/bin/security",
        ["delete-generic-password", "-s", KEYRING_SERVICE, "-a", account],
        { stdio: "ignore" },
      );
    } else {
      execFileSync(
        "secret-tool",
        ["clear", "service", KEYRING_SERVICE, "username", account],
        { stdio: "ignore" },
      );
    }
  } catch {
    // Nothing to delete.
  }
}

function keyringRestore(account, saved) {
  try {
    if (saved.found) {
      if (process.platform === "darwin") {
        execFileSync(
          "/usr/bin/security",
          // -U updates the entry in place if it still exists.
          ["add-generic-password", "-s", KEYRING_SERVICE, "-a", account, "-w", saved.secret, "-U"],
          { stdio: "ignore" },
        );
      } else {
        execFileSync(
          "secret-tool",
          ["store", "--label=cc-data", "service", KEYRING_SERVICE, "username", account],
          { input: saved.secret, stdio: ["pipe", "ignore", "ignore"] },
        );
      }
    } else {
      // No entry existed before the test; ensure none is left behind.
      keyringDelete(account);
    }
  } catch (err) {
    console.error(`warning: could not restore keychain entry for ${account}: ${err}`);
  }
}

function run(args, { captureStderr = false } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(BIN, args, {
      env: { ...process.env, HOME, CC_DATA_NO_BROWSER: "1" },
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => (stdout += d));
    child.stderr.on("data", (d) => (stderr += d));
    if (captureStderr) child.stderr.on("data", (d) => process.stderr.write(d));
    child.on("close", (code) => resolve({ code, stdout, stderr }));
    child.on("error", reject);
  });
}

// Start login and resolve once the auth URL is printed to stderr, keeping the
// process running so it can complete the exchange after the browser step.
function startLogin() {
  return new Promise((resolve, reject) => {
    const child = spawn(BIN, ["login", "--server", SERVER, "--portal", PORTAL], {
      env: { ...process.env, HOME, CC_DATA_NO_BROWSER: "1" },
    });
    let stderr = "";
    let resolved = false;
    const done = new Promise((res) => child.on("close", (code) => res(code)));
    const timer = setTimeout(() => {
      if (!resolved) {
        child.kill(); // do not orphan a login waiting on its 5-minute callback
        reject(new Error("login did not print an auth URL in time"));
      }
    }, 30_000);
    child.stderr.on("data", (d) => {
      stderr += d;
      const m = stderr.match(/https?:\/\/\S*\/auth\/cli\?\S+/);
      if (m && !resolved) {
        resolved = true;
        clearTimeout(timer);
        resolve({ child, authURL: m[0], exit: done });
      }
    });
    child.on("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
  });
}

async function driveBrowser(authURL) {
  const browser = await chromium.launch();
  const page = await browser.newPage();
  await page.goto(authURL);
  // Staging portal login form. Selectors follow the standard portal login page.
  await page.fill('input[name="user[login]"]', USER);
  await page.fill('input[name="user[password]"]', PASSWORD);
  await page.click('input[type="submit"], button[type="submit"]');
  // OAuth approve, if an authorize screen appears.
  const approve = page.locator('input[name="commit"], button:has-text("Authorize")');
  if (await approve.count()) {
    await approve.first().click();
  }
  // The final redirect lands on the loopback "you can return to your terminal" page.
  await page.waitForLoadState("networkidle");
  await browser.close();
}

async function main() {
  console.log(`Using HOME=${HOME}, server=${SERVER}, portal=${PORTAL}`);

  // Save any real keychain entry for this portal before we clobber it; the throwaway
  // HOME cannot isolate the keyring (see the note above keyringRead).
  const savedEntry = keyringRead(PORTAL);
  try {
    const { authURL, exit } = await startLogin();
    console.log(`captured auth URL, driving browser`);
    await driveBrowser(authURL);
    const loginCode = await exit;
    if (loginCode !== 0) throw new Error(`login exited ${loginCode}`);
    console.log("login OK");

    const status = await run(["auth", "status", "--check", "--json"]);
    if (status.code !== 0) throw new Error(`auth status exited ${status.code}`);
    const parsed = JSON.parse(status.stdout);
    const portal = parsed.portals.find((p) => p.portal === PORTAL);
    if (!portal || !portal.valid) throw new Error(`token not valid: ${status.stdout}`);
    console.log("auth status --check OK");

    const reports = await run(["reports", "list", "--portal", PORTAL, "--json"]);
    if (reports.code !== 0) throw new Error(`reports list exited ${reports.code}`);
    JSON.parse(reports.stdout);
    console.log("reports list OK");

    const logout = await run(["logout", "--portal", PORTAL]);
    if (logout.code !== 0) throw new Error(`logout exited ${logout.code}`);
    console.log("logout OK");

    console.log("\nLIVE PKCE CHECK PASSED");
  } finally {
    // Restore the developer's real keychain entry (or ensure none is left behind),
    // even if the test failed partway and left a valid token in the keyring.
    keyringRestore(PORTAL, savedEntry);
    console.log("restored pre-existing keychain entry for the portal (if any)");
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
