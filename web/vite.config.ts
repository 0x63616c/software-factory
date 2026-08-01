import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// No fixed port yet: the API has no deployed Service (that's the next ticket,
// ADR-0012 "Repository layout"), so this is a local-dev convenience only.
const apiPort = process.env.API_PORT ?? "8080";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": resolve(__dirname, "src"),
    },
  },
  server: {
    host: true,
    // Clear of apps/web (4200), api (4201) and manage (4210).
    port: 4211,
    proxy: {
      // Mirrors nginx.conf's same-origin proxy (ADR-0012 "Exposure and
      // authentication") so the generated client's relative /api/* calls work
      // against a locally-run `cmd/api` too.
      "/api": {
        target: `http://localhost:${apiPort}`,
        changeOrigin: true,
        // A locally-run cmd/api still authenticates every request; in dev the
        // proxy supplies the worker bearer (API_DEV_BEARER) so the browser
        // needs no Cloudflare Access JWT. Unset, nothing is added.
        headers: process.env.API_DEV_BEARER
          ? { Authorization: `Bearer ${process.env.API_DEV_BEARER}` }
          : undefined,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
});
