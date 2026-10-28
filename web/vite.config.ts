import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, API calls go to a clusterview binary running locally on :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { "/api": "http://localhost:8080" },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
