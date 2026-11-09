import "@testing-library/jest-dom/vitest"
import { cleanup } from "@testing-library/react"
import { afterEach } from "vitest"

import { client } from "@/api/client"

// Node's fetch needs an absolute URL; the browser resolves /api/v1 against the page.
client.setConfig({ baseUrl: "http://localhost/api/v1" })

afterEach(() => {
  cleanup()
})
