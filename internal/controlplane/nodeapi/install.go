package nodeapi

import (
	"bytes"
	_ "embed"
	"net/http"
	"regexp"
	"strings"
	"text/template"
)

// The node's install script (issue #69, step 1b), served by the control
// plane as Omnara serves `GET /install/omnarad.sh`: the owner pipes it into a
// shell on the new machine. It carries no secret; the node token is asked for
// on the machine.

// PathInstallScript is where the install script is served.
const PathInstallScript = "/install/node.sh"

// DefaultReleaseURL is where release manifests and binaries are downloaded
// from: the latest GitHub release of this repository.
const DefaultReleaseURL = "https://github.com/kl3inIT/hypomnema/releases/latest/download"

//go:embed install.sh.tmpl
var installScriptSource string

var installScript = template.Must(template.New("install.sh").
	Funcs(template.FuncMap{"shellQuote": shellQuote}).Parse(installScriptSource))

// A host a request may name: a DNS name or an address, with an optional port.
var requestHost = regexp.MustCompile(`^(?:[A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(?::[0-9]{1,5})?$`)

// InstallScript serves the install script. publicURL is the control plane's
// URL as nodes reach it; when it is empty the script names the host the
// request came to. releaseURL is where binaries are downloaded from; empty
// means DefaultReleaseURL.
func InstallScript(publicURL, releaseURL string) http.Handler {
	if releaseURL == "" {
		releaseURL = DefaultReleaseURL
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		url := strings.TrimSuffix(publicURL, "/")
		if url == "" {
			if !requestHost.MatchString(r.Host) {
				http.Error(w, "set HYPOMNEMA_PUBLIC_URL on the control plane", http.StatusBadRequest)

				return
			}

			url = requestScheme(r) + "://" + r.Host
		}

		var body bytes.Buffer
		if err := installScript.Execute(&body, struct{ URL, ReleaseURL string }{url, releaseURL}); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body.Bytes())
	})
}

// requestScheme is https unless the request plainly came over http: the
// control plane sits behind a TLS proxy, which says so in X-Forwarded-Proto.
func requestScheme(r *http.Request) string {
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		return "http"
	}

	return "https"
}

// shellQuote makes s one word of a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
