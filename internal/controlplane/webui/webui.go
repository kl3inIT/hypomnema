// Package webui serves the web client: its files, and index.html for every
// other path so the client's router can take it. It follows Omnara's
// internal/httpapi/spa.go.
package webui

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
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

// reserved are the server's own paths: an unknown path under them is a 404,
// never the client.
var reserved = []string{"api", "node", "livez", "readyz"}

// Available reports whether assets hold a build.
func Available(assets fs.FS) bool {
	info, err := fs.Stat(assets, index)

	return err == nil && !info.IsDir()
}

// Handler serves assets. A file is served as it is, with a year's cache under
// assets/ (Vite names them by content); a missing file or a reserved path is a
// 404; any other path gets index.html.
func Handler(assets fs.FS) http.Handler {
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
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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
