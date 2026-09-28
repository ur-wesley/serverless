import { defineConfig } from "vite";
import solid from "vite-plugin-solid";

export default defineConfig({
  plugins: [solid()],
  // Absolute base: the app is always served under /f/ui/, so assets resolve
  // identically for /f/ui and /f/ui/ (relative URLs break without trailing slash).
  base: "/f/ui/",
  build: {
    outDir: "../dist",
    emptyOutDir: true,
    target: "esnext",
  },
});
