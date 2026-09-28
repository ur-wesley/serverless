import { createSignal } from "solid-js";
import { getText } from "../api";
import { btn, card, h2, pre } from "../ui";

export default function WhoamiPanel() {
  const [out, setOut] = createSignal("press the button…");
  async function load() {
    setOut("calling…");
    try {
      setOut(await getText("/f/whoami/"));
    } catch (e) {
      setOut(`error: ${e}`);
    }
  }
  return (
    <div class={card}>
      <h2 class={h2}>whoami function</h2>
      <p>
        <button class={btn} onClick={load}>ask whoami</button>
      </p>
      <pre class={`${pre} mt-2`}>{out()}</pre>
    </div>
  );
}
