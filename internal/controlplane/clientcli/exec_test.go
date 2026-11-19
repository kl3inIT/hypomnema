package clientcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

const fakeChatPath = "/open-apis/im/v1/chats/oc_example"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) != "lark-cli" {
		os.Exit(m.Run())
	}

	if err := fakeLarkCLI(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fakeLarkCLI() error {
	if !slices.Equal(os.Args[1:], []string{"im", "chats", "get", "--as", "user", "--chat-id", "oc_example"}) {
		return errors.New("the launcher changed the command arguments")
	}

	def := agenttool.LarkCLI()
	if os.Getenv(def.UserTokenEnv) != agenttool.Placeholder || os.Getenv(def.TokenEnv) != "" ||
		os.Getenv(def.ClientEnv) != "cli_example" || os.Getenv(def.IdentityEnv) != def.UserIdentity ||
		os.Getenv(agenttool.EnvKey) != "" || os.Getenv("LARKSUITE_CLI_APP_SECRET") != "" {
		return errors.New("the launcher did not isolate the child credentials")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.larksuite.com"+fakeChatPath, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+agenttool.Placeholder)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // A test stand-in printing the already-read result.

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Lark answered HTTP %d", resp.StatusCode)
	}

	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

type larkUpstream func(*http.Request) (*http.Response, error)

func (f larkUpstream) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOwnerExecReadsLarkThroughTheServiceProxy(t *testing.T) {
	if agenttool.UserTrustSupported() {
		t.Skip("the subprocess test uses SSL_CERT_FILE and must not install a CA in the user's trust store")
	}

	w := newWorld(t)
	ctx := t.Context()
	_, owner, err := w.svc.AddToken(ctx, "local", bearertoken.Owner)
	require.NoError(t, err)
	_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "lark", Label: "me", Secret: "conn-lark-me", AppID: "cli_example", AppLabel: "app",
	})
	require.NoError(t, err)

	logger := slog.New(slog.DiscardHandler)
	agents := agentapi.New(w.svc, nil, logger)
	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: w.svc, Logger: logger,
		Token: func(_ context.Context, integration, label string) (string, error) {
			assert.Equal(t, "lark:me", orchestration.Use(integration, label))
			return "service-only-token", nil
		},
		Upstream: larkUpstream(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "open.larksuite.com", req.Host)
			assert.Equal(t, "Bearer service-only-token", req.Header.Get("Authorization"))
			var body string
			switch req.URL.Path {
			case fakeChatPath:
				body = `{"code":0,"msg":"ok","data":{"name":"Example group"}}`
			case "/open-apis/authen/v1/user_info":
				body = `{"code":0,"msg":"ok","data":{"open_id":"ou_example"}}`
			default:
				return nil, fmt.Errorf("unexpected Lark path %q", req.URL.Path)
			}

			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
				ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	})
	require.NoError(t, err)
	mux := http.NewServeMux()
	agents.Register(mux)
	proxy.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	require.NoError(t, saveConfig(w.home, Config{URL: srv.URL, Token: owner}))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+agenttool.PathTunnel+"?integration=lark&label=me&host=open.larksuite.com%3A443", nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	cli := os.Getenv("HYPOMNEMA_TEST_LARK_CLI")
	if cli == "" {
		cli, err = os.Executable()
		require.NoError(t, err)
	}

	bin := t.TempDir()
	require.NoError(t, os.Symlink(cli, filepath.Join(bin, "lark-cli")))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(agenttool.EnvKey, owner)
	t.Setenv("LARKSUITE_CLI_APP_SECRET", "not-for-the-child")
	t.Setenv("NO_PROXY", "open.larksuite.com")

	out, err := w.cmd(t, "exec", "lark:me", "--", "lark-cli", "im", "chats", "get", "--as", "user",
		"--chat-id", "oc_example")
	require.NoError(t, err)
	assert.Contains(t, out, "Example group")
	assert.NotContains(t, out, "service-only-token")
	assert.NotContains(t, out, owner)

	_, err = w.cmd(t, "exec", "lark:me", "--", "hypomnema-missing-test-cli")
	require.ErrorContains(t, err, "put it on PATH")
}
