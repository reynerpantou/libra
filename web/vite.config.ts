import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev: Vite serves the SPA on :5173 and proxies /api to the Go server on :8080.
// Build: output goes straight into the Go binary's embed directory.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": "http://localhost:8080" },
  },
  build: {
    outDir: "../internal/assets/dist",
    emptyOutDir: true,
  },
});
