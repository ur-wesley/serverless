// Shared platform → release asset mapping for @ur-wesley/oort.
// Release assets (built by .github/workflows/release-cli.yml):
//   oort-linux-x64        (linux / x64)
//   oort-darwin-arm64     (macOS Apple Silicon only)
//   oort-win-x64.exe      (windows / x64)
"use strict";

const path = require("path");

function assetName(platform = process.platform, arch = process.arch) {
  if (platform === "linux" && arch === "x64") return "oort-linux-x64";
  if (platform === "darwin" && arch === "arm64") return "oort-darwin-arm64";
  if (platform === "win32" && arch === "x64") return "oort-win-x64.exe";
  return null;
}

function unsupportedMessage(platform = process.platform, arch = process.arch) {
  if (platform === "darwin" && arch === "x64") {
    return (
      `Unsupported platform: darwin/x64 (Intel Mac). ` +
      `This package ships macOS Apple Silicon (arm64) only. ` +
      `Options: run on an arm64 Mac, use the linux-x64 binary in Docker, or build from source: go build ./cmd/oort`
    );
  }
  return (
    `Unsupported platform: ${platform}/${arch}. ` +
    `Supported: linux/x64, darwin/arm64 (Apple Silicon), win32/x64. ` +
    `Or build from source: go build ./cmd/oort`
  );
}

function binaryPath(platform = process.platform, arch = process.arch) {
  const asset = assetName(platform, arch);
  if (!asset) return null;
  return path.join(__dirname, "..", "binaries", asset);
}

module.exports = { assetName, binaryPath, unsupportedMessage };
