// Web Push in this browser (issue #1047): whether it can take a notification, the steps an iPhone needs first, and
// the subscription the control plane pushes to. Permission is asked only from a button the owner presses (the owner's
// Second Bite, web/src/features/notification/push.ts:13-21; OpenClaw, ui/src/app/web-push.ts).

// PushSupport is what this browser can do: take pushes, take them once opened from the Home Screen (an iPhone or
// iPad in Safari, OpenClaw's install-required), or not at all.
export type PushSupport = "supported" | "install" | "unsupported"

// The parts of the browser the helpers read, given in tests.
export type PushEnv = {
  navigator: Navigator & { standalone?: boolean }
  window: Window
}

const browserEnv = (): PushEnv => ({ navigator, window })

// isAppleMobile says the browser runs on an iPhone, an iPad or an iPod, an iPad in desktop mode included, where Web
// Push needs the app on the Home Screen (iOS 16.4+): OpenClaw's isIosBrowserPlatform
// (ui/src/lib/browser-platform.ts).
export function isAppleMobile(nav: Pick<Navigator, "userAgent" | "platform" | "maxTouchPoints">) {
  return (
    /iPad|iPhone|iPod/.test(nav.userAgent) ||
    (nav.platform === "MacIntel" && nav.maxTouchPoints > 1)
  )
}

// isStandalone says the app was opened from the Home Screen: Safari's navigator.standalone, or the manifest's
// display mode.
export function isStandalone(env: PushEnv = browserEnv()) {
  return (
    env.navigator.standalone === true ||
    env.window.matchMedia?.("(display-mode: standalone)").matches === true
  )
}

// pushSupport is what this browser can do with Web Push, as OpenClaw's createWebPushCapability decides it.
export function pushSupport(env: PushEnv = browserEnv()): PushSupport {
  if (isAppleMobile(env.navigator) && !isStandalone(env)) return "install"

  const able =
    "serviceWorker" in env.navigator && "PushManager" in env.window && "Notification" in env.window

  return able ? "supported" : "unsupported"
}

// deviceLabel names this device in the owner's list: what it is and which browser, from its user agent.
export function deviceLabel(userAgent: string) {
  const device = /iPad/.test(userAgent)
    ? "iPad"
    : /iPhone|iPod/.test(userAgent)
      ? "iPhone"
      : /Android/.test(userAgent)
        ? "Android"
        : /Macintosh|Mac OS X/.test(userAgent)
          ? "Mac"
          : /Windows/.test(userAgent)
            ? "Windows"
            : /Linux|X11/.test(userAgent)
              ? "Linux"
              : "Device"
  const browser = /Edg\//.test(userAgent)
    ? "Edge"
    : /Firefox\/|FxiOS/.test(userAgent)
      ? "Firefox"
      : /Chrome\/|CriOS/.test(userAgent)
        ? "Chrome"
        : /Safari\//.test(userAgent)
          ? "Safari"
          : ""

  return browser ? `${device} · ${browser}` : device
}

// serverKey is the VAPID public key, unpadded base64url, as the bytes pushManager.subscribe takes.
export function serverKey(base64url: string) {
  const base64 = base64url.replaceAll("-", "+").replaceAll("_", "/")
  const padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4)

  return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0))
}

function sameKey(a: ArrayBuffer | null, b: Uint8Array) {
  if (!a || a.byteLength !== b.byteLength) return false

  const bytes = new Uint8Array(a)

  return bytes.every((v, i) => v === b[i])
}

// registerServiceWorker registers the worker that shows pushes (web/public/sw.js), as Coder's index.tsx does. It
// handles no fetch, so it changes nothing of how the app loads.
export function registerServiceWorker(env: PushEnv = browserEnv()) {
  if (!("serviceWorker" in env.navigator)) return

  void env.navigator.serviceWorker.register("/sw.js").catch(() => {})
}

// currentSubscription is this browser's push subscription, null when it has none.
export async function currentSubscription(env: PushEnv = browserEnv()) {
  if (pushSupport(env) !== "supported") return null

  const registration = await env.navigator.serviceWorker.ready

  return registration.pushManager.getSubscription()
}

// NewSubscription is what the control plane keeps of a subscription.
export type NewSubscription = { endpoint: string; keys: { p256dh: string; auth: string } }

// subscribe asks for permission and subscribes this browser with the control plane's key. A subscription made with
// another key (the keys were made again) is dropped first: the push service would refuse what the new key signs.
// Permission is asked first, so a blocked site says so plainly, as Coder's useWebpushNotifications does.
export async function subscribe(
  publicKey: string,
  env: PushEnv = browserEnv(),
  notification: Pick<typeof Notification, "requestPermission"> = Notification,
): Promise<NewSubscription> {
  const permission = await notification.requestPermission()
  if (permission !== "granted") {
    throw new Error(
      "Notifications are blocked for this site. Allow them in the browser's settings, then try again.",
    )
  }

  const registration = await env.navigator.serviceWorker.ready
  const key = serverKey(publicKey)
  let subscription = await registration.pushManager.getSubscription()
  if (subscription && !sameKey(subscription.options.applicationServerKey, key)) {
    await subscription.unsubscribe()
    subscription = null
  }

  subscription ??= await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: key,
  })
  const json = subscription.toJSON()
  if (!json.endpoint || !json.keys?.p256dh || !json.keys.auth) {
    throw new Error("The browser gave a subscription without its keys.")
  }

  return { endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth } }
}

// unsubscribeHere drops this browser's subscription, when it has one.
export async function unsubscribeHere(env: PushEnv = browserEnv()) {
  const subscription = await currentSubscription(env)
  await subscription?.unsubscribe()
}
