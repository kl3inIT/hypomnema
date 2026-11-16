// Package controlplane assembles the control plane: its configuration, its
// SQLite database, its HTTP server with the node API, the scheduler loop, and
// the owner's commands. The parts that make it useful (orchestration, vault,
// coordinator, channels, policy) are packages under this tree.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
	larkchannel "github.com/kl3inIT/hypomnema/internal/controlplane/channels/lark"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging/whatsapp"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/controlplane/routines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillssh"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
	"github.com/kl3inIT/hypomnema/internal/controlplane/webui"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/web"
)

const (
	binary    = "hypomnema"
	envPrefix = "HYPOMNEMA"
)

// Config is the control plane's configuration, read from the environment.
type Config struct {
	HTTPAddr string
	Log      platform.LogConfig
	Database sqlite.Config
	// CoordinatorCommand starts pi for a chat id appended to it, from
	// HYPOMNEMA_COORDINATOR_COMMAND as a JSON array; empty: no coordinator.
	CoordinatorCommand []string
	// BotFilesCommand answers a question about a Bot's folder (issue #255),
	// from HYPOMNEMA_BOT_FILES_COMMAND as a JSON array, such as [sudo -n -u
	// coordinator /usr/local/bin/hypomnema files serve --root DIR]; empty:
	// the Bots' folders are not shown.
	BotFilesCommand []string
	// CoordinatorToken is the name of the coordinator token pi's runs belong
	// to, from HYPOMNEMA_COORDINATOR_TOKEN; the setup script makes it as
	// pi-coordinator. Each pi is given a token of its own made under it,
	// bound to its chat and Bot (issue #260): the Runs it creates are this
	// token's.
	CoordinatorToken string
	// RoutineCommand runs a script Routine for the Bot's name and the
	// Routine's id appended to it, from HYPOMNEMA_ROUTINE_COMMAND as a JSON
	// array; empty: a script Routine's run fails, saying so (issue #211).
	RoutineCommand []string
	// ChatRmCommand removes what pi kept for a deleted chat on this machine,
	// its run/ folder and its sessions, for the chat's id appended to it
	// (issue #484), from HYPOMNEMA_CHAT_RM_COMMAND as a JSON array, such as
	// [sudo -n -u coordinator /usr/local/bin/hypomnema-chat-rm]; empty: a
	// deleted chat's files there stay, and the deletion says so.
	ChatRmCommand []string
	// VaultKeys holds the vault's root keys, from HYPOMNEMA_VAULT_KEYS (a
	// comma-separated list of id:base64 entries) and
	// HYPOMNEMA_VAULT_ACTIVE_KEY_ID; nil: the vault is not configured.
	VaultKeys *vault.Keyring
	// PublicURL is the control plane's URL as nodes reach it, from
	// HYPOMNEMA_PUBLIC_URL; empty: the install script names the host it was
	// asked at.
	PublicURL string
	// NodeReleaseURL is where the install script downloads the node from,
	// from HYPOMNEMA_NODE_RELEASE_URL; empty: the latest GitHub release.
	NodeReleaseURL string
	// CodexIssuer is the authorization server Codex accounts log in at, from
	// HYPOMNEMA_CODEX_ISSUER; empty: OpenAI's. Tests name their own.
	CodexIssuer string
	// CodexBaseURL is the backend the gateway serves a Codex subscription
	// from, from HYPOMNEMA_CODEX_BASE_URL; empty: ChatGPT's. Tests name their own.
	CodexBaseURL string
	// ClaudeBaseURL is the Anthropic Messages backend the gateway serves a
	// Claude subscription from, from HYPOMNEMA_CLAUDE_BASE_URL; empty:
	// Anthropic's. Tests name their own.
	ClaudeBaseURL string
	// Claude configures the Claude login's OAuth hosts, from
	// HYPOMNEMA_CLAUDE_AUTH_BASE / _TOKEN_BASE / _PROFILE_BASE; empty:
	// Anthropic's. Tests name their own.
	Claude subscription.ClaudeOptions
	// AntigravityBaseURL is the Cloud Code host the gateway serves an
	// Antigravity subscription from, from HYPOMNEMA_ANTIGRAVITY_BASE_URL;
	// empty: Google's daily host. Tests name their own.
	AntigravityBaseURL string
	// Antigravity configures the Antigravity login: the OAuth secret of the
	// IDE's client, from HYPOMNEMA_ANTIGRAVITY_CLIENT_SECRET, which is never
	// in the source; empty: no Antigravity account can be logged in or
	// refreshed. Tests set its addresses.
	Antigravity subscription.AntigravityOptions
	// BrowserDir holds the browser every agent shares (issue #193): its
	// programs, its profile with the owner's sign-ins, and its sockets. From
	// HYPOMNEMA_BROWSER_DIR; empty: `browser` beside the database.
	BrowserDir string
	// BrowserOff gives no agent the shared browser, from HYPOMNEMA_BROWSER=0
	// (issue #212). Agents have it otherwise, as T3 Code's and OpenMausBot's
	// have theirs unless a setting turns it off.
	BrowserOff bool
	// BrowserNoSandbox runs Chrome without its sandbox, from
	// HYPOMNEMA_BROWSER_SANDBOX=0. Nothing else turns the sandbox off.
	BrowserNoSandbox bool
	// BrowserAllow lists the private destinations the shared browser may
	// reach (issue #202), from HYPOMNEMA_BROWSER_ALLOW: entries such as
	// `10.0.0.5`, `192.168.1.0/24` or `10.0.0.5:8080`, separated by commas.
	// Empty: the public internet only.
	BrowserAllow egress.Allow
	// BrowserLanguage is the language the shared browser's pages are told,
	// from HYPOMNEMA_BROWSER_LANG, else the control plane's locale (issue
	// #540; browser.Language).
	BrowserLanguage string
	// BrowserTimeZone is the zone the shared browser tells time in, from
	// HYPOMNEMA_BROWSER_TIMEZONE, else the control plane's TZ (browser.TimeZone).
	BrowserTimeZone string
	// PprofAddr serves Go's profiles (net/http/pprof) on a listener of its
	// own (issue #586), from HYPOMNEMA_PPROF_ADDR, such as 127.0.0.1:6060, as
	// Coder's --pprof-address; only a loopback address is taken. Empty: no
	// profiler.
	PprofAddr string
}

