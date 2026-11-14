import { render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { MarkdownView } from "./markdown-view"

// Copied from MemoryOS (web/src/features/preview/markdown-view.test.tsx).

const CHARTER = [
  "# Điều lệ công ty",
  "",
  "## Chương III",
  "",
  "Mỗi cổ đông có tổng số phiếu biểu quyết tương ứng với số cổ phần.",
  "",
  "| Điều | Nội dung |",
  "| --- | --- |",
  "| 5.1 | Bỏ phiếu kín |",
].join("\n")

const view = (text: string, truncated = false) => (
  <MarkdownView text={text} truncated={truncated} download="/api/v1/file" filename="charter.md" />
)

describe("MarkdownView", () => {
  it("reads a Markdown file as a document, not as source code", async () => {
    render(view(CHARTER))

    await waitFor(() =>
      expect(
        screen.getByRole("heading", { level: 1, name: "Điều lệ công ty" }),
      ).toBeInTheDocument(),
    )
    expect(screen.getByRole("heading", { level: 2, name: "Chương III" })).toBeInTheDocument()
    expect(screen.getByRole("table")).toBeInTheDocument()
    expect(screen.getByRole("cell", { name: "Bỏ phiếu kín" })).toBeInTheDocument()
    // The marks that make a heading a heading belong to the rendering, not to the text a reader sees.
    expect(screen.queryByText(/^#/)).toBeNull()
  })

  it("says so, beside the download, rather than showing nothing when the file was cut short", async () => {
    render(view("# Điều lệ", true))

    await waitFor(() =>
      expect(screen.getByRole("heading", { level: 1, name: "Điều lệ" })).toBeInTheDocument(),
    )
    expect(screen.getByRole("note")).toHaveTextContent("Only the first 1 MB of the file is shown.")
    expect(screen.getByRole("link", { name: "Download the whole file" })).toHaveAttribute(
      "href",
      "/api/v1/file",
    )
  })
})
