package agenttool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTool makes this test binary stand in for a tool: installed under a
// tool's name, it prints the token it was given and its arguments, and ends
// with the code the last argument names.
const fakeTool = "AGENTTOOL_TEST_FAKE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeTool) == "" {
		os.Exit(m.Run())
	}

	// The stand-in is installed under the tool's own name, which says whose variables to print.
	def, _ := Named(filepath.Base(os.Args[0]))

	fmt.Printf("token=%s key=%q args=%s\n", os.Getenv(def.TokenEnv), os.Getenv(EnvKey),
		strings.Join(os.Args[1:], " "))

	if def.ClientEnv != "" {
		fmt.Printf("client=%s brand=%s\n", os.Getenv(def.ClientEnv), os.Getenv("LARKSUITE_CLI_BRAND"))
	}

	if def.CAEnv != "" {
		_, bundleErr := os.Stat(os.Getenv(def.CAEnv)) //nolint:gosec // G703: a stand-in reads what it was given.
		profile, hasProfile := os.LookupEnv("AWS_PROFILE")
		fmt.Printf("secret=%s profile=%q/%t bundle=%t config=%s\n", os.Getenv("AWS_SECRET_ACCESS_KEY"), profile,
			hasProfile, bundleErr == nil, os.Getenv("AWS_CONFIG_FILE"))
	}

	// A tool is given a proxy on loopback and a certificate file, and reaches a host through them.
	proxy := os.Getenv("HTTPS_PROXY")
	_, caErr := os.Stat(os.Getenv("SSL_CERT_FILE")) //nolint:gosec // G703: a stand-in reads what it was given.

	fmt.Printf("proxy=%t ca=%t connect=%s\n", strings.HasPrefix(proxy, "http://127.0.0.1:"), caErr == nil,
		connect(strings.TrimPrefix(proxy, "http://"), "gmail.googleapis.com:443"))

	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "fail" {
		os.Exit(3)
	}

	os.Exit(0)
}

// connect asks a proxy for a connection to host and returns the status it
// answered with.
func connect(proxy, host string) string {
	dialer := net.Dialer{Timeout: 5 * time.Second}

	conn, err := dialer.DialContext(context.Background(), "tcp", proxy)
	if err != nil {
		return "unreachable"
	}
	defer conn.Close() //nolint:errcheck // a stand-in's connection

	_, _ = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)

	line, _ := bufio.NewReader(conn).ReadString('\n')
	if fields := strings.Fields(line); len(fields) > 1 {
		return fields[1]
	}

	return "nothing"
}

// controlPlane stands in for the agent API: the accounts this agent may use,
// and a token for each.
type controlPlane struct {
	srv      *httptest.Server
	accounts []Connection
	// listClient is the one app id a control plane from before issue #311 told for every account.
	listClient string
	calls      []string
}

func newControlPlane(t *testing.T, accounts ...Connection) *controlPlane {
	t.Helper()

	c := &controlPlane{accounts: accounts}
	mux := http.NewServeMux()

	mux.HandleFunc("GET "+PathConnections, func(w http.ResponseWriter, r *http.Request) {
		c.calls = append(c.calls, "connections "+r.URL.Query().Get("integration"))

		if r.Header.Get("Authorization") != "Bearer the-agents-key" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "the agent's credential no longer works"})

			return
		}

		list := ConnectionList{Data: c.accounts, ClientID: c.listClient}

		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("POST "+PathToken, func(w http.ResponseWriter, r *http.Request) {
		var asked TokenRequest

		_ = json.NewDecoder(r.Body).Decode(&asked)
		c.calls = append(c.calls, "token "+asked.Integration+":"+asked.Label)

		for _, a := range c.accounts {
			if a.Label == asked.Label {
				answer := TokenResponse{
					AccessToken: "ya29.fresh-for-" + asked.Label, ExpiresAt: time.Now().Add(time.Hour),
				}
				if asked.Integration == "lark" {
					answer.ClientID = "cli_a1b2"
				}

				//nolint:gosec // A made-up token, which is what this stand-in answers with.
				_ = json.NewEncoder(w).Encode(answer)

				return
			}
		}

		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "this agent was not given google:" + asked.Label})
	})

	mux.HandleFunc("GET "+PathCA, func(w http.ResponseWriter, _ *http.Request) {
		c.calls = append(c.calls, "certificate")
		_, _ = io.WriteString(w, "-----BEGIN CERTIFICATE-----\nbm90IGEgcmVhbCBvbmU=\n-----END CERTIFICATE-----\n")
	})
	// The stand-in opens no stream: it records what the bridge asked for and refuses in words.
	mux.HandleFunc("GET "+PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c.calls = append(c.calls, strings.TrimSpace(fmt.Sprintf("tunnel %s:%s %s %s", q.Get(QueryIntegration),
			q.Get(QueryLabel), q.Get(QueryHost), q.Get(QueryChat))))

		status, message := http.StatusForbidden, "the stand-in opens no stream"
		if r.Header.Get("Authorization") != "Bearer the-agents-key" {
			status, message = http.StatusUnauthorized, "the agent's credential no longer works"
		}

		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
	})

	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)

	return c
}

