package agenttool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const trustTestHost = "api.example.com"

func newTestExecCA(t *testing.T, names ...string) *execAuthority {
	t.Helper()
	record, err := createExecAuthority(names)
	require.NoError(t, err)
	data, err := json.Marshal(record)
	require.NoError(t, err)
	ca, err := decodeExecAuthority(data)
	require.NoError(t, err)
	return ca
}

func TestLocalExecCAConstrainsTheSigningKey(t *testing.T) {
	t.Parallel()
	names := []string{".example.net", trustTestHost, "nested." + trustTestHost}
	ca := newTestExecCA(t, names...)
	require.True(t, ca.root.PermittedDNSDomainsCritical)
	assert.Len(t, ca.root.ExcludedIPRanges, 2)
	assert.Empty(t, ca.root.PermittedDNSDomains)
	require.Len(t, ca.issuers, 2)
	assert.True(t, ca.issuers[0].cert.PermittedDNSDomainsCritical)
	assert.Equal(t, names[:2], ca.issuers[0].cert.PermittedDNSDomains)
	assert.Equal(t, []string{"." + trustTestHost}, ca.issuers[0].cert.ExcludedDNSDomains)

	roots := x509.NewCertPool()
	roots.AddCert(ca.root)
	for _, host := range []string{trustTestHost, "nested." + trustTestHost, "bucket.example.net"} {
		leaf, err := ca.leaf(host)
		require.NoError(t, err)
		issuer, err := x509.ParseCertificate(leaf.Certificate[1])
		require.NoError(t, err)
		intermediates := x509.NewCertPool()
		intermediates.AddCert(issuer)
		_, err = leaf.Leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: host})
		require.NoError(t, err)
	}

	for _, host := range []string{"unrelated.test", "child." + trustTestHost, "example.net", "127.0.0.1"} {
		_, err := ca.leaf(host)
		require.Error(t, err, "must refuse to issue for %s", host)
	}

	intermediates := x509.NewCertPool()
	intermediates.AddCert(ca.issuers[0].cert)
	for _, host := range []string{"unrelated.test", "child." + trustTestHost, "nested." + trustTestHost} {
		leaf, err := ca.issuers[0].leaf(host)
		require.NoError(t, err)
		_, err = leaf.Leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: host})
		var invalid x509.CertificateInvalidError
		require.ErrorAs(t, err, &invalid)
		assert.Equal(t, x509.CANotAuthorizedForThisName, invalid.Reason)
	}
}

type fakeExecStore struct {
	root                    []byte
	installs, removes       int
	installError, removeErr error
}

func (f *fakeExecStore) trusted(_ context.Context, authority *execAuthority) (bool, error) {
	return bytes.Equal(f.root, authority.root.Raw), nil
}

func (f *fakeExecStore) install(_ context.Context, path string) error {
	f.installs++
	if f.installError != nil {
		return f.installError
	}
	data, err := os.ReadFile(path) //nolint:gosec // The launcher passes its temporary public certificate.
	if err != nil {
		return err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return errors.New("expected only the public certificate")
	}
	f.root = block.Bytes
	return nil
}

func (f *fakeExecStore) remove(_ context.Context, a *execAuthority) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	if bytes.Equal(f.root, a.root.Raw) {
		f.removes++
		f.root = nil
	}
	return nil
}

