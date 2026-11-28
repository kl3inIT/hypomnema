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
	"github.com/kl3inIT/hypomnema/internal/controlplane/browsertake"
	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
	larkchannel "github.com/kl3inIT/hypomnema/internal/controlplane/channels/lark"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientcli"
	"github.com/kl3inIT/hypomnema/internal/controlplane/connproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/githubapp"
	"github.com/kl3inIT/hypomnema/internal/controlplane/inbox"
	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/meetings"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging/whatsapp"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging/zalo"
	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/controlplane/routines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/selfupdate"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillssh"
	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/controlplane/webpush"
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

// larkIntegration is the Integration of the owner's Lark sign-in.
const larkIntegration = "lark"

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
	// BackgroundCommand runs a Bot's background commands as systemd user units of the coordinator user (issue
	// #1084), with the launcher's arguments appended, from HYPOMNEMA_BACKGROUND_COMMAND as a JSON array, such as
	// [sudo -n -u coordinator /usr/local/bin/hypomnema-background]. Unset, it is the coordinator's launcher's
	// command with hypomnema-background beside hypomnema-pi, which setup-coordinator.sh installs together under one
	// sudoers rule, so an update needs no new line in the env (backgroundBeside). Empty: a background command is
	// refused, saying so.
	BackgroundCommand []string
	// Node runs the Zalo bridge (issue #657), from HYPOMNEMA_NODE; empty is the machine's Node the coordinator's
	// setup installs (zalo.DefaultNode).
	Node string
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
	// MeetingsDir holds the meetings' recordings (issue #721), from HYPOMNEMA_MEETINGS_DIR; empty: `meetings`
	// beside the database, as the browser's folder.
	MeetingsDir string
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
	// FFmpegOff installs no ffmpeg at start, from HYPOMNEMA_FFMPEG=0: for tests and a control plane that makes no
	// videos, as BrowserOff is for the browser. It is installed otherwise (issue #818, row 9).
	FFmpegOff bool
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
		MeetingsDir:      env.String(envPrefix+"_MEETINGS_DIR", ""),
		BrowserOff:       env.String(envPrefix+"_BROWSER", "1") == "0",
		BrowserNoSandbox: env.String(envPrefix+"_BROWSER_SANDBOX", "1") == "0",
		BrowserLanguage:  browser.Language(env.String(envPrefix+"_BROWSER_LANG", ""), getenv),
		BrowserTimeZone:  browser.TimeZone(env.String(envPrefix+"_BROWSER_TIMEZONE", ""), getenv),
		PprofAddr:        env.String(envPrefix+"_PPROF_ADDR", ""),
		FFmpegOff:        env.String(envPrefix+"_FFMPEG", "1") == "0",
	}

	if cfg.BrowserDir == "" {
		cfg.BrowserDir = filepath.Join(filepath.Dir(cfg.Database.Path), "browser")
	}

	if cfg.MeetingsDir == "" {
		cfg.MeetingsDir = filepath.Join(filepath.Dir(cfg.Database.Path), "meetings")
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

	if raw := env.String(envPrefix+"_BACKGROUND_COMMAND", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.BackgroundCommand); err != nil || len(cfg.BackgroundCommand) == 0 {
			cmdErr = errors.Join(cmdErr, fmt.Errorf("%s_BACKGROUND_COMMAND is not a JSON array of strings", envPrefix))
		}
	} else {
		cfg.BackgroundCommand = backgroundBeside(cfg.CoordinatorCommand)
	}

	cfg.Node = env.String(envPrefix+"_NODE", "")

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

	// The gateway keys of a deleted chat or Bot, a revoked coordinator token and a removed node end in its
	// transaction, written by the gateway (issues #405, #996).
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID,
		orchestration.WithGatewayKeys(modelgateway.Revocations{}))
	publishBuiltinSkills(ctx, svc, logger)

	// The scheduler runs while the server does; if it fails, the server stops.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	mux := http.NewServeMux()
	platform.RegisterHealth(mux, sqlite.Ready(db), logger)
	referrers := secretReferrers(svc)
	secrets := vault.New(db, platform.SystemClock{}, platform.NewID, cfg.VaultKeys, referrers)
	svc.WithShareVault(secrets.Copies())

	// The account each git host token belongs to is read at its host once, so workers commit as it (issue #991).
	tokens := connections{svc: svc, vault: secrets, identities: &gitIdentities{
		ways: &connproxy.Transports{
			Direct: connproxy.NewTransport(identityTimeout),
			New:    func() *http.Transport { return connproxy.NewTransport(identityTimeout) },
		},
		logger: logger,
	}}
	// The owner's GitHub App (issue #1262): its installations' tokens stand in for the owner's on the repositories
	// it is installed on, and its events end the waits for CI.
	app := githubapp.New(svc, secrets, cfg.PublicURL, &http.Client{Timeout: 30 * time.Second}, logger)
	tokens.app = app
	// GitHub's deliveries, signed with the App's secret, which is their only credential.
	mux.HandleFunc("POST "+githubapp.EventsPath, app.ServeEvents)
	go tokens.readIdentities(ctx)
	sshKey := &gitSSHKey{vault: secrets}
	// Web Push (issue #1047): what waits on the owner is pushed to his devices, signed with a key kept in the vault.
	pusher := webpush.New(webpush.Config{
		Store: svc, Keys: webpush.NewKeys(secrets), PublicURL: cfg.PublicURL, Logger: logger,
	})
	integrations := integration.New(svc, secrets, platform.SystemClock{}, platform.NewID,
		integration.Google(), integration.Lark(), integration.Superset(), integration.AWS(), integration.WhatsApp(),
		integration.Zalo())

	// The owner's WhatsApp, linked as a device of his (issue #615): its sessions are whatsmeow's, in a database
	// of their own beside this one, and are opened again as the control plane starts.
	wa, err := whatsapp.Open(ctx, filepath.Join(filepath.Dir(cfg.Database.Path), "whatsapp.db"), logger)
	if err != nil {
		return err
	}
	defer wa.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

	integrations.WithDevices(integration.WhatsAppID, wa)

	// The owner's Zalo (issue #657): zca-js in a bridge run with the machine's Node, its session and chats in
	// zalo.db. A control plane that cannot run the bridge links no Zalo account, and says why.
	if zl, err := zalo.Open(ctx, filepath.Join(filepath.Dir(cfg.Database.Path), "zalo.db"), cfg.Node, logger); err != nil {
		logger.WarnContext(ctx, "Zalo accounts cannot be linked", slog.String("error", err.Error()))
	} else {
		defer zl.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

		integrations.WithDevices(integration.ZaloID, zl)
	}

	if err := integrations.OpenDevices(ctx); err != nil {
		logger.WarnContext(ctx, "a linked account's session could not be opened", slog.String("error", err.Error()))
	}
	defer integrations.CloseDevices()
	// What each account can really do is read at its service and said action by action from the proxy's
	// catalog (issue #567): on connect, on sign-in again, on the owner's check, and after a 403.
	checkAccess := func(ctx context.Context, id, label string) error {
		// A git host's Connection is a token the owner typed in: there is nothing to read at the service.
		if !integrations.Has(id) {
			return nil
		}

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
		gatewaySecrets(secrets)).WithKeyVault(secrets).WithProviderList(gatewayProviderList(svc)).
		WithServedLimits(gatewayServedLimits(subscriptions)).WithServedWire(gatewayServedWire(subscriptions))
	workerKeys := dispatchKeys{gateway: gateway, svc: svc, logger: logger}

	nodes := nodeapi.New(svc, logger).WithHostTokens(tokens.Credential).WithGitHubApp(app.OfferIdentity).
		WithModelKeys(workerKeys.Key).
		WithModelSizes(workerKeys.Sizes).
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
	// A worker's offer names the tools it gets, for the "Tools" block of its rules (issue #723).
	nodes.WithToolNames(agents.WorkerTools)

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

	// The owner's Muse accounts, a pool any agent asks (issue #782), in the shared browser's Chrome.
	musePool := openMuse(ctx, cfg, svc, secrets, shared, logger)
	if musePool != nil {
		defer musePool.Close() //nolint:errcheck // Closing at shutdown; nothing is left to report it to.

		agents.WithMuse(musePool)
	}

	// The web's search and pages (issue #354). A page is read only where the shared browser may go: the same
	// rules, the owner's list of private destinations and the control plane's own port.
	searchRules := egress.Rules{Allow: cfg.BrowserAllow, OwnPort: func(port uint16) bool {
		return slices.Contains(ownPorts, port)
	}}
	webSearch := websearch.New(websearch.Config{
		// What the owner set on the Web search page and the keys in the vault, read at each search.
		Keys: searchKeys(secrets, logger), Settings: gateway.SearchSettings, Usage: gateway,
		Logger: logger,
		Rules:  searchRules,
	})
	metered := modelgateway.MeteredWeb{Web: webSearch, Gateway: gateway, Logger: logger}

	// Speech-to-text and text-to-speech (issue #694), with the owner's keys in the vault and what the owner set on
	// the Audio page, read at each call.
	voice := speech.New(speech.Config{Keys: speechKeys(secrets, logger), Settings: gateway.AudioSettings, Logger: logger})
	agents.WithRuntimes(dispatchRuntime(svc))

	// The phone a node holds (issue #417), given to every worker as the browser is. A call that finds its
	// machine stopped starts it as `machines start` does.
	// The takes agents film of it (issue #817) are kept in the data folder.
	phoneTakes, err := takes.Open(filepath.Join(filepath.Dir(cfg.Database.Path), "takes"))
	if err != nil {
		return err
	}

	// The ffmpeg videos are made with (issue #818, row 9): a pinned build installed beside the database, in the
	// background so that serving does not wait for it.
	ffmpegOff := errors.New("ffmpeg is off on this control plane (HYPOMNEMA_FFMPEG=0)")
	ffmpegTools := func(context.Context) (ffmpeg.Tools, error) { return ffmpeg.Tools{}, ffmpegOff }
	ffmpegReady := func() (ffmpeg.Tools, error) { return ffmpeg.Tools{}, ffmpegOff }

	if !cfg.FFmpegOff {
		installer := ffmpeg.New(ffmpeg.Config{Dir: filepath.Join(filepath.Dir(cfg.Database.Path), "ffmpeg"), Logger: logger})
		installer.Start(ctx)
		ffmpegTools, ffmpegReady = installer.Ensure, installer.Ready
	}

	// The videos made of the takes (issue #818, rows 1, 2, 5 and 6): rows beside the others, files in videos/
	// beside the database, one runner making them one at a time.
	// New sweeps the music folder once, before anything is served; no request's context is there yet.
	videoSvc, err := videos.New(videos.Config{ //nolint:contextcheck // As above.
		DB: db, Dir: filepath.Join(filepath.Dir(cfg.Database.Path), "videos"), Takes: phoneTakes,
		Speech:   modelgateway.VideoAudio{Speech: voice, Gateway: gateway, BotOf: chatBot(svc), Logger: logger},
		Settings: gateway.AudioSettings, Tools: ffmpegTools, Ready: ffmpegReady, Logger: logger,
	})
	if err != nil {
		return err
	}

	sharedPhone := phone.New(phone.Config{
		Nodes: svc, Link: nodes, Machines: machineMgr, Views: nodeViews(nodes), Logger: logger,
		Takes: phoneTakes, WorkAlive: workAlive(svc, logger),
	})
	agents.WithDevice(sharedPhone)
	agents.WithVideos(videoSvc, phoneTakes)

	// The takes agents film of their tabs of the shared browser (issue #1189), kept with the phone's and encoded
	// with the videos' ffmpeg.
	browserTakes := browsertake.New(browsertake.Config{
		Browser: browsertake.Engine(shared), Takes: phoneTakes, Tools: ffmpegTools, WorkAlive: workAlive(svc, logger),
		Logger: logger,
	})
	if !cfg.BrowserOff {
		agents.WithBrowserTakes(browserTakes)

		go browserTakes.Run(ctx)
	}

	// An agent's commands on a node's machine (issue #543), sudo given the node's password from the vault.
	// A command on the phone's node starts its machine stopped for idleness, as a phone call does (issue #1083).
	agents.WithNodeExec(svc, nodes, nodeSudo(secrets)).WithNodeUse(sharedPhone)

	// The owner's hold on the phone ends as the browser's does (issue #417, step 3).
	go sharedPhone.Run(ctx)
	nodes.WithPhone(sharedPhone.Held)
	nodes.WithTakes(phoneTakes, sharedPhone.NodeTakes)

	agents.Register(mux)

	// The service proxy: an agent's tool reaches a connected service through it and never holds the token.
	proxy, err := serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: svc, Logger: logger,
		Token: func(ctx context.Context, id, label string) (string, error) {
			// A git host's Connection (github.com, issue #685) keeps its token with the git hosts.
			if !integrations.Has(id) {
				return tokens.Of(ctx, id, label)
			}

			token, err := integrations.Token(ctx, id, label)

			return token.AccessToken, err
		},
		// A Bot's gh on a repository the owner's GitHub App is installed on goes with the App's token (issue #1262).
		RepoToken: func(ctx context.Context, id, repo string) (string, bool) {
			token, ok, err := app.TokenFor(ctx, id, repo)

			return token.Value, err == nil && ok
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
	agents.WithMessaging(integrations).WithJudge(proxy)
	// The owner's own Lark sign-in, by its label.
	larkToken := func(ctx context.Context, label string) (string, error) {
		if !integrations.Has(larkIntegration) {
			return tokens.Of(ctx, larkIntegration, label)
		}

		token, err := integrations.Token(ctx, larkIntegration, label)

		return token.AccessToken, err
	}
	// The Inbox checks the message the owner sent that a Bot gives as evidence that he answered (issue #1004).
	svc.Inbox().WithSent(inbox.NewSent(inbox.Config{
		Messages: integrations.Messages, Members: integrations.Members, LarkToken: larkToken,
		LarkURL: "https://" + serviceproxy.LarkHost, Client: &http.Client{Timeout: 30 * time.Second},
	}))
	// The owner sends a reply from the Inbox through his own accounts (issue #1111), tagging whom it names (#1200).
	ownerReplies := inbox.NewSender(inbox.Config{
		Send: integrations.Send, Messages: integrations.Messages, Members: integrations.Members, LarkToken: larkToken,
		GoogleToken: func(ctx context.Context, label string) (string, error) {
			token, err := integrations.Token(ctx, integration.GoogleID, label)

			return token.AccessToken, err
		},
		LarkURL: "https://" + serviceproxy.LarkHost, GmailURL: "https://gmail.googleapis.com",
		Client: &http.Client{Timeout: 30 * time.Second}, Log: logger,
	})
	// The owner's chats with whether the secretary reads each (issue #1270).
	secretaryChats := inbox.NewChats(inbox.Config{
		Chats: integrations.Chats, ChatReads: svc.Inbox().ChatReads, LarkToken: larkToken,
		LarkURL: "https://" + serviceproxy.LarkHost, Client: &http.Client{Timeout: 30 * time.Second},
	})
	gatewayServer := modelgateway.NewServer(gateway, logger, modelgateway.Config{
		ChatFallbacks: svc.ChatFallbacks,
		Subscriptions: gatewayAccounts{subs: subscriptions}, CodexBaseURL: cfg.CodexBaseURL,
		ClaudeBaseURL:      cfg.ClaudeBaseURL,
		AntigravityBaseURL: cfg.AntigravityBaseURL,
		Web:                webSearch,
		Speech:             voice,
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
		Command: cfg.CoordinatorCommand, Keys: chatKeys{gateway: gateway, chats: svc, coordinator: cfg.CoordinatorToken},
		Tokens: botTokens{svc: svc, coordinator: cfg.CoordinatorToken}, Logger: logger,
		Updating:   func() bool { return selfupdate.Updating(filepath.Dir(cfg.Database.Path)) },
		Background: cfg.BackgroundCommand,
	})
	agents.WithBackground(sup)

	// Every worker's and every Bot's pi's web tools, generate_image and speech tools (issues #354, #492, #723), each
	// call recorded on its Dispatch's key or on the key of the chat's running pi; before anything is served.
	agentsWeb := agentWeb{
		worker: modelgateway.WorkerWeb{MeteredWeb: metered}, bot: modelgateway.BotWeb{MeteredWeb: metered, Chats: sup},
	}
	agents.WithWeb(agentsWeb).
		// Social listening (issues #1287, #1289): keyless sources, its web searches on the caller's key, the pages a
		// search names read through the web fetch's address check.
		WithListening(agentListening{svc: listening.New(listening.Config{
			Platforms: listening.Sources(listening.Endpoints{Pages: websearch.PageClient(searchRules)}, http.DefaultClient),
			Logger:    logger,
		}), web: agentsWeb}).
		WithImages(agentImages{
			worker: modelgateway.WorkerImages{Server: gatewayServer, Gateway: gateway, Logger: logger},
			bot:    modelgateway.BotImages{Server: gatewayServer, Gateway: gateway, Chats: sup, Logger: logger},
		}).
		WithAudio(agentAudio{
			worker: modelgateway.WorkerAudio{Speech: voice, Gateway: gateway, Logger: logger},
			bot:    modelgateway.BotAudio{Speech: voice, Gateway: gateway, Chats: sup, Logger: logger},
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

	runtime := channels.New(channelData, sup, registry, secrets, logger).WithWebURL(cfg.PublicURL).
		WithHearer(modelgateway.ChannelVoice{Speech: voice, Gateway: gateway, BotOf: chatBot(svc), Logger: logger})
	if bots != nil {
		runtime.WithFiles(bots)
	}

	larkSettings := larkchannel.NewService(channels.NewService(channelData, secrets, runtime))

	// A node still out of disk after trimming its caches, and its recovery, are told to the owner as a failing
	// Routine is (issue #1017).
	nodes.WithDiskAlerts(tellDiskAlert(svc, runtime, logger))

	// The Routines of the Bots (issue #211): the tick, and the runs it starts.
	routineRunner := routines.New(svc, svc.Routines(), routines.Config{
		Command: cfg.RoutineCommand, Coordinator: sup, Owner: runtime, Logger: logger,
		Tokens: botTokens{svc: svc, coordinator: cfg.CoordinatorToken},
	})
	// A Bot's pi runs its own Routines with its routines tool, through the same runner (issue #536).
	agents.WithRoutines(routineRunner)
	agents.WithGitSSHKey(sshKey.Pair)
	agents.WithGitHubApp(app)

	// The meetings (issue #721): a recording heard by the gateway's speech service and written up by the owner's
	// notes model, in the background, both counted on the meetings' gateway key.
	meetingWork, err := meetings.New(meetings.Config{
		Dir: cfg.MeetingsDir, Store: svc.Meetings(), Speech: voice, Models: gatewayServer, Usage: gateway,
		Wake: svc.Subscribe, Logger: logger,
	})
	if err != nil {
		return err
	}

	// A Bot reads them with its meetings' tools, to add their actions to the owner's Inbox (issue #1023).
	agents.WithMeetings(meetingWork)

	// A file a Bot's Inbox comment names is its work only when its folder holds it (issue #1206).
	if bots != nil {
		agents.WithBotFiles(bots)
	}

	api := clientapi.New(svc, logger, ctx.Done()).WithCoordinator(sup).WithRunCoordinator(cfg.CoordinatorToken).
		WithAccounts(subscriptions).WithGatewayModels(gateway).WithGatewayKeys(gateway).WithGatewayUsage(gateway).
		WithHostTokens(tokens).WithGitSSHKey(sshKey).WithGitHubApp(clientGitHubApp{app}, cfg.PublicURL).
		WithMachines(machineMgr).WithTerminals(nodeTerminals(nodes)).WithDevices(nodes).
		WithWorkspaces(nodes.Files, bots).WithChatFiles(newChatFiles(cfg.ChatRmCommand)).
		WithSecrets(ownerSecrets{vault: secrets, referrers: referrers}).
		WithIntegrations(integrations).WithMessaging(integrations).WithInboxSender(ownerReplies).
		WithInboxChats(secretaryChats).
		WithApprovals(proxy).WithRelay(proxy).WithLarkChannels(larkSettings).
		WithSkillSources(skillsource.NewGitHub(&http.Client{Timeout: 2 * time.Minute})).
		WithSkillDirectory(skillssh.New(&http.Client{Timeout: 30 * time.Second})).
		WithBuiltinSkills(builtinSkills).
		WithRoutines(routineRunner).
		WithMeetings(meetingWork).
		WithWeb(modelgateway.BotWeb{MeteredWeb: metered, Chats: sup}).
		WithWebSearch(webSearchSettings{web: webSearch, gateway: gateway}).
		WithImages(modelgateway.BotImages{Server: gatewayServer, Gateway: gateway, Chats: sup, Logger: logger},
			imageSettings{Service: gateway, server: gatewayServer}).
		WithAudio(modelgateway.BotAudio{Speech: voice, Gateway: gateway, Chats: sup, Logger: logger},
			audioSettings{Service: gateway, speech: voice}).
		WithBotTools(agents.BotTools).WithToolUnavailability(agents.ToolUnavailability)
	if cfg.VaultKeys != nil {
		api.WithTokenAccounts(accountTokens{svc: svc, vault: secrets})
		api.WithPusher(pusher)
	}
	if !cfg.BrowserOff {
		api.WithBrowser(ownersBrowser{shared})
	}

	api.WithPhone(ownersPhone{sharedPhone})
	api.WithTakes(ownerTakes{Store: phoneTakes, phone: sharedPhone, browser: browserTakes, videos: videoSvc})
	api.WithVideos(videoSvc)

	if musePool != nil {
		api.WithMuse(musePool)
	}

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

	videosDone := make(chan struct{})

	go func() {
		defer close(videosDone)

		videoSvc.Run(ctx)
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

	// The subscription providers are given the models their vendors serve (issue #623), and again as soon as an
	// account's first list of models is read (issue #814).
	vendorModelsDone := make(chan struct{})
	vendor := subscription.NewVendorModels(subscriptions, svc.Profiles(), logger)

	subscriptions.OnFirstList(func(string) { vendor.Update(ctx) })

	go func() {
		defer close(vendorModelsDone)

		vendor.Loop(ctx)
	}()

	// Old requests and the revoked keys no request names are deleted once a day (issue #628).
	purgeDone := make(chan struct{})

	go func() {
		defer close(purgeDone)

		gateway.PurgeLoop(ctx, logger)
	}()

	// The openings of share links are written together once a minute, and a last time once the server has stopped
	// and answered its last request (issue #996).
	shareViewsCtx, stopShareViews := context.WithCancel(context.WithoutCancel(ctx))
	defer stopShareViews()

	shareViewsDone := make(chan struct{})

	go func() {
		defer close(shareViewsDone)

		writeShareViews(shareViewsCtx, svc, logger, shareViewsWriteEvery)
	}()

	// The pull requests that succeeded Tasks left with auto-merge on are followed until they merge (issue #1053).
	pullRequestsDone := make(chan struct{})

	go func() {
		defer close(pullRequestsDone)

		followPullRequests(ctx, svc, tokens, logger, cfg.PublicURL, orchestration.LookUpEvery,
			followPullRequestsEvery)
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

	// The meetings' recordings are heard and their notes written; a step the last stop interrupted is taken up again.
	meetingsDone := make(chan struct{})

	go func() {
		defer close(meetingsDone)

		meetingWork.Run(ctx)
	}()

	// Idle tabs and the idle Chrome are closed while the server runs, and Chrome when it stops.
	browserDone := make(chan struct{})

	go func() {
		defer close(browserDone)

		shared.Run(ctx)
	}()

	// What waits on the owner is pushed to his devices once its transaction commits (issue #1047).
	pushDone := make(chan struct{})

	go func() {
		defer close(pushDone)

		pusher.Run(ctx)
	}()

	// `hypomnema update` asks which Bots' turns and how many tool calls run before it stops the control plane (issues
	// #716, #1060).
	controlDone := make(chan struct{})

	go func() {
		defer close(controlDone)

		serveControl(ctx, controlSocket(cfg.Database.Path), sup.WorkingChats, agents.ToolCalls, sup.BeginUpdate, logger)
	}()

	serveErr := platform.Serve(ctx, ln, mux, logger)

	stopShareViews()
	cancel(nil)
	<-controlDone
	<-pushDone
	<-pprofDone
	<-browserDone
	<-meetingsDone
	<-routinesDone
	<-channelsDone
	<-loopDone
	<-machinesDone
	<-videosDone
	<-refreshDone
	<-vendorModelsDone
	<-keysDone
	<-purgeDone
	<-supDone
	<-shareViewsDone
	<-pullRequestsDone

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

// workAlive says whether the work a take was started for goes on (issue
// #817, row 7): a worker's Dispatch until it is settled or its worker exited
// without a report (ReportFinished, ReportExited), a Bot's turn while its
// chat's turn mark is the same (agent_settled, a cut turn and a pi that
// exited clear it). Any other agent's work goes on; its take ends at its
// stop or the phone's end. A store that cannot be read keeps the take.
func workAlive(svc *orchestration.Service, logger *slog.Logger) func(ctx context.Context, work string) bool {
	return func(ctx context.Context, work string) bool {
		switch {
		case strings.HasPrefix(work, agentapi.WorkDispatch):
			d, err := svc.GetDispatch(ctx, strings.TrimPrefix(work, agentapi.WorkDispatch))
			if errors.Is(err, orchestration.ErrNotFound) {
				return false
			}

			if err != nil {
				logger.WarnContext(ctx, "a take's Dispatch could not be read", slog.String("error", err.Error()))

				return true
			}

			return !d.Settled() && d.Status != orchestration.DispatchUnknown &&
				d.Status != orchestration.DispatchStopUnknown
		case strings.HasPrefix(work, agentapi.WorkTurn):
			chat, turn, _ := strings.Cut(strings.TrimPrefix(work, agentapi.WorkTurn), " ")

			now, err := svc.ChatTurn(ctx, chat)
			if err != nil {
				return !errors.Is(err, orchestration.ErrNotFound)
			}

			return turn != "" && now == turn
		default:
			return true
		}
	}
}

// ownerTakes are the takes as the owner reads and deletes them: a take still
// filmed is ended on its node, or in the browser (issue #1189), before its files go, and a take a video is
// being made of is not deleted (issue #818, row 2).
type ownerTakes struct {
	*takes.Store
	phone   *phone.Phone
	browser *browsertake.Recorder
	videos  *videos.Service
}

func (o ownerTakes) Delete(ctx context.Context, id string) error {
	busy, err := o.videos.TakeInWork(ctx, id)
	if err != nil {
		return err
	}

	if busy {
		return fmt.Errorf("%w: a video of take %s is being made; delete the take once it is made",
			orchestration.ErrConflict, id)
	}

	o.phone.EndTake(ctx, id)
	o.browser.End(ctx, id)

	return o.Store.Delete(id)
}

// Thumbnail is an available take's first frame, made with the videos' ffmpeg (issue #818, row 12).
func (o ownerTakes) Thumbnail(ctx context.Context, id string) (string, error) {
	return o.Store.Thumbnail(ctx, id, o.videos.FirstFrame)
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

// shareViewsWriteEvery is how often the openings of share links counted in memory are written (issue #996), and
// shareViewsLastWrite how long the last write may take as the control plane stops.
const (
	shareViewsWriteEvery = time.Minute
	shareViewsLastWrite  = 15 * time.Second
)

// writeShareViews writes the openings of share links every interval, and once more when ctx ends, so none waits for
// a later opening and the stop loses none, as Coder's stats batcher flushes on its ticker and as it exits
// (coderd/workspacestats/batcher.go).
func writeShareViews(ctx context.Context, svc *orchestration.Service, logger *slog.Logger, every time.Duration) {
	write := func(ctx context.Context) {
		if err := svc.WriteShareViews(ctx); err != nil {
			logger.WarnContext(ctx, "writing the views of share links",
				slog.String("event", "controlplane.share_views_failed"), slog.String("error", err.Error()))
		}
	}

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			last, cancel := context.WithTimeout(context.WithoutCancel(ctx), shareViewsLastWrite)
			write(last)
			cancel()

			return
		case <-tick.C:
			write(ctx)
		}
	}
}

// backgroundBeside is the background launcher's command for the coordinator's launcher's: the same command, such as
// sudo -n -u coordinator, with /usr/local/bin/hypomnema-background in place of /usr/local/bin/hypomnema-pi, the two
// launchers setup-coordinator.sh installs side by side and lets the control plane's user run (issue #1084). Nil when
// the coordinator's command is not that launcher, as in tests that play pi.
func backgroundBeside(coordinator []string) []string {
	if len(coordinator) == 0 || filepath.Base(coordinator[len(coordinator)-1]) != "hypomnema-pi" {
		return nil
	}

	out := append([]string{}, coordinator...)
	out[len(out)-1] = filepath.Join(filepath.Dir(coordinator[len(coordinator)-1]), "hypomnema-background")

	return out
}
