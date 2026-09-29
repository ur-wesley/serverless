# @ur-wesley/oort-sdk

TypeScript SDK for Oort. Versioned in lockstep with
the CLI (`@ur-wesley/oort`) — every `v*` tag publishes both.

```ts
import { defineHandler, serve } from "@ur-wesley/oort-sdk/handler";
import { envFromEnv } from "@ur-wesley/oort-sdk/sidecar";

export default defineHandler(async (ctx, event) => {
  const env = envFromEnv();
  await env.log(ctx.functionName, ctx.version, ctx.requestId, "hello");
  return { status: 200, body: "ok" };
});
```

Two entry points:

- `@ur-wesley/oort-sdk/handler` — `defineHandler` / `serve` wrap a
  function with the platform ABI (`GET /healthz`, `POST /invoke`).
- `@ur-wesley/oort-sdk/sidecar` — `envFromEnv()` client for KV, blobs,
  queue publish, logs, and function intercom. `mockEnv()` covers offline dev.