// LoadConfig reads the configuration and reports every problem at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := platform.NewEnv(getenv)
	cfg := Config{
		HTTPAddr: env.String(envPrefix+"_HTTP_ADDR", "127.0.0.1:8080"),
		Log:      env.Log(envPrefix),
		Database: sqlite.FromEnv(env, envPrefix),

		PublicURL:          strings.TrimSuffix(env.String(envPrefix+"_PUBLIC_URL", ""), "/"),
		NodeReleaseURL:     env.String(envPrefix+"_NODE_RELEASE_URL", ""),
		CodexIssuer:        env.String(envPrefix+"_CODEX_ISSUER", ""),
		CodexBaseURL:       env.String(envPrefix+"_CODEX_BASE_URL", ""),
		ClaudeBaseURL:      env.String(envPrefix+"_CLAUDE_BASE_URL", ""),
		AntigravityBaseURL: env.String(envPrefix+"_ANTIGRAVITY_BASE_URL", ""),
		Claude: subscription.ClaudeOptions{
			AuthBase:    env.String(envPrefix+"_CLAUDE_AUTH_BASE", ""),
			TokenBase:   env.String(envPrefix+"_CLAUDE_TOKEN_BASE", ""),
			ProfileBase: env.String(envPrefix+"_CLAUDE_PROFILE_BASE", ""),
		},
		Antigravity: subscription.AntigravityOptions{
			ClientSecret: env.String(envPrefix+"_ANTIGRAVITY_CLIENT_SECRET", ""),
		},
		BrowserDir:       env.String(envPrefix+"_BROWSER_DIR", ""),
		BrowserOff:       env.String(envPrefix+"_BROWSER", "1") == "0",
		BrowserNoSandbox: env.String(envPrefix+"_BROWSER_SANDBOX", "1") == "0",
		BrowserLanguage:  browser.Language(env.String(envPrefix+"_BROWSER_LANG", ""), getenv),
		BrowserTimeZone:  browser.TimeZone(env.String(envPrefix+"_BROWSER_TIMEZONE", ""), getenv),
		PprofAddr:        env.String(envPrefix+"_PPROF_ADDR", ""),
	}

	if cfg.BrowserDir == "" {
		cfg.BrowserDir = filepath.Join(filepath.Dir(cfg.Database.Path), "browser")
	}

	cfg.CoordinatorToken = env.String(envPrefix+"_COORDINATOR_TOKEN", "pi-coordinator")

	var cmdErr error
	if raw := env.String(envPrefix+"_COORDINATOR_COMMAND", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.CoordinatorCommand); err != nil || len(cfg.CoordinatorCommand) == 0 {
			cmdErr = fmt.Errorf("%s_COORDINATOR_COMMAND is not a JSON array of strings", envPrefix)
		}
	}

	if raw := env.String(envPrefix+"_BOT_FILES_COMMAND", ""); raw != "" && cmdErr == nil {
		if err := json.Unmarshal([]byte(raw), &cfg.BotFilesCommand); err != nil || len(cfg.BotFilesCommand) == 0 {
			cmdErr = fmt.Errorf("%s_BOT_FILES_COMMAND is not a JSON array of strings", envPrefix)
		}
	}

	if raw := env.String(envPrefix+"_ROUTINE_COMMAND", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.RoutineCommand); err != nil || len(cfg.RoutineCommand) == 0 {
			cmdErr = errors.Join(cmdErr, fmt.Errorf("%s_ROUTINE_COMMAND is not a JSON array of strings", envPrefix))
		}
	}

	if raw := env.String(envPrefix+"_CHAT_RM_COMMAND", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.ChatRmCommand); err != nil || len(cfg.ChatRmCommand) == 0 {
			cmdErr = errors.Join(cmdErr, fmt.Errorf("%s_CHAT_RM_COMMAND is not a JSON array of strings", envPrefix))
		}
	}

	keys, keysErr := vault.ParseKeyring(getenv(envPrefix+"_VAULT_KEYS"), getenv(envPrefix+"_VAULT_ACTIVE_KEY_ID"))
	cfg.VaultKeys = keys

	allow, allowErr := egress.ParseAllow(strings.Split(env.String(envPrefix+"_BROWSER_ALLOW", ""), ","))
	if allowErr != nil {
		allowErr = fmt.Errorf("%s_BROWSER_ALLOW: %w", envPrefix, allowErr)
	}

	cfg.BrowserAllow = allow

	var pprofErr error
	if cfg.PprofAddr != "" {
		if err := loopbackAddr(cfg.PprofAddr); err != nil {
			pprofErr = fmt.Errorf("%s_PPROF_ADDR: %w", envPrefix, err)
		}
	}

	if err := errors.Join(env.Err(), cmdErr, keysErr, allowErr, pprofErr); err != nil {
		return Config{}, fmt.Errorf("configuration: %w", err)
	}

	return cfg, nil
}

