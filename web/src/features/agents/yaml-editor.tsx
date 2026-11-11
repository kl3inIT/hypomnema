import "./monaco-environment"
import "monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution.js"

import * as monaco from "monaco-editor"
import { configureMonacoYaml } from "monaco-yaml"
import { useEffect, useRef } from "react"

import { efforts, maxFallbacks, runtimes } from "./profiles-state"

// The profile document, as the server reads it (orchestration/profiles.go, profileSource and ApplyProfile). The
// editor checks against it while the owner types; the server checks the rest when it saves.
const schemaUri = "https://hypomnema.local/schemas/profile.schema.json"
const schema = {
  $id: schemaUri,
  title: "Hypomnema profile",
  type: "object",
  additionalProperties: false,
  required: ["name", "description", "runtime"],
  properties: {
    name: {
      type: "string",
      pattern: "^[a-z][a-z0-9-]{0,62}$",
      description: "What a Task names the agent by: lowercase letters, digits and dashes.",
    },
    description: {
      type: "string",
      minLength: 1,
      maxLength: 500,
      description: "What the agent is for. The coordinator chooses an agent for each Task by this.",
    },
    runtime: {
      enum: [...runtimes],
      description: "The CLI a worker of this agent is; pi is the coordinator's.",
    },
    model: {
      type: "object",
      additionalProperties: false,
      description: "The model, or nothing for the runtime's default.",
      properties: {
        provider: { type: "string", description: "The provider that offers the model." },
        name: { type: "string", description: "The model's name at that provider." },
        effort: { enum: efforts.map((e) => e.value), description: "The reasoning effort." },
      },
    },
    instruction: {
      type: "string",
      description: "Added after the rules every worker gets, which it may not override.",
    },
    fallbacks: {
      type: "array",
      maxItems: maxFallbacks,
      uniqueItems: true,
      items: { type: "string" },
      description:
        "Agents, in order, that take this one's Tasks while its accounts wait for a limit to reset.",
    },
  },
}

let configured = false

function configureYaml() {
  if (configured) return
  configured = true

  configureMonacoYaml(monaco, {
    completion: true,
    enableSchemaRequest: false,
    hover: true,
    validate: true,
    format: { enable: true },
    schemas: [{ uri: schemaUri, fileMatch: ["*.yaml"], schema }],
  })
}

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
function appearance(element: HTMLElement) {
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

// YamlEditor edits a profile's YAML in Monaco, with completion, hover and errors against the profile's schema.
// It is Omnara's (Apache License 2.0, https://github.com/omnara-ai/omnara; frontend/apps/web/src/components/
// agents/AgentConfigYamlEditor.tsx): one model per editor, the theme following the page, suggestions offered on a
// new line. Changed: the schema is written here, where Omnara generates it from its server's types; the server's
// refusal is one line the page shows, so there are no markers from it.
export default function YamlEditor({
  id,
  label,
  value,
  onChange,
}: {
  id: string
  label: string
  value: string
  onChange: (value: string) => void
}) {
  const element = useRef<HTMLDivElement | null>(null)
  const model = useRef<monaco.editor.ITextModel | null>(null)
  const changed = useRef(onChange)
  const first = useRef({ value, label })

  useEffect(() => {
    changed.current = onChange
  }, [onChange])

  useEffect(() => {
    const host = element.current
    if (!host) return

    configureYaml()

    const uri = monaco.Uri.parse(`file:///${id}.yaml`)
    const text =
      monaco.editor.getModel(uri) ?? monaco.editor.createModel(first.current.value, "yaml", uri)
    model.current = text

    const editor = monaco.editor.create(host, {
      model: text,
      ariaLabel: first.current.label,
      automaticLayout: true,
      ...appearance(host),
      contextmenu: false,
      folding: false,
      glyphMargin: false,
      lineDecorationsWidth: 12,
      lineNumbersMinChars: 3,
      minimap: { enabled: false },
      overviewRulerLanes: 0,
      padding: { top: 12, bottom: 12 },
      quickSuggestions: { comments: false, other: true, strings: true },
      renderLineHighlightOnlyWhenFocus: true,
      scrollBeyondLastLine: false,
      scrollbar: { verticalScrollbarSize: 10, horizontalScrollbarSize: 10 },
      stickyScroll: { enabled: false },
      tabSize: 2,
      wordWrap: "on",
      wrappingIndent: "same",
    })

    const themes = new MutationObserver(() => editor.updateOptions(appearance(host)))
    themes.observe(document.documentElement, { attributeFilter: ["class"], attributes: true })

    const edits = text.onDidChangeContent((event) => {
      changed.current(text.getValue())

      const newLine = !event.isFlush && event.changes.some((change) => change.text.includes("\n"))
      if (!newLine || !editor.hasTextFocus()) return

      window.requestAnimationFrame(() => {
        if (editor.hasTextFocus()) editor.trigger("new-line", "editor.action.triggerSuggest", {})
      })
    })

    return () => {
      themes.disconnect()
      edits.dispose()
      editor.dispose()
      text.dispose()
      model.current = null
    }
  }, [id])

  useEffect(() => {
    const text = model.current
    if (text && value !== text.getValue()) text.setValue(value)
  }, [value])

  return (
    <div
      id={id}
      ref={element}
      data-testid="yaml-editor"
      className="h-[26rem] overflow-hidden rounded-lg border border-input bg-input/30 font-mono text-[13px] leading-[1.6] focus-within:border-ring focus-within:ring-[3px] focus-within:ring-ring/50"
    />
  )
}
