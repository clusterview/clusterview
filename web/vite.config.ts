import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, API calls go to a clusterview binary running locally on :8080.
export default defineConfig({
  plugins: [react()],
  // Files load relative to the page, so the same build works at the root of
  // a host and under a path such as /clusterview/ (--base-path).
  base: "./",
  server: {
    proxy: { "/api": "http://localhost:8080" },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
