// Package web holds the web client's production build, embedded so that the
// control plane serves it from its own binary, as Omnara's
// frontend/apps/web/embed.go and Coder's site/site_embed.go do. dist/ is
// filled by `pnpm build`; without it only dist/.gitkeep is embedded and the
// control plane serves no web client.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the build: index.html and assets/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded above; fs.Sub fails only on an invalid name.
	}

	return sub
}
