// The pdf.js worker, self-hosted: the bundler emits it under the app's origin, so the app's script-src 'self'
// covers it. The address lives apart from the reader so it can be preloaded before the reader's own code has
// arrived. Copied from MemoryOS (web/src/features/preview/pdf-worker.ts).
export const pdfWorkerUrl = new URL(
  "pdfjs-dist/build/pdf.worker.min.mjs",
  import.meta.url,
).toString()
