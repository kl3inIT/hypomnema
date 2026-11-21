// The web client's service worker (issue #1047). It shows Web Push notifications and opens their page on a click;
// it handles no fetch and caches nothing, so the app loads as it would without it. Hand-written, as the owner's
// Second Bite's sw.js and OpenClaw's ui/public/sw.js are, and served at the root so its scope is the whole app.

/* global self, clients */

// A push is a Declarative Web Push message ({web_push: 8030, notification: {title, body, navigate, tag}}), which
// Safari shows by itself; this shows it in the browsers that hand it here, as Coder's serviceWorker.ts does. Safari
// shows the declarative notification when this handler shows none (WebKit's "Meet Declarative Web Push"), so the
// chat in view is not hidden there; the design keeps the declarative form so a push shows even when the worker does
// not run (issue #1047).
self.addEventListener("push", (event) => {
  if (!event.data) return

  let notification
  try {
    notification = event.data.json().notification
  } catch {
    return
  }
  if (!notification || !notification.title) return

  const target = new URL(notification.navigate || "/", self.location.origin)

  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
      // A push for the chat the owner is reading is not shown (Coder, serviceWorker.ts:25-79); any other page's is,
      // the test push that opens Settings included.
      const looking =
        target.pathname.startsWith("/chats/") &&
        windows.some(
          (w) =>
            w.visibilityState === "visible" &&
            w.focused &&
            new URL(w.url).pathname === target.pathname,
        )
      if (looking) return undefined

      return self.registration.showNotification(notification.title, {
        body: notification.body || "",
        tag: notification.tag,
        icon: "/icon-192x192.png",
        badge: "/favicon-32x32.png",
        data: { url: target.pathname + target.search },
        navigate: target.href,
      })
    }),
  )
})

// A click focuses a window already on the page, else takes an open window of the app there, else opens one, as all
// four references do (Coder's serviceWorker.ts:141-158, OpenClaw's sw.js:654-725).
self.addEventListener("notificationclick", (event) => {
  event.notification.close()

  const target = new URL(event.notification.data?.url || "/", self.location.origin)

  event.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
      const same = windows.find((w) => new URL(w.url).pathname === target.pathname)
      if (same) return same.focus()

      const open = windows.find((w) => new URL(w.url).origin === target.origin)
      if (open) {
        return open
          .navigate(target.href)
          .then((w) => (w ?? open).focus())
          .catch(() => clients.openWindow(target.href))
      }

      return clients.openWindow(target.href)
    }),
  )
})

// The push service changed the subscription's keys (after an iOS reinstall, for one): the new one is kept, so pushes
// are encrypted to the keys the browser holds, as Coder's pushsubscriptionchange handler does.
self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil(
    (async () => {
      const key =
        event.newSubscription?.options.applicationServerKey ??
        event.oldSubscription?.options.applicationServerKey
      if (!key) return

      const subscription =
        event.newSubscription ??
        (await self.registration.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: key,
        }))
      const json = subscription.toJSON()
      if (!json.endpoint || !json.keys) return

      await fetch("/api/v1/push/subscriptions", {
        method: "PUT",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint: json.endpoint,
          keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
        }),
      })
    })(),
  )
})
