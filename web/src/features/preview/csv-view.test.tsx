import { render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it } from "vitest"

import { CsvView } from "./csv-view"
import { MAX_TABLE_ROWS } from "./preview-kind"

// Adapted from MemoryOS (web/src/features/preview/csv-view.test.tsx), whose cases are about cited rows, which are
// not kept here: these say what the table shows and when it says it is cut.

const csv = 'Khu vực,Doanh thu\nHà Nội,120\n"Đà Nẵng, miền Trung",80\nCần Thơ,45\n'

const LAID_OUT = { offsetHeight: 600, offsetWidth: 800, clientHeight: 600, clientWidth: 800 }

// Only the rows inside the scrolling window are rendered, and jsdom lays nothing out, so the window would be zero
// high and the sheet would come out empty. TanStack Virtual sizes the window by its client box and the lines by
// their offset box.
beforeEach(() => {
  for (const [property, value] of Object.entries(LAID_OUT))
    Object.defineProperty(HTMLElement.prototype, property, { configurable: true, value })
})

afterEach(() => {
  for (const property of Object.keys(LAID_OUT))
    Reflect.deleteProperty(HTMLElement.prototype, property)
})

const view = (text: string, truncated = false) => (
  <CsvView csv={text} truncated={truncated} download="/api/v1/file" filename="sales.csv" />
)

describe("CsvView", () => {
  it("draws the first line as the header and each later line as a row, quoted commas kept", () => {
    render(view(csv))

    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual([
      "Khu vực",
      "Doanh thu",
    ])
    const rows = screen.getAllByRole("row")
    // The header and three rows.
    expect(rows).toHaveLength(4)
    expect(screen.getByRole("cell", { name: "Đà Nẵng, miền Trung" })).toBeInTheDocument()
    expect(screen.queryByRole("note")).toBeNull()
  })

  it("says a sheet past the row limit is cut, beside the download of the whole file", () => {
    const long = ["n", ...Array.from({ length: MAX_TABLE_ROWS + 5 }, (_, i) => String(i))].join(
      "\n",
    )
    render(view(long))

    expect(screen.getByRole("note")).toHaveTextContent("Only the first 1,000 rows are shown.")
    expect(screen.getByRole("link", { name: "Download the whole file" })).toHaveAttribute(
      "href",
      "/api/v1/file",
    )
  })

  it("says a sheet the server cut shows only its beginning", () => {
    render(view(csv, true))

    expect(screen.getByRole("note")).toHaveTextContent("Only the beginning of this sheet is shown.")
  })

  it("says an empty sheet is empty", () => {
    render(view(""))

    expect(screen.getByText("An empty sheet")).toBeInTheDocument()
  })
})
