// postinstall: download the matching prebuilt CLI binary from GitHub Releases
// and verify its sha256 against CHECKSUMS.txt.
"use strict";

const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const { assetName, unsupportedMessage } = require("./lib/asset");

const REPO = process.env.ACTIONS_CLI_GITHUB_REPO || "ur-wesley/serverless";

async function main() {
  const asset = assetName();
  if (!asset) {
    throw new Error(unsupportedMessage());
  }
  const version = require("./package.json").version;
  const dir = path.join(__dirname, "binaries");
  const dest = path.join(dir, asset);

  if (fs.existsSync(dest) && !process.env.SERVERLESS_FORCE_DOWNLOAD) {
    console.log(`[serverless] binary already present: ${dest}`);
    return;
  }

  const base = `https://github.com/${REPO}/releases/download/v${version}`;
  console.log(`[serverless] downloading ${asset} v${version} ...`);
  fs.mkdirSync(dir, { recursive: true });

  const binRes = await fetch(`${base}/${asset}`);
  if (!binRes.ok) {
    throw new Error(
      `Download failed: ${base}/${asset} (HTTP ${binRes.status}). ` +
        `Is v${version} released? Repo override: ACTIONS_CLI_GITHUB_REPO.`
    );
  }
  const buf = Buffer.from(await binRes.arrayBuffer());

  // Verify checksum when CHECKSUMS.txt is available (best-effort on mirrors).
  try {
    const sumRes = await fetch(`${base}/CHECKSUMS.txt`);
    if (sumRes.ok) {
      const text = await sumRes.text();
      const line = text.split("\n").find((l) => l.trim().endsWith(` ${asset}`) || l.trim().endsWith(`  ${asset}`));
      if (line) {
        const expected = line.trim().split(/\s+/)[0];
        const actual = crypto.createHash("sha256").update(buf).digest("hex");
        if (expected !== actual) {
          throw new Error(`Checksum mismatch for ${asset}: expected ${expected}, got ${actual}.`);
        }
        console.log("[serverless] checksum ok");
      }
    }
  } catch (err) {
    if (err && err.message && err.message.startsWith("Checksum mismatch")) throw err;
    console.warn(`[serverless] warning: checksum check skipped (${err.message})`);
  }

  fs.writeFileSync(dest, buf);
  if (process.platform !== "win32") {
    fs.chmodSync(dest, 0o755);
  }
  console.log(`[serverless] installed to ${dest}`);
}

main().catch((err) => {
  console.error(`[serverless] install failed: ${err.message}`);
  process.exitCode = 1;
});
