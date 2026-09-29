// Release versioning for Oort.
//
// Root package.json is the source of truth; `npx bumpp` bumps every
// lockstep manifest in one go. The release workflow fails the release
// unless all of these agree, so never bump them by hand:
//
//   package.json, sdks/ts/package.json (+ lockfile), npm/package.json,
//   internal/version/package.json (must stay a byte-exact copy of the root)
//
// Usage (from the repo root):
//   npx bumpp patch        # 0.1.1 -> 0.1.2
//   npx bumpp 0.2.0        # explicit version
// Both commit ("chore: release vX.Y.Z") and tag (vX.Y.Z) without pushing;
// push the tag deliberately to cut the release:
//   git push --follow-tags
import { defineConfig } from 'bumpp'

export default defineConfig({
  // First entry is the current-version source.
  files: [
    'package.json',
    'sdks/ts/package.json',
    'sdks/ts/package-lock.json',
    'npm/package.json',
    'internal/version/package.json',
  ],
  // Re-copy instead of trusting the in-place edit: the workflow enforces
  // `cmp package.json internal/version/package.json`. node (not cp) so this
  // works on Windows too.
  execute:
    "node -e \"require('node:fs').copyFileSync('package.json', 'internal/version/package.json')\"",
  commit: 'chore: release v{version}',
  tag: 'v{version}',
  // Pushing the tag starts the release workflow — keep that deliberate.
  push: false,
})
