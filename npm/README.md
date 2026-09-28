# @ur-wesley/serverless

Prebuilt `actions` CLI (serverless binary platform) — downloads the matching
binary from GitHub Releases on install.

```sh
npm i -g @ur-wesley/serverless
actions --help
```

Supported: `linux/x64`, `darwin/arm64` (Apple Silicon only), `win32/x64`.
Intel Macs and ARM Linux are not shipped — build from source instead:

```sh
go build -o actions ./cmd/actions
```

Version tracks the repo-root `package.json` (source of truth). Binaries come
from the `v<version>` GitHub Release (`serverless-<os>-<arch>[.exe]` +
`CHECKSUMS.txt`); the installer verifies sha256.

Env overrides:

- `ACTIONS_CLI_GITHUB_REPO=owner/repo` — download from a fork.
- `SERVERLESS_FORCE_DOWNLOAD=1` — re-download on rebuild.
