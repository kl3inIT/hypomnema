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
export const failure = (id, error) =>
  JSON.stringify({ id, error: String(error?.message ?? error), ...(error?.code ? { code: error.code } : {}) });
export const event = (name, data) => JSON.stringify({ event: name, data: data ?? null });

// zca-js's words when Zalo answers a login without the account's info, as for cookies it no longer takes
// (src/zalo.ts, loginCookie); a network failure says something else.
const REFUSED = "Đăng nhập thất bại";

// rejected marks a login error as Zalo refusing the session (code "rejected"), which no retry mends.
export function rejected(err) {
  if (String(err?.message ?? err).includes(REFUSED)) {
    const out = new Error(String(err.message));
    out.code = "rejected";

    return out;
  }

  return err;
}

// The close codes Zalo ends a listener with when another session took the account
// (zca-js src/apis/listen.ts: DuplicateConnection, KickConnection).
const REPLACED = new Set([3000, 3003]);

// closed says how a listener's close is told: replaced by another Zalo Web, or lost.
export function closed(code, reason) {
  return { code, reason: String(reason ?? ""), replaced: REPLACED.has(Number(code)) };
}

const str = (v) => (typeof v === "string" ? v : "");

// params is an attachment's extra fields, which Zalo sends as a JSON string (zca-js TAttachmentContent.params).
function params(content) {
  if (typeof content.params !== "string" || content.params === "") return {};
  try {
    const out = JSON.parse(content.params);

    return out && typeof out === "object" ? out : {};
  } catch {
    return {};
  }
}

// media reads a message's content as its text and its file, after the Hermes Zalo plugin's parseContent
// (sidecar/server.js) and zaloclaw's media URLs (src/channel/monitor.ts): an image's best URL is hdUrl, then
// normalUrl; a file's is fileUrl, then href; a voice note is an .m4a href. A link keeps its title as the text and
// its address after it; a sticker and a call keep no file.
export function media(msgType, content) {
  const type = str(msgType);
  if (typeof content === "string") return { text: content, media: null };
  if (!content || typeof content !== "object") return { text: "", media: null };

  const p = params(content);
  const title = str(content.title);
  const description = str(content.description);
  const href = str(content.href);
  const file = (mediaType, url, fileName, size, text) => ({
    text,
    media: url ? { type: mediaType, url, fileName, size: Number(size) || 0 } : { type: mediaType, url: "", fileName, size: 0 },
  });

  if (/call/i.test(str(content.action))) return { text: [title, description].filter(Boolean).join("\n"), media: null };
  if (type === "chat.sticker") return { text: "", media: { type: "sticker", url: "", fileName: "", size: 0 } };
  if (type === "chat.photo" || type === "chat.gif" || content.hdUrl || content.normalUrl || content.oriUrl) {
    const url = str(content.hdUrl) || str(p.hd) || str(content.normalUrl) || str(content.oriUrl) || href;

    return file("image", url, "", 0, title);
  }
  if (type === "chat.video.msg") return file("video", href, "", p.fileSize, title);
  if (type === "chat.voice" || /\.(m4a|aac|mp3|opus|ogg)(\?|$)/i.test(str(content.fileName) || href)) {
    return file("audio", str(content.fileUrl) || href, str(content.fileName), content.totalSize ?? p.fileSize, "");
  }
  if (type === "share.file" || content.fileUrl) {
    return file("document", str(content.fileUrl) || href, str(content.fileName) || title, content.totalSize ?? p.fileSize, "");
  }
  if (href) return { text: [title, description, href].filter(Boolean).join("\n"), media: null };

  return { text: [title, description].filter(Boolean).join("\n"), media: null };
}

// message is a zca-js UserMessage or GroupMessage as the control plane keeps it (zca-js src/models/Message.ts).
export function message(m) {
  const d = m?.data ?? {};
  const { text, media: file } = media(d.msgType, d.content);

  return {
    id: String(d.msgId ?? ""),
    cliId: String(d.cliMsgId ?? ""),
    actionId: String(d.actionId ?? ""),
    chat: String(m?.threadId ?? ""),
    group: m?.type === 1,
    from: String(d.uidFrom ?? ""),
    fromName: str(d.dName),
    fromMe: Boolean(m?.isSelf),
    at: Number(d.ts ?? 0),
    kind: str(d.msgType),
    text,
    media: file,
    quoteId: d.quote ? String(d.quote.globalMsgId ?? "") : "",
  };
}

// older is the oldest of some actionIds, the cursor of the next page of the direct chats' queue (zca-js
// requestOldMessages; softtyos realtime/listener.ts names it the actionId). They are decimal strings too long for a
// Number.
export function older(ids) {
  let out = "";
  for (const id of ids) {
    if (!/^\d+$/.test(id)) continue;
    if (out === "" || id.length < out.length || (id.length === out.length && id < out)) out = id;
  }

  return out;
}
