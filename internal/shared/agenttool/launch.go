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
	"path/filepath"
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
	// PathDeviceTools lists the tools of the phone every worker shares (issue
	// #417), and PathDeviceCall runs one; both in the browser's shape
	// (BrowserToolList, BrowserCall).
	PathDeviceTools = "/agent/v1/device/tools"
	PathDeviceCall  = "/agent/v1/device/call"
	// PathTools lists every tool of the control plane's own an agent gets
	// (issue #536), each by the agent kinds its definition names: memory,
	// the gateway's (web_search, web_fetch, generate_image) and, for a
	// worker, the browser's; PathToolsCall runs one. Both in the browser's
	// shape (BrowserToolList, BrowserCall). The tools server of a Bot
	// (`hypomnema mcp`) and of a worker (`hypomnema-node mcp`) list them here;
	// the phone's stay on PathDeviceTools.
	PathTools     = "/agent/v1/tools"
	PathToolsCall = "/agent/v1/tools/call"
	// PathGatewayTools lists the gateway's tools alone and PathGatewayCall
	// runs one (issue #492): kept for a `hypomnema mcp` of a release before
	// PathTools. A node of such a release finds them on the browser's routes.
	PathGatewayTools = "/agent/v1/gateway/tools"
	PathGatewayCall  = "/agent/v1/gateway/call"
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

// ToolGenerateImage is the gateway's image tool (issue #492), listed and
// called as the web's tools are. The agent names files, as opencode's
// experimental_image_generate takes them
// (core/src/tool/plugin/image-generate.ts): the tools server on its machine
// reads the images to edit into ImageCall and saves the image of the answer,
// which the control plane gives as MCP image content, where the agent can
// read it.
const ToolGenerateImage = "generate_image"

// ImageCall is the arguments of a generate_image call as the control plane
// takes them.
type ImageCall struct {
	Prompt  string       `json:"prompt"`
	Size    string       `json:"size,omitempty"`
	Quality string       `json:"quality,omitempty"`
	Images  []ImageInput `json:"images,omitempty"`
}

// ImageInput is an image to edit or to use as a reference: its bytes, base64
// in JSON.
type ImageInput struct {
	MIME string `json:"mime_type"`
	Data []byte `json:"data"`
}

const (
	// MaxImageInputs and MaxImageInputBytes bound the images one call sends:
	// opencode's image tool takes up to 8, each up to 20 MiB (MAX_INPUT_BYTES).
	MaxImageInputs     = 8
	MaxImageInputBytes = 20 << 20
	// MaxCallBytes bounds a call's body on PathBrowserCall: an image edit's
	// images, sent whole in base64, are the largest part of one, and the
	// bound is the model gateway's own for an image request
	// (modelgateway.MaxImageRequestBytes).
	MaxCallBytes = 64 << 20
)

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

// HeaderTodo names, on a tunnel's opening, the Inbox item a Bot's pi works on, from EnvTodo (issue #1004): an
// Approval asked through the tunnel waits as that item's, which goes to review until the owner answers.
const HeaderTodo = "Hypomnema-Todo"

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
	// EnvTodo is the id of the Inbox item a Bot's command is run for: HYPOMNEMA_TODO=tdo_… before the command.
	EnvTodo = "HYPOMNEMA_TODO"
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
	// User says the account is the owner's own sign-in through an app (issue #468): the tool acts as him.
	User bool `json:"user,omitempty"`
	// Cannot are the actions the account cannot do at the service, as last read (issue #567): a request for one
	// is refused before it is sent.
	Cannot []Cannot `json:"cannot,omitempty"`
	// Host is the git host the account is on, for a tool of a kind of host (Def.HostKind, issue #740): the
	// Integration the account is of.
	Host string `json:"host,omitempty"`
}

