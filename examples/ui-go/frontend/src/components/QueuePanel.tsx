import { createSignal } from "solid-js";
import { getJSON, FnInfo } from "../api";

export default function QueuePanel() {
  const [topic, setTopic] = createSignal("orders.created");
  const [msg, setMsg] = createSignal("hello-queue");
  const [out, setOut] = createSignal("publishes to a topic; subscribed functions fire async.");
  async function publish() {
    setOut("publishing…");
    try {
      const res = await fetch(`/pub/${encodeURIComponent(topic())}`, {
        method: "POST",
        body: msg(),
      });
      setOut(`POST /pub/${topic()} -> ${res.status} ${res.statusText}\ncheck the logs panel for delivery.`);
    } catch (e) {
      setOut(`error: ${e}`);
    }
  }
  async function subscribers() {
    try {
      const fns = await getJSON<FnInfo[]>("/functions");
      setOut(`known functions: ${fns.map((f) => f.Name).join(", ")}\n(echo subscribes to orders.created)`);
    } catch (e) {
      setOut(`error: ${e}`);
    }
  }
  return (
    <div class="card">
      <h2>publish queue event</h2>
      <p>
        <input value={topic()} onInput={(e) => setTopic(e.target.value)} style={{ width: "35%" }} />
        <input value={msg()} onInput={(e) => setMsg(e.target.value)} />
        <button onClick={publish}>publish</button>
        <button onClick={subscribers}>list functions</button>
      </p>
      <pre>{out()}</pre>
    </div>
  );
}
