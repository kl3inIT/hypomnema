package nodeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fetchScript(t *testing.T, h http.Handler, host string, header http.Header) (int, string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, PathInstallScript, nil)
	req.Host = host

	for k, v := range header {
		req.Header[k] = v
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)

	return rec.Code, string(body)
}

// Scenario: the script names the control plane the node will connect to. The
// configured public URL wins; without one it is the host the request came to,
// https when the proxy in front says so; a host that is not a host name is
// refused instead of being written into a shell script.
func TestInstallScriptNamesTheControlPlane(t *testing.T) {
	t.Parallel()

	_, body := fetchScript(t, InstallScript("https://hyp.example/", ""), "ignored.example", nil)
	assert.Contains(t, body, "default_url='https://hyp.example'\n")
	assert.Contains(t, body, "default_release_url='"+DefaultReleaseURL+"'\n")

	_, body = fetchScript(t, InstallScript("", "https://mirror.example/node"), "hyp.example:8443",
		http.Header{"X-Forwarded-Proto": {"https"}})
	assert.Contains(t, body, "default_url='https://hyp.example:8443'\n")
	assert.Contains(t, body, "default_release_url='https://mirror.example/node'\n")

	_, body = fetchScript(t, InstallScript("", ""), "127.0.0.1:8080", nil)
	assert.Contains(t, body, "default_url='http://127.0.0.1:8080'\n")

	status, body := fetchScript(t, InstallScript("", ""), "x';touch${IFS}/tmp/p;'", nil)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.NotContains(t, body, "touch")
}

// release is a fake release: a manifest and the "binary" it names, which is a
// shell script that answers `version` and records how `install` was called.
type release struct {
	t        *testing.T
	srv      *httptest.Server
	called   string
	manifest string
}

func newRelease(t *testing.T, version string) *release {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the install script supports Linux and macOS")
	}

	r := &release{t: t, called: filepath.Join(t.TempDir(), "called")}
	binary := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = version ]; then echo %s; exit 0; fi\necho \"$@\" > %s\n", version,
		r.called)
	sum := sha256.Sum256([]byte(binary))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /node", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, binary) })
	mux.HandleFunc("GET /{manifest}", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, r.manifest) })

	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)

	r.manifest = fmt.Sprintf("version=%s\nurl=%s/node\nsha256=%s\n", version, r.srv.URL, hex.EncodeToString(sum[:]))

	return r
}

// install runs the script the control plane serves against the fake release.
func (r *release) install() (string, error) {
	r.t.Helper()

	_, script := fetchScript(r.t, InstallScript("https://hyp.example", r.srv.URL), "hyp.example", nil)

	cmd := exec.CommandContext(r.t.Context(), "sh", "-s", "--", "--no-start")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "TMPDIR="+r.t.TempDir())

	out, err := cmd.CombinedOutput()

	return string(out), err
}

// Scenario: the owner pipes the script into a shell. It downloads the binary
// the manifest names, and runs its install with the control plane's URL and
// the script's own arguments.
func TestInstallScriptRunsTheVerifiedBinary(t *testing.T) {
	t.Parallel()

	r := newRelease(t, "v1.2.3")

	out, err := r.install()
	require.NoError(t, err, out)

	called, err := os.ReadFile(r.called)
	require.NoError(t, err)
	assert.Equal(t, "install --url https://hyp.example --no-start\n", string(called))
}

// Scenario: what was downloaded is not what the release says. A binary whose
// checksum or version differs from the manifest is never run, and neither is
// one the manifest places at a URL that is not https.
func TestInstallScriptRefusesWhatTheManifestDoesNotVouchFor(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		change func(manifest string) string
		says   string
	}{
		"checksum": {
			func(m string) string { return m[:strings.Index(m, "sha256=")] + "sha256=" + strings.Repeat("0", 64) + "\n" },
			"checksum does not match",
		},
		"version": {
			func(m string) string { return strings.Replace(m, "version=v1.2.3", "version=v9.9.9", 1) },
			"not the version",
		},
		"plain http elsewhere": {
			func(m string) string { return strings.Replace(m, "url=http://127.0.0.1", "url=http://example.com", 1) },
			"url is not allowed",
		},
		"two urls": {
			func(m string) string { return m + "url=https://example.com/other\n" },
			"exactly one url",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := newRelease(t, "v1.2.3")
			r.manifest = tc.change(r.manifest)

			out, err := r.install()
			require.Error(t, err)
			assert.Contains(t, out, tc.says)
			assert.NoFileExists(t, r.called)
		})
	}
}
