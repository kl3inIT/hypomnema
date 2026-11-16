// The bridge's protocol with the control plane (issue #657): JSON lines on stdin and stdout, as Hermes's WhatsApp
// adapter and MemoryOS's bridge speak (MemoryOS ADR 0031). A request is {id, method, params}; its answer
// {id, result} or {id, error}; an event, which answers nothing, {event, data}. These are pure functions, tested
// without Zalo.

// parseRequest reads one line from the control plane: a request, or why it is not one.
export function parseRequest(line) {
  let req;
  try {
    req = JSON.parse(line);
  } catch {
    return { error: "not JSON" };
  }

  if (req === null || typeof req !== "object" || typeof req.method !== "string" || req.id === undefined) {
    return { error: "a request is {id, method, params}" };
  }

  return { request: { id: req.id, method: req.method, params: req.params ?? {} } };
}

export const answer = (id, result) => JSON.stringify({ id, result });
export const failure = (id, error) => JSON.stringify({ id, error: String(error?.message ?? error) });
export const event = (name, data) => JSON.stringify({ event: name, data: data ?? null });

// The close codes Zalo ends a listener with when another session took the account
// (zca-js src/apis/listen.ts: DuplicateConnection, KickConnection).
const REPLACED = new Set([3000, 3003]);

// closed says how a listener's close is told: replaced by another Zalo Web, or lost.
export function closed(code, reason) {
  return { code, reason: String(reason ?? ""), replaced: REPLACED.has(Number(code)) };
}

// text is what a message says: its text, or an attachment's title and description.
function text(content) {
  if (typeof content === "string") return content;
  if (content && typeof content === "object") {
    return [content.title, content.description].filter((s) => typeof s === "string" && s !== "").join("\n");
  }

  return "";
}

// attachment is a message's file or link, when it has one (zca-js TAttachmentContent).
function attachment(content) {
  if (!content || typeof content !== "object" || typeof content.href !== "string" || content.href === "") {
    return null;
  }

  return {
    url: content.href,
    title: typeof content.title === "string" ? content.title : "",
    thumb: typeof content.thumb === "string" ? content.thumb : "",
  };
}

// message is a zca-js UserMessage or GroupMessage as the control plane keeps it (zca-js src/models/Message.ts).
export function message(m) {
  const d = m?.data ?? {};

  return {
    id: String(d.msgId ?? ""),
    cliId: String(d.cliMsgId ?? ""),
    chat: String(m?.threadId ?? ""),
    group: m?.type === 1,
    from: String(d.uidFrom ?? ""),
    fromName: typeof d.dName === "string" ? d.dName : "",
    fromMe: Boolean(m?.isSelf),
    at: Number(d.ts ?? 0),
    kind: typeof d.msgType === "string" ? d.msgType : "",
    text: text(d.content),
    attachment: attachment(d.content),
    quoteId: d.quote ? String(d.quote.globalMsgId ?? "") : "",
  };
}
