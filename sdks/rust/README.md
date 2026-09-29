# oort-sdk

Rust SDK for Oort. Versioned in lockstep with the CLI (`@ur-wesley/oort`)
and the other SDKs — every `v*` tag publishes them together.

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

- `serve` / `router` wrap a handler with the platform ABI
  (`GET /healthz`, `POST /invoke`). `router` is there for embedding and tests.
- `Env::from_env()` clients KV, blobs, queue publish, logs, and function
  intercom from `ACTIONS_SIDECAR_URL` / `ACTIONS_SIDECAR_TOKEN`.
  `MockEnv` covers offline dev (`oort dev --offline`).
