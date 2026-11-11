// Vite makes a worker of a module imported with ?worker; the linter does not see its default export.
// oxlint-disable-next-line import/default
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker"

// oxlint-disable-next-line import/default
import YamlWorker from "./yaml.worker.js?worker"

// Monaco's workers, as Omnara sets them up (Apache License 2.0, https://github.com/omnara-ai/omnara;
// frontend/apps/web/src/components/agents/monacoEnvironment.ts): the YAML worker is handed over once it has said
// it is ready. Changed: the ready message is checked by hand, where Omnara parses it with zod.
function readyYamlWorker() {
  const worker = new YamlWorker()

  return new Promise<Worker>((resolve, reject) => {
    const onMessage = (event: MessageEvent) => {
      const data: unknown = event.data
      if (typeof data !== "object" || data === null || !("type" in data)) return
      if (data.type !== "hypomnema-yaml-worker-ready") return

      worker.removeEventListener("message", onMessage)
      worker.removeEventListener("error", onError)
      resolve(worker)
    }
    const onError = (event: ErrorEvent) => {
      worker.removeEventListener("message", onMessage)
      worker.removeEventListener("error", onError)
      reject(event.error instanceof Error ? event.error : new Error(event.message))
    }

    worker.addEventListener("message", onMessage)
    worker.addEventListener("error", onError)
  })
}

self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    return label === "yaml" ? readyYamlWorker() : new EditorWorker()
  },
}
