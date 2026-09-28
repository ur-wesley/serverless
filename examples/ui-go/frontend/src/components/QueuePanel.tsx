import { createSignal } from "solid-js";
import { getJSON, FnInfo } from "../api";
import { btn, btnBlue, card, h2, input, pre, row } from "../ui";

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
      setOut(`POST /pub/${topic()} -> ${res.status} ${res.statusText}\ncheck the logs tab for delivery.`);
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
    <div class={card}>
      <h2 class={h2}>publish queue event</h2>
      <p class={row}>
        <input class={`${input} w-1/3`} value={topic()} onInput={(e) => setTopic(e.target.value)} />
        <input class={`${input} w-1/3`} value={msg()} onInput={(e) => setMsg(e.target.value)} />
        <button class={btn} onClick={publish}>publish</button>
        <button class={btnBlue} onClick={subscribers}>list functions</button>
      </p>
      <pre class={`${pre} mt-2`}>{out()}</pre>
    </div>
  );
}
