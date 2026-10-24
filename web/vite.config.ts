import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Go server embeds web/dist. In development, run `npm run dev` and
// `bee-scope` side by side: Vite proxies /api (including the SSE stream) to
// the Go server.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, sourcemap: false },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:7070",
        changeOrigin: false,
        // Keep SSE unbuffered through the proxy.
        configure: (proxy) => {
          proxy.on("proxyRes", (res) => {
            if (res.headers["content-type"]?.includes("text/event-stream")) {
              res.headers["cache-control"] = "no-cache, no-transform";
            }
          });
        },
      },
    },
  },
});
