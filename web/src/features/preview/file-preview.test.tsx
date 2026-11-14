import { render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"

import { FilePreview } from "./file-preview"
import { readPreviewContent } from "./preview-content"
import { MAX_TEXT_PREVIEW_BYTES } from "./preview-kind"

// Adapted from MemoryOS (web/src/features/preview/file-preview.test.tsx): the cases that are not about citations.

// jsdom runs neither pdf.js nor its worker. The PDF reader's own frame and toolbar are what is rendered here; the
// browser tests draw the pages.
vi.mock("react-pdf", () => ({
  Document: () => null,
  Page: () => null,
  pdfjs: { version: "0", GlobalWorkerOptions: {}, PDFWorker: class {} },
}))
vi.mock("./pdf-reader-worker", () => ({ useReaderWorker: () => undefined }))

beforeEach(() => {
  // jsdom has no object URLs.
  URL.createObjectURL = () => "blob:picture"
  URL.revokeObjectURL = vi.fn<(url: string) => void>()
})
afterEach(() => {
  Reflect.deleteProperty(URL, "createObjectURL")
  Reflect.deleteProperty(URL, "revokeObjectURL")
})

it("offers the download for a file it cannot show", () => {
  render(<FilePreview content={{ kind: "doc" }} filename="cu.doc" download="/api/v1/file" />)

  expect(
    screen.getByText("An old .doc file cannot be shown here. Download it to open it."),
  ).toBeVisible()
  expect(screen.getByRole("link", { name: "Download" })).toHaveAttribute("href", "/api/v1/file")
})

it("draws a PDF with its page and zoom controls as one floating toolbar", async () => {
  render(
    <FilePreview
      content={{ kind: "pdf", file: new Blob(["%PDF-"], { type: "application/pdf" }) }}
      filename="bao-cao.pdf"
      download="/api/v1/file"
    />,
  )

  const toolbar = await screen.findByRole("toolbar", { name: "Document controls" })
  expect(screen.getAllByRole("toolbar")).toHaveLength(1)
  expect(within(toolbar).getByRole("button", { name: "Zoom out" })).toBeDisabled()
  expect(within(toolbar).getByText("Fit")).toBeInTheDocument()
})

it("draws a raster image from a blob: address of its exact type", async () => {
  const content = await readPreviewContent(
    new Blob(["picture"], { type: "application/octet-stream" }),
    "image",
    "image/png",
  )
  expect(content.kind === "image" && content.blob.type).toBe("image/png")

  render(<FilePreview content={content} filename="chart.png" download="/api/v1/file" />)

  expect(screen.getByRole("img", { name: "chart.png" })).toHaveAttribute("src", "blob:picture")
  expect(screen.getByRole("button", { name: "Zoom in" })).toBeInTheDocument()
})

it("draws an SVG from its sandboxed address or its text, never from a blob: address", async () => {
  const markup = '<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>'
  const made = vi.spyOn(URL, "createObjectURL")

  const fromBytes = await readPreviewContent(new Blob([markup]), "svg", "image/svg+xml")
  expect(fromBytes).toEqual({ kind: "svg", markup })
  const { unmount } = render(
    <FilePreview content={fromBytes} filename="flow.svg" download="/api/v1/file" />,
  )
  expect(screen.getByTitle("flow.svg")).toHaveAttribute("sandbox", "")
  unmount()

  const at = "http://localhost/api/v1/artifact-files/tok/flow.svg"
  render(<FilePreview content={{ kind: "svg", url: at }} filename="flow.svg" download="/f" />)
  expect(screen.getByRole("img", { name: "flow.svg" })).toHaveAttribute("src", at)
  expect(made).not.toHaveBeenCalled()
})

it("reads text up to its ceiling and says it was cut", async () => {
  const content = await readPreviewContent(
    new Blob(["a".repeat(MAX_TEXT_PREVIEW_BYTES + 10)]),
    "text",
    "text/plain",
  )

  expect(content).toMatchObject({
    kind: "text",
    truncated: true,
    bytes: MAX_TEXT_PREVIEW_BYTES + 10,
  })
  expect(content.kind === "text" && content.text.length).toBe(MAX_TEXT_PREVIEW_BYTES)
})
