import { Tabs } from "@kobalte/core";
import Counter from "./components/Counter";
import EchoCaller from "./components/EchoCaller";
import WhoamiPanel from "./components/WhoamiPanel";
import HeartbeatPanel from "./components/HeartbeatPanel";
import QueuePanel from "./components/QueuePanel";
import LogsPanel from "./components/LogsPanel";
import "./app.css";

const tabTrigger =
  "rounded-t-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-800 data-[selected]:bg-slate-900 data-[selected]:text-white data-[selected]:font-semibold";

const tabs = [
  { value: "counter", label: "Counter", el: <Counter /> },
  { value: "echo", label: "Echo", el: <EchoCaller /> },
  { value: "whoami", label: "Whoami", el: <WhoamiPanel /> },
  { value: "heartbeat", label: "Heartbeat", el: <HeartbeatPanel /> },
  { value: "queue", label: "Queue", el: <QueuePanel /> },
  { value: "logs", label: "Logs", el: <LogsPanel /> },
] as const;

export default function App() {
  return (
    <main class="mx-auto max-w-3xl px-4 pb-16 pt-8">
      <h1 class="text-xl font-bold">
        solidjs dashboard served by <span class="text-sky-400">fn=ui</span>
      </h1>
      <p class="mt-1 text-sm text-slate-400">
        Vite + Solid + Kobalte + Tailwind, embedded into the Go handler.
      </p>
      <Tabs defaultValue="counter" class="mt-4">
        <Tabs.List class="flex gap-1 border-b border-slate-700">
          {tabs.map((t) => (
            <Tabs.Trigger value={t.value} class={tabTrigger}>
              {t.label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        {tabs.map((t) => (
          <Tabs.Content value={t.value} class="pt-4">
            {t.el}
          </Tabs.Content>
        ))}
      </Tabs>
    </main>
  );
}
