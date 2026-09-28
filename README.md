# Actions — single-node serverless binary platform

Any language that compiles to a Linux binary runs if it speaks the ABI:
`GET /healthz` → 200, `POST /invoke` (`{event, ctx}` protojson) → `{status, headers, body}`.

## Quickstart (dev on Docker host)

```sh
# 1. Scaffold
go run ./cmd/actions init --runtime go --name hello --dir ./hello
# 2. Run control plane (needs Docker; NATS optional — falls back to memory bus)
go run ./cmd/controlplane
# 3. Deploy / call / list (flags before positional args)
go run ./cmd/actions deploy --dir ./hello
go run ./cmd/actions invoke -d ping hello/hi
go run ./cmd/actions ls
go run ./cmd/actions logs hello
```

Offline handler dev (in-memory KV/Blob/Queue mock, no control plane):

```sh
go run ./cmd/actions dev --dir ./hello --offline
```

## Full platform

```sh
docker compose up            # controlplane + nats + rustfs + caddy
docker compose --profile backup up   # + Litestream SQLite backup to RustFS
```

Contracts: `proto/actions/v1/` (`buf lint` clean). Regenerate clients:
`buf generate` → `gen/go/*`, `gen/ts/*` (needs protoc-gen-go, protoc-gen-connect-go,
protoc-gen-es, protoc-gen-connect-es on PATH).

## Layout

`cmd/{controlplane,actions}/` · `internal/{gateway,runner,scheduler,deploy,store,artifacts,bus,sidecar,functions,builder,cli,devmock}/`
· `builders/builder-{ts,go}/` · `sdks/{go,ts}/` · `examples/hello-{ts,go}/` · `examples/echo-go/`

## Hardening (docker-runsc only, no Firecracker)

- Egress: `allow_egress=false` + `RUNNER_SANDBOX_NET` (compose) runs handlers on
  the `--internal` `actions-sandbox` net — no published ports, invoked via
  container DNS, external egress/DNS blocked by the daemon. Dev (unset) is
  warn-only shared-network mode. Per container: read-only rootfs, 64MB /tmp,
  cap-drop ALL, no-new-privs, mem+swap capped, 0.5 CPU, pids-limit 128,
  nofile 256, `actions-managed` labels (reaped on startup).
- Sidecar tokens: runner mints per-instance `ACTIONS_SIDECAR_TOKEN`; sidecar
  enforces it per `function_name` (403 otherwise). Queue publish needs any
  valid token; external producers use `POST /pub/:topic` (202).
- Durability: deploy persists `images/<img>.tar` in artifacts; runner
  `docker load`s it when the image is missing (daemon restart survival).
- Deploy: per-function serialization, zip caps (2000 files / 256MB total /
  64MB per file), zip-slip guard, `[[cron]]` hint (`cron = [...]`).
- Server: read/write timeouts, panic recovery, per-invoke access logs;
  cron uses SkipIfStillRunning; stable NATS durable consumers (no replays).
