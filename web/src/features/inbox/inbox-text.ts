// An Inbox item's text as Markdown (issue #1173): its description and its comments render as Markdown, as circle's
// issue description and Linear's editor, and the ids in them render as links, as Linear renders an issue's id or a
// mention as a link (AGENTS.md: never a bare internal identifier). The text is kept as it was written; only what is
// shown changes.

// IdKind is what an app's id the Inbox links names: a meeting, a chat, a Task, a Run, another Inbox item or an
// Artifact (issue #1207).
type IdKind = "mtg" | "cht" | "tsk" | "run" | "tdo" | "art"

// Service is a messaging service whose message ids an item's text names (issue #1186): Zalo's are numbers, WhatsApp's
// upper-case hexadecimal, Lark's begin with om_ (open-apis/im/v1 message_id).
export type Service = "zalo" | "whatsapp" | "lark"

// Ref is what one link of an item's text names: an app's id, a message of the owner's chats, or a Lark chat (its
// oc_ chat_id).
export type Ref =
  | { kind: IdKind; id: string }
  | { kind: "message"; service: Service; id: string }
  | { kind: "lark_chat"; id: string }

// An app's id is its kind's prefix, an underscore and 26 letters of base32, as the server makes them (ids.New). A
// message's id is as each service makes it: Zalo's msgId, 10 to 20 digits (messaging/zalo, zl_messages.msg_id);
// WhatsApp's, 16 to 40 upper-case hexadecimal digits with a letter and a digit (whatsmeow's MessageID, such as
// 3EB0E056F0533EEF55A863); Lark's om_ message_id and oc_ chat_id (larksuite/cli skills/lark-im/SKILL.md). A number
// is only a candidate: InboxMarkdown links it once the server keeps a message of that id, else it stays as written.
const refs =
  /\b(?:(?:mtg|cht|tsk|run|tdo|art)_[a-z2-7]{26}|om_[0-9a-z]{16,64}|oc_[0-9a-f]{32}|\d{10,20}|(?=[0-9A-F]*\d)(?=[0-9A-F]*[A-F])[0-9A-F]{16,40})\b/g

// refOf is what one match of refs names.
function refOf(match: string): Ref {
  const app = /^(mtg|cht|tsk|run|tdo|art)_/.exec(match)
  if (app) return { kind: app[1] as IdKind, id: match }
  if (match.startsWith("om_")) return { kind: "message", service: "lark", id: match }
  if (match.startsWith("oc_")) return { kind: "lark_chat", id: match }

  return { kind: "message", service: /^\d+$/.test(match) ? "zalo" : "whatsapp", id: match }
}

// hrefOf is where a link of an id leads: a meeting's page, a chat, a Task on Team, the Team page for a Run, which
// opens its first Task once the Run is read, an Inbox item, and an Artifact's own page. A message or a Lark chat has no page of the app's:
// its link is a fragment InboxMarkdown reads back, and opens as its service allows. The link carries the id, so that
// refOfHref finds it again.
function hrefOf(ref: Ref) {
  switch (ref.kind) {
    case "mtg":
      return `/meetings/${ref.id}`
    case "cht":
      return `/chats/${ref.id}`
    case "tsk":
      return `/team?task=${ref.id}`
    case "run":
      return `/team?run=${ref.id}`
    case "tdo":
      return `/inbox?item=${ref.id}`
    case "art":
      return `/artifacts/${ref.id}`
    case "message":
      return `#message-${ref.service}-${ref.id}`
    case "lark_chat":
      return `#lark-chat-${ref.id}`
  }
}

// refOfHref is what a link hrefOf made names; nothing for any other link.
export function refOfHref(href: string | undefined): Ref | undefined {
  const at = href ?? ""
  const app =
    /^\/(?:meetings\/|chats\/|team\?(?:task|run)=|inbox\?item=|artifacts\/)((mtg|cht|tsk|run|tdo|art)_[a-z2-7]{26})$/.exec(
      at,
    )
  if (app) return { kind: app[2] as IdKind, id: app[1]! }

  const message = /^#message-(zalo|whatsapp|lark)-([0-9A-Za-z_]+)$/.exec(at)
  if (message) return { kind: "message", service: message[1] as Service, id: message[2]! }

  const chat = /^#lark-chat-(oc_[0-9a-f]{32})$/.exec(at)

  return chat ? { kind: "lark_chat", id: chat[1]! } : undefined
}

// MdNode is the part of an mdast node the plugin reads and writes; line marks a break that was a line of the text.
type MdNode = {
  type: string
  value?: string
  url?: string
  title?: string | null
  children?: MdNode[]
  line?: boolean
}

// What is never split: a link's own words, code, and raw HTML.
const kept = new Set(["link", "linkReference", "inlineCode", "code", "html", "definition"])

// remarkInboxText turns the ids of a text into links to what they name, and, with paragraphs, each line of a
// paragraph into a paragraph of its own (issue #1186), as Linear's editor makes Enter a new paragraph and
// Shift+Enter a line break: an item's description was plain text before it was Markdown, written line by line, and
// its lines read as paragraphs with space between them. In a list item a line stays a line break inside it, as
// remark-breaks makes it (https://github.com/remarkjs/remark-breaks), so a tight list stays tight.
export function remarkInboxText({ paragraphs = false }: { paragraphs?: boolean } = {}) {
  return () => (tree: MdNode) => walk(tree, paragraphs)
}

function walk(node: MdNode, paragraphs: boolean) {
  if (!node.children) return

  node.children = node.children.flatMap((child) => {
    if (child.type === "text") return split(child.value ?? "", paragraphs)
    if (!kept.has(child.type)) walk(child, paragraphs)
    if (paragraphs && child.type === "paragraph" && node.type !== "listItem") return lines(child)

    return [child]
  })
}

// lines is a paragraph cut at each of its lines: those of its own text, not a hard break written in it, nor a line
// inside its emphasis or a link. A line with nothing on it is left out.
function lines(paragraph: MdNode): MdNode[] {
  const parts: MdNode[][] = [[]]
  for (const child of paragraph.children ?? []) {
    if (child.type === "break" && child.line) parts.push([])
    else parts.at(-1)!.push(child)
  }

  return parts
    .filter((part) => part.some((n) => n.type !== "text" || n.value?.trim()))
    .map((children) => ({ ...paragraph, children }))
}

function split(text: string, paragraphs: boolean): MdNode[] {
  const out: MdNode[] = []
  const words = (part: string) => {
    if (!paragraphs) {
      if (part) out.push({ type: "text", value: part })

      return
    }
    part.split(/\r?\n|\r/).forEach((line, i) => {
      if (i > 0) out.push({ type: "break", line: true })
      if (line) out.push({ type: "text", value: line })
    })
  }

  let at = 0
  for (const match of text.matchAll(refs)) {
    words(text.slice(at, match.index))
    out.push({
      type: "link",
      url: hrefOf(refOf(match[0])),
      title: null,
      children: [{ type: "text", value: match[0] }],
    })
    at = match.index + match[0].length
  }
  words(text.slice(at))

  return out
}
