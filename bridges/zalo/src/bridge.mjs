// The Zalo bridge (issue #657, step 1 of #613): the owner's personal Zalo through zca-js, one account a process,
// run by the control plane with the machine's Node and spoken to in JSON lines on stdin and stdout (protocol.mjs).
// zca-js is what OpenClaw's zalouser, Hermes's Zalo sidecar and zlog use; its session is {imei, cookie, userAgent}
// (zca-js src/zalo.ts, Credentials), which the control plane keeps: the bridge keeps nothing on disk.
//
// Methods:
//   loginQR {}            QR events until the phone confirms, then a `credentials` event; answers {uid, name}
//   login {credentials}   a session kept before; answers {uid, name}
//   groups {}             every group the account is in: [{id, name}]
//   stop {}               ends the listener and the process
// Events: qr {image, code}, scanned {name}, declined, expired, credentials {imei, cookie, userAgent},
//   connected, message {...}, closed {code, reason, replaced}, error {message}.

import { createInterface } from "node:readline";

import { LoginQRCallbackEventType, Zalo } from "zca-js";

import { answer, closed, event, failure, message, parseRequest } from "./protocol.mjs";

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
  api.listener.on("closed", (code, reason) => emit("closed", closed(code, reason)));
  api.listener.on("error", (err) => emit("error", { message: String(err?.message ?? err) }));
  api.listener.start({ retryOnClose: true });
}

async function loginQR() {
  let shown = 0;

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
        emit("credentials", e.data);
        break;
    }
  });

  if (!api) throw new Error("the link ended before the phone confirmed");

  listen();

  return whoAmI();
}

async function login(params) {
  api = await zalo.login(params.credentials);
  listen();

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

const methods = { loginQR, login, groups, stop };

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
