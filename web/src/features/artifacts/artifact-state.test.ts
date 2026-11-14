import { describe, expect, it } from "vitest"

import type { ArtifactRevision } from "@/api/generated/types.gen"

import { typeLabel } from "./artifact-state"

const revision = (entry: string, media = "application/octet-stream"): ArtifactRevision => ({
  revision: 1,
  title: entry,
  type: "file",
  entry,
  size: 1,
  files: [{ path: entry, size: 1, media_type: media }],
  created_at: "2026-11-14T09:00:00Z",
})

describe("an artifact's card", () => {
  it("names a file by the kind its viewer shows it as", () => {
    expect(typeLabel("file", revision("orders.xlsx"))).toBe("Spreadsheet")
    expect(typeLabel("file", revision("bao-cao.docx"))).toBe("Word")
    expect(typeLabel("file", revision("orders.csv"))).toBe("CSV")
    expect(typeLabel("file", revision("summary.md"))).toBe("Markdown")
    expect(typeLabel("file", revision("revenue.py"))).toBe("Code")
    expect(typeLabel("file", revision("slides.pptx"))).toBe("Slides")
    expect(typeLabel("image", revision("flow.svg", "image/svg+xml"))).toBe("SVG")
    expect(typeLabel("pdf", revision("report.pdf", "application/pdf"))).toBe("PDF")
    expect(typeLabel("file", revision("tool.bin"))).toBe("File")
    expect(typeLabel("html")).toBe("Page")
  })

  it("names it by its type until its revision is read", () => {
    expect(typeLabel("pdf")).toBe("PDF")
    expect(typeLabel("image")).toBe("Image")
    expect(typeLabel("file")).toBe("File")
  })
})
