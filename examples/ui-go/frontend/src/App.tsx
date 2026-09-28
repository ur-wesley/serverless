import { Content, List, Root, Trigger } from "@kobalte/core/tabs";
import Counter from "./components/Counter";
import EchoCaller from "./components/EchoCaller";
import WhoamiPanel from "./components/WhoamiPanel";
import HeartbeatPanel from "./components/HeartbeatPanel";
import QueuePanel from "./components/QueuePanel";
import LogsPanel from "./components/LogsPanel";
import "./app.css";

const tabTrigger =
  "rounded-t-md px-4 py-2 text-sm text-slate-300 hover:bg-slate-800 data-[selected]:bg-slate-900 data-[selected]:text-white data-[selected]:font-semibold";

export default function App() {
  return (
    <main class="mx-auto max-w-3xl px-4 pb-16 pt-8">
      <h1 class="text-xl font-bold">
        solidjs dashboard served by <span class="text-sky-400">fn=ui</span>
      </h1>
      <p class="mt-1 text-sm text-slate-400">
        Vite + Solid + Kobalte + Tailwind, embedded into the Go handler.
      </p>
      <Root defaultValue="counter" class="mt-4">
        <List class="flex gap-1 border-b border-slate-700">
          <Trigger value="counter" class={tabTrigger}>Counter</Trigger>
          <Trigger value="echo" class={tabTrigger}>Echo</Trigger>
          <Trigger value="whoami" class={tabTrigger}>Whoami</Trigger>
          <Trigger value="heartbeat" class={tabTrigger}>Heartbeat</Trigger>
          <Trigger value="queue" class={tabTrigger}>Queue</Trigger>
          <Trigger value="logs" class={tabTrigger}>Logs</Trigger>
        </List>
        <Content value="counter" class="pt-4"><Counter /></Content>
        <Content value="echo" class="pt-4"><EchoCaller /></Content>
        <Content value="whoami" class="pt-4"><WhoamiPanel /></Content>
        <Content value="heartbeat" class="pt-4"><HeartbeatPanel /></Content>
        <Content value="queue" class="pt-4"><QueuePanel /></Content>
        <Content value="logs" class="pt-4"><LogsPanel /></Content>
      </Root>
    </main>
  );
}
