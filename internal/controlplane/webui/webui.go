// Package webui serves the web client: its files, and index.html for every
// other path so the client's router can take it. It follows Omnara's
// internal/httpapi/spa.go.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

const index = "index.html"

// contentSecurityPolicy allows only what the build loads from this origin. Styles may be inline, as the UI
// components set some.
var contentSecurityPolicy = strings.Join([]string{
	"default-src 'self'",
	"script-src 'self'",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: blob:",
	"font-src 'self' data:",
	"connect-src 'self'",
	"object-src 'none'",
	"base-uri 'none'",
	"frame-ancestors 'none'",
}, "; ")

// serviceWorker is the web client's service worker (web/public/sw.js, issue #1047), served at the root so that its
// scope is the whole app. It handles pushes and their clicks only and caches nothing.
const serviceWorker = "sw.js"

// sharedPage starts the path of a view-only link's page, /s/<token>.
const sharedPage = "/s/"

// reserved are the server's own paths: an unknown path under them is a 404,
// never the client.
var reserved = []string{"api", "node", "livez", "readyz"}

// Available reports whether assets hold a build.
func Available(assets fs.FS) bool {
	info, err := fs.Stat(assets, index)

	return err == nil && !info.IsDir()
}

// compressionLevel is gzip's, Coder's for its site (coderd/coderd.go
// compressHandler).
const compressionLevel = 5

// compressed are the types the build is sent gzipped in, when the browser
// accepts it (issue #510): text, scripts, styles and JSON. Fonts and images
// other than SVG are compressed already.
var compressed = []string{
	"text/html", "text/css", "text/javascript", "application/javascript", "application/json", "image/svg+xml",
	"application/manifest+json",
}

// contentTypes are the build's types Go's mime package may not know: it reads
// them from the host's mime database, which a slim image lacks, and has no
// built-in type for .webmanifest (issue #728). The type is OpenClaw's
// (src/gateway/control-ui-static.ts).
var contentTypes = map[string]string{
	".webmanifest": "application/manifest+json; charset=utf-8",
}

// Handler serves assets. A file is served as it is, with a year's cache under
// assets/ (Vite names them by content); a missing file or a reserved path is a
// 404; any other path gets index.html. What the browser accepts gzipped of the
// compressed types goes gzipped, through chi's Compressor as Coder's site does.
func Handler(assets fs.FS) http.Handler {
	return middleware.NewCompressor(compressionLevel, compressed...).Handler(serve(assets))
}

func serve(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		switch {
		case name == "" || name == index:
			serveIndex(w, r, assets)
		case hidden(name) || isReserved(name):
			http.NotFound(w, r)
		case isFile(assets, name):
			switch {
			case strings.HasPrefix(name, "assets/"):
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			case name == serviceWorker:
				// The browser asks again before each use, so a new worker is taken at once: OpenClaw serves its
				// sw.js no-cache, never immutable (src/gateway/control-ui-conditional.http.test.ts:326).
				w.Header().Set("Cache-Control", "no-cache")
			}

			if contentType, ok := contentTypes[path.Ext(name)]; ok {
				w.Header().Set("Content-Type", contentType)
			}

			files.ServeHTTP(w, r)
		case name == "assets" || strings.HasPrefix(name, "assets/") || path.Ext(name) != "":
			http.NotFound(w, r)
		default:
			serveIndex(w, r, assets)
		}
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	page, err := fs.ReadFile(assets, index)
	if err != nil {
		http.Error(w, "the web client is unavailable", http.StatusInternalServerError)

		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", contentSecurityPolicy)

	// A view-only link's page (issue #745) is opened without signing in by whoever has the link: it is never
	// indexed (LibreChat's X-Robots-Tag) nor kept (LibreChat's no-store), as the client API's answers for it.
	if strings.HasPrefix(path.Clean(r.URL.Path), sharedPage) {
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Cache-Control", "private, no-store")
	}

	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(page)
}

func isFile(assets fs.FS, name string) bool {
	info, err := fs.Stat(assets, name)

	return err == nil && !info.IsDir()
}

func hidden(name string) bool {
	for segment := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}

	return false
}

func isReserved(name string) bool {
	for _, p := range reserved {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}

	return false
}
