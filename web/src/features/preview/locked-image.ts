// The locked frame an SVG is drawn in when only its text is at hand (issue #422, row 4): a srcdoc frame sandboxed
// with nothing allowed, whose policy loads nothing but the picture as a data: address, so it has an opaque origin
// and runs no script. An SVG never gets a blob: address of this origin, where its script would run if it were
// opened on its own (the #402 review).

// lockedImage is the locked frame's document for an SVG.
export function lockedImage(markup: string, alt: string) {
  const escape = (value: string) =>
    value.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;")

  return (
    '<!DOCTYPE html><meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data:; ' +
    "style-src 'unsafe-inline'\">" +
    // Light, as the frame element is (image-view.tsx): a frame whose scheme is the element's draws no canvas of its
    // own, so the picture sits on the viewer's backdrop.
    '<meta name="color-scheme" content="light">' +
    "<style>html,body{margin:0;height:100%}body{display:flex;align-items:center;justify-content:center}" +
    "img{max-width:100%;max-height:100%}</style>" +
    `<img alt="${escape(alt)}" src="data:image/svg+xml,${encodeURIComponent(markup)}">`
  )
}

// svgSize is an SVG's own width and height, from its attributes or its viewBox, read by the browser's XML parser,
// which runs nothing; undefined when it says neither.
export function svgSize(markup: string): { width: number; height: number } | undefined {
  const root = new DOMParser().parseFromString(markup, "image/svg+xml").documentElement
  if (root.nodeName.toLowerCase() !== "svg") return undefined
  const length = (name: string) => {
    const value = Number.parseFloat(root.getAttribute(name) ?? "")
    return Number.isFinite(value) && value > 0 ? value : undefined
  }
  const box = (root.getAttribute("viewBox") ?? "").split(/[\s,]+/).map(Number)
  const width = length("width") ?? (box[2] && box[2] > 0 ? box[2] : undefined)
  const height = length("height") ?? (box[3] && box[3] > 0 ? box[3] : undefined)

  return width && height ? { width, height } : undefined
}
