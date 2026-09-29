// Small fetch helpers. All dashboard backend calls go through the ui
// function's own /api/* proxy (intercom via sidecar, same-owner only).
// api() resolves against the served base so both /f/ui and /f/ui/ work,
// including namespaced /f/<owner>/ui routes.
export function apiBase(): string {
  const p = window.location.pathname.replace(/\/$/, "");
  // Served as /f/<...>/ui[/...]; strip any trailing /api/... remainder.
  const idx = p.indexOf("/api/");
  return idx >= 0 ? p.slice(0, idx) : p;
}

export function api(path: string): string {
  const clean = path.startsWith("/") ? path : `/${path}`;
  if (clean.startsWith("/api/")) return `${apiBase()}${clean}`;
  return clean;
}

export async function getText(path: string): Promise<string> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}: ${await res.text()}`);
  return res.text();
}

export async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}: ${await res.text()}`);
  return res.json() as Promise<T>;
}

export async function postText(path: string, body: string): Promise<string> {
  const res = await fetch(path, { method: "POST", body });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}: ${await res.text()}`);
  return res.text();
}

export async function postJSON<T>(path: string, body?: string): Promise<T> {
  const res = await fetch(path, { method: "POST", body: body ?? "" });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}: ${await res.text()}`);
  const text = await res.text();
  try {
    return JSON.parse(text) as T;
  } catch {
    return text as unknown as T;
  }
}

// Invoke another own function through the ui backend intercom proxy:
// POST <base>/api/invoke/:fn[/subpath] forwards method/path/query/body.
export async function invokeOther(fn: string, opts?: { subpath?: string; query?: Record<string, string> }): Promise<string> {
  let p = `${apiBase()}/api/invoke/${encodeURIComponent(fn)}`;
  if (opts?.subpath) p += `/${opts.subpath.replace(/^\//, "")}`;
  if (opts?.query) {
    const qs = new URLSearchParams(opts.query).toString();
    if (qs) p += `?${qs}`;
  }
  return postText(p, "");
}

export interface FnInfo {
  Name: string;
  ActiveVersion: string;
}
