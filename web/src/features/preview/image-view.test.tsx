import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { ImageView, InlineSvg } from "./image-view"
import { lockedImage, svgSize } from "./locked-image"

// Adapted from MemoryOS (web/src/features/preview/image-view.test.tsx), whose cases are about cited regions,
// which are not kept here: these say how a picture is turned and zoomed, and that an SVG is never at a blob:
// address (issue #422, row 4).

const svg =
  '<svg xmlns="http://www.w3.org/2000/svg" width="160" height="60"><rect width="160" height="60"/>' +
  "<script>parent.alert(1)</script></svg>"

describe("ImageView", () => {
  it("draws a picture from its address with the zoom and turn its controls give", () => {
    const { rerender } = render(
      <ImageView source={{ url: "blob:picture" }} alt="chart.png" zoom={100} rotation={0} />,
    )
    const picture = screen.getByRole("img", { name: "chart.png" })
    expect(picture).toHaveAttribute("src", "blob:picture")
    expect(picture.style.transform).toContain("scale(1) rotate(0deg)")

    rerender(
      <ImageView source={{ url: "blob:picture" }} alt="chart.png" zoom={150} rotation={90} />,
    )
    expect(picture.style.transform).toContain("scale(1.5) rotate(90deg)")
  })

  it("draws an SVG's text in a frame that may do nothing, as a data: picture its policy alone allows", () => {
    render(<ImageView source={{ markup: svg }} alt="flow.svg" zoom={100} rotation={0} />)

    const frame = screen.getByTitle("flow.svg")
    expect(frame.tagName).toBe("IFRAME")
    expect(frame).toHaveAttribute("sandbox", "")
    const doc = frame.getAttribute("srcdoc") ?? ""
    expect(doc).toMatch(/^<!DOCTYPE html><meta http-equiv="Content-Security-Policy"/)
    expect(doc).toContain("default-src 'none'; img-src data:;")
    expect(doc).toContain(`src="data:image/svg+xml,${encodeURIComponent(svg)}"`)
    expect(doc).not.toContain("blob:")
    // The SVG's markup is only inside the picture's address, never markup of the frame's document.
    expect(doc).not.toContain("<script>")
    expect(document.querySelector('img[src^="blob:"]')).toBeNull()
  })
})

describe("an SVG in a document", () => {
  it("takes its own size, from its attributes or its viewBox", () => {
    expect(svgSize(svg)).toEqual({ width: 160, height: 60 })
    expect(svgSize('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 30 10"/>')).toEqual({
      width: 30,
      height: 10,
    })
    expect(svgSize("<p>not a picture</p>")).toBeUndefined()
  })

  it("is drawn in the locked frame, as wide as it says", () => {
    render(<InlineSvg markup={svg} alt="flow" />)

    const frame = screen.getByTitle("flow")
    expect(frame).toHaveAttribute("sandbox", "")
    expect(frame.getAttribute("srcdoc")).toBe(lockedImage(svg, "flow"))
    expect(frame.style.width).toBe("160px")
  })

  it("escapes the picture's name in the frame's document", () => {
    expect(lockedImage(svg, '"><script>x</script>')).toContain(
      'alt="&quot;>&lt;script>x&lt;/script>"',
    )
  })
})
