import { createSignal, For, onMount } from "solid-js";
import { getJSON, FnInfo } from "../api";

interface LogLine {
  Function: string;
  Version: string;
  RequestID: string;
  Line: string;
  Time: string;
}

export default function LogsPanel() {
  const [fns, setFns] = createSignal<FnInfo[]>([]);
  const [fn, setFn] = createSignal("echo");
  const [out, setOut] = createSignal("pick a function and press refresh…");
  onMount(async () => {
    try {
      setFns(await getJSON<FnInfo[]>("/functions"));
    } catch {
      /* control plane unreachable; manual name still works */
    }
  });
  async function load() {
    setOut("loading…");
    try {
      const lines = await getJSON<LogLine[]>(`/logs?fn=${encodeURIComponent(fn())}`);
      setOut(
        lines.length === 0
          ? "(no log lines yet)"
          : lines.map((l) => `${l.Time} [${l.Version}/${l.RequestID}] ${l.Line}`).join("\n"),
      );
    } catch (e) {
      setOut(`error: ${e}`);
    }
  }
  return (
    <div class="card">
      <h2>function logs</h2>
      <p>
        <select value={fn()} onChange={(e) => setFn(e.target.value)}>
          <For each={fns()}>
            {(f) => <option value={f.Name}>{f.Name}</option>}
          </For>
        </select>
        <button onClick={load}>refresh logs</button>
      </p>
      <pre>{out()}</pre>
    </div>
  );
}
