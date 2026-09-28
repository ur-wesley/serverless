import { createSignal } from "solid-js";
import { getText } from "../api";

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
    <div class="card">
      <h2>whoami function</h2>
      <p>
        <button onClick={load}>ask whoami</button>
      </p>
      <pre>{out()}</pre>
    </div>
  );
}
