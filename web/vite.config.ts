import { readdirSync, readFileSync } from "node:fs"
import { resolve } from "node:path"
import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import babel from "@rolldown/plugin-babel"
import react, { reactCompilerPreset } from "@vitejs/plugin-react"
import { defineConfig, type Plugin } from "vite"

// In development the control plane serves the API; Vite forwards to it. In production the control plane serves
// this build itself (web/embed.go).
const api = process.env.HYPOMNEMA_API_URL ?? "http://127.0.0.1:8080"

// pdfjsDecoders serves pdf.js's image decoders (JBIG2, JPEG 2000), which it loads from `wasmUrl` by fixed file
// names, under a directory named for the pdf.js release, so the year-long cache of assets/ never mixes decoders of
// another release. Copied from MemoryOS (web/vite.config.ts, the owner's own code). The app's policy allows no
// WebAssembly, so pdf.js uses their JavaScript fallbacks.
function pdfjsDecoders(): Plugin {
  const root = resolve(import.meta.dirname, "node_modules/pdfjs-dist")
  const version = (
    JSON.parse(readFileSync(resolve(root, "package.json"), "utf8")) as { version: string }
  ).version
  const directory = `assets/pdfjs-${version}/`
  const files = readdirSync(resolve(root, "wasm")).filter((name) =>
    /^(jbig2|openjpeg)[\w-]*\.(wasm|js)$/.test(name),
  )
  const read = (name: string) => readFileSync(resolve(root, "wasm", name))

  return {
    name: "hypomnema-pdfjs-decoders",
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const path = request.url?.split("?")[0] ?? ""
        const name = path.startsWith(`/${directory}`) ? path.slice(directory.length + 1) : ""
        if (!files.includes(name)) return next()
        response.setHeader(
          "Content-Type",
          name.endsWith(".wasm") ? "application/wasm" : "text/javascript",
        )
        response.end(read(name))
      })
    },
    generateBundle() {
      for (const name of files) {
        this.emitFile({ type: "asset", fileName: `${directory}${name}`, source: read(name) })
      }
    },
  }
}

export default defineConfig({
  plugins: [
    pdfjsDecoders(),
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    // The React Compiler memoizes components at build time, as Omnara, Onyx, Coder and Hermes build theirs. It is
    // the Babel plugin, not the plugin's own Rust port, which its README calls experimental.
    babel({ presets: [reactCompilerPreset()] }),
    tailwindcss(),
  ],
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
