package agenttool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// The agent API of the control plane, which the launcher calls. It is two
// requests, each with the agent's own credential as a bearer token: which
// accounts of an Integration the agent may use, and a fresh access token for
// one of them.
const (
	PathConnections = "/agent/v1/connections"
	PathToken       = "/agent/v1/token"
	// PathTunnel carries a tool's connection to a service through the
	// control plane's service proxy (issue #154); PathCA gives the
	// certificate the tool must trust to talk to it.
	PathTunnel = "/agent/v1/tunnel"
	PathCA     = "/agent/v1/ca"
	// PathBrowserTools lists the tools of the browser every agent shares
	// (issue #193), and PathBrowserCall runs one in the agent's own tab.
	PathBrowserTools = "/agent/v1/browser/tools"
	PathBrowserCall  = "/agent/v1/browser/call"
)

// BrowserTool is one browser tool as PathBrowserTools lists it: an MCP
// tool's name, description and input schema.
type BrowserTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// BrowserToolList answers PathBrowserTools.
type BrowserToolList struct {
	Data []BrowserTool `json:"data"`
}

// BrowserCall is the body of PathBrowserCall. Its answer is the tool's
// result as MCP has it: content, and isError when the tool failed.
type BrowserCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// The parameters of PathTunnel.
const (
	QueryIntegration = "integration"
	QueryLabel       = "label"
	QueryHost        = "host"
	QueryChat        = "chat"
)

// HeaderRoutineRun carries the key of a Routine's run on a request its
// script makes: on the tunnel's opening and on the client API.
const HeaderRoutineRun = "Hypomnema-Routine-Run"

// RoutineRunKey reads the key of the Routine's run this process runs in, or
// "" outside one.
func RoutineRunKey() string {
	file := os.Getenv(EnvRoutineRun)
	if file == "" {
		return ""
	}

	raw, err := os.ReadFile(file) //nolint:gosec // The file the launcher of this run named.
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(raw))
}

// Placeholder is what a tool is given in place of a token when its
// Integration goes through the service proxy, for a tool that will not send
// a request without one. The proxy puts the real token in its place;
// anywhere else it opens nothing (Omnigent's oa_cred placeholder).
const Placeholder = "hypomnema-placeholder"

// The launcher's environment, set by whoever starts the agent.
const (
	// EnvURL is the control plane's address.
	EnvURL = "HYPOMNEMA_AGENT_URL"
	// EnvKey is the agent's credential: a Dispatch's own key.
	EnvKey = "HYPOMNEMA_AGENT_KEY"
	// EnvTools is the directory the real tools are installed under.
	EnvTools = "HYPOMNEMA_TOOLS_HOME"
	// EnvChat is the chat a coordinator works in, so that what it asks the
	// owner to approve is shown there. A pi's own token is bound to its chat
	// and decides instead (issue #260).
	EnvChat = "HYPOMNEMA_CHAT_ID"
	// EnvRoutineRun names the file that holds the key of the Routine's run a
	// script runs in, written by its launcher (issue #211): a request that
	// presents the key is that run's, so what the script asks the owner to
	// approve belongs to the run.
	EnvRoutineRun = "HYPOMNEMA_ROUTINE_RUN_FILE"
)

// accountFlag names the account to act as when the agent may use several of
// one Integration, as gog's --account does. The tool itself never sees it.
const accountFlag = "--account"

const launchTimeout = 60 * time.Second

// Connection is one account an agent may use.
type Connection struct {
	Label   string `json:"label"`
	Account string `json:"account"`
	// ClientID is the id of the account's app, for a tool that will not run
	// without it (Def.ClientEnv): a Lark app's id, each account being an app
	// of its own (issue #311). Not a secret.
	ClientID string `json:"client_id,omitempty"`
}

// ConnectionList answers PathConnections.
type ConnectionList struct {
	Data []Connection `json:"data"`
	// ClientID is the one app's id when every account is the same app, for a
	// launcher from before each account had its own. Not a secret.
	ClientID string `json:"client_id,omitempty"`
}