// Migrate brings the database schema up to this binary's version.
func Migrate(ctx context.Context, cfg Config, stdout io.Writer) error {
	logger := platform.NewLogger(stdout, cfg.Log, binary)

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at exit; nothing is left to report it to.

	if err := orchestration.Migrate(ctx, db); err != nil {
		return err
	}

	logger.InfoContext(ctx, "database migrated", slog.String("path", cfg.Database.Path))

	return nil
}

// Serve runs the control plane until ctx is canceled. It refuses a database
// whose schema is not this binary's; `hypomnema migrate` fixes that.
func Serve(ctx context.Context, cfg Config, stdout io.Writer) error {
	logger := platform.NewLogger(stdout, cfg.Log, binary)

	db, err := sqlite.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	if err := orchestration.CheckSchema(ctx, db); err != nil {
		return err
	}

	// A deleted chat's gateway keys end in its transaction, written by the gateway (issue #405).
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithChatKeys(modelgateway.RevokeDeletedChatKeysTx))
	publishBuiltinSkills(ctx, svc, logger)

	// The scheduler runs while the server does; if it fails, the server stops.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	mux := http.NewServeMux()
	platform.RegisterHealth(mux, sqlite.Ready(db), logger)
	referrers := secretReferrers(svc)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, referrers)
	tokens := connections{svc: svc, vault: secrets}
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID,
		integration.Google(), integration.Lark(), integration.Superset(), integration.AWS(), integration.WhatsApp())

	// The owner's WhatsApp, linked as a device of his (issue #615): its sessions are whatsmeow's, in a database
	// of their own beside this one, and are opened again as the control plane starts.
	wa, err := whatsapp.Open(ctx, filepath.Join(filepath.Dir(cfg.Database.Path), "whatsapp.db"), logger)
	if err != nil {
		return err
	}
	defer wa.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	integrations.WithDevices(integration.WhatsAppID, wa)

	if err := integrations.OpenDevices(ctx); err != nil {
		logger.WarnContext(ctx, "a linked account's session could not be opened", slog.String("error", err.Error()))
	}
	defer integrations.CloseDevices()
	// What each account can really do is read at its service and said action by action from the proxy's
	// catalog (issue #567): on connect, on sign-in again, on the owner's check, and after a 403.
	checkAccess := func(ctx context.Context, id, label string) error {
		held, err := integrations.Holdings(ctx, id, label)
		if err != nil {
			return err
		}

		return svc.SetConnectionAccess(ctx, id, label, serviceproxy.Assess(held, time.Now()))
	}
	integrations.WithChecker(checkAccess)
	machineMgr := machineManager(svc, secrets, awsRoles(integrations), cfg.PublicURL, logger)

	mux.Handle("GET "+nodeapi.PathInstallScript, nodeapi.InstallScript(cfg.PublicURL, cfg.NodeReleaseURL))

	subscriptions := newSubscriptions(cfg, svc, secrets, logger)
	gateway := modelgateway.New(db, platform.SystemClock{}, platform.NewID, gatewayProviders(svc),
		gatewaySecrets(secrets)).WithProviderList(gatewayProviderList(svc)).
		WithServedLimits(gatewayServedLimits(subscriptions))
	workerKeys := dispatchKeys{gateway: gateway, svc: svc, logger: logger}

	nodes := nodeapi.New(svc, logger).WithHostTokens(tokens.Token).WithModelKeys(workerKeys.Key).
		WithAgentKeys(workerKeys.AgentKey).WithTools(integrations.Tools).WithRuntimeTools(agenttool.RuntimeTools).
		WithReleaseURL(cfg.NodeReleaseURL)
	nodes.Register(mux)
	// One API for every agent's tools: a worker with its Dispatch's key, the coordinator with its token.
	agents := agentapi.New(svc, func(ctx context.Context, id, label string) (agentapi.Token, error) {
		token, err := integrations.Token(ctx, id, label)

		return agentapi.Token{
			AccessToken: token.AccessToken, ExpiresAt: token.ExpiresAt, ClientID: token.ClientID,
		}, err
	}, logger)

	// The browser every agent shares (issue #193). Nothing is installed or started until an agent uses it. Its
	// directory is made absolute: agent-browser's sockets are named by their full path.
	browserDir, err := filepath.Abs(cfg.BrowserDir)
	if err != nil {
		return fmt.Errorf("the browser's directory: %w", err)
	}

	// The browser never reaches the control plane's own port, whatever the owner's list allows (issue #202).
	var ownPorts []uint16

	if _, port, err := net.SplitHostPort(cfg.HTTPAddr); err == nil {
		if n, err := strconv.ParseUint(port, 10, 16); err == nil && n != 0 {
			ownPorts = append(ownPorts, uint16(n))
		}
	}

	shared := browser.New(browser.Config{
		Dir: browserDir, NoSandbox: cfg.BrowserNoSandbox, Allow: cfg.BrowserAllow, OwnPorts: ownPorts,
		Language: cfg.BrowserLanguage, TimeZone: cfg.BrowserTimeZone, Logger: logger,
	})
	if !cfg.BrowserOff {
		agents.WithBrowser(shared)
	}

	// The web's search and pages (issue #354). A page is read only where the shared browser may go: the same
	// rules, the owner's list of private destinations and the control plane's own port.
	webSearch := websearch.New(websearch.Config{
		// What the owner set on the Web search page and the keys in the vault, read at each search.
		Keys: searchKeys(secrets, logger), Settings: gateway.SearchSettings, Usage: gateway,
		Logger: logger,
		Rules: egress.Rules{Allow: cfg.BrowserAllow, OwnPort: func(port uint16) bool {
			return slices.Contains(ownPorts, port)
		}},
	})
	metered := meteredWeb{web: webSearch, gateway: gateway, logger: logger}
	agents.WithRuntimes(dispatchRuntime(svc))

	// The phone a node holds (issue #417), given to every worker as the browser is. A call that finds its
	// machine stopped starts it as `machines start` does.
	sharedPhone := phone.New(phone.Config{
		Nodes: svc, Link: nodes, Machines: machineMgr, Views: nodeViews(nodes), Logger: logger,
	})
	agents.WithDevice(sharedPhone)

	// An agent's commands on a node's machine (issue #543), sudo given the node's password from the vault.
	agents.WithNodeExec(svc, nodes, nodeSudo(secrets))

	// The owner's hold on the phone ends as the browser's does (issue #417, step 3).
	go sharedPhone.Run(ctx)
	nodes.WithPhone(sharedPhone.Held)

	agents.Register(mux)

	// The service proxy: an agent's tool reaches a connected service through it and never holds the token.
	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: svc, Logger: logger,
		Token: func(ctx context.Context, id, label string) (string, error) {
			token, err := integrations.Token(ctx, id, label)

			return token.AccessToken, err
		},
		// A request to AWS is signed again with the session of the role it was made for (issue #331).
		AWS: integrations.RoleCredentials,
		// A Connection that has its own address (Superset) is sent a relayed request by the service that signs
		// it in, which adds the token there.
		Send: func(ctx context.Context, id, label string, r serviceproxy.Sent) (serviceproxy.Answer, error) {
			answer, err := integrations.Call(ctx, id, label, integration.AccountRequest{
				Method: r.Method, Path: r.Path, RawQuery: r.RawQuery, ContentType: r.ContentType, Body: r.Body,
			})

			return serviceproxy.Answer{Status: answer.Status, ContentType: answer.ContentType, Body: answer.Body}, err
		},
		Recheck: checkAccess,
	})
	if err != nil {
		return err
	}

	proxy.Register(mux)
	// Superset's tools of a worker and a Bot's pi (issue #536) are relayed through the same decision as the
	// client API's Superset routes; the proxy is made after the agent API it asks who an agent is.
	agents.WithRelay(proxy)
	gatewayServer := modelgateway.NewServer(gateway, logger, modelgateway.Config{
		Subscriptions: gatewayAccounts{subs: subscriptions}, CodexBaseURL: cfg.CodexBaseURL,
		ClaudeBaseURL:      cfg.ClaudeBaseURL,
		AntigravityBaseURL: cfg.AntigravityBaseURL,
		Web:                webSearch,
	})
	gatewayServer.Register(mux)
	// The subscriptions the owner logged in search too, through the gateway's calls to them (issue #354); the
	// gateway is made after the search it serves, before anything is served.
	webSearch.WithSubscriptions(gatewayServer.SubscriptionSearch())

	// No pi outlives the control plane, so a chat's key left from an earlier run has nobody to use it (issue #204).
	if err := gateway.RevokeAllChatKeys(ctx); err != nil {
		return fmt.Errorf("ending the gateway keys of earlier chats: %w", err)
	}

	sup := coordinator.New(svc, coordinator.Config{
		Command: cfg.CoordinatorCommand, Keys: chatKeys{gateway: gateway, chats: svc},
		Tokens: botTokens{svc: svc, coordinator: cfg.CoordinatorToken}, Logger: logger,
	})

	// Every worker's and every Bot's pi's web tools and generate_image (issues #354, #492), each call recorded on
	// its Dispatch's key or on the key of the chat's running pi; before anything is served.
	agents.WithWeb(agentWeb{worker: workerWeb{metered}, bot: botWeb{meteredWeb: metered, chats: sup}}).
		WithImages(agentImages{
			worker: workerImages{server: gatewayServer, gateway: gateway, logger: logger},
			bot:    botImages{server: gatewayServer, gateway: gateway, chats: sup, logger: logger},
		})

	// The Channels (issues #136, #169 and #404): one runtime for every kind, each kind's Channel found in the
	// registry, which refuses a kind given twice; each account's secret is opened from the vault to connect.
	registry, err := channels.NewRegistry(larkchannel.LarkChannel{})
	if err != nil {
		return fmt.Errorf("registering the channels: %w", err)
	}

	// A Bot's folder is read for the Files panel and for the files a Bot sends into its chat (issue #312).
	bots := newBotFiles(cfg.BotFilesCommand)

	channelData := channelStore{Service: svc, Store: svc.Channels()}

	runtime := channels.New(channelData, sup, registry, secrets, logger).WithWebURL(cfg.PublicURL)
	if bots != nil {
		runtime.WithFiles(bots)
	}

	larkSettings := larkchannel.NewService(channels.NewService(channelData, secrets, runtime))

	// The Routines of the Bots (issue #211): the tick, and the runs it starts.
	routineRunner := routines.New(svc, svc.Routines(), routines.Config{
		Command: cfg.RoutineCommand, Coordinator: sup, Owner: runtime, Logger: logger,
		Tokens: botTokens{svc: svc, coordinator: cfg.CoordinatorToken},
	})
	// A Bot's pi runs its own Routines with its routines tool, through the same runner (issue #536).
	agents.WithRoutines(routineRunner)

	api := clientapi.New(svc, logger, ctx.Done()).WithCoordinator(sup).
		WithAccounts(subscriptions).WithGatewayModels(gateway).WithGatewayKeys(gateway).WithGatewayUsage(gateway).
		WithHostTokens(tokens).
		WithMachines(machineMgr).WithTerminals(nodeTerminals(nodes)).WithDevices(nodes).
		WithWorkspaces(nodes.Files, bots).WithChatFiles(newChatFiles(cfg.ChatRmCommand)).
		WithSecrets(ownerSecrets{vault: secrets, referrers: referrers}).
		WithIntegrations(integrations).WithApprovals(proxy).WithRelay(proxy).WithLarkChannels(larkSettings).
		WithSkillSources(skillsource.NewGitHub(&http.Client{Timeout: 2 * time.Minute})).
		WithSkillDirectory(skillssh.New(&http.Client{Timeout: 30 * time.Second})).
		WithBuiltinSkills(builtinSkills).
		WithRoutines(routineRunner).
		WithWeb(botWeb{meteredWeb: metered, chats: sup}).
		WithWebSearch(webSearchSettings{web: webSearch, gateway: gateway}).
		WithImages(botImages{server: gatewayServer, gateway: gateway, chats: sup, logger: logger},
			imageSettings{Service: gateway, server: gatewayServer})
	if cfg.VaultKeys != nil {
		api.WithTokenAccounts(accountTokens{svc: svc, vault: secrets})
	}
	if !cfg.BrowserOff {
		api.WithBrowser(ownersBrowser{shared})
	}

	api.WithPhone(ownersPhone{sharedPhone})

	if err := api.Register(mux); err != nil {
		return err
	}

	if assets := web.Dist(); webui.Available(assets) {
		mux.Handle("/", webui.Handler(assets))
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err)
	}

	// The profiler, when one is asked for, has its own listener and never the server's (issue #586). Like
	// Coder's, a profiler that cannot listen is logged and the control plane serves without it.
	pprofDone := make(chan struct{})

	go func() {
		defer close(pprofDone)

		if cfg.PprofAddr == "" {
			return
		}

		pln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.PprofAddr)
		if err != nil {
			logger.ErrorContext(ctx, "profiler cannot listen", slog.String("event", "controlplane.pprof.failed"),
				slog.String("addr", cfg.PprofAddr), slog.String("error", err.Error()))

			return
		}

		serveProfiler(ctx, pln, logger)
	}()

	loopDone := make(chan struct{})

	go func() {
		defer close(loopDone)

		if err := svc.Loop(ctx); err != nil {
			logger.ErrorContext(ctx, "scheduler stopped",
				slog.String("event", "controlplane.loop_failed"), slog.String("error", err.Error()))
			cancel(fmt.Errorf("scheduler: %w", err))
		}
	}()

	machinesDone := make(chan struct{})

	go func() {
		defer close(machinesDone)

		machineMgr.Loop(ctx)
	}()

	// A Dispatch's gateway key stops working when the Dispatch settles.
	keysDone := make(chan struct{})

	go func() {
		defer close(keysDone)

		workerKeys.Loop(ctx)
	}()

	// Tokens of the accounts logged in here are replaced before they end.
	refreshDone := make(chan struct{})

	go func() {
		defer close(refreshDone)

		subscriptions.Loop(ctx)
	}()

	// The subscription providers are given the models their vendors serve (issue #623).
	vendorModelsDone := make(chan struct{})

	go func() {
		defer close(vendorModelsDone)

		vendorModelsLoop(ctx, subscriptions, svc, logger)
	}()

	// Old requests and the revoked keys no request names are deleted once a day (issue #628).
	purgeDone := make(chan struct{})

	go func() {
		defer close(purgeDone)

		gateway.PurgeLoop(ctx, logger)
	}()

	supDone := make(chan struct{})

	go func() {
		defer close(supDone)

		_ = sup.Run(ctx)
	}()

	routinesDone := make(chan struct{})

	go func() {
		defer close(routinesDone)

		_ = routineRunner.Run(ctx)
	}()

	channelsDone := make(chan struct{})

	go func() {
		defer close(channelsDone)

		_ = runtime.Run(ctx)
	}()

	// Idle tabs and the idle Chrome are closed while the server runs, and Chrome when it stops.
	browserDone := make(chan struct{})

	go func() {
		defer close(browserDone)

		shared.Run(ctx)
	}()

	serveErr := platform.Serve(ctx, ln, mux, logger)

	cancel(nil)
	<-pprofDone
	<-browserDone
	<-routinesDone
	<-channelsDone
	<-loopDone
	<-machinesDone
	<-refreshDone
	<-vendorModelsDone
	<-keysDone
	<-purgeDone
	<-supDone

	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return errors.Join(serveErr, cause)
	}

	return serveErr
}

