import { defineConfig } from "@hey-api/openapi-ts"

// The typed client for the client API, generated from its contract, as Omnara generates its SDK
// (.tmp/omnara/frontend/packages/sdk/openapi-ts.config.ts). `make openapi-check` fails when it is stale.
// The next build is used because the latest release needs the JavaScript compiler API that TypeScript 7 dropped.
export default defineConfig({
  input: "../api/openapi.yaml",
  output: "src/api/generated",
  plugins: [
    { name: "@hey-api/client-fetch", throwOnError: true },
    { name: "@hey-api/typescript" },
    { name: "@hey-api/sdk" },
  ],
})
