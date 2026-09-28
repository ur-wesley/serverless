// Sidecar Env client (fetch-based) + in-memory mock for offline dev.
// Mirrors internal/sidecar routes; field-compatible with sidecar.proto.

export interface Env {
  kvGet(fn: string, key: string): Promise<{ value: Uint8Array; found: boolean }>;
  kvPut(fn: string, key: string, value: Uint8Array | string, ttlSeconds?: number): Promise<void>;
  kvDel(fn: string, key: string): Promise<void>;
  blobPutBytes(fn: string, key: string, data: Uint8Array | string): Promise<void>;
  blobGetBytes(fn: string, key: string): Promise<Uint8Array>;
  queuePublish(topic: string, message: Uint8Array | string): Promise<void>;
  log(fn: string, version: string, requestId: string, line: string): Promise<void>;
  logsTail(target: string, limit?: number): Promise<LogLine[]>;
  functionsList(): Promise<string[]>;
  invokeOther(target: string, method: string, path: string, body?: Uint8Array | string, headers?: Record<string, string>, query?: Record<string, string>): Promise<{ status: number; headers: Record<string, string>; body: Uint8Array }>;
}

export interface LogLine {
  Function: string;
  Version: string;
  RequestID: string;
  Line: string;
  Time: string;
}

const enc = new TextEncoder();
const dec = new TextDecoder();

function bytes(v: Uint8Array | string): Uint8Array {
  return typeof v === "string" ? enc.encode(v) : v;
}
function b64encode(v: Uint8Array | string): string {
  return Buffer.from(bytes(v)).toString("base64");
}
function b64decode(b64: string): Uint8Array {
  return b64 ? new Uint8Array(Buffer.from(b64, "base64")) : new Uint8Array(0);
}

export function envFromEnv(): Env {
  const base = process.env.ACTIONS_SIDECAR_URL ?? "";
  return new HttpEnv(base);
}

class HttpEnv implements Env {
  constructor(private base: string) {}
  private token(): string {
    return process.env.ACTIONS_SIDECAR_TOKEN ?? "";
  }
  private async post(path: string, body: unknown): Promise<any> {
    const headers: Record<string, string> = { "content-type": "application/json" };
    if (this.token()) headers["x-actions-token"] = this.token();
    const res = await fetch(this.base + path, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
    return res.json();
  }
  async kvGet(fn: string, key: string) {
    const out = await this.post("/sidecar/kv/get", { function_name: fn, key });
    return { value: b64decode(out.value), found: out.found };
  }
  async kvPut(fn: string, key: string, value: Uint8Array | string, ttlSeconds = 0) {
    await this.post("/sidecar/kv/put", { function_name: fn, key, value: b64encode(value), ttl_seconds: ttlSeconds });
  }
  async kvDel(fn: string, key: string) {
    await this.post("/sidecar/kv/del", { function_name: fn, key });
  }
  async blobPutBytes(fn: string, key: string, data: Uint8Array | string) {
    const { url } = await this.post("/sidecar/blob/put-url", { function_name: fn, key });
    const headers: Record<string, string> = {};
    if (this.token()) headers["x-actions-token"] = this.token();
    const res = await fetch(url, { method: "PUT", headers, body: bytes(data) as BodyInit });
    if (!res.ok) throw new Error(`blob put: ${res.status}`);
  }
  async blobGetBytes(fn: string, key: string): Promise<Uint8Array> {
    const { url } = await this.post("/sidecar/blob/get-url", { function_name: fn, key });
    const headers: Record<string, string> = {};
    if (this.token()) headers["x-actions-token"] = this.token();
    const res = await fetch(url, { headers });
    if (!res.ok) throw new Error(`blob get: ${res.status}`);
    return new Uint8Array(await res.arrayBuffer());
  }
  async queuePublish(topic: string, message: Uint8Array | string) {
    await this.post("/sidecar/queue/publish", { topic, message: b64encode(message) });
  }
  async log(fn: string, version: string, requestId: string, line: string) {
    await this.post("/sidecar/log/append", { function_name: fn, version, request_id: requestId, line });
  }
  async logsTail(target: string, limit = 100): Promise<LogLine[]> {
    const out = await this.post("/sidecar/logs/tail", { target_function: target, limit });
    return (out.lines ?? []) as LogLine[];
  }
  async functionsList(): Promise<string[]> {
    const out = await this.post("/sidecar/functions/list", {});
    return (out.functions ?? []) as string[];
  }
  async invokeOther(target: string, method: string, path: string, body: Uint8Array | string = new Uint8Array(0), headers: Record<string, string> = {}, query: Record<string, string> = {}) {
    const out = await this.post("/sidecar/invoke", {
      target_function: target, method, path, headers, query,
      body: b64encode(body),
    });
    return { status: out.status as number, headers: (out.headers ?? {}) as Record<string, string>, body: b64decode(out.body ?? "") };
  }
}

/** In-memory mock for `actions dev --offline`. */
export function mockEnv(): Env & { published: { topic: string; message: Uint8Array }[]; logs: string[] } {
  const kv = new Map<string, Uint8Array>();
  const blobs = new Map<string, Uint8Array>();
  const published: { topic: string; message: Uint8Array }[] = [];
  const logs: string[] = [];
  return {
    published,
    logs,
    async kvGet(fn: string, key: string) {
      const v = kv.get(`${fn}\0${key}`);
      return { value: v ?? new Uint8Array(0), found: v !== undefined };
    },
    async kvPut(fn: string, key: string, value: Uint8Array | string) {
      kv.set(`${fn}\0${key}`, bytes(value));
    },
    async kvDel(fn: string, key: string) {
      kv.delete(`${fn}\0${key}`);
    },
    async blobPutBytes(fn: string, key: string, data: Uint8Array | string) {
      blobs.set(`${fn}\0${key}`, bytes(data));
    },
    async blobGetBytes(fn: string, key: string) {
      const v = blobs.get(`${fn}\0${key}`);
      if (!v) throw new Error("not found");
      return v;
    },
    async queuePublish(topic: string, message: Uint8Array | string) {
      published.push({ topic, message: bytes(message) });
    },
    async log(_fn: string, _v: string, _r: string, line: string) {
      logs.push(line);
    },
    async logsTail(_target: string, _limit = 100): Promise<LogLine[]> {
      return [];
    },
    async functionsList(): Promise<string[]> {
      return [];
    },
    async invokeOther(_t: string, _m: string, _p: string) {
      throw new Error("invokeOther not supported offline");
    },
  };
}

export { dec as utf8Decoder, enc as utf8Encoder };