// ownersBrowser is the shared browser as the client API sees it: the owner
// watches its tabs and takes control (issue #219).
type ownersBrowser struct{ *browser.Engine }

func (b ownersBrowser) Watch(ctx context.Context, tab string) (clientapi.BrowserView, error) {
	view, err := b.Engine.Watch(ctx, tab)
	if err != nil {
		return nil, err
	}

	return view, nil
}

// ownersPhone is the phone as the client API sees it: the owner watches it
// and takes it over (issue #417, step 3).
type ownersPhone struct{ *phone.Phone }

func (p ownersPhone) Watch(ctx context.Context) (clientapi.PhoneView, error) {
	view, err := p.Phone.Watch(ctx)
	if err != nil {
		return nil, err
	}

	return view, nil
}

// nodeViews lets the phone open a view of its screen on the node that holds
// it, through the node API.
func nodeViews(nodes *nodeapi.Server) phone.Views {
	return func(ctx context.Context, nodeID string) (phone.ViewEnd, error) {
		end, err := nodes.OpenDeviceView(ctx, nodeID)
		if err != nil {
			return nil, err
		}

		return end, nil
	}
}

// nodeTerminals lets the client API reach a node for a terminal through the
// node API, which holds the nodes' connections.
func nodeTerminals(nodes *nodeapi.Server) clientapi.Terminals {
	return func(ctx context.Context, nodeID, sessionID string, rows, cols int) (clientapi.TerminalEnd, error) {
		end, err := nodes.OpenTerminal(ctx, nodeID, sessionID, rows, cols)
		if err != nil {
			return nil, err
		}

		return end, nil
	}
}

