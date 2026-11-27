import { expect, test } from "vitest"

import { fileIconURL } from "./file-icon"

test.each([
  ["reports/sales.XLSX", "table"],
  ["sales.csv", "table"],
  ["notes.docx", "word"],
  ["report.pdf", "pdf"],
  ["slides.pptx", "powerpoint"],
  ["archive.zip", "zip"],
  ["chart.png", "image"],
  ["demo.mp4", "video"],
  ["README.md", "markdown"],
  ["backup.unknown", "file"],
  ["folder.with.dots/README", "file"],
  ["backup.constructor", "file"],
])("%s uses the %s icon", (name, icon) => {
  expect(fileIconURL(name)).toMatch(new RegExp(`/${icon}\\.svg(?:\\?|$)`))
})
