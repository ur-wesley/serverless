# @ur-wesley/oort

Prebuilt `oort` CLI (Oort binary platform) — downloads the matching
binary from GitHub Releases on install.

```sh
npm i -g @ur-wesley/oort
oort --help
```

Supported: `linux/x64`, `darwin/arm64` (Apple Silicon only), `win32/x64`.
Intel Macs and ARM Linux are not shipped — build from source instead:

```sh
go build -o oort ./cmd/oort
```

Version tracks the repo-root `package.json` (source of truth). Binaries come
from the `v<version>` GitHub Release (`oort-<os>-<arch>[.exe]` +
`CHECKSUMS.txt`); the installer verifies sha256.

Env overrides:

- `ACTIONS_CLI_GITHUB_REPO=owner/repo` — download from a fork.
- `OORT_FORCE_DOWNLOAD=1` — re-download on rebuild (`SERVERLESS_FORCE_DOWNLOAD` still works).

## Bun

Bun blocks `postinstall` scripts by default, so the binary download is
skipped on `bun install` / `bun add @ur-wesley/oort`. Two options:

1. Trust the package (one-time, in your project):
   ```sh
   bun pm trust @ur-wesley/oort
   bun install --trust
   ```
   or add to your project's `package.json`:
   ```json
   { "trustedDependencies": ["@ur-wesley/oort"] }
   ```
2. Do nothing — `oort` downloads the binary automatically on first run
   if it is missing (same checksum-verified download as postinstall).
