import { defineConfig, devices } from "@playwright/test"
export default defineConfig({
  testDir: "./tests",
  reporter: "list",
  use: { baseURL: "http://127.0.0.1:54577" },
  projects: [
    { name: "phone", use: { ...devices["Pixel 7"] } },
    { name: "desktop", use: { ...devices["Desktop Chrome"] } },
  ],
})
