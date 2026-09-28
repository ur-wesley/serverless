import { createSignal } from "solid-js";
import { btn, btnBlue, card, h2 } from "../ui";

export default function Counter() {
  const [count, setCount] = createSignal(0);
  return (
    <div class={card}>
      <h2 class={h2}>counter (local state)</h2>
      <div class="text-3xl font-bold">{count()}</div>
      <p class="mt-2 flex gap-2">
        <button class={btnBlue} onClick={() => setCount(count() - 1)}>-</button>
        <button class={btn} onClick={() => setCount(count() + 1)}>+</button>
      </p>
    </div>
  );
}