func TestLocalExecCAIsInstalledOnceAndRemovedByIdentity(t *testing.T) {
	t.Parallel()
	var notice bytes.Buffer
	store := &fakeExecStore{installError: errors.New("declined")}
	trust := ExecTrust{Home: t.TempDir(), Names: func(context.Context) ([]string, error) {
		return []string{trustTestHost}, nil
	}}
	ctx := t.Context()
	_, err := trust.prepare(ctx, store, &notice)
	require.ErrorIs(t, err, store.installError)
	assert.Contains(t, notice.String(), "OS prompt")

	store.installError = nil
	ca, err := trust.prepare(ctx, store, &notice)
	require.NoError(t, err)
	assert.Equal(t, 2, store.installs)
	assert.Equal(t, ca.root.Raw, store.root)
	path := filepath.Join(trust.Home, execCADir, execCAName)
	before, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), before.Mode().Perm())
	}

	notice.Reset()
	again, err := trust.prepare(ctx, store, &notice)
	require.NoError(t, err)
	assert.True(t, ca.root.Equal(again.root))
	assert.Equal(t, 2, store.installs)
	assert.Empty(t, notice.String())

	trust.Names = func(context.Context) ([]string, error) { return []string{"other.example.org"}, nil }
	_, err = trust.prepare(ctx, store, &notice)
	require.ErrorContains(t, err, "exec remove-ca")
	assert.Equal(t, ca.root.Raw, store.root)
	store.removeErr = errors.New("untrusting refused")
	_, err = removeExecAuthority(ctx, trust.Home, store)
	require.ErrorIs(t, err, store.removeErr)
	assert.FileExists(t, path)
	store.removeErr = nil

	removed, err := removeExecAuthority(ctx, trust.Home, store)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.Equal(t, 1, store.removes)
	assert.Empty(t, store.root)
	assert.NoFileExists(t, path)
	removed, err = removeExecAuthority(ctx, trust.Home, store)
	require.NoError(t, err)
	assert.False(t, removed)
}

func TestDesktopBridgeUsesSeparateTLSOnEachSide(t *testing.T) {
	t.Parallel()
	local, remote := newTestExecCA(t, trustTestHost), newTestExecCA(t, trustTestHost)
	leaf, err := remote.leaf(trustTestHost)
	require.NoError(t, err)

	for _, test := range []struct {
		name       string
		advertised *execAuthority
	}{
		{name: "relays the request", advertised: remote},
		{name: "refuses a different upstream CA", advertised: local},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc("GET "+PathCA, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(test.advertised.rootPEM())
			})
			mux.HandleFunc("GET "+PathTunnel, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer the-owner-token", r.Header.Get("Authorization"))
				assert.Equal(t, trustTestHost+":443", r.URL.Query().Get(QueryHost))
				assert.Equal(t, "lark", r.URL.Query().Get(QueryIntegration))
				assert.Equal(t, "example", r.URL.Query().Get(QueryLabel))
				serveTLSForBridgeTest(t, w, r, leaf, test.advertised == remote)
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			launcher := Launcher{URL: server.URL, Key: "the-owner-token", Client: server.Client()}
			env, refused, stop, err := launcher.bridge(t.Context(), LarkCLI(), "lark", "example", true, local)
			require.NoError(t, err)
			t.Cleanup(stop)

			var proxy, caFile string
			for _, line := range env {
				key, value, _ := strings.Cut(line, "=")
				switch key {
				case "HTTPS_PROXY":
					proxy = value
				case "SSL_CERT_FILE":
					caFile = value
				}
			}
			addr, err := url.Parse(proxy)
			require.NoError(t, err)
			pem, err := os.ReadFile(caFile) //nolint:gosec // A public certificate made by the launcher under test.
			require.NoError(t, err)
			assert.Equal(t, local.rootPEM(), pem)
			roots := x509.NewCertPool()
			require.True(t, roots.AppendCertsFromPEM(pem))
			transport := &http.Transport{
				Proxy: http.ProxyURL(addr), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+trustTestHost+"/path", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+Placeholder)
			resp, err := client.Do(req)
			if test.advertised != remote {
				require.Error(t, err)
				assert.Contains(t, refused.said(), "verifying the control plane's service proxy")
				return
			}
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, "relayed", string(body))
		})
	}
}

//nolint:testifylint // An HTTP handler must report errors instead of calling FailNow from its goroutine.
func serveTLSForBridgeTest(t *testing.T, w http.ResponseWriter, r *http.Request, leaf *tls.Certificate, good bool) {
	t.Helper()
	ws, err := websocket.Accept(w, r, nil)
	if !assert.NoError(t, err) {
		return
	}
	stream := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)
	defer stream.Close() //nolint:errcheck // The peer may already have closed the test stream.
	conn := tls.Server(stream, &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(r.Context()); !good || !assert.NoError(t, err) {
		return
	}
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if !assert.NoError(t, err) {
		return
	}
	assert.NoError(t, req.Body.Close())
	assert.Equal(t, "/path", req.URL.Path)
	assert.Equal(t, "Bearer "+Placeholder, req.Header.Get("Authorization"))
	_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 7\r\nConnection: close\r\n\r\nrelayed")
	assert.NoError(t, err)
}
