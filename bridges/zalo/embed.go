// Package zalo holds the Zalo bridge's build (issue #657, step 1 of #613): zca-js and the bridge's code bundled
// into one file, embedded so that the control plane runs the bridge of its own release, as web/embed.go embeds the
// web client. dist/ is filled by `make zalo-bridge-build`; without it only dist/.gitkeep is embedded and the control
// plane links no Zalo account. dist/LICENSES.txt, embedded with it, holds the licenses of the packages the bundle
// carries.
package zalo

import "embed"

//go:embed all:dist
var dist embed.FS

// Bundle is the bridge's one file, and whether this binary was built with it.
func Bundle() ([]byte, bool) {
	data, err := dist.ReadFile("dist/bridge.cjs")

	return data, err == nil && len(data) > 0
}
