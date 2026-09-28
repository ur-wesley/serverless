// Small fetch helpers. Absolute /f/... paths match production routing
// (https://svr.w4y.io/f/<name>).
export async function getText(path: string): Promise<string> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.text();
}

export async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.json() as Promise<T>;
}

export async function postText(path: string, body: string): Promise<string> {
  const res = await fetch(path, { method: "POST", body });
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
  return res.text();
}

export interface FnInfo {
  Name: string;
  ActiveVersion: string;
}
