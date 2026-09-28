import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  // Relative asset URLs so the app works mounted under /f/ui/ (not domain root).
  base: "./",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    target: "esnext",
  },
});
