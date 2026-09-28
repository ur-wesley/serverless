import { createSignal } from "solid-js";
import { postText } from "../api";
import { btn, card, h2, input, pre, row } from "../ui";

export default function EchoCaller() {
  const [msg, setMsg] = createSignal("hello from the browser");
  const [out, setOut] = createSignal("press the button…");
  async function call() {
    setOut("calling…");
    try {
      setOut(await postText("/f/echo/call", msg()));
    } catch (e) {
      setOut(`error: ${e}`);
    }
  }
  return (
    <div class={card}>
      <h2 class={h2}>call the echo function</h2>
      <p class={row}>
        <input class={`${input} w-3/5`} value={msg()} onInput={(e) => setMsg(e.target.value)} />
        <button class={btn} onClick={call}>call /f/echo</button>
      </p>
      <pre class={`${pre} mt-2`}>{out()}</pre>
    </div>
  );
}
