import { expect, test, vi } from "vitest"

import {
  deviceLabel,
  isAppleMobile,
  isStandalone,
  pushSupport,
  serverKey,
  subscribe,
  type PushEnv,
} from "./push"

const iPhone =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 18_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Mobile/15E148 Safari/604.1"
const macChrome =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
const macSafari =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.4 Safari/605.1.15"

// A P-256 public key as the control plane serves it: the uncompressed point, unpadded base64url.
const publicKey =
  "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM"

type Stub = {
  userAgent?: string
  platform?: string
  maxTouchPoints?: number
  standalone?: boolean
  displayStandalone?: boolean
  push?: boolean
}

function env(stub: Stub, pushManager?: unknown): PushEnv {
  const nav: Record<string, unknown> = {
    userAgent: stub.userAgent ?? macChrome,
    platform: stub.platform ?? "MacIntel",
    maxTouchPoints: stub.maxTouchPoints ?? 0,
    standalone: stub.standalone,
  }
  const win: Record<string, unknown> = {
    matchMedia: () => ({ matches: stub.displayStandalone === true }),
  }
  if (stub.push !== false) {
    nav.serviceWorker = { ready: Promise.resolve({ pushManager }) }
    win.PushManager = function PushManager() {}
    win.Notification = function Notification() {}
  }

  return { navigator: nav as unknown as PushEnv["navigator"], window: win as unknown as Window }
}

// An iPhone or iPad in Safari takes pushes only once Hypomnema is opened from the Home Screen (iOS 16.4+): until
// then the page shows the steps to add it, as OpenClaw's install-required. An iPad in desktop mode reads as a Mac
// with touch. A desktop browser with the Push API takes them; one without does not.
test("an Apple phone needs the Home Screen first; a desktop browser takes pushes", () => {
  expect(isAppleMobile({ userAgent: iPhone, platform: "iPhone", maxTouchPoints: 5 })).toBe(true)
  expect(isAppleMobile({ userAgent: macSafari, platform: "MacIntel", maxTouchPoints: 5 })).toBe(
    true,
  )
  expect(isAppleMobile({ userAgent: macSafari, platform: "MacIntel", maxTouchPoints: 0 })).toBe(
    false,
  )

  expect(pushSupport(env({ userAgent: iPhone, platform: "iPhone", maxTouchPoints: 5 }))).toBe(
    "install",
  )
  expect(
    pushSupport(
      env({ userAgent: iPhone, platform: "iPhone", maxTouchPoints: 5, standalone: true }),
    ),
  ).toBe("supported")
  expect(isStandalone(env({ displayStandalone: true }))).toBe(true)
  expect(pushSupport(env({ userAgent: macChrome }))).toBe("supported")
  expect(pushSupport(env({ userAgent: macChrome, push: false }))).toBe("unsupported")
})

test("a device is named by what it is and its browser", () => {
  expect(deviceLabel(iPhone)).toBe("iPhone · Safari")
  expect(deviceLabel(macChrome)).toBe("Mac · Chrome")
  expect(deviceLabel(macSafari)).toBe("Mac · Safari")
  expect(
    deviceLabel(
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0",
    ),
  ).toBe("Windows · Edge")
  expect(
    deviceLabel("Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"),
  ).toBe("Linux · Firefox")
})

test("the control plane's key becomes the 65 bytes of a P-256 point", () => {
  const key = serverKey(publicKey)
  expect(key).toHaveLength(65)
  expect(key[0]).toBe(4)
})

function subscription(endpoint: string, key: Uint8Array | null) {
  return {
    endpoint,
    options: { applicationServerKey: key ? key.buffer : null },
    unsubscribe: vi.fn<() => Promise<boolean>>(async () => true),
    toJSON: () => ({ endpoint, keys: { p256dh: "BUser", auth: "secret" } }),
  }
}

type Sub = ReturnType<typeof subscription>

// Turning on asks the browser's permission, then subscribes with the control plane's key. A subscription made with
// the key before it was made again is dropped and made anew; one made with this key is kept.
test("subscribing asks permission, then subscribes with the control plane's key", async () => {
  const old = subscription("https://push.example/old", new Uint8Array(65).fill(1))
  const made = subscription("https://push.example/new", serverKey(publicKey))
  const pushManager = {
    getSubscription: vi.fn<() => Promise<Sub>>(async () => old),
    subscribe: vi.fn<() => Promise<Sub>>(async () => made),
  }
  const granted = {
    requestPermission: vi.fn<() => Promise<NotificationPermission>>(async () => "granted"),
  }

  await expect(subscribe(publicKey, env({}, pushManager), granted)).resolves.toEqual({
    endpoint: "https://push.example/new",
    keys: { p256dh: "BUser", auth: "secret" },
  })
  expect(granted.requestPermission).toHaveBeenCalledOnce()
  expect(old.unsubscribe).toHaveBeenCalledOnce()
  expect(pushManager.subscribe).toHaveBeenCalledWith({
    userVisibleOnly: true,
    applicationServerKey: serverKey(publicKey),
  })

  pushManager.getSubscription.mockResolvedValue(made)
  pushManager.subscribe.mockClear()
  await subscribe(publicKey, env({}, pushManager), granted)
  expect(pushManager.subscribe).not.toHaveBeenCalled()
  expect(made.unsubscribe).not.toHaveBeenCalled()
})

test("a site whose notifications are blocked says so and subscribes nothing", async () => {
  const pushManager = {
    getSubscription: vi.fn<() => Promise<null>>(),
    subscribe: vi.fn<() => Promise<null>>(),
  }
  const denied = {
    requestPermission: vi.fn<() => Promise<NotificationPermission>>(async () => "denied"),
  }

  await expect(subscribe(publicKey, env({}, pushManager), denied)).rejects.toThrow(/blocked/)
  expect(pushManager.subscribe).not.toHaveBeenCalled()
})
