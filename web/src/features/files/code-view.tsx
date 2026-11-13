import "@/features/agents/monaco-environment"

import * as monaco from "monaco-editor"
import { useEffect, useRef, useState } from "react"

import { appearance } from "@/lib/monaco-appearance"

// CodeView shows a file of a chat's folder in Monaco, read only, or its diff since HEAD in Monaco's diff editor
// (issue #255). As Omnigent's CodeViewer.tsx and MonacoDiffViewer.tsx: the language from the file's name, the
// diff unified with unchanged lines folded to three of context, side by side once there is room (920 px,
// SPLIT_DIFF_MIN_WIDTH); a file read again keeps where the owner had scrolled. Orca shows files and diffs in Monaco
// too (editor/monaco-view, editor/diff).
// A file is editable when onChange is given (issue #297): its text is then the owner's, sent back on each change,
// and Ctrl+S (Cmd+S) calls onSave.
export type CodeViewProps =
  | {
      kind: "file"
      path: string
      content: string
      onChange?: (text: string) => void
      onSave?: () => void
    }
  | { kind: "diff"; path: string; before: string; after: string }

const splitFrom = 920

// languageOf is Monaco's language for a file's name, or plain text.
function languageOf(path: string) {
  const name = path.slice(path.lastIndexOf("/") + 1).toLowerCase()
  const dot = name.lastIndexOf(".")
  const ext = dot > 0 ? name.slice(dot) : ""

  return (
    monaco.languages
      .getLanguages()
      .find(
        (l) =>
          l.filenames?.some((f) => f.toLowerCase() === name) ||
          (ext !== "" && l.extensions?.includes(ext)),
      )?.id ?? "plaintext"
  )
}

let models = 0

// model is a new model for a text, with a name of its own.
function model(text: string, path: string) {
  models += 1

  return monaco.editor.createModel(
    text,
    languageOf(path),
    monaco.Uri.parse(`inmemory://files/${models}/${path}`),
  )
}

// keep sets a model's text and keeps where the editor was.
function keep(editor: monaco.editor.ICodeEditor, m: monaco.editor.ITextModel, text: string) {
  if (m.getValue() === text) return

  const state = editor.saveViewState()
  m.setValue(text)
  if (state) editor.restoreViewState(state)
}

// Orca's file editor's options (MonacoEditor.tsx): no minimap, the line in front highlighted, no space above the
// first line, smooth scrolling. Changed: the overview ruler shows no cursor and no border, which on a read-only
// view is only a stray mark (owner, 2026-11-13).
const common = {
  automaticLayout: true,
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  renderLineHighlight: "line",
  padding: { top: 0 },
  smoothScrolling: true,
  hideCursorInOverviewRuler: true,
  overviewRulerBorder: false,
} satisfies monaco.editor.IEditorOptions

// Orca's diff scrollbars are wider, so that a dense diff's marks leave something to drag
// (diff-editor-scrollbar-options.ts).
const diffScrollbar = {
  verticalScrollbarSize: 20,
  horizontalScrollbarSize: 20,
  verticalSliderSize: 20,
  horizontalSliderSize: 20,
} satisfies monaco.editor.IEditorScrollbarOptions

export default function CodeView(props: CodeViewProps) {
  const host = useRef<HTMLDivElement>(null)
  // The latest callbacks, for the editor's own events, which are wired once.
  const handlers = useRef(props.kind === "file" ? props : undefined)
  useEffect(() => {
    handlers.current = props.kind === "file" ? props : undefined
  })
  const file = useRef<{
    editor: monaco.editor.IStandaloneCodeEditor
    model: monaco.editor.ITextModel
  }>(null)
  const diff = useRef<{
    editor: monaco.editor.IStandaloneDiffEditor
    before: monaco.editor.ITextModel
    after: monaco.editor.ITextModel
  }>(null)

  // The editor is made once, with the text it first has; a new text goes into its models. Another file or view is
  // another CodeView: its tab's.
  const [first] = useState(props)
  useEffect(() => {
    const element = host.current
    if (!element) return

    const look = appearance(element)
    const themes = new MutationObserver(() => monaco.editor.setTheme(appearance(element).theme))
    themes.observe(document.documentElement, { attributeFilter: ["class"], attributes: true })

    if (first.kind === "file") {
      const m = model(first.content, first.path)
      const editable = first.onChange !== undefined
      const editor = monaco.editor.create(element, {
        ...common,
        ...look,
        model: m,
        readOnly: !editable,
        domReadOnly: !editable,
      })
      file.current = { editor, model: m }

      const typed = m.onDidChangeContent((event) => {
        // Text put in from outside (a reload) is not the owner's typing.
        if (!event.isFlush) handlers.current?.onChange?.(m.getValue())
      })
      editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () =>
        handlers.current?.onSave?.(),
      )

      return () => {
        typed.dispose()
        themes.disconnect()
        editor.dispose()
        m.dispose()
        file.current = null
      }
    }

    const before = model(first.before, first.path)
    const after = model(first.after, first.path)
    const editor = monaco.editor.createDiffEditor(element, {
      ...common,
      ...look,
      readOnly: true,
      domReadOnly: true,
      originalEditable: false,
      renderSideBySide: element.clientWidth >= splitFrom,
      useInlineViewWhenSpaceIsLimited: false,
      hideUnchangedRegions: { enabled: true, contextLineCount: 3 },
      renderOverviewRuler: true,
      scrollbar: diffScrollbar,
    })
    editor.setModel({ original: before, modified: after })
    diff.current = { editor, before, after }

    const sizes = new ResizeObserver(() =>
      editor.updateOptions({ renderSideBySide: element.clientWidth >= splitFrom }),
    )
    sizes.observe(element)

    return () => {
      themes.disconnect()
      sizes.disconnect()
      editor.dispose()
      before.dispose()
      after.dispose()
      diff.current = null
    }
  }, [first])

  useEffect(() => {
    if (props.kind === "file" && file.current) {
      keep(file.current.editor, file.current.model, props.content)
    }
    if (props.kind === "diff" && diff.current) {
      keep(diff.current.editor.getOriginalEditor(), diff.current.before, props.before)
      keep(diff.current.editor.getModifiedEditor(), diff.current.after, props.after)
    }
  })

  return (
    <div
      ref={host}
      data-testid={props.kind === "diff" ? "diff-view" : "code-view"}
      className="min-h-0 flex-1 font-mono text-[13px]"
    />
  )
}
