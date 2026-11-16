// The Zalo bridge (issue #657, step 1 of #613): the owner's personal Zalo through zca-js, one account a process,
// run by the control plane with the machine's Node and spoken to in JSON lines on stdin and stdout (protocol.mjs).
// zca-js is what OpenClaw's zalouser, Hermes's Zalo sidecar and zlog use; its session is {imei, cookie, userAgent}
// (zca-js src/zalo.ts, Credentials), which the control plane keeps: the bridge keeps nothing on disk.
//
// Methods:
//   loginQR {}            QR events until the phone confirms; answers {uid, name, credentials}
//   login {credentials}   a session kept before; answers {uid, name}, or the code "rejected" when Zalo refuses it
//   listen {}             starts the listener, once the control plane follows the events: no message comes before
//   groupHistory {group, lastMsgId, isOld}   one page (50) of a group's past: {messages, lastMsgId, isOld, more}
//   oldMessages {lastId}  one page of Zalo's queue of every direct chat's past: {messages, next}
//   users {ids}           people's names: [{id, name}]
//   groups {}             every group the account is in: [{id, name}]
//   stop {}               ends the listener and the process
// Events: qr {image, code}, scanned {name}, declined, expired, credentials {imei, cookie, userAgent},
//   connected, message {...}, closed {code, reason, replaced}, error {message}. A closed listener has stopped
//   retrying (zca-js src/apis/listen.ts): the bridge ends after it, for the control plane to start it again.

import { createInterface } from "node:readline";

import { LoginQRCallbackEventType, Zalo } from "zca-js";

import { answer, closed, event, failure, message, older, parseRequest, rejected } from "./protocol.mjs";
import { ThreadType } from "zca-js";

// The listener reconnects a socket Zalo closed (zlog's zalo-client.ts starts it so); a session another Zalo Web took
// over is told as replaced and not fought for (zca-js README: one web listener an account).
const zalo = new Zalo({ selfListen: true, checkUpdate: false, logging: false });

// maxQR is how many QR codes are shown before the link gives up, as WhatsApp's linking ends after its codes.
const maxQR = 5;

let api = null;

const write = (line) => process.stdout.write(line + "\n");
const emit = (name, data) => write(event(name, data));

function credentials() {
  const ctx = api.getContext();

  return { imei: ctx.imei, cookie: api.getCookie().toJSON().cookies, userAgent: ctx.userAgent };
}

async function whoAmI() {
  const uid = api.getOwnId();
  let name = "";

  try {
    const info = await api.fetchAccountInfo();
    name = info?.profile?.displayName ?? info?.displayName ?? "";
  } catch {
    // The name is shown on the card; the account works without it.
  }

  return { uid: String(uid), name };
}

function listen() {
  api.listener.on("connected", () => {
    emit("connected");
    // Cookies Zalo renewed are kept by the control plane (zlog's cookie-refresh.service.ts keeps them too).
    emit("credentials", credentials());
  });
  api.listener.on("message", (m) => emit("message", message(m)));
  api.listener.on("closed", (code, reason) => {
    // Written, then the process ends: stdout is flushed before exit.
    process.stdout.write(event("closed", closed(code, reason)) + "\n", () => process.exit(0));
  });
  api.listener.on("error", (err) => emit("error", { message: String(err?.message ?? err) }));
  // Answered once the socket is open: a request sent on it before (the direct chats' past) would be lost, since
  // zca-js sends it as it is (listen.ts, sendWs).
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("the listener did not connect in time")), listenWait);
    api.listener.once("connected", () => {
      clearTimeout(timer);
      resolve({});
    });
    api.listener.start({ retryOnClose: true });
  });
}

async function loginQR() {
  let shown = 0;
  let session = null;

  api = await zalo.loginQR({}, (e) => {
    switch (e.type) {
      case LoginQRCallbackEventType.QRCodeGenerated:
        shown++;
        emit("qr", { image: e.data.image, code: e.data.code });
        break;
      case LoginQRCallbackEventType.QRCodeExpired:
        emit("expired");
        if (shown < maxQR) e.actions.retry();
        else e.actions.abort();
        break;
      case LoginQRCallbackEventType.QRCodeScanned:
        emit("scanned", { name: e.data?.display_name ?? "" });
        break;
      case LoginQRCallbackEventType.QRCodeDeclined:
        emit("declined");
        e.actions.abort();
        break;
      case LoginQRCallbackEventType.GotLoginInfo:
        session = e.data;
        break;
    }
  });

  if (!api) throw new Error("the link ended before the phone confirmed");

  return { ...(await whoAmI()), credentials: session ?? credentials() };
}

