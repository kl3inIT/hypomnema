// Package githubapp is a worker's side of the owner's GitHub App (issue #1262). An offer whose repository the App is
// installed on carries a token of its installation, good for an hour; a worker runs longer. So the worker's gh and
// git ask the control plane for a fresh one at each call, as Omnara's agents ask their daemon
// (internal/machinedaemon/git_credential_helper.go: RunGitHubCLI, a `gh` before the real one on PATH, and
// RunGitCredentialHelper, git's credential helper; Apache-2.0, third_party/omnara/NOTICE). Here they ask the agent
// API with the Dispatch's own key (agenttool.PathGitHubToken), as `hypomnema-node gitssh` asks it for the SSH key.
//
// The same directory holds `hypomnema`, whose only command here is `ci wait <pr>` (internal/shared/ciwait): the
// worker waits for its pull request's CI on the App's events, with no call to GitHub.
package githubapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/ciwait"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// The node binary's subcommands the scripts and git run.
const (
	GHSubcommand         = "gh"
	CredentialSubcommand = "git-credential" //nolint:gosec // G101: a subcommand's name, not a credential
	CLISubcommand        = "hypomnema"
)

// binDir is the directory, in a Dispatch's directory, that holds the scripts the worker's PATH starts with.
const binDir = "github-bin"

// Prepare writes the scripts `gh` and `hypomnema` into the Dispatch's directory dir and returns what the worker's
// environment gains: PATH with their directory first, git's credential helper for github.com, and the agent API's
// address and key the helpers ask with. path is the worker's PATH. It holds no token: none is kept for the worker,
// in its environment or in the shim's spec, which keeps the environment (review round 1).
func Prepare(dir, exe, apiURL, key, path string) ([]string, error) {
	if apiURL == "" || key == "" {
		return nil, errors.New("the offer is the GitHub App's, but the node has no key or control plane " +
			"address to ask for a fresh one with")
	}

	bin := filepath.Join(dir, binDir)
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return nil, fmt.Errorf("making the GitHub App's commands: %w", err)
	}

	self := quote(filepath.ToSlash(exe))
	scripts := map[string]string{
		"gh":        "#!/bin/sh\nexec " + self + " " + GHSubcommand + " " + quote(bin) + " \"$@\"\n",
		"hypomnema": "#!/bin/sh\nexec " + self + " " + CLISubcommand + " " + quote(bin) + " \"$@\"\n",
	}

	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil { //nolint:gosec // a script
			return nil, fmt.Errorf("writing the GitHub App's %s: %w", name, err)
		}
	}

	if path != "" {
		path = bin + string(os.PathListSeparator) + path
	} else {
		path = bin
	}

	// An empty helper first drops the helpers the user's configuration names, so git asks this one alone
	// (gitcredentials(7): "If credential.helper is configured to the empty string, this resets the helper list").
	helper := "!" + self + " " + CredentialSubcommand

	return []string{
		"PATH=" + path,
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.https://" + githost.GitHubHost + ".helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.https://" + githost.GitHubHost + ".helper", "GIT_CONFIG_VALUE_1=" + helper,
		agenttool.EnvURL + "=" + apiURL, agenttool.EnvKey + "=" + key,
	}, nil
}

// quote quotes s for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// WithoutHostToken is a worker's git host environment without the token the offer carried: the helpers give a fresh
// one instead. It drops gh's token and git's header, both made from it (githost's Env).
func WithoutHostToken(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		for _, prefix := range []string{
			"GH_TOKEN=", "GH_ENTERPRISE_TOKEN=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
		} {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}

		return false
	})
}

// Client asks the agent API.
type Client struct {
	URL  string
	Key  string
	HTTP *http.Client
}

// FromEnv is the client the helpers' environment names.
func FromEnv() Client {
	return Client{
		URL: os.Getenv(agenttool.EnvURL), Key: os.Getenv(agenttool.EnvKey), HTTP: &http.Client{Timeout: time.Minute},
	}
}

