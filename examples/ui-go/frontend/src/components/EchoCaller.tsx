import { createSignal } from "solid-js";
import { postText } from "../api";

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
    <div class="card">
      <h2>call the echo function</h2>
      <p>
        <input value={msg()} onInput={(e) => setMsg(e.target.value)} />
        <button onClick={call}>call /f/echo</button>
      </p>
      <pre>{out()}</pre>
    </div>
  );
}
