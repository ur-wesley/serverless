#!/usr/bin/env node
// Launcher shim: exec the platform binary downloaded by postinstall.
"use strict";

const { spawnSync } = require("child_process");
const fs = require("fs");
const { binaryPath, unsupportedMessage } = require("../lib/asset");

function main() {
  const bin = binaryPath();
  if (!bin) {
    console.error(`[oort] ${unsupportedMessage()}`);
    process.exit(1);
  }
  if (!fs.existsSync(bin)) {
    console.error(
      `[oort] binary missing at ${bin}.\n` +
        `The postinstall download probably failed. Try:\n` +
        `  OORT_FORCE_DOWNLOAD=1 npm rebuild @ur-wesley/oort\n` +
        `or download manually from https://github.com/ur-wesley/oort/releases`
    );
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
