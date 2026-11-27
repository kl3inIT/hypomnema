import { defineConfig, devices } from "@playwright/test"

// Runs against the production build, as the control plane serves it: the client's own tests on Vite's preview with a
// fake client API, and the server and notification renewal tests on the control plane itself (tests/server).
// The servers start in order, so the control plane embeds the build prepared by CI or the first server.
const server = "http://127.0.0.1:4180"
const preview = "pnpm preview --host 127.0.0.1 --port 4173 --strictPort"

export default defineConfig({
  testDir: "./tests",
  fullyParallel: !!process.env.CI,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? "github" : "list",
  use: { baseURL: "http://127.0.0.1:4173" },
  projects: [
    { name: "phone", use: { ...devices["Pixel 7"] }, testIgnore: "server.spec.ts" },
    { name: "desktop", use: { ...devices["Desktop Chrome"] }, testIgnore: "server.spec.ts" },
    {
      name: "server",
      use: { ...devices["Desktop Chrome"], baseURL: server },
      testMatch: "server.spec.ts",
    },
  ],
  webServer: [
    {
      command: process.env.PLAYWRIGHT_PREBUILT === "1" ? preview : `pnpm build && ${preview}`,
      url: "http://127.0.0.1:4173",
      reuseExistingServer: !process.env.CI,
      // The build runs the React Compiler; on a busy machine it takes longer than the default minute.
      timeout: 180_000,
    },
    {
      command: "go run ./tests/server",
      url: `${server}/readyz`,
      reuseExistingServer: !process.env.CI,
      // The first build of the control plane on a fresh cache takes minutes.
      timeout: 600_000,
      gracefulShutdown: { signal: "SIGTERM", timeout: 5_000 },
    },
  ],
})
