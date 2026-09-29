# Serverless Binary Platform — Idea + Stack

## Idea

Single-node, easy-setup serverless platform similar to Cloudflare Workers, but polyglot via compiled binaries.

Any language that compiles to a Linux x86_64 binary runs if it speaks the ABI. No per-language runtime in production — only per-language builders at deploy time. Binaries run sandboxed with gVisor and scale to zero when idle.

### Core principles

- Binary is the unit of deployment.
- `POST /invoke + GET /healthz` is the only runtime contract.
- Sandboxed by default, stopped when not needed.
- Single `docker compose up` for the whole platform.
- SDKs auto-generated from Protobuf, never hand-maintained clients.

## Runtime model

- Artifact: static Linux binary at `/app/handler`.
- Env: `$PORT` (invoke server), `$ACTIONS_SIDECAR_URL` (KV/Blob/Queue/Log).
- ABI:
  - `GET /healthz` → 200 when ready (warm-pool check).
  - `POST /invoke` with `InvokeRequest { event, ctx }` (protojson) → `InvokeResponse { status, headers, body }`.
- Hand-rolling JSON without proto libs is allowed; proto is for codegen, not a hard runtime dependency.

### Sandbox (gVisor)

- `rootless Docker + gVisor runsc` via `RunnerBackend=docker-runsc` interface.
- Hardening per invocation container: `read-only rootfs`, `tmpfs /tmp`, `cap-drop ALL`, `no-new-privileges`, `memory 256MB`, `cpus 0.5`, isolated `actions-net`, egress deny by default (opt-in per function).
- Firecracker deferred to v2 for untrusted multi-tenant; interface allows swap without touching gateway/scheduler.

### Lifecycle (scale-to-zero)

- `0 → cold pull/start (healthz) → serve /invoke → idle TTL 60s → stop/rm`.
- Concurrency `0..N` per function, queue buffers bursts during cold start.
- Defaults: `10s timeout (max 60s), 256MB, 60s idle`. Kill on timeout.

## Stack

| Layer | Choice |
| --- | --- |
| Control plane | Go 1.27, stdlib `net/http` ServeMux, `slog`, `otel` (no chi/gin/echo) |
| Metadata DB | SQLite WAL (`modernc.org/sqlite`) + `sqlc`, optional `Litestream` backup |
| Artifacts + blobs | RustFS (S3-compat): `src.zip`, compiled `handler` binaries, user blobs |
| Queue / cron / KV | NATS JetStream: `invoke.queue`, `events.*`, `cron.tick`, NATS KV for small KV |
| Edge | Caddy → `controlplane:8080` |
| Sandbox | Docker + gVisor `runsc` |
| Builders | BuildKit per runtime: `builder-ts (bun build --compile)`, `builder-go (go build)`, `builder-rust`, `builder-zig`, … |
| IDL / SDK gen | Protobuf + buf v2 + ConnectRPC (`protoc-gen-go`, `protoc-gen-connect-go`, `protobuf-es`, `protoc-gen-connect-es`) |
| Compose | `controlplane, nats (file-store), rustfs, caddy` + `docker.sock` for builds/runs |

No Postgres (multi-node only), no MinIO (replaced by RustFS), no Redis (replaced by NATS), no chi.

## Triggers

- HTTP: `/f/:name/*` via gateway router.
- Cron: scheduler → JetStream delayed jobs → invoke.
- Queue: `env.Queue.publish(topic, msg)` → subscriber function.
- KV + blob: `env.KV.get/put/del` (NATS KV + SQLite meta), `env.Blob.put/get/url` (RustFS presigned via sidecar).

## Developer workflow

### SDKs (auto-generated)

- Source: `proto/actions/v1/` — `events.proto`, `gateway.proto`, `sidecar.proto`.
- `buf generate` → `gen/go/*`, `gen/ts/*`. Thin hand wrappers only: `defineHandler` (TS), `HandleFunc` (Go).
- `buf breaking` in CI blocks contract drift. Local `sidecar-mock` for offline dev.

### CLI from source

```sh
git clone <repo>
go run ./cmd/oort --help
# init, dev, deploy, invoke, logs, ls
```

- `oort init --runtime ts|go` scaffolds `actions.toml + src/`.
- `oort dev` runs SDK local server + proxies KV/Blob/Queue to dev control plane (or fully offline with mock).
- `oort deploy` zips `src/`, uploads to `/deploy`, polls build.
- `oort logs -f`, `oort invoke`, `oort ls`.

### Config (`actions.toml`)

```toml
name = "hello"
runtime = "ts" # | go | rust | zig
route = "/f/hello"
timeout_ms = 10000
memory_mb = 256
allow_egress = false
[[cron]] = "*/5 * * * *"
[[queue]] = "orders.created"
```

Flow: `deploy → validate → RustFS bundles/<name>/<ver>/src.zip → builder-<runtime> compiles → RustFS bundles/<name>/<ver>/handler + sha256 → SQLite version=active → runner pulls on next trigger`.

## Build order

1. ABI + gateway + docker-runsc runner (TS only).
2. Go/Rust builders + RustFS unify.
3. NATS cron/queue + sidecar + proto gen.
4. CLI + logs/metrics.
