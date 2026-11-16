import "@testing-library/jest-dom/vitest"
import { cleanup } from "@testing-library/react"
import { afterEach } from "vitest"

import { client } from "@/api/client"

// Node's fetch needs an absolute URL; the browser resolves /api/v1 against the page.
client.setConfig({ baseUrl: "http://localhost/api/v1" })

// jsdom has no matchMedia; the window is a desktop's, where no query (a phone's width, a dark scheme) matches.
window.matchMedia ??= (query: string) =>
  ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }) as MediaQueryList

afterEach(() => {
  cleanup()
})