// nodeSudo reads a node's sudo password from the vault: the secret
// sudo-<node>, its value. A node with none, or with a name no secret may
// have, gets none, and its sudo asks for a terminal as it would.
func nodeSudo(secrets *vault.Service) agentapi.SudoPassword {
	return func(ctx context.Context, n orchestration.Node) (string, error) {
		opened, err := secrets.Open(ctx, NodeSudoSecret(n.Name))

		switch {
		case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotConfigured):
			return "", nil
		case err != nil:
			return "", err
		default:
			return opened.Payload[vault.KeyValue], nil
		}
	}
}

// NodeSudoSecret is the name of the secret that holds a node's sudo password.
func NodeSudoSecret(node string) string { return "sudo-" + strings.ToLower(node) }

// publishBuiltinSkills puts the skills this binary gives every agent kind
// (pdf) into the skills store as this release's revision (issue
// #492). A failure is logged and the control plane serves on, as Codex goes
// on when it cannot install its system skills (ext/skills/src/host_service.rs,
// ensure_system_skills_installed): the store keeps the last release's.
func publishBuiltinSkills(ctx context.Context, svc *orchestration.Service, logger *slog.Logger) {
	skills, err := clientcli.StoreSkills()
	if err == nil {
		var done []orchestration.BuiltinPublish

		done, err = svc.PublishBuiltinSkills(ctx, skills, orchestration.SkillBuiltin+":"+platform.Version)
		for _, d := range done {
			logger.InfoContext(ctx, "built-in skill published", slog.String("event", "controlplane.builtin_skill"),
				slog.String("skill", d.Name), slog.String("did", d.Did))
		}
	}

	if err != nil {
		logger.ErrorContext(ctx, "publishing the built-in skills failed",
			slog.String("event", "controlplane.builtin_skills_failed"), slog.String("error", err.Error()))
	}
}

// builtinSkills lists the skills this binary ships for the client API (issue #21).
func builtinSkills() ([]clientapi.BuiltinSkill, error) {
	skills, err := clientcli.BuiltinSkills()
	if err != nil {
		return nil, err
	}

	out := make([]clientapi.BuiltinSkill, len(skills))
	for i, s := range skills {
		out[i] = clientapi.BuiltinSkill{Name: s.Name, Description: s.Description}
	}

	return out, nil
}

// channelStore is what the Lark Channels keep: the Channels' own store, and
// the orchestration for the chats, questions and Bots they also use and for
// starting the chat of a Lark chat, which writes both (issue #404).
type channelStore struct {
	*orchestration.Service
	*channelstore.Store
}