// Cannot is one action an account cannot do, and why.
type Cannot struct {
	Action  string `json:"action"`
	Service string `json:"service"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// ConnectionList answers PathConnections.
type ConnectionList struct {
	Data []Connection `json:"data"`
}

// clientOf is the id of the app of the account named label.
func (l ConnectionList) clientOf(label string) string {
	for _, c := range l.Data {
		if c.Label == label {
			return c.ClientID
		}
	}

	return ""
}

// cannotLine says in one line what the account named label cannot do, service by service, or nothing when it can
// do all that was read (issue #567). It is printed before the tool runs, so that an agent is told before it tries,
// as lark-cli's `auth check` tells an agent which scopes are missing (cmd/auth/check.go).
func (l ConnectionList) cannotLine(integration, label string) string {
	i := slices.IndexFunc(l.Data, func(c Connection) bool { return c.Label == label })
	if i < 0 || len(l.Data[i].Cannot) == 0 {
		return ""
	}

	var (
		services []string
		names    = map[string][]string{}
	)

	for _, c := range l.Data[i].Cannot {
		if _, seen := names[c.Service]; !seen {
			services = append(services, c.Service)
		}

		names[c.Service] = append(names[c.Service], c.Name)
	}

	parts := make([]string, 0, len(services))
	for _, service := range services {
		parts = append(parts, service+": "+strings.Join(names[service], ", "))
	}

	return fmt.Sprintf("hypomnema: %s:%s cannot do these at the service: %s. A command that needs one is refused "+
		"before it is sent; do not try it, tell the owner what is missing.", integration, label,
		strings.Join(parts, "; "))
}

// isUser says whether the account named label is the owner's own sign-in.
func (l ConnectionList) isUser(label string) bool {
	return slices.ContainsFunc(l.Data, func(c Connection) bool { return c.Label == label && c.User })
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
	// ExecTrust is set by the desktop CLI, never by an agent's launcher.
	ExecTrust *ExecTrust
	// Chat is the chat a coordinator works in; empty for a worker.
	Chat string
	// RunKey is the key of the Routine's run whose script runs the tool;
	// empty otherwise.
	RunKey string
	// Todo is the Inbox item the tool is run for (EnvTodo), sent on each tunnel's opening (HeaderTodo).
	Todo            string
	OwnsReplacement replacementOwner
	Client          *http.Client
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// FromEnv is the launcher of an agent whose starter set the environment.
func FromEnv() Launcher {
	return Launcher{
		URL: os.Getenv(EnvURL), Key: os.Getenv(EnvKey), ToolsHome: os.Getenv(EnvTools), Chat: os.Getenv(EnvChat),
		RunKey: RoutineRunKey(), Todo: strings.TrimSpace(os.Getenv(EnvTodo)),
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}
}

// ExitError is the exit code of the tool that ran.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("the tool exited with code %d", e.Code) }

// RunCommand is the name under which the launcher runs a command of the agent's choosing instead of a pinned tool:
// `hypomnema tool run <integration> [--account label] -- <command> [args...]`.
const RunCommand = "run"

// Run runs the tool named name with args. Its errors are written for the
// agent that reads them: they say what is missing and who can give it.
//
// Named RunCommand, it runs any command, such as curl, as a pinned tool runs: through the service proxy of the
// Integration named, which adds the account's credential to a request to one of the Integration's hosts and holds
// it to the owner's rules (issue #617). A pinned tool covers only what its own list knows (gws has no Search
// Console); the proxy relays what the catalog names. As treg runs a command or a shell with HTTPS_PROXY and its CA
// (`treg run`, `treg shell --proxy`) and as every process of Onyx's sandbox goes through its egress proxy.
func (l Launcher) Run(ctx context.Context, name string, args []string) error {
	if name == RunCommand {
		return l.runCommand(ctx, args)
	}

	def, ok := Named(name)
	if !ok {
		return fmt.Errorf("%s is not a tool Hypomnema provides", name)
	}

	label, rest, err := account(args)
	if err != nil {
		return err
	}

	if name == "gh" && len(rest) > 0 && rest[0] == "reconcile-replacement" {
		return reconcileReplacement(ctx, rest[1:], l.Stdout, func(out io.Writer, args ...string) error {
			call := l
			call.Stdout = out
			return call.runPinned(ctx, def, label, args)
		}, l.OwnsReplacement)
	}

	return l.runPinned(ctx, def, label, rest)
}

func (l Launcher) runPinned(ctx context.Context, def Def, label string, args []string) error {
	if l.ToolsHome == "" {
		return fmt.Errorf("%s is run through Hypomnema, which did not set it up for this agent "+
			"(%s, %s and %s are needed)", def.Name, EnvURL, EnvKey, EnvTools)
	}

	//nolint:gosec // The pinned tool, installed by this machine's own installer.
	return l.launch(ctx, def, label, exec.CommandContext(ctx, ToolPath(l.ToolsHome, def.Name, def.Version), args...))
}

// runCommand runs `<integration> [--account label] -- <command> [args...]`. The account flag is read only before
// the "--", so the command's own arguments are its own.
func (l Launcher) runCommand(ctx context.Context, args []string) error {
	usage := fmt.Errorf("%s needs an integration and a command: hypomnema tool %s google [--account work] -- "+
		"curl https://searchconsole.googleapis.com/webmasters/v3/sites", RunCommand, RunCommand)

	if len(args) == 0 {
		return usage
	}

	def, ok := Of(args[0])
	if !ok {
		return fmt.Errorf("%s is not an integration Hypomnema runs a command through", args[0])
	}

	dash := slices.Index(args, "--")
	if dash < 0 || dash == len(args)-1 {
		return usage
	}

	label, rest, err := account(args[1:dash])
	if err != nil {
		return err
	}

	if len(rest) != 0 {
		return fmt.Errorf("%q comes before \"--\"; only %s may: %w", rest[0], accountFlag, usage)
	}

	return l.runOnPath(ctx, def, label, args[dash+1:])
}

// Exec runs a local command through the proxy of an explicitly named Connection.
func (l Launcher) Exec(ctx context.Context, integration, label string, command []string) error {
	if integration == "" || label == "" || len(command) == 0 {
		return errors.New("name a Connection and a command: hypomnema exec <integration>:<label> -- <command> [args...]")
	}

	def, ok := Of(integration)
	if !ok {
		name := strings.TrimSuffix(filepath.Base(command[0]), ".exe")
		def, ok = Named(name)
		if !ok || def.HostKind == "" {
			return fmt.Errorf("%s is not an integration Hypomnema runs %s through", integration, name)
		}

		label = integration + ":" + label
	}

	return l.runOnPath(ctx, def, label, command)
}

func (l Launcher) runOnPath(ctx context.Context, def Def, label string, command []string) error {
	//nolint:gosec // The agent's own command, run as the agent, as any command it runs.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if errors.Is(cmd.Err, exec.ErrNotFound) || errors.Is(cmd.Err, os.ErrNotExist) {
		return fmt.Errorf("%s is not installed: install it from its official distribution and put it on PATH", command[0])
	}

	return l.launch(ctx, def, label, cmd)
}

// launch runs cmd for an Integration's account: the account named, or the only one the agent was given, through
// the service proxy, with the tool's placeholder and variables and without the agent's key.
func (l Launcher) launch(ctx context.Context, def Def, label string, cmd *exec.Cmd) error {
	name := filepath.Base(cmd.Path)

	if l.URL == "" || l.Key == "" {
		return fmt.Errorf("%s is run through Hypomnema, which did not set it up for this agent "+
			"(%s and %s are needed)", name, EnvURL, EnvKey)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()

	// The accounts are asked for on every run, so that a wrong label, or none where several were given, is said
	// in words and not found out by the tool's first request.
	list, err := l.accounts(fetchCtx, def)
	if err != nil {
		return err
	}

	integration := def.Integration

	if def.HostKind != "" {
		// A tool for every host of a kind acts on the host of the account it acts as: that host is the Integration.
		if integration, label, err = pickOnHost(def, list, label); err != nil {
			return err
		}

		list = list.on(integration)
	} else if label, err = pick(def, list, label); err != nil {
		return err
	}

	if line := list.cannotLine(integration, label); line != "" && l.Stderr != nil {
		_, _ = fmt.Fprintln(l.Stderr, line)
	}

	cmd.Stdin, cmd.Stdout, cmd.Stderr = l.Stdin, l.Stdout, l.Stderr
	cmd.Env = append(without(os.Environ(), append([]string{EnvKey}, def.Unset...)...), def.Env...)

	if def.HostEnv != "" {
		cmd.Env = append(cmd.Env, def.HostEnv+"="+integration)
	}

	// The tool holds no token: its requests go through the control plane's proxy, which adds it.
	var local *execAuthority
	if store := userTrustStore(); l.ExecTrust != nil && store != nil {
		local, err = l.ExecTrust.prepare(ctx, store, l.Stderr)
		if err != nil {
			return err
		}
	}
	if local != nil {
		var finishFetch context.CancelFunc
		fetchCtx, finishFetch = context.WithTimeout(ctx, launchTimeout)
		defer finishFetch()
	}
	env, refused, stop, err := l.bridge(fetchCtx, def, integration, label, list.isUser(label), local)
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

// pickOnHost is the account a run of a tool for a kind of host acts as, and its host: the one named, host:label or
// a label only one host has, or the only one the agent was given. With none, or several and none named, there is
// no answer: nothing is guessed.
func pickOnHost(def Def, list ConnectionList, named string) (host, label string, err error) {
	var found []Connection

	for _, c := range list.Data {
		if named == "" || named == c.Label || named == c.Host+":"+c.Label {
			found = append(found, c)
		}
	}

	switch {
	case len(found) == 1:
		return found[0].Host, found[0].Label, nil
	case named != "" && len(found) == 0:
		return "", "", fmt.Errorf("this agent was not given %s on a %s host", named, def.HostKind)
	case len(found) == 0:
		return "", "", fmt.Errorf("this agent was given no account of a %s host. The owner connects one with "+
			"`hypomnema integrations connect <host>` and allows it for chats (`integrations chats <host>:<label> on`)",
			def.HostKind)
	}

	accounts := make([]string, 0, len(found))
	for _, c := range found {
		accounts = append(accounts, c.Host+":"+c.Label)
	}

	return "", "", fmt.Errorf("this agent may use several %s accounts: %s. Name one with %s <host>:<label>",
		def.HostKind, strings.Join(accounts, ", "), accountFlag)
}

// on is the list's accounts on one host.
func (l ConnectionList) on(host string) ConnectionList {
	out := ConnectionList{}

	for _, c := range l.Data {
		if c.Host == host {
			out.Data = append(out.Data, c)
		}
	}

	return out
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
