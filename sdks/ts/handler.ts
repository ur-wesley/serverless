// Thin TypeScript wrapper over gen/ts (protobuf-es + ConnectRPC types).
// Wire bodies are base64 (bytes fields in events.proto).

export interface HandlerEvent {
  method: string;
  path: string;
  headers: Record<string, string>;
  query: Record<string, string>;
  body: Uint8Array;
}

export interface HandlerCtx {
  functionName: string;
  version: string;
  requestId: string;
  deadlineMs: number;
  sidecarUrl: string;
}

export interface HandlerResponse {
  status?: number;
  headers?: Record<string, string>;
  body?: Uint8Array | string;
}

export type Handler = (ctx: HandlerCtx, event: HandlerEvent) => HandlerResponse | Promise<HandlerResponse>;

function b64encode(data: Uint8Array | string): string {
  const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

function b64decode(b64: string): Uint8Array {
  if (!b64) return new Uint8Array(0);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** Wrap a handler fn with the ABI server (GET /healthz, POST /invoke). */
export function defineHandler(handler: Handler) {
  return {
    async fetch(req: Request): Promise<Response> {
      const url = new URL(req.url);
      if (req.method === "GET" && url.pathname === "/healthz") {
        return new Response("ok", { status: 200 });
      }
      if (req.method === "POST" && url.pathname === "/invoke") {
        let payload: any;
        try {
          payload = await req.json();
        } catch {
          return Response.json({ status: 400, headers: {}, body: b64encode("bad invoke JSON") });
        }
        const ctx: HandlerCtx = {
          functionName: payload?.ctx?.function_name ?? "",
          version: payload?.ctx?.version ?? "",
          requestId: payload?.ctx?.request_id ?? "",
          deadlineMs: payload?.ctx?.deadline_ms ?? 0,
          sidecarUrl: payload?.ctx?.sidecar_url ?? process.env.ACTIONS_SIDECAR_URL ?? "",
        };
        const event: HandlerEvent = {
          method: payload?.event?.method ?? "",
          path: payload?.event?.path ?? "",
          headers: payload?.event?.headers ?? {},
          query: payload?.event?.query ?? {},
          body: b64decode(payload?.event?.body ?? ""),
        };
        const out = await handler(ctx, event);
        return Response.json({
          status: out.status ?? 200,
          headers: out.headers ?? {},
          body: b64encode(out.body ?? new Uint8Array(0)),
        });
      }
      return new Response("not found", { status: 404 });
    },
  };
}

/** Serve a defined handler on $PORT (default 3000). For src/index.ts: serve(defineHandler(myFn)). */
export function serve(handler: { fetch(req: Request): Promise<Response> }) {
  const port = Number(process.env.PORT ?? 3000);
  Bun.serve({ port, fetch: handler.fetch });
  console.log(`listening on ${port}`);
}