// clientOf is the id of the app of the account named label: its own, or
// the list's from a control plane from before each account had its own.
func (l ConnectionList) clientOf(label string) string {
	for _, c := range l.Data {
		if c.Label == label && c.ClientID != "" {
			return c.ClientID
		}
	}

	return l.ClientID
}

// TokenRequest is the body of PathToken.
type TokenRequest struct {
	Integration string `json:"integration"`
	Label       string `json:"label"`
}

// TokenResponse answers PathToken, which only the owner's own token may ask:
// no launcher asks for a token, since every tool goes through the service
// proxy.
type TokenResponse struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	// ClientID is the id of the Integration's client, for a tool that will
	// not run without it (Def.ClientEnv): a Lark app's id. Not a secret.
	ClientID string `json:"client_id,omitempty"`
}

// Launcher runs a tool for an agent with a fresh token of the account it acts
// as. It asks for the token on every call, so a tool run hours into a Dispatch
// still gets one that works: an agent's environment cannot be changed once it
// has started. Coder answers git's prompt the same way (GIT_ASKPASS), and
// Omnigent its git credential helper.
type Launcher struct {
	// URL is the control plane's address; Key the agent's credential.
	URL string
	Key string
	// ToolsHome is the directory the real tools are installed under.
	ToolsHome string
	// Chat is the chat a coordinator works in; empty for a worker.
	Chat string
	// RunKey is the key of the Routine's run whose script runs the tool;
	// empty otherwise.
	RunKey string
	Client *http.Client
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// FromEnv is the launcher of an agent whose starter set the environment.
func FromEnv() Launcher {
	return Launcher{
		URL: os.Getenv(EnvURL), Key: os.Getenv(EnvKey), ToolsHome: os.Getenv(EnvTools), Chat: os.Getenv(EnvChat),
		RunKey: RoutineRunKey(),
		Stdin:  os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}
}

// ExitError is the exit code of the tool that ran.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("the tool exited with code %d", e.Code) }

// Run runs the tool named name with args. Its errors are written for the
// agent that reads them: they say what is missing and who can give it.
func (l Launcher) Run(ctx context.Context, name string, args []string) error {
	def, ok := Named(name)
	if !ok {
		return fmt.Errorf("%s is not a tool Hypomnema provides", name)
	}

	if l.URL == "" || l.Key == "" || l.ToolsHome == "" {
		return fmt.Errorf("%s is run through Hypomnema, which did not set it up for this agent "+
			"(%s, %s and %s are needed)", name, EnvURL, EnvKey, EnvTools)
	}

	label, rest, err := account(args)
	if err != nil {
		return err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	// The accounts are asked for on every run, so that a wrong label, or none where several were given, is said
	// in words and not found out by the tool's first request.
	list, err := l.accounts(fetchCtx, def)
	if err != nil {
		return err
	}

	label, err = pick(def, list, label)
	if err != nil {
		return err
	}

	//nolint:gosec // The pinned tool, installed by this machine's own installer.
	cmd := exec.CommandContext(ctx, ToolPath(l.ToolsHome, def.Name, def.Version), rest...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = l.Stdin, l.Stdout, l.Stderr
	cmd.Env = append(without(os.Environ(), append([]string{EnvKey}, def.Unset...)...), def.Env...)

	// The tool holds no token: its requests go through the control plane's proxy, which adds it.
	env, refused, stop, err := l.bridge(fetchCtx, def, label)
	if err != nil {
		return err
	}
	defer stop()

	// Said after the tool has ended, in the control plane's own words: the tool itself only knows that its proxy
	// refused.
	defer func() {
		if reason := refused.said(); reason != "" && l.Stderr != nil {
			_, _ = fmt.Fprintln(l.Stderr, "hypomnema: "+reason)
		}
	}()

	cmd.Env = append(without(cmd.Env, proxyVariables...), env...)

	if client := list.clientOf(label); def.ClientEnv != "" && client != "" {
		cmd.Env = append(cmd.Env, def.ClientEnv+"="+client)
	}

	err = cmd.Run()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return &ExitError{Code: exit.ExitCode()}
	}

	if err != nil {
		return fmt.Errorf("running %s: %w", name, err)
	}

	return nil
}

// account takes "--account label" or "--account=label" out of the arguments.
func account(args []string) (label string, rest []string, err error) {
	rest = make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == accountFlag:
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s needs the account's label, such as %s work", accountFlag, accountFlag)
			}

			label = args[i+1]
			i++
		case strings.HasPrefix(arg, accountFlag+"="):
			label = strings.TrimPrefix(arg, accountFlag+"=")
		default:
			rest = append(rest, arg)
		}
	}

	return label, rest, nil
}

