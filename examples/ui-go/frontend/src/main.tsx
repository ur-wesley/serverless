import { render } from "solid-js/web";
import App from "./App";

const el = document.getElementById("app")!;
// Drop the static "loading…" placeholder so it never lingers above the app.
el.innerHTML = "";
render(() => <App />, el);
