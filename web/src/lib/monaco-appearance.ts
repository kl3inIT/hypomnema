import * as monaco from "monaco-editor"

// hex is a CSS colour as Monaco's themes take it. The page's tokens are oklch and color-mix, so the colour is
// painted on a canvas and read back.
function hex(color: string, fallback: string) {
  const context = document.createElement("canvas").getContext("2d", { willReadFrequently: true })
  if (!context) return fallback

  context.fillStyle = color
  context.fillRect(0, 0, 1, 1)
  const [r = 0, g = 0, b = 0, a = 0] = context.getImageData(0, 0, 1, 1).data

  return `#${[r, g, b, a].map((channel) => channel.toString(16).padStart(2, "0")).join("")}`
}

// appearance is the editor's theme and type, taken from the page: Omnara's editorAppearance (styles/editor.ts),
// with this page's tokens.
export function appearance(element: HTMLElement) {
  const style = getComputedStyle(element)
  const dark = document.documentElement.classList.contains("dark")
  const theme = dark ? "hypomnema-dark" : "hypomnema-light"
  const token = (name: string, fallback: string) =>
    hex(getComputedStyle(document.documentElement).getPropertyValue(name).trim(), fallback)
  const foreground = hex(style.color, dark ? "#f5f5f5ff" : "#18181bff").slice(0, 7)
  const primary = token("--primary", "#3b6cf6ff").slice(0, 7)

  monaco.editor.defineTheme(theme, {
    base: dark ? "vs-dark" : "vs",
    inherit: true,
    rules: [],
    colors: {
      // The field's own background shows through.
      "editor.background": "#00000000",
      "editorGutter.background": "#00000000",
      "editor.foreground": foreground,
      "editorLineNumber.foreground": `${foreground}66`,
      "editorLineNumber.activeForeground": foreground,
      "editorCursor.foreground": foreground,
      "editor.selectionBackground": `${primary}55`,
      "editor.inactiveSelectionBackground": `${primary}2a`,
      "editor.lineHighlightBackground": `${primary}12`,
      "editor.lineHighlightBorder": "#00000000",
      "editorWidget.background": token("--popover", dark ? "#171717ff" : "#ffffffff").slice(0, 7),
      "editorWidget.border": token("--border", dark ? "#ffffff14" : "#e4e4e7ff"),
      focusBorder: "#00000000",
    },
  })

  return {
    theme,
    fontFamily: style.fontFamily,
    fontSize: Number.parseFloat(style.fontSize),
    lineHeight: Number.parseFloat(style.lineHeight),
  }
}
