# ui-go — SolidJS dashboard example

Serves a Vite-built SolidJS single-page app from the Go handler
(`dist/` embedded via `go:embed`, served by invoke path).

Panels: local counter, echo caller, whoami inspector,
cron heartbeat (KV, auto-refresh), function logs.

## Build the frontend first

```sh
cd frontend
bun install
bun run build   # -> ../dist (relative asset URLs for /f/ui/)
```

`dist/` is committed so the example deploys out of the box;
rerun the build after changing `frontend/src`.

## Deploy

```sh
go run ./cmd/actions --url https://svr.w4y.io deploy --dir .
go run ./cmd/actions --url https://svr.w4y.io invoke ui/
# open https://svr.w4y.io/f/ui/
```

## Intercom (opt-in cross-function access)

The dashboard reads other functions through its own handler — the browser
never holds an operator token. `main.go` proxies `/api/*` via the sidecar
with `ACTIONS_SIDECAR_TOKEN`:

* `GET api/functions` → `POST /sidecar/functions/list` (own functions)
* `GET api/logs?fn=` → `POST /sidecar/logs/tail` (needs `allow_logs`)
* `POST api/pub/:topic` → `POST /sidecar/queue/publish`
* `POST api/invoke/:fn` → `POST /sidecar/invoke` (needs `allow_invoke`)

`actions.toml` opts in (default deny, same-owner only, `"*"` = all own):

```toml
allow_logs = ["*"]
allow_invoke = ["*"]
allow_kv = ["*"]
allow_blobs = ["*"]
```

Cross-owner is always 403, even with `"*"`. KV/blob sharing uses the same
`allow_kv` / `allow_blobs` lists.
