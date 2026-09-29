# Oort — single-node binary platform

Any language that compiles to a Linux binary runs if it speaks the ABI:
`GET /healthz` → 200, `POST /invoke` (`{event, ctx}` protojson) → `{status, headers, body}`.

## Install (prebuilt CLI)

```sh
npm i -g @ur-wesley/oort
oort --help
```

Ships `linux/x64`, `macOS Apple Silicon (arm64)`, `windows/x64` binaries from
the `v<version>` GitHub Release. Other platforms: `go build -o oort ./cmd/oort`.
Releases are cut by pushing a `v*` tag matching root `package.json`
(`.github/workflows/release-cli.yml` builds, publishes the release,
auto-publishes the npm wrapper, and auto-publishes the SDKs in lockstep:
`@ur-wesley/oort-sdk` on npm and `github.com/ur-wesley/oort/sdks/go`
via the same tag).
Bump with `npx bumpp [patch|minor|major|<version>]` (`bump.config.ts` keeps
every manifest in lockstep; commits + tags, does not push).

## Quickstart (dev on Docker host)

```sh
# 1. Scaffold
go run ./cmd/oort init --runtime go --name hello --dir ./hello
# 2. Log in (first login creates your account via browser verification)
go run ./cmd/oort login
# 3. Run control plane (needs Docker; NATS optional — falls back to memory bus)
go run ./cmd/controlplane
# 4. Deploy / call / list (URL + token come from the saved login)
go run ./cmd/oort deploy --dir ./hello   # enqueues, streams build log, waits
go run ./cmd/oort deploy --dir ./hello --no-wait  # enqueue only
go run ./cmd/oort jobs hello             # deploy history + build status
go run ./cmd/oort invoke -d ping hello/hi
go run ./cmd/oort ls
go run ./cmd/oort logs hello
```

Versions are semver: first deploy is `0.1.0`, then patch auto-bumps.
Pin with `actions.toml: version = "1.2.3"` or `bump = "major"|"minor"|"patch"`
(mutually exclusive); explicit versions must exceed the active one.
`GET /versions?fn=`, `POST /rollback`, `GET /deploys?fn=` / `GET /deploys/:id[/logs]`
cover history, rollback, and build logs.

Auth: `login` asks for the control plane URL, prints a verification link
(`open it in a browser; the first visit creates the first account, later
visits bind the CLI to your own account — no second-person approval`),
then saves URL + token to `~/.config/actions/auth.json` (`0600`).
`logout` / `whoami` / `config get|set url` manage it. Operator routes
(`deploy`, `ls`, `logs`, `/pub/*`, `/keys`) require the token; invoke is
open by default.

Per-function protection (`actions.toml: auth_mode = "public"|"key"|"private"`):

```sh
go run ./cmd/oort keys create hello --name ci   # prints ak_... once
go run ./cmd/oort keys ls hello
go run ./cmd/oort keys revoke <id>
go run ./cmd/oort invoke --api-key ak_... hello/hi
```

Each app can have multiple keys; only `sha256` hashes are stored.
Functions are user-scoped: `/f/<you>/<app>/...` namespace routing plus a
random public slug per function (`/s/<slug>/...`) for sharing.

Display: on a TTY the CLI uses color, tables and spinners; pipes and CI
automatically get plain text. `--plain` (or `NO_COLOR=1`) forces plain
output, `-o json` gives JSON for `ls`, `jobs`, `keys ls`, `whoami`,
`logs` and `invoke`, and `oort completion [bash|zsh|fish|powershell]`
emits shell completion.

Offline handler dev = `bun run dev` equivalent (in-memory KV/Blob/Queue
mock, no control plane; stable local URL + auto-reload):

```sh
go run ./cmd/oort dev --dir ./hello --port 3000 --watch
# open http://localhost:3000/f/hello/hi — any path becomes event.path
# via POST /invoke; edit src to restart the handler, gateway stays up
```

## SDKs

`sdks/{go,ts,rust}/` are thin wrappers over the ABI — no proto dependency in
handlers. Any language works raw (`GET /healthz` → 200, `POST /invoke` →
`{status, headers, body}`, bodies base64); see `examples/hello-ts/src/index.ts`
and `examples/hello-go/main.go` for hand-rolled JSON. TS details:
`sdks/ts/README.md`, Rust details: `sdks/rust/README.md`.

```sh
npm i @ur-wesley/oort-sdk   # TS
cargo add oort-sdk          # Rust
```

```ts
import { defineHandler, serve } from "@ur-wesley/oort-sdk/handler";
import { envFromEnv } from "@ur-wesley/oort-sdk/sidecar";

export default defineHandler(async (ctx, event) => {
  const env = envFromEnv();
  await env.log(ctx.functionName, ctx.version, ctx.requestId, "hello");
  return { status: 200, body: "ok" };
});
```

```go
import sdk "github.com/ur-wesley/oort/sdks/go"

func main() {
  sdk.HandleFunc(func(ctx context.Context, c sdk.Ctx, e sdk.Event) sdk.Response {
    env := sdk.FromEnv()
    _ = env.Logf(ctx, c.FunctionName, c.Version, c.RequestID, "hello")
    return sdk.Response{Status: 200, Body: []byte("ok")}
  })
}
```

```rust
use oort_sdk::{serve, Ctx, Env, Event, Response};

#[tokio::main]
async fn main() {
    serve(|ctx: Ctx, _event: Event| async move {
        let env = Env::from_env();
        let _ = env.log(&ctx.function_name, &ctx.version, &ctx.request_id, "hello").await;
        Response::ok("ok")
    })
    .await;
}
```

* Handler: TS `defineHandler` / `serve` (`sdks/ts/handler.ts`), Go
  `sdk.HandleFunc` / `sdk.NewHandler(h)` for embedding/tests (`sdks/go/sdk.go`),
  Rust `oort_sdk::serve` / `oort_sdk::router` (`sdks/rust/src/lib.rs`).
* Sidecar client: TS `envFromEnv()` (`sdks/ts/sidecar.ts`), Go `sdk.FromEnv()`,
  Rust `Env::from_env()`.
  Config comes from `ACTIONS_SIDECAR_URL` / `ACTIONS_SIDECAR_TOKEN`
  (auto-injected; also on `ctx.sidecarUrl` / `c.SidecarURL` / `ctx.sidecar_url`).
  `mockEnv()` (TS) / `MockEnv` (Rust) covers `oort dev --offline`.
* API (same all SDKs): `KVGet/Put/Del`, `BlobPutBytes/GetBytes`,
  `QueuePublish`, `Log`, `LogsTail` + `FunctionsList` + `InvokeOther`
  (intercom: needs `allow_*` + same owner).

## Full platform

```sh
docker compose up            # controlplane + nats + rustfs + caddy
docker compose --profile backup up   # + Litestream SQLite backup to RustFS
```

Contracts: `proto/actions/v1/` (`buf lint` clean). Regenerate clients:
`buf generate` → `gen/go/*`, `gen/ts/*` (needs protoc-gen-go, protoc-gen-connect-go,
protoc-gen-es, protoc-gen-connect-es on PATH).

## Layout

`cmd/{controlplane,oort}/` · `internal/{gateway,runner,scheduler,deploy,store,artifacts,bus,sidecar,functions,builder,cli,devmock}/`
· `builders/builder-{ts,go}/` · `sdks/{go,ts,rust}/` · `examples/hello-{ts,go,rust}/` · `examples/echo-go/`

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
