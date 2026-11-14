// Geometry and windowing for the PDF reader: how tall an undrawn page is, and which pages are worth mounting.
// Copied from MemoryOS (web/src/features/preview/pdf-pages.ts), without what places a citation on a page.

export type PdfPageView = { view: readonly number[]; rotate: number }

// US Letter portrait, used until a page reports its own size.
const LETTER: PdfPageView = { view: [0, 0, 612, 792], rotate: 0 }

// pagesToRender is the pages whose canvases are mounted: the page the view opens at, plus the pages near the
// viewport and `overscan` neighbours of each. Every other page stays a sized placeholder, so a long PDF only draws
// what the reader scrolls to.
export function pagesToRender(
  near: Iterable<number>,
  anchor: number,
  pageCount: number,
  overscan = 1,
): number[] {
  const pages = new Set<number>()
  const add = (page: number) => {
    if (Number.isInteger(page) && page >= 1 && page <= pageCount) pages.add(page)
  }
  add(anchor)
  for (const page of near) {
    for (let offset = -overscan; offset <= overscan; offset += 1) add(page + offset)
  }

  return [...pages].sort((first, second) => first - second)
}

// pageHeight is the CSS height of a page drawn `width` pixels wide; a page not loaded yet borrows `fallback`'s
// proportions.
export function pageHeight(
  page: PdfPageView | undefined,
  width: number,
  fallback: PdfPageView = LETTER,
) {
  const proportions = (candidate: PdfPageView) => {
    const [left = 0, bottom = 0, right = 0, top = 0] = candidate.view
    const across = Math.abs(right - left)
    const down = Math.abs(top - bottom)
    const ratio = Math.abs(candidate.rotate % 180) === 90 ? across / down : down / across
    return Number.isFinite(ratio) && ratio > 0 ? ratio : undefined
  }
  const ratio = (page && proportions(page)) ?? proportions(fallback) ?? proportions(LETTER)!

  return Math.round(width * ratio)
}

// nearestPage is the page of `pages` closest to `to`, the earlier one on a tie; the order the page rail draws in.
export function nearestPage(pages: readonly number[], to: number) {
  let best: number | undefined
  for (const page of pages) {
    const closer = best === undefined || Math.abs(page - to) < Math.abs(best - to)
    if (closer || (Math.abs(page - to) === Math.abs(best! - to) && page < best!)) best = page
  }

  return best
}

// mostVisiblePage is the page showing the largest share of itself, for the page counter; ties keep the earlier.
export function mostVisiblePage(ratios: ReadonlyMap<number, number>) {
  let best: number | undefined
  let bestRatio = 0
  for (const [page, ratio] of ratios) {
    if (ratio > bestRatio || (ratio === bestRatio && best !== undefined && page < best)) {
      best = page
      bestRatio = ratio
    }
  }

  return best
}
