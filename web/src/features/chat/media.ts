// A Bot asks for a file to go with its reply with a line MEDIA:<path> (issue #312), which a Channel such as Lark
// sends after the reply's text (internal/controlplane/channels/media.go). In the web chat the line becomes a file
// in the message to download, and leaves the text shown (issue #699), as OpenClaw's web chat attaches a reply's
// MEDIA: lines to its message and strips them from what it shows (chat-send-commentary-media.ts,
// splitMediaFromOutput; chat-display-projection.helpers.ts, stripAssistantMediaDirectivesForDisplay). The lines
// are read by media.go's rules, mediaOf and cleanPath, so a reply asks for the same files in every chat.

// mediaLine is a line that asks for a file: MEDIA: at its start, which consumes the whole line (media.go).
const mediaLine = /^\s*MEDIA:\s*(.*?)\s*$/i

// mediaOf splits a reply into the text shown and the paths of the files it asks for. A line in a fenced code block
// is text. A reply that asks for none is as it was. fence is the fence open where the text starts: a message the
// server keeps in several text parts is one text to a Channel, which reads them joined (coordinator/activity.go), so
// a fence opened in one part goes on in the next; the fence open where the text ends is returned for the next part.
export function mediaOf(
  text: string,
  fence = "",
): { shown: string; paths: string[]; fence: string } {
  const kept: string[] = []
  const paths: string[] = []
  let open = fence
  let asked = false

  for (const line of text.split("\n")) {
    const trimmed = line.trim()

    if (open !== "") {
      if (trimmed.startsWith(open)) open = ""
    } else if (trimmed.startsWith("```")) {
      open = "```"
    } else if (trimmed.startsWith("~~~")) {
      open = "~~~"
    } else {
      const m = mediaLine.exec(line)
      if (m) {
        asked = true
        const p = cleanPath(m[1] ?? "")
        if (p !== "") paths.push(p)
        continue
      }
    }

    kept.push(line)
  }

  if (!asked) return { shown: text, paths: [], fence: open }

  return { shown: kept.join("\n").trim(), paths, fence: open }
}

// cleanPath is a path without the quotes, backticks and brackets a model wraps it in, and without file://
// (media.go, cleanPath).
function cleanPath(raw: string) {
  const p = raw.replace(/^[`"'[{(<]+/, "").replace(/[`"'\\})\],>]+$/, "")

  return (p.startsWith("file://") ? p.slice("file://".length) : p).trim()
}

// folderPath is the path a reply named, as the Bot's folder is read with it: relative to the folder, as the Bots
// are told to write it (deploy/coordinator/AGENTS.md); the server refuses one outside the folder.
export function folderPath(path: string) {
  return path.replace(/^(\.\/)+/, "")
}

// fileName is the last part of a path.
export function fileName(path: string) {
  return path.slice(path.lastIndexOf("/") + 1) || path
}