// installed puts the stand-in where the pinned gws would be.
func installed(t *testing.T) string {
	t.Helper()

	return installedAs(t, GoogleWorkspaceCLI())
}

// installedAs puts the stand-in where a pinned tool would be.
func installedAs(t *testing.T, def Def) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("a tool is a file without an extension, which Windows does not run; nodes are Linux")
	}

	home := t.TempDir()
	target := ToolPath(home, def.Name, def.Version)

	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))

	self, err := os.ReadFile(os.Args[0]) //nolint:gosec // this test's own binary
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, self, 0o700)) //nolint:gosec // a command, run by this test

	t.Setenv(fakeTool, "1")

	return home
}

func (c *controlPlane) launcher(home string, out io.Writer) Launcher {
	return Launcher{URL: c.srv.URL, Key: "the-agents-key", ToolsHome: home, Stdout: out, Stderr: out}
}

// Scenario: an agent given one Google account runs gws, whose Integration
// goes through the service proxy. The launcher asks which account and runs
// the real tool with the agent's own arguments, a placeholder where a token
// would be, and a proxy on loopback that carries the tool's connection to the
// control plane with the agent's key, the account and, for a coordinator, its
// chat. No token is asked for. The tool does not get the agent's key, what
// the control plane refuses is said once in its own words, and the tool's
// exit code is the launcher's.
func TestTheLauncherRunsAProxiedToolWithAPlaceholderAndABridge(t *testing.T) {
	home := installed(t)
	c := newControlPlane(t, Connection{Label: "work", Account: "owner@example.com"})

	var out, errs bytes.Buffer

	t.Setenv(EnvKey, "the-agents-key")
	t.Setenv("HTTPS_PROXY", "http://proxy.of.the.agent.example:3128")

	launcher := c.launcher(home, &out)
	launcher.Stderr, launcher.Chat = &errs, "cht_1"

	require.NoError(t, launcher.Run(t.Context(), "gws", []string{"gmail", "+triage"}))
	assert.Equal(t, "token="+Placeholder+" key=\"\" args=gmail +triage\nproxy=true ca=true connect=502\n", out.String())
	assert.Equal(t, []string{"connections google", "certificate", "tunnel google:work gmail.googleapis.com:443 cht_1"},
		c.calls)
	assert.Equal(t, "hypomnema: the stand-in opens no stream\n", errs.String())

	leftovers, err := filepath.Glob(filepath.Join(os.TempDir(), "hypomnema-proxy-ca-*.pem"))
	require.NoError(t, err)
	assert.Empty(t, leftovers, "the certificate file is removed when the tool ends")

	out.Reset()

	err = c.launcher(home, &out).Run(t.Context(), "gws", []string{"gmail", "fail"})

	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.Code)
}

