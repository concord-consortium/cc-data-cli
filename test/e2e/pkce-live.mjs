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
//   CC_DATA_HOME           throwaway HOME for isolated credentials (default: a temp dir)

import { spawn } from "node:child_process";
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
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
