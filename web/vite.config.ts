import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// In development the control plane serves the API; Vite forwards to it. In production the control plane serves
// this build itself (web/embed.go).
const api = process.env.HYPOMNEMA_API_URL ?? "http://127.0.0.1:8080"

export default defineConfig({
  plugins: [tanstackRouter({ target: "react", autoCodeSplitting: true }), react(), tailwindcss()],
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    proxy: {
      "/api": api,
      "/readyz": api,
      "/livez": api,
    },
  },
  build: {
    // dist/.gitkeep is committed so that `go build` works before the first web build.
    emptyOutDir: false,
  },
})