// errNoRoute says the control plane has no route for it: one of an earlier release.
var errNoRoute = errors.New("the control plane has no such route")

// errNotCovered says the App is not installed on the repository any more.
var errNotCovered = errors.New("the GitHub App is not installed on this repository")

func (c Client) get(ctx context.Context, method, path string, query url.Values, out any) error {
	if c.URL == "" || c.Key == "" {
		return fmt.Errorf("the control plane's address or this worker's key is missing (%s, %s)",
			agenttool.EnvURL, agenttool.EnvKey)
	}

	target := strings.TrimRight(c.URL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.Key)

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("asking the control plane: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return errNoRoute
	}

	if resp.StatusCode != http.StatusOK {
		var refused struct {
			Message string `json:"message"`
		}

		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refused)

		if resp.StatusCode == http.StatusConflict {
			return fmt.Errorf("%w: %s", errNotCovered, refused.Message)
		}

		return fmt.Errorf("the control plane answered %d: %s", resp.StatusCode, refused.Message)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out); err != nil {
		return errors.New("the control plane's answer is not what was asked")
	}

	return nil
}

// Token is a fresh token of the App's installation on the Dispatch's repository. A control plane without the route
// (one of an earlier release the node reconnected to) gives none: the call fails, saying so, since no token is kept
// on the node to fall back on (review round 1).
func (c Client) Token(ctx context.Context) (string, error) {
	token, err := c.token(ctx, agenttool.PathGitHubToken)
	if err != nil {
		return "", fmt.Errorf("asking the control plane for the GitHub App's token: %w", err)
	}

	return token, nil
}

func (c Client) token(ctx context.Context, path string) (string, error) {
	var t agenttool.GitHubToken

	if err := c.get(ctx, http.MethodPost, path, nil, &t); err != nil {
		return "", err
	}

	if t.Token == "" || strings.ContainsAny(t.Token, " \r\n") {
		return "", errors.New("the control plane's answer holds no token")
	}

	return t.Token, nil
}

// pathWithout is PATH without the scripts' directory, so the real command is found.
func pathWithout(bin string) string {
	var dirs []string

	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(d) != filepath.Clean(bin) {
			dirs = append(dirs, d)
		}
	}

	return strings.Join(dirs, string(os.PathListSeparator))
}

// MainGH runs the real gh with a fresh token as GH_TOKEN: `hypomnema-node gh <bin> <gh's arguments>`.
// For gh pr create, the owner's GitHub Connection takes precedence (issue #1339). Other commands keep a GH_TOKEN
// or GITHUB_TOKEN the worker set itself, as Omnara's RunGitHubCLI keeps it. If the control plane cannot provide a
// requested token, gh fails closed (review rounds 1 and 2 of issue #1262).
func MainGH(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: hypomnema-node gh <dir> [gh arguments]")
	}

	if err := os.Setenv("PATH", pathWithout(args[0])); err != nil {
		return err
	}

	gh, err := exec.LookPath("gh")
	if err != nil {
		return errors.New("gh: command not found")
	}

	args = args[1:]
	token, err := FromEnv().ghToken(ctx, args)
	if err != nil {
		return err
	}

	if token != "" {
		if err := os.Setenv("GH_TOKEN", token); err != nil {
			return err
		}
	}

	return syscall.Exec(gh, append([]string{"gh"}, args...), os.Environ()) //nolint:gosec // gh, found on PATH
}

func (c Client) ghToken(ctx context.Context, args []string) (string, error) {
	if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
		token, err := c.token(ctx, agenttool.PathGitHubPRCreateToken)
		if err != nil {
			return "", fmt.Errorf("opening a pull request needs the owner's GitHub Connection: %w", err)
		}

		return token, nil
	}

	if os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" {
		return "", nil
	}

	token, err := c.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("%w; gh does not run with this machine's own login: ask the coordinator, or wait for "+
			"the control plane to give the GitHub App's token again", err)
	}

	return token, nil
}

