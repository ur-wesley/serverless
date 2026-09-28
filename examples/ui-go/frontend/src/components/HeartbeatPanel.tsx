import { createSignal, onCleanup, onMount } from "solid-js";
import { getJSON } from "../api";

interface Beat {
  fn: string;
  beats: string;
  last_beat: string;
  hint: string;
}

export default function HeartbeatPanel() {
  const [beat, setBeat] = createSignal<Beat | null>(null);
  const [error, setError] = createSignal("");
  async function load() {
    try {
      setBeat(await getJSON<Beat>("/f/heartbeat/"));
      setError("");
    } catch (e) {
      setError(`error: ${e}`);
    }
  }
  let timer: number | undefined;
  onMount(() => {
    load();
    timer = window.setInterval(load, 30000);
  });
  onCleanup(() => window.clearInterval(timer));
  return (
    <div class="card">
      <h2>cron heartbeat (KV)</h2>
      <p>
        <button onClick={load}>refresh</button>
        <span class="muted"> auto-refreshes every 30s</span>
      </p>
      {error() ? (
        <pre>{error()}</pre>
      ) : beat() ? (
        <pre>{`beats: ${beat()!.beats}\nlast:  ${beat()!.last_beat || "-"}\n${beat()!.hint}`}</pre>
      ) : (
        <pre>loading…</pre>
      )}
    </div>
  );
}
