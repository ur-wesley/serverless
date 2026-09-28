import Counter from "./components/Counter";
import EchoCaller from "./components/EchoCaller";
import WhoamiPanel from "./components/WhoamiPanel";
import HeartbeatPanel from "./components/HeartbeatPanel";
import LogsPanel from "./components/LogsPanel";
import "./app.css";

export default function App() {
  return (
    <main>
      <h1>
        solidjs dashboard served by <span>fn=ui</span>
      </h1>
      <p class="muted">
        Built with <code>bun run build</code> (vite + solid), embedded into the Go handler.
      </p>
      <Counter />
      <EchoCaller />
      <WhoamiPanel />
      <HeartbeatPanel />
      <LogsPanel />
    </main>
  );
}