// MainCredential is git's credential helper: `hypomnema-node git-credential get`, answering a request for
// https://github.com with a fresh token as x-access-token's password, as Omnara's RunGitCredentialHelper does. git
// asks to store or erase too; there is nothing to keep.
func MainCredential(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: hypomnema-node git-credential get")
	}

	if args[0] == "store" || args[0] == "erase" {
		return nil
	}

	if args[0] != "get" {
		return errors.New("git asked the credential helper for something it does not do")
	}

	var protocol, host string

	scanner := bufio.NewScanner(io.LimitReader(in, 64<<10))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}

		key, value, _ := strings.Cut(line, "=")

		switch key {
		case "protocol":
			protocol = value
		case "host":
			host = value
		}
	}

	github := strings.EqualFold(host, githost.GitHubHost) || strings.EqualFold(host, "github.com:443")
	if protocol != "https" || !github {
		// Not github.com: git asks the next helper, or the user.
		return nil
	}

	token, err := FromEnv().Token(ctx)
	if err != nil {
		_, _ = io.WriteString(out, "quit=true\n\n")

		return err
	}

	_, err = fmt.Fprintf(out, "username=x-access-token\npassword=%s\n\n", token)

	return err
}

// timeoutDefault bounds `hypomnema ci wait`: a wait that never ends would hold a worker; two hours is more than any
// of the repositories' CI takes.
const timeoutDefault = 2 * time.Hour

// MainCLI is the worker's `hypomnema`: `hypomnema-node hypomnema <bin> ci wait <pr> [--timeout 2h]`. Any other
// command goes to the `hypomnema` that follows on PATH, when there is one. The wait asks the agent API; a control
// plane without the route, or a repository the App no longer covers, is waited for with `gh pr checks <pr>
// --required --watch`, as before.
func MainCLI(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hypomnema-node hypomnema <dir> ci wait <pr>")
	}

	bin, rest := args[0], args[1:]

	if len(rest) < 2 || rest[0] != "ci" || rest[1] != "wait" {
		if err := os.Setenv("PATH", pathWithout(bin)); err != nil {
			return err
		}

		next, err := exec.LookPath("hypomnema")
		if err != nil {
			return errors.New("only `hypomnema ci wait <pr>` is here, for the CI of your pull request")
		}

		return syscall.Exec(next, append([]string{"hypomnema"}, rest...), os.Environ()) //nolint:gosec // found on PATH
	}

	pr, timeout, err := waitArgs(rest[2:])
	if err != nil {
		return err
	}

	c := FromEnv()
	ask := func(ctx context.Context, hold time.Duration) (ciwait.State, error) {
		var st ciwait.State

		err := c.get(ctx, http.MethodGet, agenttool.PathCIWait, url.Values{
			agenttool.QueryPullRequest: {pr}, agenttool.QueryWait: {fmt.Sprint(int(hold.Seconds()))},
		}, &st)
		if errors.Is(err, errNoRoute) || errors.Is(err, errNotCovered) {
			return st, ciwait.Stop(err)
		}

		return st, err
	}

	return ciwait.Run(ctx, pr, ask, timeout, func(err error) bool {
		return errors.Is(err, errNoRoute) || errors.Is(err, errNotCovered)
	}, out, errOut)
}

// waitArgs reads `ci wait`'s arguments: the pull request, and --timeout.
func waitArgs(args []string) (string, time.Duration, error) {
	var (
		pr      string
		timeout = timeoutDefault
	)

	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d < 0 {
				return "", 0, errors.New("--timeout is a duration such as 90m")
			}

			timeout, i = d, i+1
		case strings.HasPrefix(a, "--timeout="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--timeout="))
			if err != nil || d < 0 {
				return "", 0, errors.New("--timeout is a duration such as 90m")
			}

			timeout = d
		case pr == "" && !strings.HasPrefix(a, "-"):
			pr = a
		default:
			return "", 0, errors.New("usage: hypomnema ci wait <pr> [--timeout 2h]")
		}
	}

	if pr == "" {
		return "", 0, errors.New("usage: hypomnema ci wait <pr> [--timeout 2h]")
	}

	return pr, timeout, nil
}
