// An Inbox item's text as Markdown (issue #1173): its description and its comments render as Markdown, as circle's
// issue description and Linear's editor, and the app's ids in them render as links, as Linear renders an issue's id
// or a mention as a link (AGENTS.md: never a bare internal identifier). The text is kept as it was written; only
// what is shown changes.

// IdKind is what an id the Inbox links names: a meeting, a chat, a Task or a Run.
export type IdKind = "mtg" | "cht" | "tsk" | "run"

// An id is its kind's prefix, an underscore and 26 letters of base32, as the server makes them (ids.New).
const ids = /\b(mtg|cht|tsk|run)_[a-z2-7]{26}\b/g

// idHref is where an id's link leads: a meeting's page, a chat, a Task on Team, and the Team page for a Run, which
// opens its first Task once the Run is read (IdLink). The link carries the id, so that IdLink finds it again.
function idHref(id: string) {
  switch (id.slice(0, 3) as IdKind) {
    case "mtg":
      return `/meetings/${id}`
    case "cht":
      return `/chats/${id}`
    case "tsk":
      return `/team?task=${id}`
    case "run":
      return `/team?run=${id}`
  }
}

// idOfHref is the id a link idHref made names; nothing for any other link.
export function idOfHref(href: string | undefined): { kind: IdKind; id: string } | undefined {
  const match =
    /^\/(?:meetings\/|chats\/|team\?(?:task|run)=)((mtg|cht|tsk|run)_[a-z2-7]{26})$/.exec(
      href ?? "",
    )

  return match ? { kind: match[2] as IdKind, id: match[1]! } : undefined
}

// MdNode is the part of an mdast node the plugin reads and writes.
type MdNode = {
  type: string
  value?: string
  url?: string
  title?: string | null
  children?: MdNode[]
}

// What is never split: a link's own words, code, and raw HTML.
const kept = new Set(["link", "linkReference", "inlineCode", "code", "html", "definition"])

// remarkInboxText turns the ids of a text into links to what they name, and, with breaks, each line break into a
// break, as remark-breaks does (https://github.com/remarkjs/remark-breaks: a newline is a <br>, as GitHub shows a
// comment): an item's description was plain text before it was Markdown, written line by line.
export function remarkInboxText({ breaks = false }: { breaks?: boolean } = {}) {
  return () => (tree: MdNode) => walk(tree, breaks)
}

function walk(node: MdNode, breaks: boolean) {
  if (!node.children) return

  node.children = node.children.flatMap((child) => {
    if (child.type === "text") return split(child.value ?? "", breaks)
    if (!kept.has(child.type)) walk(child, breaks)

    return [child]
  })
}

function split(text: string, breaks: boolean): MdNode[] {
  const out: MdNode[] = []
  const words = (part: string) => {
    if (!breaks) {
      if (part) out.push({ type: "text", value: part })

      return
    }
    part.split(/\r?\n|\r/).forEach((line, i) => {
      if (i > 0) out.push({ type: "break" })
      if (line) out.push({ type: "text", value: line })
    })
  }

  let at = 0
  for (const match of text.matchAll(ids)) {
    words(text.slice(at, match.index))
    out.push({
      type: "link",
      url: idHref(match[0]),
      title: null,
      children: [{ type: "text", value: match[0] }],
    })
    at = match.index + match[0].length
  }
  words(text.slice(at))

  return out
}
