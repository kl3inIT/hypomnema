import { resolve } from "node:path"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vitest/config"

export default defineConfig({
  plugins: [react()],
  // Every file is compiled with the web's own settings, a file outside web/ too (the Orca plugin's rule in
  // ../orca/src/waiting.ts, which orca-waiting.test.ts imports): the tsconfig beside it extends typings that Orca
  // writes and Git ignores, absent from a clean checkout.
  tsconfig: "./tsconfig.app.json",
  resolve: {
    alias: {
      "@": resolve(import.meta.dirname, "./src"),
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
  },
})
