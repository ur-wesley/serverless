#!/usr/bin/env node
// Launcher shim: exec the platform binary downloaded by postinstall.
"use strict";

const { spawnSync } = require("child_process");
const fs = require("fs");
const path = require("path");
const { binaryPath, unsupportedMessage } = require("../lib/asset");

function ensureBinary(bin) {
  if (fs.existsSync(bin)) return true;
  // postinstall may have been skipped (e.g. Bun blocks lifecycle scripts
  // unless the package is trusted). Self-heal by downloading on first run:
  // a direct `node install.js` invocation is not subject to the block.
  console.log("[oort] binary missing, downloading ...");
  const installer = path.join(__dirname, "..", "install.js");
  const res = spawnSync(process.execPath, [installer], { stdio: "inherit" });
  if (res.error) {
    console.error(`[oort] download failed to launch: ${res.error.message}`);
    return false;
  }
  if (res.status !== 0) return false;
  return fs.existsSync(bin);
}

function installHint() {
  return (
    `The postinstall download probably failed or was skipped.\n` +
    `Try:\n` +
    `  OORT_FORCE_DOWNLOAD=1 npm rebuild @ur-wesley/oort\n` +
    `Bun blocks postinstall scripts by default; either trust this package:\n` +
    `  bun pm trust @ur-wesley/oort\n` +
    `  bun install --trust\n` +
    `or add to your project's package.json:\n` +
    `  { "trustedDependencies": ["@ur-wesley/oort"] }\n` +
    `or download manually from https://github.com/ur-wesley/serverless/releases`
  );
}

function main() {
  const bin = binaryPath();
  if (!bin) {
    console.error(`[oort] ${unsupportedMessage()}`);
    process.exit(1);
  }
  if (!ensureBinary(bin)) {
    console.error(`[oort] binary missing at ${bin}.\n` + installHint());
    process.exit(1);
  }
  const res = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
  if (res.error) {
    console.error(`[oort] failed to launch: ${res.error.message}`);
    process.exit(1);
  }
  process.exit(res.status == null ? 1 : res.status);
}

main();
