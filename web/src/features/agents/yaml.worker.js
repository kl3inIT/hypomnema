/* global self */

// The YAML language worker, as Omnara loads it (Apache License 2.0, https://github.com/omnara-ai/omnara;
// frontend/apps/web/src/components/agents/yaml.worker.js): it says when it is ready, so the editor asks nothing of
// it before.
import "monaco-yaml/yaml.worker.js"

self.postMessage({ type: "hypomnema-yaml-worker-ready" })
