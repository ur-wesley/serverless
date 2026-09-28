# Actions — single-node serverless binary platform

Any language that compiles to a Linux binary runs if it speaks the ABI:
`GET /healthz` → 200, `POST /invoke` (`{event, ctx}` protojson) → `{status, headers, body}`.

## Install (prebuilt CLI)

```sh
npm i -g @ur-wesley/serverless
actions --help
```

Ships `linux/x64`, `macOS Apple Silicon (arm64)`, `windows/x64` binaries from
the `v<version>` GitHub Release. Other platforms: `go build -o actions ./cmd/actions`.
Releases are cut by pushing a `v*` tag matching root `package.json`
(`.github/workflows/release-cli.yml` builds, publishes the release, and
auto-publishes the npm wrapper).

## Quickstart (dev on Docker host)

```sh
# 1. Scaffold
go run ./cmd/actions init --runtime go --name hello --dir ./hello
# 2. Log in (first login creates your account via browser verification)
go run ./cmd/actions login
# 3. Run control plane (needs Docker; NATS optional — falls back to memory bus)
go run ./cmd/controlplane
# 4. Deploy / call / list (URL + token come from the saved login)
go run ./cmd/actions deploy --dir ./hello   # enqueues, streams build log, waits
go run ./cmd/actions deploy --dir ./hello --no-wait  # enqueue only
go run ./cmd/actions jobs hello             # deploy history + build status
go run ./cmd/actions invoke -d ping hello/hi
go run ./cmd/actions ls
go run ./cmd/actions logs hello
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
go run ./cmd/actions keys create hello --name ci   # prints ak_... once
go run ./cmd/actions keys ls hello
go run ./cmd/actions keys revoke <id>
go run ./cmd/actions invoke --api-key ak_... hello/hi
```

Each app can have multiple keys; only `sha256` hashes are stored.
Functions are user-scoped: `/f/<you>/<app>/...` namespace routing plus a
random public slug per function (`/s/<slug>/...`) for sharing.

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
