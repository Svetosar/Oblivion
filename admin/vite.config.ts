import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The built admin is embedded into the Go binary from ../internal/admin/dist.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: {
    outDir: "../internal/admin/dist",
    emptyOutDir: false, // the Makefile cleans it; .gitkeep must survive
    sourcemap: false,
  },
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/media": "http://127.0.0.1:8080" } },
});
