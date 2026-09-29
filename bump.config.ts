// Release versioning for Oort.
//
// Root package.json is the source of truth; `npx bumpp` bumps every
// lockstep manifest in one go. The release workflow fails the release
// unless all of these agree, so never bump them by hand:
//
//   package.json, sdks/ts/package.json (+ lockfile), sdks/rust/Cargo.toml,
//   npm/package.json, internal/version/package.json (byte-exact root copy)
//
// Usage (from the repo root):
//   npx bumpp patch        # 0.1.1 -> 0.1.2
//   npx bumpp 0.2.0        # explicit version
// Both commit ("chore: release vX.Y.Z") and tag (vX.Y.Z) without pushing;
// push the tag deliberately to cut the release:
//   git push --follow-tags
import { execFileSync } from 'node:child_process'
import { copyFileSync } from 'node:fs'
import { defineConfig } from 'bumpp'

export default defineConfig({
  // First entry is the current-version source.
  files: [
    'package.json',
    'sdks/ts/package.json',
    'sdks/ts/package-lock.json',
    'sdks/rust/Cargo.toml',
    'npm/package.json',
    'internal/version/package.json',
  ],
  // Sync the embedded version and Rust lockfiles after bumping their manifest.
  execute: () => {
    copyFileSync('package.json', 'internal/version/package.json')
    for (const manifest of ['sdks/rust/Cargo.toml', 'examples/hello-rust/Cargo.toml']) {
      execFileSync('cargo', ['update', '--manifest-path', manifest, '--package', 'oort-sdk', '--offline'], {
        stdio: 'inherit',
      })
    }
  },
  commit: 'chore: release v%s',
  tag: 'v%s',
  // Pushing the tag starts the release workflow — keep that deliberate.
  push: false,
})