// Scenario: an agent given two Lark accounts, each an app of its own (issue
// #311), runs lark-cli as "report". Lark's tool will not run on a token alone:
// the launcher also gives it the id of that account's app, which the control
// plane tells with each account, and the brand it is always run with. Like
// every tool it holds a placeholder and goes through the proxy; no token is
// asked for. A control plane from before told one id for every account, and
// the launcher still gives that one.
func TestTheLauncherGivesLarksToolTheAppOfTheAccountBesideThePlaceholder(t *testing.T) {
	home := installedAs(t, LarkCLI())
	c := newControlPlane(t, Connection{Label: "work", Account: "Hypomnema", ClientID: "cli_a1b2"},
		Connection{Label: "report", Account: "Superset Report", ClientID: "cli_report"})

	var out bytes.Buffer

	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "lark-cli",
		[]string{"--account", "report", "calendar", "+agenda"}))
	assert.Equal(t, "token="+Placeholder+" key=\"\" args=calendar +agenda\nclient=cli_report brand=lark\n"+
		"proxy=true ca=true connect=502\nhypomnema: the stand-in opens no stream\n", out.String())
	assert.Equal(t, []string{"connections lark", "certificate", "tunnel lark:report gmail.googleapis.com:443"}, c.calls)

	older := newControlPlane(t, Connection{Label: "work", Account: "Hypomnema"})
	older.listClient = "cli_a1b2"
	out.Reset()

	require.NoError(t, older.launcher(home, &out).Run(t.Context(), "lark-cli", []string{"calendar", "+agenda"}))
	assert.Contains(t, out.String(), "client=cli_a1b2 brand=lark\n")
}

// Scenario: an agent given an AWS role runs the AWS CLI (issue #331). It
// holds placeholders for a key, trusts the proxy's certificate through the
// variable it reads, and runs without the profile the agent's machine names:
// the CLI refuses an empty one, so it is taken away. No file of the machine's
// own configuration is read.
func TestTheLauncherRunsTheAWSCLIWithPlaceholdersAndNoProfile(t *testing.T) {
	home := installedAs(t, AWSCLI())
	c := newControlPlane(t, Connection{Label: "main-read", Account: "123456789012"})
	t.Setenv("AWS_PROFILE", "the-machines-own")

	var out bytes.Buffer

	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "aws", []string{"sts", "get-caller-identity"}))
	assert.Equal(t, "token="+Placeholder+" key=\"\" args=sts get-caller-identity\n"+
		"secret="+Placeholder+" profile=\"\"/false bundle=true config=/dev/null\n"+
		"proxy=true ca=true connect=502\nhypomnema: the stand-in opens no stream\n", out.String())
}

// Scenario: an agent given two accounts must name one; the flag that names
// it is the launcher's and does not reach the tool. An account the agent was
// not given is refused in the control plane's own words, and nothing runs.
func TestTheLauncherActsAsTheAccountThatIsNamed(t *testing.T) {
	home := installed(t)
	c := newControlPlane(t,
		Connection{Label: "work", Account: "owner@example.com"}, Connection{Label: "personal", Account: "me@example.com"})

	var out bytes.Buffer

	err := c.launcher(home, &out).Run(t.Context(), "gws", []string{"calendar", "+agenda"})
	require.ErrorContains(t, err, "several google accounts")
	require.ErrorContains(t, err, "work (owner@example.com), personal (me@example.com)")
	assert.Empty(t, out.String(), "nothing ran")

	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "gws",
		[]string{"--account", "personal", "calendar", "+agenda"}))
	assert.Contains(t, c.calls, "tunnel google:personal gmail.googleapis.com:443")
	assert.Contains(t, out.String(), "args=calendar +agenda\n")

	out.Reset()
	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "gws", []string{"drive", "--account=work", "files"}))
	assert.Contains(t, c.calls, "tunnel google:work gmail.googleapis.com:443")
	assert.Contains(t, out.String(), "args=drive files\n")

	out.Reset()

	err = c.launcher(home, &out).Run(t.Context(), "gws", []string{"--account", "boss", "gmail"})
	require.ErrorContains(t, err, "this agent was not given google:boss")
	assert.Empty(t, out.String())
}

