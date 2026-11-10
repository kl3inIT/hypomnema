package agenttool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "fail" {
		os.Exit(3)
	}

	os.Exit(0)
}

// controlPlane stands in for the agent API: the accounts this agent may use,
// and a token for each.
type controlPlane struct {
	srv      *httptest.Server
	accounts []Connection
	calls    []string
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

		_ = json.NewEncoder(w).Encode(ConnectionList{Data: c.accounts})
	})
	mux.HandleFunc("POST "+PathToken, func(w http.ResponseWriter, r *http.Request) {
		var asked TokenRequest

		_ = json.NewDecoder(r.Body).Decode(&asked)
		c.calls = append(c.calls, "token "+asked.Integration+":"+asked.Label)

		for _, a := range c.accounts {
			if a.Label == asked.Label {
				//nolint:gosec // A made-up token, which is what this stand-in answers with.
				answer := TokenResponse{
					AccessToken: "ya29.fresh-for-" + asked.Label, ExpiresAt: time.Now().Add(time.Hour),
				}
				if asked.Integration == "lark" {
					answer.ClientID = "cli_a1b2"
				}

				_ = json.NewEncoder(w).Encode(answer)

				return
			}
		}

		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "this agent was not given google:" + asked.Label})
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

// Scenario: an agent given one Google account runs gws. The launcher asks
// which account, gets a fresh token for it and runs the real tool with that
// token and the agent's own arguments. The tool does not get the agent's key,
// and its exit code is the launcher's.
func TestTheLauncherRunsAToolWithAFreshToken(t *testing.T) {
	home := installed(t)
	c := newControlPlane(t, Connection{Label: "work", Account: "owner@example.com"})

	var out bytes.Buffer

	t.Setenv(EnvKey, "the-agents-key")
	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "gws", []string{"gmail", "+triage"}))
	assert.Equal(t, "token=ya29.fresh-for-work key=\"\" args=gmail +triage\n", out.String())
	assert.Equal(t, []string{"connections google", "token google:work"}, c.calls)

	out.Reset()

	err := c.launcher(home, &out).Run(t.Context(), "gws", []string{"gmail", "fail"})

	var exit *ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.Code)
}

// Scenario: an agent given the owner's Lark app runs lark-cli. Lark's tool
// will not run on a token alone: the launcher also gives it the app's id,
// which the control plane sent with the token, and the brand it is always
// run with.
func TestTheLauncherGivesLarksToolTheAppsIDBesideTheToken(t *testing.T) {
	home := installedAs(t, LarkCLI())
	c := newControlPlane(t, Connection{Label: "work", Account: "Hypomnema"})

	var out bytes.Buffer

	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "lark-cli", []string{"calendar", "+agenda"}))
	assert.Equal(t, "token=ya29.fresh-for-work key=\"\" args=calendar +agenda\nclient=cli_a1b2 brand=lark\n",
		out.String())
	assert.Equal(t, []string{"connections lark", "token lark:work"}, c.calls)
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
	assert.Contains(t, out.String(), "token=ya29.fresh-for-personal")
	assert.Contains(t, out.String(), "args=calendar +agenda\n")

	out.Reset()
	require.NoError(t, c.launcher(home, &out).Run(t.Context(), "gws", []string{"drive", "--account=work", "files"}))
	assert.Contains(t, out.String(), "token=ya29.fresh-for-work")
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
}
