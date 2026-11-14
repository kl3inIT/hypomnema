import { describe, expect, it } from "vitest"

import {
  mediaTypeOf,
  parseCsv,
  previewKind,
  sanitizeDocxInPlace,
  scrubDocxCss,
} from "./preview-kind"

// Copied from MemoryOS (web/src/features/preview/preview-kind.test.ts); an SVG is a kind of its own here.

describe("a file's preview", () => {
  it("picks the first Onyx variant that matches", () => {
    const xlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
    const docx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
    expect(previewKind("phân tích.py", "application/octet-stream")).toBe("code")
    expect(previewKind("data.json", "application/json")).toBe("code")
    expect(previewKind("chart.png", "image/png")).toBe("image")
    expect(previewKind("photo.avif", "image/avif")).toBe("image")
    expect(previewKind("chart.svg", "image/svg+xml")).toBe("svg")
    expect(previewKind("báo cáo.pdf", "application/pdf")).toBe("pdf")
    expect(previewKind("doanh thu.csv", "text/csv")).toBe("csv")
    expect(previewKind("doanh thu.xlsx", xlsx)).toBe("xlsx")
    expect(previewKind("README.md", "text/markdown")).toBe("markdown")
    expect(previewKind("báo cáo.docx", docx)).toBe("docx")
    expect(previewKind("cũ.doc", "application/msword")).toBe("doc")
    expect(previewKind("notes.txt", "text/plain")).toBe("text")
    expect(previewKind("slides.pptx", "application/vnd.ms-powerpoint")).toBe("pptx")
    expect(previewKind("slides.ppt", "application/vnd.ms-powerpoint")).toBe("unsupported")
  })

  it("reads an untyped file by its name, as a chat's folder sends it", () => {
    const kind = (path: string) => previewKind(path, mediaTypeOf(path))
    expect(kind("out/report.pdf")).toBe("pdf")
    expect(kind("img/logo.PNG")).toBe("image")
    expect(kind("img/flow.svg")).toBe("svg")
    expect(kind("out/sales.xlsx")).toBe("xlsx")
    expect(kind("docs/brief.docx")).toBe("docx")
    expect(kind("bin/tool")).toBe("unsupported")
    // A folder with a dot names no kind: only the file's own name does.
    expect(kind("v1.2/README")).toBe("unsupported")
  })

  it("reads quoted CSV cells with commas, doubled quotes and line breaks", () => {
    expect(parseCsv('Tên,Ghi chú\n"Hà Nội, VN","nói ""xin chào""\nlần 2"\r\n,3\n')).toEqual([
      ["Tên", "Ghi chú"],
      ["Hà Nội, VN", 'nói "xin chào"\nlần 2'],
      ["", "3"],
    ])
    expect(parseCsv("a,b")).toEqual([["a", "b"]])
    expect(parseCsv("")).toEqual([])
  })

  it("takes @import and every url() that is not data: out of a document's styles", () => {
    const css =
      '@import url("https://evil.example/x.css");\n@import "y.css";\n' +
      ".a{background:url(https://evil.example/p.png)}\n" +
      ".b{background:url( 'data:image/png;base64,AAAA' )}\n" +
      '@font-face{src:url("//evil.example/f.woff")}'
    const scrubbed = scrubDocxCss(css)
    expect(scrubbed).not.toContain("@import")
    expect(scrubbed).not.toContain("evil.example")
    expect(scrubbed).toContain("url( 'data:image/png;base64,AAAA' )")
    expect(scrubbed).toContain(".a{background:none}")
  })

  it("keeps only http(s) and mailto links, base64 images and inline styles in docx output", () => {
    const root = document.createElement("div")
    root.innerHTML =
      '<p onclick="x()" style="padding: 4pt">Chào</p><a href="javascript:alert(1)">bad</a>' +
      '<a href="https://example.com">ok</a><img src="data:image/png;base64,AAAA"><script>alert(1)</script>'
    sanitizeDocxInPlace(root)
    const html = root.innerHTML
    expect(html).toContain('style="padding: 4pt"')
    expect(html).not.toContain("onclick")
    expect(html).not.toContain("javascript:")
    expect(html).not.toContain("<script")
    expect(html).toContain('href="https://example.com"')
    expect(html).toContain('src="data:image/png;base64,AAAA"')
  })
})