// Scenario: what the launcher says when it cannot run a tool, each in words
// the agent can act on or pass to the owner.
func TestTheLauncherSaysWhatIsMissing(t *testing.T) {
	t.Parallel()

	none := newControlPlane(t)

	for name, tc := range map[string]struct {
		launcher Launcher
		tool     string
		args     []string
		want     string
	}{
		"a tool Hypomnema does not have": {none.launcher("home", io.Discard), "himalaya", nil, "not a tool Hypomnema provides"},
		"an agent that was not set up":   {Launcher{}, "gws", nil, EnvURL},
		"no account given":               {none.launcher("home", io.Discard), "gws", []string{"gmail"}, "was given no google account"},
		"the flag without a label":       {none.launcher("home", io.Discard), "gws", []string{"gmail", "--account"}, "needs the account's label"},
		"a key that no longer works": {
			Launcher{URL: none.srv.URL, Key: "an-old-key", ToolsHome: "home"}, "gws", []string{"gmail"},
			"the agent's credential no longer works",
		},
		"a control plane that is not there": {
			Launcher{URL: "http://127.0.0.1:1", Key: "k", ToolsHome: "home"}, "gws", []string{"gmail"},
			"the control plane could not be reached",
		},
	} {
		err := tc.launcher.Run(t.Context(), tc.tool, tc.args)
		require.ErrorContains(t, err, tc.want, name)

		var exit *ExitError
		assert.NotErrorAs(t, err, &exit, name)
		assert.NotContains(t, err.Error(), "the-agents-key", name)
	}
}

// Scenario: a node prepares a Dispatch that may use a Google account. The
// script named gws is first on the worker's PATH and runs the node's
// launcher; the guide and its references are where the worker is told to
// read them; the worker's first message names the account and both. A
// Dispatch that uses no account gets nothing.
func TestANodePreparesADispatchForItsAccounts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()

	env, err := ForDispatch(home, "/opt/hypomnema-node", "https://cp.example", nil, "")
	require.NoError(t, err)
	assert.Empty(t, env)

	_, err = ForDispatch(home, "/opt/hypomnema-node", "https://cp.example", []string{"google:work"}, "")
	require.ErrorContains(t, err, "no key")

	env, err = ForDispatch(home, "/opt/it's/hypomnema-node", "https://cp.example", []string{"google:work"}, "the-key")
	require.NoError(t, err)

	place := Place{Home: home}
	assert.True(t, strings.HasPrefix(env[0], "PATH="+place.Bin()+string(os.PathListSeparator)), env[0])
	assert.Contains(t, env, EnvURL+"=https://cp.example")
	assert.Contains(t, env, EnvKey+"=the-key")
	assert.Contains(t, env, EnvTools+"="+home)
	assert.Contains(t, env, EnvSkills+"="+place.SkillsDir())

	script, err := os.ReadFile(filepath.Join(place.Bin(), "gws"))
	require.NoError(t, err)
	assert.Contains(t, string(script), `exec '/opt/it'\''s/hypomnema-node' tool gws "$@"`)

	guide, err := os.ReadFile(filepath.Join(place.SkillsDir(), "google-workspace", "guide.md"))
	require.NoError(t, err)
	assert.Contains(t, string(guide), "Never run `gws auth")

	for _, reference := range []string{"gws-gmail-triage", "gws-calendar-agenda", "gmail-search-syntax", "daily-brief"} {
		assert.FileExists(t, filepath.Join(place.SkillsDir(), "google-workspace", "references", reference+".md"))
	}

	assert.NoFileExists(t, filepath.Join(place.SkillsDir(), "google-workspace", "SKILL.md"),
		"the stub is for an agent with the hypomnema CLI; a worker reads the guide itself")

	brief := Brief([]string{"google:work", "google:personal"})
	assert.Contains(t, brief, "google:work, google:personal")
	assert.Contains(t, brief, "`gws` command is on your PATH and already signed in")
	assert.Contains(t, brief, `"$HYPOMNEMA_SKILLS/google-workspace/guide.md"`)
	assert.Contains(t, brief, "gws --account <label>")
	assert.Empty(t, Brief(nil))

	// A Dispatch given the Lark app gets Lark's script and guide instead; the guide has no references of its
	// own, since lark-cli carries its guides itself.
	_, err = ForDispatch(home, "/opt/hypomnema-node", "https://cp.example", []string{"lark:work"}, "the-key")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(place.Bin(), "lark-cli"))

	guide, err = os.ReadFile(filepath.Join(place.SkillsDir(), "lark", "guide.md"))
	require.NoError(t, err)
	assert.Contains(t, string(guide), "Never run `lark-cli auth")
	assert.Contains(t, Brief([]string{"lark:work"}), `"$HYPOMNEMA_SKILLS/lark/guide.md"`)
}
