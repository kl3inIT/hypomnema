import { defineConfig, devices } from "@playwright/test"

// Runs against the production build, as the control plane serves it: the client's own tests on Vite's preview with a
// fake client API, and server.spec.ts on the control plane itself (tests/server), for what only its answers show.
// The servers start in order, so the control plane embeds the build the first one made.
const server = "http://127.0.0.1:4180"
const prebuilt = process.env.PLAYWRIGHT_PREBUILT
const preview = "pnpm preview --host 127.0.0.1 --port 4173 --strictPort"

const vite = {
  command: prebuilt ? preview : `pnpm build && ${preview}`,
  url: "http://127.0.0.1:4173",
  reuseExistingServer: !process.env.CI,
  // The build runs the React Compiler, which can take longer than the default minute.
  timeout: 180_000,
}
const controlPlane = {
  command: "go run ./tests/server",
  url: `${server}/readyz`,
  reuseExistingServer: !process.env.CI,
  // The first build of the control plane on a fresh cache takes minutes.
  timeout: 600_000,
  gracefulShutdown: { signal: "SIGTERM" as const, timeout: 5_000 },
}

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
  webServer:
    prebuilt === "server" ? [controlPlane] : prebuilt === "preview" ? [vite] : [vite, controlPlane],
})
