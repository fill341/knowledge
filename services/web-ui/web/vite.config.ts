import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import pkg from "./package.json" with { type: "json" };

// Dev: `npm run dev` proxies API/auth calls to a running kn-ui (make up starts it on :8790).
export default defineConfig({
  plugins: [react()],
  define: { __APP_VERSION__: JSON.stringify(pkg.version) },
  // The lazy 3D graph chunk (three.js) is ~1.5 MB by design; it loads only on the overview page.
  build: { outDir: "../internal/static/dist", emptyOutDir: true, chunkSizeWarningLimit: 1600 },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8790",
      "/auth": "http://127.0.0.1:8790",
    },
  },
});
