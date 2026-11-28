package clientcli

import (
	"flag"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

// commands is every command this package runs. The coordinator skill names
// only these, with only these flags (TestSkillCommandsParse).
var commands = []command{
	{
		name: "login", summary: "check a client token and keep it with the control plane's URL", offline: true,
		flags: func(fs *flag.FlagSet) {
			fs.String("url", "", "the control plane, http(s)://host[:port]")
			fs.String("token-file", "", "a file holding the token from `hypomnema tokens add`")
		},
		run: login,
	},
	{
		name: "whoami", summary: "the token this machine is logged in with", flags: jsonFlag, run: whoami,
	},
	{
		name: "run", args: "<objective>", min: 1, max: -1,
		summary: "create a Run with one Task and follow it until the Task ends",
		flags: func(fs *flag.FlagSet) {
			fs.String("repo", "", "repository the work happens in: a registered name, owner/name, or its URL")
			fs.String("base", "", "branch the worktree starts from (default main)")
			fs.String("chat", "", "the chat to wake (default: this Bot's chat or the main Bot's main chat)")
			fs.String("profile", "", "profile of the Task (default implementer)")
			fs.String("runtime", "", "runtime, instead of the profile's")
			fs.String("model", "", "model as <provider>/<model>, such as opencode-go/deepseek-v4-pro, instead of "+
				"the profile's")
			fs.Bool("detach", false, "create the Run and return without following it")
			fs.String("use", "", "accounts the workers may use, as integration:label separated by commas, such as google:work")
		},
		run: ownerRun,
	},
	{
		name: "runs create", args: "[objective]", max: -1, summary: "create a Run with a chat to continue it",
		flags: func(fs *flag.FlagSet) {
			fs.String("repo", "", "repository the work happens in: a registered name, owner/name, or its URL")
			fs.String("base", "", "branch the worktrees start from (default main)")
			fs.String("chat", "", "the chat to wake (default: this Bot's chat or the main Bot's main chat)")
			fs.String("objective-file", "", "read the objective from this file, or - for standard input")
			fs.String("key", "", "idempotency key; repeat a create whose result was lost with the same key")
			fs.String("use", "", "accounts the workers may use, as integration:label separated by commas, such as google:work")
			jsonFlag(fs)
		},
		run: runsCreate,
	},
	{
		name: "runs adopt", args: "<run_id>", min: 1, max: 1,
		summary: "assign an open Run without a chat to a coordinator (owner only)",
		flags: func(fs *flag.FlagSet) {
			fs.String("chat", "", "the chat to wake (default: the main Bot's main chat)")
			jsonFlag(fs)
		},
		run: runsAdopt,
	},
	{
		name: "runs list", summary: "Runs, newest first",
		flags: func(fs *flag.FlagSet) {
			fs.String("status", "", "open, finished or canceled")
			fs.String("chat", "", "only the Runs made from this chat")
			fs.Int("limit", 20, "how many")
			jsonFlag(fs)
		},
		run: runsList,
	},
	{
		name: "runs show", args: "<run_id>", min: 1, max: 1,
		summary: "a Run with its Tasks and each Task's latest Dispatch", flags: jsonFlag, run: runsShow,
	},
	{
		name: "runs finish", args: "<run_id>", min: 1, max: 1, summary: "close a Run whose Tasks have all ended",
		flags: jsonFlag, run: runsFinish,
	},
	{
		name: "runs cancel", args: "<run_id>", min: 1, max: 1,
		summary: "cancel a Run and stop its unfinished Tasks", flags: jsonFlag, run: runsCancel,
	},
	{
		name: "runs wait", args: "<run_id>", min: 1, max: 1,
		summary: "wait until something in the Run needs its coordinator, then print it and the next step",
		flags: func(fs *flag.FlagSet) {
			fs.Int64("after", 0, "the after value the previous wait printed")
			fs.Duration("timeout", 0, "give up after this long (0: wait until something happens)")
			jsonFlag(fs)
		},
		run: runsWait,
	},
	{
		name: "tasks create", args: "<run_id>", min: 1, max: 1,
		summary: "add Tasks to a Run, from a file of {\"tasks\": [...]} or one Task from flags",
		flags: func(fs *flag.FlagSet) {
			fs.String("file", "", `a JSON file {"tasks": [...]}, or - for standard input`)
			fs.String("ref", "task", "the Task's name within this request")
			fs.String("profile", "", "the Task's profile (default implementer)")
			fs.String("spec", "", "the Task's spec")
			fs.String("spec-file", "", "read the spec from this file, or - for standard input")
			fs.String("depends-on", "", "comma-separated refs or Task ids it depends on")
			fs.String("kind", "", "broad (default), narrow or quick")
			fs.String("review-of", "", "id of the succeeded Task whose work this reviewer checks")
			fs.String("continues", "", "id of a Task whose branch and pull request this Task continues")
			fs.String("start-from", "", "branch the worktree starts from instead of the Run's base")
			fs.String("pr-base", "", "with --start-from: start_from (default, stacked) or run_base")
			fs.String("runtime", "", "runtime, instead of the profile's")
			fs.String("model", "", "model as <provider>/<model>, such as opencode-go/deepseek-v4-pro, instead of "+
				"the profile's")
			fs.String("effort", "", "effort, instead of the profile's")
			fs.String("key", "", "idempotency key; repeat a create whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: tasksCreate,
	},
	{
		name: "tasks list", summary: "the Tasks in flight, as the board shows them: every open Run's, and those just ended",
		flags: func(fs *flag.FlagSet) {
			fs.Duration("since", 0, "how far back ended Tasks of other Runs go (default 24h)")
			fs.Int("limit", 0, "how many (default 200)")
			jsonFlag(fs)
		},
		run: tasksList,
	},
	{
		name: "tasks show", args: "<task_id>", min: 1, max: 1, summary: "a Task with its latest Dispatch and result",
		flags: jsonFlag, run: tasksShow,
	},
	{
		name: "tasks transcript", args: "<task_id>", min: 1, max: 1,
		summary: "what the Task's worker said and did in its latest attempt",
		flags: func(fs *flag.FlagSet) {
			fs.Int64("after", 0, "only the items after this seq")
			fs.Bool("follow", false, "keep printing items as the worker goes on, until it ends")
			jsonFlag(fs)
		},
		run: tasksTranscript,
	},
	{
		name: "tasks cancel", args: "<task_id>", min: 1, max: 1,
		summary: "cancel a Task; a running worker is stopped first", flags: jsonFlag, run: tasksCancel,
	},
	{
		name: "tasks reopen", args: "<task_id>", min: 1, max: 1,
		summary: "give a failed Task, or one waiting for a login, a new start", flags: jsonFlag, run: tasksReopen,
	},
	{
		name: "tasks follow-up", args: "<task_id> [note]", min: 1, max: -1,
		summary: "ask more of a succeeded Task's worker; it continues its own conversation, branch and pull request",
		flags: func(fs *flag.FlagSet) {
			fs.String("note-file", "", "read the note from this file, or - for standard input")
			jsonFlag(fs)
		},
		run: tasksFollowUp,
	},
	{
		name: "questions", summary: "open questions from workers",
		flags: func(fs *flag.FlagSet) {
			fs.String("to", "owner", "owner or coordinator")
			fs.String("run", "", "only this Run's")
			fs.String("chat", "", "only those of the Runs made from this chat")
			fs.String("status", "open", "open, answered or canceled")
			jsonFlag(fs)
		},
		run: questionsList,
	},
	{
		name: "questions show", args: "<question_id>", min: 1, max: 1, summary: "one question with its options",
		flags: jsonFlag, run: questionsShow,
	},
	{
		name: "questions forward", args: "<question_id>", min: 1, max: 1,
		summary: "pass a question asked of the coordinator to the owner", flags: jsonFlag, run: questionsForward,
	},
	{
		name: "answer", args: "<question_id> [answer]", min: 1, max: -1,
		summary: "answer a question: an option's number or label, or text",
		flags: func(fs *flag.FlagSet) {
			fs.String("text-file", "", "read the answer from this file, or - for standard input")
			jsonFlag(fs)
		},
		run: answer,
	},
	{
		name: "chat", args: "[message]", max: -1,
		summary: "write to a Bot's main chat on the server (the main Bot unless --bot) and print its replies; " +
			"without a message, show the chat",
		flags: func(fs *flag.FlagSet) {
			fs.String("chat", "", "the chat to use (default the Bot's main chat)")
			fs.String("bot", "", "the Bot to write to, by its id from bots list (default the main Bot)")
			fs.Bool("new", false, "start a new chat, which becomes the Bot's main chat (as the message /new)")
			fs.String("text-file", "", "read the message from this file, or - for standard input")
			fs.String("timeout", "10m", "stop printing replies after this long")
			fs.Int("last", 20, "without a message, how many messages to show")
		},
		run: chat,
	},
	{
		name: "chats", summary: "the chats with the coordinator, the most recently active first",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "only the chats of this Bot, by its id from bots list")
			fs.Int("offset", 0, "skip this many chats, for the next page; the first page also lists every Bot's "+
				"main chat and every chat an approval waits in")
			jsonFlag(fs)
		},
		run: chatsList,
	},
	{
		name: "chats rm", args: "<chat>", min: 1, max: 1,
		summary: "delete a chat for good, with its messages and its files; runs started in it stay (owner)",
		flags:   jsonFlag, run: chatsRemove,
	},
	{
		name: "chats main", args: "<chat>", min: 1, max: 1,
		summary: "make a chat its Bot's main chat, the one `hypomnema chat` and a click on the Bot open; " +
			"not a Routine's or a Lark chat (owner)",
		flags: jsonFlag, run: chatsMain,
	},
	{
		name: "hosts list", summary: "the git hosts a Run can be on", flags: jsonFlag, run: hostsList,
	},
	{
		name: "hosts add", args: "<host>", min: 1, max: 1,
		summary: "declare a company's git server, by its name or URL (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("kind", "gitlab", "github or gitlab")
			jsonFlag(fs)
		},
		run: hostsAdd,
	},
	{
		name: "hosts app", summary: "the owner's GitHub App: whether it is registered, the repositories it is " +
			"installed on and the last delivery it was sent; it is registered in the browser at " +
			"/api/v1/integrations/github/app/setup (owner)",
		flags: jsonFlag, run: hostsApp,
	},
	{
		name: "hosts app sync", summary: "read again from GitHub the repositories the GitHub App is installed on (owner)",
		flags: jsonFlag, run: hostsAppSync,
	},
	{
		name: "hosts app test", summary: "ask GitHub to send the GitHub App's latest delivery again, as a test (owner)",
		flags: jsonFlag, run: hostsAppTest,
	},
	{
		name: "ci wait", args: "<pr>", min: 1, max: 1,
		summary: "wait for a pull request's required checks to end, on the GitHub App's events, without calling " +
			"GitHub; exits 0 when they passed or it merged, 1 when one failed or it closed, 8 when still pending at " +
			"--timeout; a repository the App is not installed on is waited for with `gh pr checks --watch`",
		flags: func(fs *flag.FlagSet) {
			fs.Duration("timeout", 2*time.Hour, "give up after this long (0: no limit)")
		},
		run: ciWait,
	},
	{
		name: "hosts rm", args: "<host>", min: 1, max: 1,
		summary: "forget a declared host and its token; no open Run or registered repository may be on it (owner)",
		flags:   jsonFlag, run: hostsRemove,
	},
	{
		name: "integrations list", summary: "the services Hypomnema works with and the accounts connected to each",
		flags: jsonFlag, run: integrationsList,
	},
	{
		name: "integrations connect", args: "<integration>", min: 1, max: 1,
		summary: "connect an account under a label: a git host with a token read from a file, a service such as " +
			"google by signing in, an app of lark with its app_id and app_secret read from a file, yourself at lark " +
			"through one of its apps (--as-user --app work), superset with its address, a user name and a password " +
			"read from a file, a role of an aws account with its ARN and the key that assumes it (owner)",
		// A sign-in as yourself waits until the code was entered.
		long: true,
		flags: func(fs *flag.FlagSet) {
			fs.String("label", "default", "the name of this account, such as work")
			fs.String("token-file", "", "for a git host: the file holding the token, or - for standard input")
			fs.String("app-file", "", "for an app such as lark: a JSON file with its app_id and app_secret, or - "+
				"for standard input; without it, a label that exists is checked again")
			fs.Bool("as-user", false, "for lark: sign in as yourself, by a code entered on any device; the "+
				"account acts as you, for a Bot given it, on your own turn only")
			fs.String("app", "", "with --as-user: the label of the app to sign in through, such as work")
			fs.String("services", "", "for a service that signs in: what to connect, such as gmail:read,calendar "+
				"(:read is read only)")
			fs.String("url", "", "for superset: its address, such as https://superset.example.com")
			fs.String("proxy", "", "for superset: the proxy its address is reached through, such as "+
				"socks5://127.0.0.1:1055 (default: the one the label has, or none)")
			fs.String("dial-address", "", "for superset: an address to dial instead of its host's, as curl's "+
				"--resolve; temporary (default: the one the label has, or none)")
			fs.String("user", "", "for superset: the account's user name")
			fs.String("password-file", "", "for superset: the file holding the password, or - for standard input")
			fs.String("role", "", "for aws: the role's ARN, such as arn:aws:iam::123456789012:role/hypomnema/read")
			fs.String("key", "main", "for aws: the name of the key that assumes the role")
			fs.String("key-file", "", "for aws, when the key is new or replaced: the access key file the AWS "+
				"console gives (.csv), or a file with ACCESS_KEY_ID:SECRET_ACCESS_KEY, or - for standard input")
			jsonFlag(fs)
		},
		run: integrationsConnect,
	},
	{
		name: "superset dashboards", summary: "the dashboards of a Superset account, by title", long: true,
		flags: func(fs *flag.FlagSet) {
			fs.String("search", "", "only the dashboards whose title contains this")
			supersetFlags(fs)
			jsonFlag(fs)
		},
		run: supersetDashboards,
	},
	{
		name: "superset dashboard", args: "<dashboard>", min: 1, max: 1, long: true,
		summary: "a dashboard, by its number or slug, with its charts and its filters",
		flags: func(fs *flag.FlagSet) {
			supersetFlags(fs)
			jsonFlag(fs)
		},
		run: supersetDashboard,
	},
	{
		name: "superset chart", args: "<dashboard> <chart>", min: 2, max: 2, long: true,
		summary: "a chart's rows as its dashboard shows them: columns, rows, the SQL Superset ran and when the " +
			"figures were computed",
		flags: func(fs *flag.FlagSet) {
			fs.String("format", "json", "json, or csv for the rows alone with a header line")
			fs.Bool("sql", false, "print only the SQL Superset ran")
			fs.Var(&listFlag{}, "filter", "a value of one of the dashboard's filters, \"Name=value\", the filter "+
				"named as the dashboard names it; give it once a value, \"Name=\" to clear the filter (default: "+
				"each filter's value when the dashboard opens)")
			supersetFlags(fs)
		},
		run: supersetChart,
	},
	{
		name: "superset filter-values", args: "<dashboard> <filter>", min: 2, max: 2, long: true,
		summary: "the values a select filter of a dashboard offers, the most frequent first, with how many rows " +
			"have each",
		flags: func(fs *flag.FlagSet) {
			fs.String("search", "", "only the values that contain this")
			supersetFlags(fs)
			jsonFlag(fs)
		},
		run: supersetFilterValues,
	},
	{
		name: "superset api", args: "<METHOD> <path>", min: 2, max: 2, long: true,
		summary: "send one request to Superset's API and print its answer; the owner's answer for the action " +
			"decides whether it runs, asks or is refused",
		flags: func(fs *flag.FlagSet) {
			fs.String("data", "", "the file holding the request's JSON body, or - for standard input")
			supersetFlags(fs)
			jsonFlag(fs)
		},
		run: supersetAPI,
	},
	{
		name: "integrations chats", args: "<integration>:<label> on|off", min: 2, max: 2,
		summary: "let the coordinator use an account in chats, and give it to its Runs, or stop it (owner)",
		flags:   jsonFlag, run: integrationsChats,
	},
	{
		name: "integrations proxy", args: "<integration>:<label> <proxy>|off", min: 2, max: 2,
		summary: "send an account's requests to its service through a proxy, such as socks5://127.0.0.1:1055 for a " +
			"service in a private network, or off to send them direct (owner)",
		flags: jsonFlag, run: integrationsProxy,
	},
	{
		name: "integrations dial-address", args: "<integration>:<label> <host[:port]>|off", min: 2, max: 2,
		summary: "dial an account's service at another address while its name and certificate stay the service's, " +
			"as curl's --resolve: a temporary way round a name that does not resolve; off dials the name (owner)",
		flags: jsonFlag, run: integrationsDialAddress,
	},
	{
		name: "integrations actions", args: "<integration>:<label>", min: 1, max: 1,
		summary: "what agents may do with an account, action by action, and whether the account can (owner)",
		flags:   jsonFlag,
		run:     integrationsActions,
	},
	{
		name: "integrations check", args: "<integration>:<label>", min: 1, max: 1,
		summary: "read again at the service what an account can really do, and list what it cannot (owner)",
		flags:   jsonFlag, run: integrationsCheck,
	},
	{
		name: "integrations actions set", args: "<integration>:<label> <action> allow|ask|never", min: 3, max: 3,
		summary: "let an action run on its own, ask you each time, or never run (owner)", flags: jsonFlag,
		run: integrationsActionsSet,
	},
	{
		name: "approvals", summary: "the requests agents made to your accounts that wait for your answer (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("status", "pending", "pending, approved, rejected or all")
			jsonFlag(fs)
		},
		run: approvalsList,
	},
	{
		name: "approvals approve", args: "<approval_id>", min: 1, max: 1,
		summary: "let a request run (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.Bool("session", false, "also stop asking for this action for the rest of the Run or the chat")
			fs.Bool("routine", false, "also stop asking for this action in the later runs of the routine that asked")
			jsonFlag(fs)
		},
		run: approvalsApprove,
	},
	{
		name: "approvals reject", args: "<approval_id>", min: 1, max: 1, summary: "refuse a request (owner)",
		flags: jsonFlag, run: approvalsReject,
	},
	{
		name: "integrations client set", args: "<integration>", min: 1, max: 1,
		summary: "keep the OAuth client a service signs in with, from the JSON file the service gave; an app such " +
			"as lark has none, each account is connected with its own (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("client-file", "", "the client's JSON file, or - for standard input")
			jsonFlag(fs)
		},
		run: integrationsClientSet,
	},
	{
		name: "integrations disconnect", args: "<integration>:<label>", min: 1, max: 1,
		summary: "forget a connected account and its token (owner)",
		flags:   jsonFlag, run: integrationsDisconnect,
	},
	{
		name: "channels lark list", summary: "the Lark channels: each a Lark app, the bot it talks to and whether " +
			"it is connected (owner)",
		flags: jsonFlag, run: larkChannelsList,
	},
	{
		name: "channels lark show", args: "<channel>", min: 1, max: 1,
		summary: "a Lark channel, who may talk through it and whether it is connected (owner)",
		flags:   jsonFlag, run: larkChannelShow,
	},
	{
		name: "channels lark set", args: "<channel>", min: 1, max: 1,
		summary: "set a Lark channel up or change it: the bot it talks to, its app from a JSON file with app_id " +
			"and app_secret, the owner's email at Lark, who may talk through it, whether it is on, and whether " +
			"a reply ends with a link to the web client (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "the bot the channel talks to, by id or name; the main bot for a new channel")
			fs.String("app-file", "", "a JSON file with the app's app_id and app_secret, or - for standard input")
			fs.String("owner", "", "the owner's email in the app's Lark organization, or his open_id")
			fs.String("mode", "", "who may talk through the channel: owner, allowlist or everyone")
			fs.Bool("on", false, "switch the channel on")
			fs.Bool("off", false, "switch the channel off")
			fs.Bool("web-link", false, "end a direct chat's reply with a link to the chat in the web client; "+
				"-web-link=false takes it off. On for the first channel, off for the next")
			jsonFlag(fs)
		},
		run: larkChannelSet,
	},
	{
		name: "channels lark rm", args: "<channel>", min: 1, max: 1,
		summary: "forget a Lark channel, its people and its app's secret (owner)",
		flags:   jsonFlag, run: larkChannelRemove,
	},
	{
		name: "channels lark allow", args: "<channel> <email or open_id>", min: 2, max: 2,
		summary: "add someone who may talk through a Lark channel when its mode is allowlist (owner)",
		flags:   jsonFlag, run: larkChannelPersonAdd,
	},
	{
		name: "channels lark disallow", args: "<channel> <email or open_id>", min: 2, max: 2,
		summary: "take someone off a Lark channel's list (owner)",
		flags:   jsonFlag, run: larkChannelPersonRemove,
	},
	{
		name: "bots list", summary: "the bots: each a model, a persona and a memory", flags: jsonFlag, run: botsList,
	},
	{
		name: "bots show", args: "<bot>", min: 1, max: 1, summary: "a bot's model, persona and memory",
		flags: jsonFlag, run: botsShow,
	},
	{
		name: "bots set", args: "<bot>", min: 1, max: 1,
		summary: "change a bot's settings, or make a bot of that name; what is left out stays (owner, or the main bot)",
		flags: func(fs *flag.FlagSet) {
			fs.String("provider", "", "the provider of the model; or name both in --model as <provider>/<model>")
			fs.String("model", "", "the model pi runs for the bot, as <provider>/<model> or with --provider; empty "+
				"for pi's default")
			fs.String("fallbacks", "", "ordered gateway models as provider/model, separated by commas; empty clears them")
			fs.String("effort", "", "the reasoning effort: low, medium, high, xhigh or max")
			fs.String("persona-file", "", "a file with what the bot is and how it behaves, or - for standard input")
			fs.String("skills", "", "the skills the bot gets beside those for everyone, separated by commas; \"\" for none")
			fs.String("skills-off", "", "the skills for everyone the bot does not get, separated by commas; \"\" for none")
			fs.String("preset", "", "tool family preset: full (default) or report")
			fs.String("tools", "", "additional Hypomnema tools or families, separated by commas; \"\" for none")
			fs.String("tools-off", "", "the Hypomnema tools the bot does not get, names or families separated by commas; "+
				"\"\" for none")
			fs.String("connections", "", "only these connections, integration:label separated by commas; \"\" for none")
			fs.Bool("all-connections", false, "every connection the owner switched on for chats")
			fs.String("workers", "", "only these worker profiles, separated by commas; \"\" for none")
			fs.Bool("all-workers", false, "every worker profile")
			fs.String("superset-dashboards", "", "only these dashboards of a Superset connection, label=number or "+
				"slug separated by commas, such as prod=8,9; label= for none, label=all for every one")
			fs.String("tool-display", "", "what its chats show of its tool calls: off (none), on (each tool's name) "+
				"or full (arguments, output and time as well)")
			fs.String("owner-accounts", "", "what it may do with the owner's own accounts (owner only): use, or read, "+
				"which refuses every write and send through his Lark sign-in, Google, Zalo and WhatsApp")
			fs.Int("guarantee", 0, "its guaranteed share of the room, in workers across every node: 1 to 64 (4 for a "+
				"new bot); it may use more when the others leave room free")
			fs.String("repos", "", "the repositories whose work it owns, as runs create --repo takes them, separated "+
				"by commas; \"\" for none (owner only): a Run created on one goes to its chat, and a repository has "+
				"one owning bot")
			jsonFlag(fs)
		},
		run: botsSet,
	},
	{
		name: "bots rename", args: "<bot> <new name>", min: 2, max: 2,
		summary: "give a bot another name; its id, chats, routines and channels stay (owner, or the main bot)",
		flags:   jsonFlag, run: botsRename,
	},
	{
		name: "bots rm", args: "<bot>", min: 1, max: 1,
		summary: "remove a bot that has no chats and no memory (owner, or the main bot)",
		flags: func(fs *flag.FlagSet) {
			fs.Bool("force", false, "also remove its chats and its memory (owner)")
			jsonFlag(fs)
		},
		run: botsRemove,
	},
	{
		name: "bots prompt", args: "<bot>", min: 1, max: 1,
		summary: "what pi is given about a bot when it starts for a chat: the persona and the memory",
		flags: func(fs *flag.FlagSet) {
			fs.Bool("group", false, "for the chat of a Lark group: without the memory, and saying who writes there")
		},
		run: botsPrompt,
	},
	{
		name:    "setup list",
		summary: "what the main bot made, changed or removed of bots and profiles in a chat (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("chat", "", "the chat")
			jsonFlag(fs)
		},
		run: setupList,
	},
	{
		name: "setup undo", args: "<change_id>", min: 1, max: 1,
		summary: "undo a change of the bot coordinator, unless what it changed was changed or used since (owner)",
		flags:   jsonFlag, run: setupUndo,
	},
	{
		name: "routines list", summary: "the routines: work a bot does on a schedule, a prompt or a script",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "only this bot's")
			jsonFlag(fs)
		},
		run: routinesList,
	},
	{
		name: "routines show", args: "<routine_id>", min: 1, max: 1,
		summary: "a routine with its prompt or script, what it is always allowed, and its runs",
		flags: func(fs *flag.FlagSet) {
			fs.Int("runs", 10, "how many runs, the newest first")
			jsonFlag(fs)
		},
		run: routinesShow,
	},
	{
		name: "routines set", args: "<name>", min: 1, max: 1,
		summary: "make a routine of a bot, switched on at once, or change it; what is left out stays",
		flags:   routineFlags, run: routinesSet,
	},
	{
		name: "routines undo", args: "<routine_id>", min: 1, max: 1,
		summary: "undo a bot's last change of a routine: one it made is removed, one it changed is as before (owner)",
		flags:   jsonFlag, run: routinesUndo,
	},
	{
		name: "routines pause", args: "<routine_id>", min: 1, max: 1, summary: "stop a routine's schedule (owner)",
		flags: jsonFlag, run: routinesPause,
	},
	{
		name: "routines resume", args: "<routine_id>", min: 1, max: 1,
		summary: "switch a paused routine on again, from now (owner)", flags: jsonFlag, run: routinesResume,
	},
	{
		name: "routines run", args: "<routine_id>", min: 1, max: 1,
		summary: "run a routine now; refused while a run of it still goes or waits",
		flags:   jsonFlag, run: routinesRun,
	},
	{
		name: "routines rm", args: "<routine_id>", min: 1, max: 1,
		summary: "remove a routine with its runs, and stop the run that still goes (owner)",
		flags:   jsonFlag, run: routinesRemove,
	},
	{
		name: "routines revoke", args: "<routine_id> <integration>:<label> <action>", min: 3, max: 3,
		summary: "take back an action a routine was allowed always: it asks again (owner)",
		flags:   jsonFlag, run: routinesRevoke,
	},
	{
		name: "routines script", args: "<routine_id>", min: 1, max: 1,
		summary: "write a script routine's file and its run's key into a directory and print the file's name and " +
			"limit; the launcher runs this, once a run",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "the bot the routine must belong to")
			fs.String("into", "", "the bot's routines directory")
		},
		run: routinesScript,
	},
	{
		name:    "pi-models",
		summary: "pi's models.json: the providers that are the model gateway, with their models; the launcher writes it",
		run:     piModels,
	},
	{
		name: "pi-settings", args: "<models.json>", min: 1, max: 1, offline: true,
		summary: "pi's settings.json for the models of a models.json: compaction scaled to each window, grep, " +
			"find and ls on; the launcher writes it",
		run: piSettings,
	},
	{
		name: "pi-mcp", args: "<hypomnema>", min: 1, max: 1, offline: true,
		summary: "pi's mcp.json: Hypomnema's tools server, `<hypomnema> mcp`, every tool declared to the model; " +
			"the launcher writes it",
		run: piMCP,
	},
	{
		name: "mcp", offline: true,
		summary: "Hypomnema's tools for the login in client.json, as an MCP server on standard input and output",
		run:     mcpServe,
	},
	{
		name: "tools list", offline: true,
		summary: "List the tools the control plane gives this login",
		flags:   jsonFlag,
		run:     toolsList,
	},
	{
		name: "tools call", args: "<tool>", min: 1, max: 1, offline: true,
		summary: "Call a tool with a JSON object from a file or standard input",
		flags: func(fs *flag.FlagSet) {
			fs.String("args-file", "", "read arguments from a JSON file (- for standard input); defaults to {}")
			jsonFlag(fs)
		},
		run: toolsCall,
	},
	{
		name: "exec", args: "<integration>:<label> -- <command> [args...]", min: 3, max: -1, offline: true,
		literalArgs: true,
		summary:     "Run a local service CLI through a Connection's proxy with no service credentials on this machine",
		run:         execService,
	},
	{
		name: "exec remove-ca", offline: true,
		summary: "Remove the local service proxy CA from your trust store and CLI home",
		run:     removeExecCA,
	},
	{
		name:    "model-key",
		summary: "the model gateway's key of the chat pi runs for; pi's models.json runs this at each request",
		run:     modelKey,
	},
	{
		name:    "artifact show",
		summary: "show the owner a page, a PDF, an image or a file in your chat, or replace one you showed (a Bot)",
		flags: func(fs *flag.FlagSet) {
			fs.String("title", "", "one line of at most 80 characters; a new artifact needs it")
			fs.String("html-file", "", "one HTML page, at most 256 KB, or - for standard input")
			fs.String("path", "", "in your directory: a folder with index.html and its data (5 MB), an .html file, "+
				"a PDF, an image or any file")
			fs.String("id", "", "the artifact to replace with this as its next revision")
			jsonFlag(fs)
		},
		run: artifactShow,
	},
	{
		name: "artifact list", summary: "artifacts, the latest changed first",
		flags: func(fs *flag.FlagSet) {
			fs.String("chat", "", "only this chat's")
			fs.Int("limit", 0, "how many (default 100)")
			jsonFlag(fs)
		},
		run: artifactList,
	},
	{
		name: "artifact info", args: "<artifact_id>", min: 1, max: 1,
		summary: "an artifact with its revisions and their files", flags: jsonFlag, run: artifactInfo,
	},
	{
		name: "artifact download", args: "<artifact_id>", min: 1, max: 1,
		summary: "save a revision of an artifact: its file, or a folder as a zip",
		flags: func(fs *flag.FlagSet) {
			fs.Int("rev", 0, "the revision (default the newest)")
			fs.String("out", "", "the file to write (default its own name)")
			jsonFlag(fs)
		},
		run: artifactDownload,
	},
	{
		name: "artifact share", args: "<artifact_id>", min: 1, max: 1,
		summary: "make a view-only link that always shows the artifact's newest revision, no sign-in needed (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("label", "", "whom the link is for, such as Laura")
			jsonFlag(fs)
		},
		run: artifactShare,
	},
	{
		name: "artifact shares", args: "<artifact_id>", min: 1, max: 1,
		summary: "an artifact's view-only links, with their views (owner)", flags: jsonFlag, run: artifactShares,
	},
	{
		name: "artifact unshare", args: "<share_id>", min: 1, max: 1,
		summary: "revoke a view-only link at once (owner)", flags: jsonFlag, run: artifactUnshare,
	},
	{
		name: "memory show", summary: "a memory: a bot's, an agent's (--agent) or the one about the owner (--owner)",
		flags: memoryFlags, run: memoryShow,
	},
	{
		name: "memory set", args: "<file>", min: 1, max: 1,
		summary: "replace a whole memory with a file's lines, or - for standard input (owner)",
		flags:   memoryFlags, run: memorySet,
	},
	{
		name: "memory add", args: "<entry>", min: 1, max: 1, summary: "add one line to a memory",
		flags: memoryFlags, run: memoryChange(memoryAdd),
	},
	{
		name: "memory replace", args: "<part of an entry> <the new line>", min: 2, max: 2,
		summary: "replace the one entry of a memory that has the text",
		flags:   memoryFlags, run: memoryChange(memoryReplace),
	},
	{
		name: "memory remove", args: "<part of an entry>", min: 1, max: 1,
		summary: "remove the one entry of a memory that has the text",
		flags:   memoryFlags, run: memoryChange(memoryRemove),
	},
	{
		name: "memory ls", summary: "the notes of a bot's or an agent's memory, with their sizes",
		flags: notesFlags, run: notesList,
	},
	{
		name: "memory cat", args: "<path>", min: 1, max: 1, summary: "one note of a memory, such as /memories/x.md",
		flags: notesFlags, run: notesCat,
	},
	{
		name: "memory put", args: "<path> <file>", min: 2, max: 2,
		summary: "write a note with a file's text, or - for standard input (owner)", flags: notesFlags, run: notesPut,
	},
	{
		name: "memory rm", args: "<path>", min: 1, max: 1, summary: "remove one note of a memory (owner)",
		flags: notesFlags, run: notesRm,
	},
	{
		name: "repos list", summary: "the repositories registered under a name, each with the bot whose chat takes " +
			"its Runs", flags: jsonFlag, run: reposList,
	},
	{
		name: "repos add", args: "<repo>", min: 1, max: 1,
		summary: "register a repository (owner/name or its URL) under a name a Run can use (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("name", "", "the name (default: the last part of its path)")
			fs.String("base", "", "branch worktrees start from when a Run names none")
			fs.String("connection", "", "the label of the host's connected account work on it uses")
			fs.String("ssh-url", "", "clone it over SSH from this address, with the key `hypomnema git ssh-key` "+
				"shows, such as ssh://git@host:222/group/name.git")
			fs.String("ssh-host-key-file", "", "with --ssh-url: the SSH host's keys as `ssh-keyscan -p <port> <host>` "+
				"prints them, in a file or - for standard input; check the fingerprints it prints")
			jsonFlag(fs)
		},
		run: reposAdd,
	},
	{
		name: "repos set", args: "<name>", min: 1, max: 1,
		summary: "change what a registered repository says; a flag given empty clears it (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("base", "", "branch worktrees start from when a Run names none")
			fs.String("connection", "", "the label of the host's connected account work on it uses")
			fs.String("ssh-url", "", "clone it over SSH from this address; empty clones it over HTTPS")
			fs.String("ssh-host-key-file", "", "the SSH host's keys as `ssh-keyscan -p <port> <host>` prints them, "+
				"in a file or - for standard input; check the fingerprints it prints")
			jsonFlag(fs)
		},
		run: reposSet,
	},
	{
		name: "git ssh-key", summary: "the public key nodes clone over SSH with, to add to the git host (owner)",
		flags: jsonFlag, run: gitSSHKey,
	},
	{
		name: "repos rm", args: "<name>", min: 1, max: 1, summary: "forget a registered repository (owner)",
		flags: jsonFlag, run: reposRemove,
	},
	{
		name: "profiles routing", summary: "show the effective role and Task-kind mapping as YAML",
		flags: jsonFlag, run: profilesRouting,
	},
	{
		name: "profiles routing-apply", args: "<file>", min: 1, max: 1,
		summary: "validate and replace the role routing YAML (owner)", run: profilesRoutingApply,
	},
	{
		name: "profiles metrics", summary: "settled attempts and cost by role, kind and chain entry (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("since", "", "start of the period in RFC3339 (default: 14 days ago)")
			jsonFlag(fs)
		},
		run: profilesMetrics,
	},
	{
		name: "profiles list", summary: "the profiles Tasks can name, at their current versions",
		flags: jsonFlag, run: profilesList,
	},
	{
		name: "profiles show", args: "<profile>", min: 1, max: 1, summary: "a profile's current version and source",
		flags: jsonFlag, run: profilesShow,
	},
	{
		name: "profiles versions", args: "<profile>", min: 1, max: 1, summary: "a profile's versions, newest first",
		flags: jsonFlag, run: profilesVersions,
	},
	{
		name: "profiles apply", args: "<file>", min: 1, max: 1,
		summary: "store a profile's YAML (owner, or the bot coordinator); a changed source makes a new version",
		flags:   jsonFlag, run: profilesApply,
	},
	{
		name: "profiles rm", args: "<profile>", min: 1, max: 1,
		summary: "delete a profile no unfinished task, backup or bot's workers list names; one used before is " +
			"hidden and kept for its runs, and applying it again brings it back (owner)",
		flags: jsonFlag, run: profilesRemove,
	},
	{
		name: "providers list", summary: "the sources of models, with the models each offers",
		flags: jsonFlag, run: providersList,
	},
	{
		name: "providers apply", args: "<file>", min: 1, max: 1,
		summary: "store a provider's YAML and its models (owner)", flags: jsonFlag, run: providersApply,
	},
	{
		name: "providers rm", args: "<name>", min: 1, max: 1,
		summary: "remove a provider and its models, when nothing names it (owner)", flags: jsonFlag, run: providersRemove,
	},
	{
		name: "models list", summary: "every model, with its provider", flags: jsonFlag, run: modelsList,
	},
	{
		name: "browser status", summary: "the browser every agent shares: its state and its tabs (owner)",
		flags: jsonFlag, run: browserStatus,
	},
	{
		name: "takes list", summary: "the takes agents filmed of the phone or the browser, the newest first (owner)",
		flags: jsonFlag, run: takesList,
	},
	{
		name: "takes get", args: "<take_id>", min: 1, max: 1,
		summary: "a take with its segments, its steps and where the owner paused it (owner)",
		flags:   jsonFlag, run: takesGet,
	},
	{
		name: "takes download", args: "<take_id>", min: 1, max: 1,
		summary: "save an available take's segments and take.json as a zip (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("out", "", "the file to write (default <take_id>.zip)")
			jsonFlag(fs)
		},
		run: takesDownload,
	},
	{
		name: "takes delete", args: "<take_id>", min: 1, max: 1,
		summary: "delete a take and its files; one still filmed is ended first (owner)",
		flags:   jsonFlag, run: takesDelete,
	},
	{
		name: "videos list", summary: "the videos made of takes, the newest first, with their revisions (owner)",
		flags: jsonFlag, run: videosList,
	},
	{
		name: "videos get", args: "<video_id>", min: 1, max: 1,
		summary: "a video with each revision's state, voice and note, and the latest one's lines (owner)",
		flags:   jsonFlag, run: videosGet,
	},
	{
		name: "videos make", args: "<take_id>", min: 1, max: 1,
		summary: "make an available take into a video, or a new revision of its video, made in the background (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("title", "", "the video's title on its first make (default the take's)")
			fs.String("template", "", "a template from `videos templates`: its aspect, captions, look and cards")
			fs.String("aspect", "", "9:16, 16:9 or 886x1920 (default the template's first, else 9:16)")
			fs.Bool("no-captions", false, "make it without captions")
			fs.String("voice", "", "the voice to speak with (default each provider's, from `audio voice`)")
			fs.Float64("speed", 0, "the speed every line is spoken at, 0.7 to 1.3 (left out: the provider's own)")
			lookFlags(fs)
			musicFlags(fs)
			fs.String("key", "", "idempotency key; repeat a make whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: videosMake,
	},
	{
		name: "videos look", args: "<video_id> <rev>", min: 2, max: 2,
		summary: "make a made revision again in another look, with its voice; only the flagged parts change (owner)",
		flags: func(fs *flag.FlagSet) {
			lookFlags(fs)
			musicFlags(fs)
			fs.String("key", "", "idempotency key; repeat a request whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: videosLook,
	},
	{
		name: "videos retry", args: "<video_id> <rev>", min: 2, max: 2,
		summary: "make a failed revision again as it was asked: its lines, look and voice (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("key", "", "idempotency key; repeat a request whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: videosRetry,
	},
	{
		name:    "videos looks",
		summary: "the caption presets, backgrounds and chromes a look is chosen from, and the default look (owner)",
		flags:   jsonFlag, run: videosLooks,
	},
	{
		name:    "videos templates",
		summary: "the templates a video is made from: length, aspects, cards and the sections' words (owner)",
		flags:   jsonFlag, run: videosTemplates,
	},
	{
		name: "videos preview", args: "<take_id>", min: 1, max: 1,
		summary: "save a look sample of a take as a JPEG: the look over its picture at the first step (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("template", "", "a template from `videos templates`, the look resolved as a make with it")
			fs.String("aspect", "", "9:16, 16:9 or 886x1920 (default the template's first, else 9:16)")
			fs.Bool("no-captions", false, "without captions")
			fs.Int("rev", 0, "start from this revision's look and line, as `videos look` does")
			fs.String("card", "", "draw the look's title or end card in place of the picture: title or end")
			lookFlags(fs)
			fs.String("out", "", "the file to write (default <take_id>-look.jpg)")
			jsonFlag(fs)
		},
		run: videosPreview,
	},
	{
		name: "videos request-changes", args: "<video_id> <rev>", min: 2, max: 2,
		summary: "ask for changes to a revision in review: a note for the agent, edited lines queue a new revision (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("note", "", "what to change, for the agent")
			fs.String("lines-file", "", "each scene's line, one a line, from this file or - for standard input")
			fs.String("key", "", "idempotency key; repeat a request whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: videosRequestChanges,
	},
	{
		name: "videos approve", args: "<video_id> <rev>", min: 2, max: 2,
		summary: "approve a revision in review; the one approved before goes back to review (owner)",
		flags:   jsonFlag, run: videosApprove,
	},
	{
		name: "videos delete", args: "<video_id>", min: 1, max: 1,
		summary: "delete a video, its revisions and their files; refused while one is being made (owner)",
		flags:   jsonFlag, run: videosDelete,
	},
	{
		name: "videos music list", summary: "the music library a video's music is chosen from, with its quota (owner)",
		flags: jsonFlag, run: videosMusicList,
	},
	{
		name: "videos music add", args: "<file>", min: 1, max: 1, long: true,
		summary: "upload a track to the music library: mp3, m4a, wav, flac or ogg, 10 s to 20 min, at most 64 MB (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("name", "", "the track's name (default the file's name without its extension)")
			jsonFlag(fs)
		},
		run: videosMusicAdd,
	},
	{
		name: "videos music rm", args: "<music_id>", min: 1, max: 1,
		summary: "delete a track of the music library; refused while a revision being made has it (owner)",
		flags:   jsonFlag, run: videosMusicRemove,
	},
	{
		name: "videos download", args: "<video_id> <rev>", min: 2, max: 2,
		summary: "save a made revision's MP4 (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("out", "", "the file to write (default <video_id>-r<rev>.mp4)")
			jsonFlag(fs)
		},
		run: videosDownload,
	},
	{
		name:    "device status",
		summary: "the nodes that hold an Android phone: each phone's state and what is installed (owner)",
		flags:   jsonFlag, run: deviceStatus,
	},
	{
		name: "device start", args: "<node>", min: 1, max: 1,
		summary: "ask a node to start its phone, installing it at the first start (owner)",
		flags:   jsonFlag, run: deviceStart,
	},
	{
		name: "device stop", args: "<node>", min: 1, max: 1,
		summary: "ask a node to stop its phone; what is signed in on it stays (owner)",
		flags:   jsonFlag, run: deviceStop,
	},
	{
		name: "web search", args: "<query>", min: 1, max: -1,
		summary: "search the web: each result's title, address, date and snippet, and which provider answered",
		flags: func(fs *flag.FlagSet) {
			fs.Int("max-results", 0, "how many results, 1 to 20 (default 5)")
			fs.String("provider", "", "ask only this provider: "+strings.Join(websearch.Providers(), ", ")+
				" (default: the first that answers)")
			jsonFlag(fs)
		},
		run: webSearch,
	},
	{
		name: "web fetch", args: "<url>", min: 1, max: 1,
		summary: "read a public web page as markdown, at most 20,000 characters",
		flags:   jsonFlag, run: webFetch,
	},
	{
		name: "web providers", summary: "the web search providers in the order a search tries them, with each one's state",
		flags: jsonFlag, run: webProviders,
	},
	{
		name: "web providers set", args: "<provider> on|off", min: 2, max: 2,
		summary: "switch a web search provider on or off (owner)", flags: jsonFlag, run: webProvidersSet,
	},
	{
		name: "web keys add", args: "<provider>", min: 1, max: 1,
		summary: "keep one more API key of exa, firecrawl, parallel, tavily or tinyfish in the vault (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("key-file", "", "the file holding the key, or - for standard input")
			jsonFlag(fs)
		},
		run: webKeysAdd,
	},
	{
		name: "web keys rm", args: "<provider> <name>", min: 2, max: 2,
		summary: "remove one of a provider's keys from the vault, by its name (owner)",
		flags:   jsonFlag, run: webKeysRemove,
	},
	{
		name: "web order", args: "<provider>...", min: 5, max: 5,
		summary: "the order a search tries the keyed providers in, each named once (owner)",
		flags:   jsonFlag, run: webOrder,
	},
	{
		name: "image generate", args: "<prompt>", min: 1, max: -1, long: true,
		summary: "make images through the owner's subscriptions, save them and print their paths",
		flags:   imageFlags(false), run: imageGenerate,
	},
	{
		name: "image edit", args: "<prompt>", min: 1, max: -1, long: true,
		summary: "edit images, or make one from reference images (--image), save it and print its path",
		flags:   imageFlags(true), run: imageEdit,
	},
	{
		name:    "image providers",
		summary: "the image providers in the order a request asks them, with each one's model and state",
		flags:   jsonFlag, run: imageProviders,
	},
	{
		name: "image providers set", args: "<provider> on|off", min: 2, max: 2,
		summary: "switch an image provider on or off (owner)", flags: jsonFlag, run: imageProvidersSet,
	},
	{
		name: "image order", args: "<provider> <provider>", min: 2, max: 2,
		summary: "the order a request asks the image providers in, each named once (owner)",
		flags:   jsonFlag, run: imageOrder,
	},
	{
		name: "accounts import", args: "claude", min: 1, max: 1,
		summary: "keep a Claude account on the control plane from the session Claude Code saved: --file, or --detect (owner)",
		flags:   accountsImportFlags, run: accountsImport,
	},
	{
		name:    "muse accounts",
		summary: "the owner's Muse accounts in order, each with its state and usage, and the tools' switch (owner)",
		flags:   jsonFlag, run: museAccounts,
	},
	{
		name: "muse tools", args: "on|off", min: 1, max: 1,
		summary: "switch the Muse tools of workers and Bots on or off (owner)", flags: jsonFlag, run: museTools,
	},
	{
		name: "muse account set", args: "<account> on|off", min: 2, max: 2,
		summary: "put a Muse account in use or out of it (owner)", flags: jsonFlag, run: museAccountSet,
	},
	{
		name: "audio transcribe", args: "<file>", min: 1, max: 1, long: true,
		summary: "turn a recording into text through the owner's audio providers, with who spoke when if asked",
		flags:   audioTranscribeFlags, run: audioTranscribe,
	},
	{
		name: "audio speak", args: "<text>", min: 1, max: -1, long: true,
		summary: "turn text into speech through the owner's audio providers and save it (-o <file>)",
		flags:   audioSpeakFlags, run: audioSpeak,
	},
	{
		name:    "audio providers",
		summary: "the audio providers with their keys, and the order speech-to-text and text-to-speech ask them in",
		flags:   jsonFlag, run: audioProviders,
	},
	{
		name: "audio providers set", args: "<provider> on|off", min: 2, max: 2,
		summary: "switch an audio provider on or off, for both kinds (owner)", flags: jsonFlag, run: audioProvidersSet,
	},
	{
		name: "audio order", args: "stt|tts <provider> <provider>", min: 3, max: 3,
		summary: "the order speech-to-text (stt) or text-to-speech (tts) asks the providers in, each named once (owner)",
		flags:   jsonFlag, run: audioOrder,
	},
	{
		name: "audio voice", args: "<provider> [voice]", min: 1, max: 2,
		summary: "the voice a provider speaks videos with when none is asked; none: its own default (owner)",
		flags:   jsonFlag, run: audioVoice,
	},
	{
		name: "audio keys add", args: "<provider>", min: 1, max: 1,
		summary: "keep one more API key of soniox or elevenlabs in the vault (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("key-file", "", "the file holding the key, or - for standard input")
			jsonFlag(fs)
		},
		run: audioKeysAdd,
	},
	{
		name: "audio keys rm", args: "<provider> <name>", min: 2, max: 2,
		summary: "remove one of an audio provider's keys from the vault, by its name (owner)",
		flags:   jsonFlag, run: audioKeysRemove,
	},
	{
		name: "meetings list", summary: "the meetings, the newest first, with where each stands",
		flags: func(fs *flag.FlagSet) {
			fs.String("q", "", "only those whose title or transcript holds this")
			fs.Int("limit", 0, "how many (default 200)")
			jsonFlag(fs)
		},
		run: meetingsList,
	},
	{
		name: "meetings show", args: "<meeting_id>", min: 1, max: 1,
		summary: "a meeting: where it stands, its speakers, notes, decisions and actions",
		flags: func(fs *flag.FlagSet) {
			fs.Bool("transcript", false, "print its transcript too")
			jsonFlag(fs)
		},
		run: meetingsShow,
	},
	{
		name: "meetings upload", args: "<file>", min: 1, max: 1, long: true,
		summary: "upload a meeting's recording (at most 128 MB); it is heard and written up in the background (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("language", "", "the meeting's language, an ISO-639-1 code such as vi (default: found)")
			fs.String("title", "", "the meeting's title (default: written from the meeting)")
			fs.String("people", "", "who was there, separated by commas; they help name the speakers")
			jsonFlag(fs)
		},
		run: meetingsUpload,
	},
	{
		name: "meetings rm", args: "<meeting_id>", min: 1, max: 1,
		summary: "delete a meeting with its recording, transcript and notes (owner)", flags: jsonFlag,
		run: meetingsRemove,
	},
	{
		name: "meetings notes", args: "<meeting_id>", min: 1, max: 1,
		summary: "write a meeting's notes again (owner)", flags: jsonFlag,
		run: meetingsNotes,
	},
	{
		name: "meetings speaker", args: "<meeting_id> <label> <name>", min: 3, max: 3,
		summary: "name a voice for the whole meeting; another voice's name merges the two, \"\" unnames it (owner)",
		flags:   jsonFlag, run: meetingsSpeaker,
	},
	{
		name: "meetings line", args: "<meeting_id> <n> <name>", min: 3, max: 3,
		summary: "say who spoke one line of a meeting (owner)", flags: jsonFlag, run: meetingsLine,
	},
	{
		name: "meetings done", args: "<meeting_id> <action_id>", min: 2, max: 2,
		summary: "mark an action of a meeting done (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.Bool("undo", false, "mark it not done")
			jsonFlag(fs)
		},
		run: meetingsDone,
	},
	{
		name: "meetings actions", summary: "the actions of every meeting, the newest meeting first",
		flags: func(fs *flag.FlagSet) {
			fs.String("who", "", "only this person's, by the name shown")
			fs.String("state", "", "open, done or all (default all)")
			jsonFlag(fs)
		},
		run: meetingsActions,
	},
	{
		name: "meetings recording", args: "<meeting_id>", min: 1, max: 1, long: true,
		summary: "save a meeting's recording",
		flags: func(fs *flag.FlagSet) {
			fs.String("out", "", "the file to write (default its own name)")
			jsonFlag(fs)
		},
		run: meetingsRecording,
	},
	{
		name: "meetings settings", summary: "the model that writes notes; --notes-model sets it",
		flags: func(fs *flag.FlagSet) {
			fs.String("notes-model", "", "the gateway chat model that writes notes; \"\" for none (owner)")
			jsonFlag(fs)
		},
		run: meetingsSettings,
	},
	{
		name:    "meetings words",
		summary: "the words to hear right every transcription is given; --add and --remove change yours",
		flags: func(fs *flag.FlagSet) {
			fs.String("add", "", "words to add, separated by commas (owner)")
			fs.String("remove", "", "words to remove, separated by commas (owner)")
			jsonFlag(fs)
		},
		run: meetingsWords,
	},
	{
		name: "meetings fix", args: "<meeting_id> <heard> <said>", min: 3, max: 3,
		summary: "replace a word heard wrong in a meeting's transcript; the right word joins your words (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.Int("line", 0, "only this line (default: the whole meeting)")
			jsonFlag(fs)
		},
		run: meetingsFix,
	},
	{
		name: "inbox list", summary: "the inbox's to-dos, by due then added; closed, snoozed and stale ones are left out",
		flags: func(fs *flag.FlagSet) {
			fs.String("state", "", "only these states, separated by commas: proposed, open, doing, review, done, "+
				"dropped, duplicate")
			fs.String("who", "", "only those of me, unknown, bot:<bot id> or a person's name")
			fs.String("source", "", "only those from owner, chat, routine, lark or zalo")
			fs.String("project", "", "only those of this project, by its name or id; none for those without one")
			fs.String("due-from", "", "only those due from this time (YYYY-MM-DD[ HH:MM], this machine's zone)")
			fs.String("due-to", "", "only those due before this time")
			fs.Bool("needs-you", false, "only those that need you now")
			fs.Bool("snoozed", false, "snoozed ones too")
			fs.Bool("closed", false, "closed ones too")
			fs.Bool("stale", false, "stale proposals too: proposed and untouched for three days")
			fs.String("cursor", "", "the next page, as the previous one printed")
			fs.Int("limit", 0, "how many (default 200)")
			jsonFlag(fs)
		},
		run: inboxList,
	},
	{
		name: "inbox show", args: "<todo_id>", min: 1, max: 1, summary: "a to-do with its history",
		flags: jsonFlag, run: inboxShow,
	},
	{
		name: "inbox add", args: "<title>", min: 1, max: 1,
		summary: "add a to-do: by hand for the owner, from its chat for a bot",
		flags: func(fs *flag.FlagSet) {
			inboxFlags(fs)
			fs.String("key", "", "idempotency key; repeat an add whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: inboxAdd,
	},
	{
		name: "inbox edit", args: "<todo_id>", min: 1, max: 1,
		summary: "change a to-do's title, notes, due, who, priority or project",
		flags: func(fs *flag.FlagSet) {
			fs.String("title", "", "the title")
			inboxFlags(fs)
			jsonFlag(fs)
		},
		run: inboxEdit,
	},
	{
		name: "inbox accept", args: "<todo_id>", min: 1, max: 1, summary: "open a proposed to-do (owner)",
		flags: jsonFlag, run: inboxAccept,
	},
	{
		name: "inbox done", args: "<todo_id>", min: 1, max: 1, summary: "close a to-do as done",
		flags: func(fs *flag.FlagSet) {
			fs.String("evidence", "", "a URL that shows it is done")
			fs.String("reason", "", "a word on how")
			jsonFlag(fs)
		},
		run: inboxDone,
	},
	{
		name: "inbox drop", args: "<todo_id> <reason>", min: 2, max: 2, summary: "close a to-do as dropped, with why",
		flags: jsonFlag, run: inboxDrop,
	},
	{
		name: "inbox duplicate", args: "<todo_id> <of_todo_id>", min: 2, max: 2,
		summary: "close a to-do as a duplicate of another (owner)", flags: jsonFlag, run: inboxDuplicate,
	},
	{
		name: "inbox snooze", args: "<todo_id> <until>", min: 2, max: 2,
		summary: "hide a to-do until YYYY-MM-DD[ HH:MM] in this machine's zone, or off to show it again (owner)",
		flags:   jsonFlag, run: inboxSnooze,
	},
	{
		name: "inbox reopen", args: "<todo_id>", min: 1, max: 1, summary: "open a closed to-do again (owner)",
		flags: jsonFlag, run: inboxReopen,
	},
	{
		name: "inbox chats", args: "[<integration>:<label>]", max: 1,
		summary: "the owner's accounts whose chats the secretary reads, or one's chats with whether it reads each (owner)",
		flags:   inboxChatFilterFlags, run: inboxChats,
	},
	{
		name: "inbox chats set", args: "<integration>:<label> [<chat_id>[,<chat_id>...]] on|off", min: 2, max: 3,
		summary: "set whether the secretary reads one chat, a list of chat IDs, or the filtered list (owner)",
		flags: func(fs *flag.FlagSet) {
			inboxChatFilterFlags(fs)
			fs.Bool("shown", false, "set the first 500 shown chats, even without search or filters")
		},
		run: inboxChatsSet,
	},
	{
		name:    "inbox chats move",
		summary: "move the chat ids of a Bot's thu-ky/filters.json dropChats into the owner's choices, once (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "the Bot whose folder holds thu-ky/filters.json, by name or id (default the main Bot)")
			jsonFlag(fs)
		},
		run: inboxChatsMove,
	},
	{
		name: "skills list", summary: "the skills the owner added, each at its newest revision",
		flags: jsonFlag, run: skillsList,
	},
	{
		name: "skills show", args: "<skill>", min: 1, max: 1, summary: "a skill the owner added, with every revision",
		flags: jsonFlag, run: skillsShow,
	},
	{
		name:    "skills add",
		summary: "add a skill, or its next revision, from a public GitHub repository or an archive (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("github", "", "owner/repo[/path][@ref]; no ref is the default branch")
			fs.String("skill", "", "with --github owner/repo, the skill's name, as `skills search` gives it, "+
				"instead of its folder")
			fs.String("file", "", "a zip or a gzipped tar with SKILL.md at its root or in its one folder")
			jsonFlag(fs)
		},
		run: skillsAdd,
	},
	{
		name: "skills search", args: "<words>", min: 1, max: -1, summary: "search the skills on skills.sh (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("owner", "", "only the skills of this GitHub owner")
			jsonFlag(fs)
		},
		run: skillsSearch,
	},
	{
		name: "skills sync", summary: "put the skills a bot gets in a folder, for pi's --skill (the launcher's)",
		flags: func(fs *flag.FlagSet) {
			fs.String("bot", "", "the bot")
			fs.String("dir", "", "the folder; a skill the bot no longer gets is taken out of it")
		},
		run: skillsSync,
	},
	{
		name: "skills everyone", args: "<skill> on|off", min: 2, max: 2,
		summary: "give a skill to every worker and Bot, or take it back (owner)", flags: jsonFlag, run: skillsEveryone,
	},
	{
		name: "skills rm", args: "<skill>", min: 1, max: 1, summary: "remove a skill and its revisions (owner)",
		flags: jsonFlag, run: skillsRemove,
	},
	{
		name: "skills get", args: "[skill]", max: 1, offline: true,
		summary: "print a skill's guide, as this binary ships it",
		flags: func(fs *flag.FlagSet) {
			fs.String("reference", "", "print one of its references instead")
			fs.Bool("references", false, "list its references")
			fs.Bool("full", false, "print the guide and every reference")
		},
		run: skillsGet,
	},
	{
		name: "skills install", offline: true,
		summary: "install the skills for Claude Code (~/.claude/skills) and for pi and Codex (~/.agents/skills)",
		flags: func(fs *flag.FlagSet) {
			fs.String("agent", "all", "claude, agents (pi, Codex) or all")
			fs.Bool("dry-run", false, "print what would change and change nothing")
			fs.Bool("force", false, "replace copies that were edited, keeping a backup")
		},
		run: skillsInstall,
	},
}