async function login(params) {
  try {
    api = await zalo.login(params.credentials);
  } catch (err) {
    throw rejected(err);
  }

  return whoAmI();
}

async function groups() {
  const all = await api.getAllGroups();
  const ids = Object.keys(all?.gridVerMap ?? {});
  const out = [];

  // getGroupInfo takes several groups a call; fifty keeps each request small.
  for (let i = 0; i < ids.length; i += 50) {
    const info = await api.getGroupInfo(ids.slice(i, i + 50));
    for (const [id, g] of Object.entries(info?.gridInfoMap ?? {})) {
      out.push({ id, name: typeof g?.name === "string" ? g.name : "" });
    }
  }

  return out;
}

function stop() {
  api?.listener?.stop();
  setImmediate(() => process.exit(0));

  return {};
}

// listenWait bounds the wait for the listener's socket to open.
const listenWait = 30_000;

// oldWait bounds the wait for the listener's answer to an old-messages request (cmd 510), which comes as an event.
const oldWait = 30_000;

// groupHistory is one page of a group's past, as zca-js's getCloudMessage pages it: older with lastMsgId and isOld,
// until hasMore is 0, or a page brings nothing and is filtered or does not move (its own example).
async function groupHistory(p) {
  let page;
  try {
    page = await api.getCloudMessage(String(p.group), p.lastMsgId || undefined, { isOld: p.isOld ? 1 : 0 });
  } catch (err) {
    // -69: the server has nothing more to load (zca-js getCloudMessage).
    if (err?.code === -69) return { messages: [], lastMsgId: "", isOld: false, more: false };
    throw err;
  }

  const messages = (page.groupMsgs ?? []).map(message);
  const stuck = messages.length === 0 && (page.isFiltered || page.lastMsgId === p.lastMsgId);

  return {
    messages,
    lastMsgId: String(page.lastMsgId ?? ""),
    isOld: Boolean(page.isOld),
    more: Boolean(page.hasMore) && !stuck,
  };
}

// oldMessages asks the listener for a page of the direct chats' queue and waits for it (zca-js listen.ts, cmd 510:
// its answer is an old_messages event). The next page starts at the oldest actionId of this one.
function oldMessages(p) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      api.listener.off("old_messages", got);
      reject(new Error("Zalo did not answer the request for older messages"));
    }, oldWait);

    function got(msgs, type) {
      if (type !== ThreadType.User) return;
      clearTimeout(timer);
      api.listener.off("old_messages", got);

      const messages = msgs.map(message);
      resolve({ messages, next: older(messages.map((m) => m.actionId)) });
    }

    api.listener.on("old_messages", got);
    api.listener.requestOldMessages(ThreadType.User, p.lastId || null);
  });
}

// users is people's names, as zaloclaw resolves them: displayName, else zaloName (zca-js getUserInfo).
async function users(p) {
  const ids = (p.ids ?? []).map(String);
  if (ids.length === 0) return [];

  const info = await api.getUserInfo(ids);
  const profiles = { ...(info?.unchanged_profiles ?? {}), ...(info?.changed_profiles ?? {}) };

  return ids.map((id) => ({ id, name: str(profiles[id]?.displayName) || str(profiles[id]?.zaloName) }));
}

const str = (v) => (typeof v === "string" ? v : "");

const methods = { loginQR, login, listen, groups, groupHistory, oldMessages, users, stop };

const lines = createInterface({ input: process.stdin });

lines.on("line", async (line) => {
  const { request, error } = parseRequest(line);
  if (error) {
    emit("error", { message: error });

    return;
  }

  const run = methods[request.method];
  if (!run) {
    write(failure(request.id, `no method ${request.method}`));

    return;
  }

  if (request.method !== "loginQR" && request.method !== "login" && request.method !== "stop" && !api) {
    write(failure(request.id, "not logged in"));

    return;
  }

  try {
    write(answer(request.id, await run(request.params)));
  } catch (err) {
    write(failure(request.id, err));
  }
});

// The control plane closing stdin ends the bridge, as it ends with its parent.
lines.on("close", () => {
  api?.listener?.stop();
  process.exit(0);
});
