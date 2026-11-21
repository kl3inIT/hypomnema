// stubPush replaces the browser's service worker and Push API with ones that answer at once: permission is granted
// when asked, and subscribing gives a subscription with its endpoint and keys. window.pushCalls records what the
// page asked of them. The page's scripts see only what a browser gives, so the stub is a script, not a module.
export const stubPush = `
  (() => {
    const calls = []
    window.pushCalls = calls
    let permission = "default"
    let current = null
    const subscription = (key) => ({
      endpoint: "https://push.example/this-browser",
      options: { applicationServerKey: key },
      toJSON: () => ({ endpoint: "https://push.example/this-browser", keys: { p256dh: "BUserKey", auth: "authSecret" } }),
      unsubscribe: async () => { calls.push("unsubscribe"); current = null; return true },
    })
    const pushManager = {
      getSubscription: async () => current,
      subscribe: async (options) => {
        calls.push("subscribe")
        current = subscription(options.applicationServerKey.buffer)
        return current
      },
    }
    const registration = { pushManager, scope: location.origin + "/" }
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: { register: async () => registration, ready: Promise.resolve(registration) },
    })
    window.PushManager = function PushManager() {}
    window.Notification = {
      get permission() { return permission },
      requestPermission: async () => { calls.push("requestPermission"); permission = "granted"; return permission },
    }
  })()
`