// accounts asks the control plane which accounts of the tool's Integration
// the agent may use.
func (l Launcher) accounts(ctx context.Context, def Def) (ConnectionList, error) {
	var list ConnectionList
	if err := l.call(ctx, http.MethodGet, PathConnections+"?integration="+url.QueryEscape(def.Integration), nil,
		&list); err != nil {
		return ConnectionList{}, fmt.Errorf("asking which %s accounts this agent may use: %w", def.Integration, err)
	}

	return list, nil
}

// pick is the account a run acts as: the one that was named, when the agent
// was given it, or the only one it was given. With none, or several and none
// named, there is no answer: nothing is guessed.
func pick(def Def, list ConnectionList, named string) (string, error) {
	if named != "" {
		if !slices.ContainsFunc(list.Data, func(c Connection) bool { return c.Label == named }) {
			return "", fmt.Errorf("this agent was not given %s:%s", def.Integration, named)
		}

		return named, nil
	}

	switch len(list.Data) {
	case 0:
		return "", fmt.Errorf("this agent was given no %s account. The owner connects one in Settings, Integrations, "+
			"and allows it for chats or gives it to the Run (--use %s:<label>)", def.Integration, def.Integration)
	case 1:
		return list.Data[0].Label, nil
	default:
		labels := make([]string, 0, len(list.Data))
		for _, c := range list.Data {
			labels = append(labels, c.Label+" ("+c.Account+")")
		}

		return "", fmt.Errorf("this agent may use several %s accounts: %s. Name one with %s <label>", def.Integration,
			strings.Join(labels, ", "), accountFlag)
	}
}

// call sends one request to the agent API. Its errors hold the server's own
// message and never the credential.
func (l Launcher) call(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}

		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(l.URL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+l.Key)
	req.Header.Set("Content-Type", "application/json")

	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: launchTimeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}

		return fmt.Errorf("the control plane could not be reached: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading the control plane's answer: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var refused struct {
			Message string `json:"message"`
		}

		if json.Unmarshal(raw, &refused) == nil && refused.Message != "" {
			return errors.New(refused.Message)
		}

		return fmt.Errorf("the control plane answered %d", resp.StatusCode)
	}

	if err := json.Unmarshal(raw, into); err != nil {
		return errors.New("the control plane's answer could not be read")
	}

	return nil
}

// without drops variables from an environment: the tool has no use for the
// agent's credential.
func without(env []string, names ...string) []string {
	out := make([]string, 0, len(env))

	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(names, name) {
			out = append(out, kv)
		}
	}

	return out
}

// Shim is the script put on an agent's PATH under a tool's name: it runs the
// launcher of the binary exe. The agent calls the tool as it would anywhere.
func Shim(exe, name string) string {
	return "#!/bin/sh\n# Runs " + name + " through Hypomnema, which gives it a fresh credential on every call.\n" +
		"exec '" + strings.ReplaceAll(exe, "'", `'\''`) + "' tool " + name + ` "$@"` + "\n"
}
