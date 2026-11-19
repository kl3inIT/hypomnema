import { defineConfig } from "@hey-api/openapi-ts"

// Server-sent event streams: read through the SDK's stream, never cached as a TanStack query
// (MemoryOS web/openapi-ts.config.ts keeps its chat stream out of queries the same way).
const streams = new Set(["StreamEvents", "StreamChat"])

// Reads sent as a POST: they get query options whose key carries the request body, as MemoryOS gives its
// document search (web/openapi-ts.config.ts). A schedule's preview changes nothing on the server. A folder page's
// frame address is read as a query and kept for half its life (features/artifacts/artifact-state.ts,
// useFrameFiles); asking it again only gives another address to the same files.
const postReads = new Set(["PreviewRoutineSchedule", "OpenArtifactFrame"])

// The typed client for the client API, generated from its contract, as Omnara generates its SDK
// (.tmp/omnara/frontend/packages/sdk/openapi-ts.config.ts). `make openapi-check` fails when it is stale.
// The next build is used because the latest release needs the JavaScript compiler API that TypeScript 7 dropped.
export default defineConfig({
  input: "../api/openapi.yaml",
  // No entry file: features import types.gen, sdk.gen and @tanstack/react-query.gen directly, and an index.ts
  // re-exporting the whole API on one line made every two API changes conflict (#902). Kilocode keeps its entry
  // empty the same way (.tmp/kilocode/packages/sdk/js/script/build.ts:47-70, exportFromIndex: false).
  output: { path: "src/api/generated", entryFile: false },
  parser: {
    hooks: {
      operations: {
        isQuery: (operation) =>
          operation.operationId && streams.has(operation.operationId)
            ? false
            : operation.operationId && postReads.has(operation.operationId)
              ? true
              : undefined,
      },
    },
  },
  plugins: [
    { name: "@hey-api/client-fetch", throwOnError: true },
    { name: "@hey-api/typescript" },
    { name: "@hey-api/sdk" },
    // Query keys, query options and mutation options for TanStack Query, with the plugin's defaults, as Omnara
    // and MemoryOS have them. A feature's use-*.ts spreads them (docs/conventions.md, Web client).
    "@tanstack/react-query",
  ],
})
