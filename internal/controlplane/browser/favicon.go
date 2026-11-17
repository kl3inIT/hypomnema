package browser

// A tab's icon (issue #703): the icon its page names, read by the page itself
// and given as a data: address, since the web client loads no image from
// elsewhere. It is read once per site, in the background: a list of tabs
// never waits for it, and a tab shows its icon from the next list on.
//
// Adapted from OpenClaw (ui/src/components/browser/browser-favicon-script.ts,
// MIT License, Copyright (c) 2026 OpenClaw Foundation), which reads a native
// tab's icon with this script, 64 KiB and 10 seconds at most
// (apps/macos/Sources/OpenClaw/DashboardNativeBrowserHost.swift), and keeps
// at most 128 icons by site (ui/src/pages/chat/link-favicon-cache.ts,
// MAX_FAVICONS). Changed: written for Chrome's DevTools, in a world of its
// own beside the page's so that the page neither sees the script nor
// changes what it calls, and a site's icon is kept in the control plane.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	faviconBytes = 64 << 10
	faviconRead  = 10 * time.Second
	faviconSites = 128
)

// faviconScript returns the page's icon as a data: address, or null.
const faviconScript = `(async function readFavicon(maxBytes, timeoutMs) {
  let timeout;
  try {
    const controller = new AbortController();
    timeout = setTimeout(() => controller.abort(), timeoutMs);
    const icons = Array.from(document.querySelectorAll('link[rel~="icon"]'))
      .filter((link) => link.getAttribute("href")?.trim());
    const preferred = icons.find((link) => /(?:^|\s)32x32(?:\s|$)/i.test(link.getAttribute("sizes") || ""));
    const candidates = [
      ...(preferred ? [preferred.href] : []),
      ...icons.filter((link) => link !== preferred).map((link) => link.href),
      ...Array.from(document.querySelectorAll('link[rel="apple-touch-icon"]'))
        .filter((link) => link.getAttribute("href")?.trim()).map((link) => link.href),
      new URL("/favicon.ico", location.href).href,
    ];
    for (const url of new Set(candidates)) {
      if (controller.signal.aborted) return null;
      try {
        const response = await fetch(url, { credentials: "same-origin", mode: "cors", signal: controller.signal });
        if (!response.ok) continue;
        const blob = await response.blob();
        const mime = blob.type.split(";")[0].trim().toLowerCase();
        if (!/^image\/[a-z0-9.+-]+$/.test(mime) || blob.size > maxBytes) continue;
        const dataUrl = await new Promise((resolve) => {
          const reader = new FileReader();
          reader.onload = () => resolve(typeof reader.result === "string" ? reader.result : null);
          reader.onerror = reader.onabort = () => resolve(null);
          reader.readAsDataURL(blob);
        });
        if (dataUrl) {
          const b64 = dataUrl.slice(dataUrl.indexOf(",") + 1);
          if (b64) return "data:" + mime + ";base64," + b64;
        }
      } catch {
        // A page may name an icon that is missing or of another origin; the next is tried.
      }
    }
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
  }
  return null;
})(%d, %d)`

// favicons are the icons read, by site, and the sites being read. A site
// whose icon could not be read is kept with none, so that it is not read
// again at every list.
type favicons struct {
	mu      sync.Mutex
	icons   map[string]string
	reading map[string]bool
}

// site is what an icon is kept by: a page's scheme and host, for a web page
// only.
func site(address string) string {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return u.Scheme + "://" + u.Host
}

// icon is a site's icon, and whether it was read. When it was not, and no
// read of it is under way, the caller is to read it.
func (f *favicons) icon(key string) (icon string, known, read bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if icon, ok := f.icons[key]; ok {
		return icon, true, false
	}

	if f.reading[key] {
		return "", false, false
	}

	if f.reading == nil {
		f.reading = map[string]bool{}
	}

	f.reading[key] = true

	return "", false, true
}

// keep stores what was read of a site. Past faviconSites, the icons kept are
// let go and read again when asked for.
func (f *favicons) keep(key, icon string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.reading, key)

	if f.icons == nil || len(f.icons) >= faviconSites {
		f.icons = map[string]string{}
	}

	f.icons[key] = icon
}

// iconOf is the icon of a tab's page, when it is known. When it is not, it is
// read in the background from the tab, the Chrome target target of the
// browser at cdp.
func (e *Engine) iconOf(ctx context.Context, cdp, target, address string) string {
	key := site(address)
	if key == "" {
		return ""
	}

	icon, known, read := e.icons.icon(key)
	if known || !read {
		return icon
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), faviconRead+5*time.Second)
		defer cancel()

		e.icons.keep(key, readFavicon(ctx, cdp, target))
	}()

	return ""
}

// readFavicon has a page read its icon, in a world of the engine's own:
// attached to the tab on a connection of its own, it runs the script and
// leaves. It is empty when the page has none it can give.
func readFavicon(ctx context.Context, cdp, target string) string {
	conn, err := dialCDP(ctx, cdp, nil)
	if err != nil {
		return ""
	}
	defer conn.close()

	var attached struct {
		SessionID string `json:"sessionId"`
	}

	err = conn.send(ctx, "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true}, "", &attached)
	if err != nil || attached.SessionID == "" {
		return ""
	}

	session := attached.SessionID

	defer func() {
		_ = conn.send(ctx, "Target.detachFromTarget", map[string]any{"sessionId": session}, "", nil)
	}()

	// A page's main frame has its target's id.
	var world struct {
		ExecutionContextID int64 `json:"executionContextId"`
	}

	err = conn.send(ctx, "Page.createIsolatedWorld",
		map[string]any{"frameId": target, "worldName": "hypomnema-favicon"}, session, &world)
	if err != nil || world.ExecutionContextID == 0 {
		return ""
	}

	var evaluated struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}

	err = conn.send(ctx, "Runtime.evaluate", map[string]any{
		"expression":    fmt.Sprintf(faviconScript, faviconBytes, faviconRead.Milliseconds()),
		"contextId":     world.ExecutionContextID,
		"awaitPromise":  true,
		"returnByValue": true,
	}, session, &evaluated)
	if err != nil {
		return ""
	}

	icon, _ := evaluated.Result.Value.(string)

	return faviconData(icon)
}

// faviconData is a data: address of an image within faviconBytes, or empty.
// The page wrote it, so it is checked here again.
func faviconData(icon string) string {
	head, body, ok := strings.Cut(icon, ",")
	if !ok || body == "" || len(body) > faviconBytes*4/3+4 {
		return ""
	}

	mime, ok := strings.CutSuffix(strings.TrimPrefix(head, "data:"), ";base64")
	if !ok || !strings.HasPrefix(head, "data:image/") || strings.ContainsAny(mime, " ;\"'<>") {
		return ""
	}

	for _, c := range body {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return ""
		}
	}

	return icon
}
