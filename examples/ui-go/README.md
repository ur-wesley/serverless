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
