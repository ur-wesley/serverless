import { createSignal } from "solid-js";

export default function Counter() {
  const [count, setCount] = createSignal(0);
  return (
    <div class="card">
      <h2>counter (local state)</h2>
      <div class="count">{count()}</div>
      <p>
        <button onClick={() => setCount(count() - 1)}>-</button>
        <button onClick={() => setCount(count() + 1)}>+</button>
      </p>
    </div>
  );
}
