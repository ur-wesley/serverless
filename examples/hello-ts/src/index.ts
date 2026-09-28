// Minimal ABI handler: GET /healthz -> 200, POST /invoke -> InvokeResponse.
// Hand-rolled JSON, no proto dependency. Wire bodies are base64 (bytes fields).
const port = Number(process.env.PORT ?? 3000);

function b64encode(s: string): string {
  return Buffer.from(s, "utf8").toString("base64");
}
function b64decode(b64: string): string {
  if (!b64) return "";
  return Buffer.from(b64, "base64").toString("utf8");
}

Bun.serve({
  port,
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
        return Response.json(
          { status: 400, headers: {}, body: b64encode("bad invoke JSON") },
          { status: 200 },
        );
      }
      const method = payload?.event?.method ?? "?";
      const path = payload?.event?.path ?? "?";
      const reqBody = b64decode(payload?.event?.body ?? "");
      const text = `hello from hello-ts ${method} ${path} body=${reqBody}`;
      return Response.json({
        status: 200,
        headers: { "content-type": "text/plain" },
        body: b64encode(text),
      });
    }
    return new Response("not found", { status: 404 });
  },
});

console.log(`hello-ts listening on ${port}`);
