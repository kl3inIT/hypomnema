package clientcli

import "flag"

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
			fs.String("profile", "", "profile of the Task (default implementer)")
			fs.String("runtime", "", "runtime, instead of the profile's")
			fs.String("model", "", "model, instead of the profile's")
			fs.Bool("detach", false, "create the Run and return without following it")
			fs.String("use", "", "accounts the workers may use, as integration:label separated by commas, such as google:work")
		},
		run: ownerRun,
	},
	{
		name: "runs create", args: "[objective]", max: -1, summary: "create a Run; a coordinator token coordinates it",
		flags: func(fs *flag.FlagSet) {
			fs.String("repo", "", "repository the work happens in: a registered name, owner/name, or its URL")
			fs.String("base", "", "branch the worktrees start from (default main)")
			fs.String("objective-file", "", "read the objective from this file, or - for standard input")
			fs.String("key", "", "idempotency key; repeat a create whose result was lost with the same key")
			fs.String("use", "", "accounts the workers may use, as integration:label separated by commas, such as google:work")
			jsonFlag(fs)
		},
		run: runsCreate,
	},
	{
		name: "runs list", summary: "Runs, newest first",
		flags: func(fs *flag.FlagSet) {
			fs.String("status", "", "open, finished or canceled")
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
			fs.String("runtime", "", "runtime, instead of the profile's")
			fs.String("model", "", "model, instead of the profile's")
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
		name: "questions", summary: "open questions from workers",
		flags: func(fs *flag.FlagSet) {
			fs.String("to", "owner", "owner or coordinator")
			fs.String("run", "", "only this Run's")
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
		summary: "write to the coordinator on the server and print its replies; without a message, show the chat",
		flags: func(fs *flag.FlagSet) {
			fs.String("chat", "", "the chat to use (default the most recent)")
			fs.Bool("new", false, "start a new chat")
			fs.String("text-file", "", "read the message from this file, or - for standard input")
			fs.String("timeout", "10m", "stop printing replies after this long")
			fs.Int("last", 20, "without a message, how many messages to show")
		},
		run: chat,
	},
	{
		name: "chats", summary: "the chats with the coordinator, the most recently active first",
		flags: jsonFlag, run: chatsList,
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
		name: "hosts token set", args: "<host>", min: 1, max: 1,
		summary: "the same as `integrations connect <host>` with the label default (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("token-file", "", "the file holding the token, or - for standard input")
			jsonFlag(fs)
		},
		run: hostsTokenSet,
	},
	{
		name: "hosts token rm", args: "<host>", min: 1, max: 1,
		summary: "the same as `integrations disconnect <host>:default` (owner)",
		flags:   jsonFlag, run: hostsTokenRemove,
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
			"google by signing in, an app such as lark with nothing more (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("label", "default", "the name of this account, such as work")
			fs.String("token-file", "", "for a git host: the file holding the token, or - for standard input")
			fs.String("services", "", "for a service that signs in: what to connect, such as gmail:read,calendar "+
				"(:read is read only)")
			jsonFlag(fs)
		},
		run: integrationsConnect,
	},
	{
		name: "integrations chats", args: "<integration>:<label> on|off", min: 2, max: 2,
		summary: "let the coordinator use an account in chats, and give it to its Runs, or stop it (owner)",
		flags:   jsonFlag, run: integrationsChats,
	},
	{
		name: "integrations client set", args: "<integration>", min: 1, max: 1,
		summary: "keep the OAuth client a service signs in with, from the JSON file the service gave, or the app " +
			"a service is, from a JSON file with its app_id and app_secret (owner)",
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
		name: "channels lark show", summary: "the Lark bot, who may talk to it and whether it is connected (owner)",
		flags: jsonFlag, run: larkBotShow,
	},
	{
		name: "channels lark set",
		summary: "set the Lark bot up or change it: its app from a JSON file with app_id and app_secret, the " +
			"owner's email at Lark, who may talk to it, and whether it is on (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("app-file", "", "a JSON file with the app's app_id and app_secret, or - for standard input")
			fs.String("owner", "", "the owner's email in the app's Lark organization, or his open_id")
			fs.String("mode", "", "who may talk to the bot: owner, allowlist or everyone")
			fs.Bool("on", false, "switch the bot on")
			fs.Bool("off", false, "switch the bot off")
			jsonFlag(fs)
		},
		run: larkBotSet,
	},
	{
		name: "channels lark rm", summary: "forget the Lark bot and its app's secret (owner)",
		flags: jsonFlag, run: larkBotRemove,
	},
	{
		name: "channels lark allow", args: "<email or open_id>", min: 1, max: 1,
		summary: "add someone who may talk to the Lark bot when its mode is allowlist (owner)",
		flags:   jsonFlag, run: larkBotPersonAdd,
	},
	{
		name: "channels lark disallow", args: "<email or open_id>", min: 1, max: 1,
		summary: "take someone off the Lark bot's list (owner)",
		flags:   jsonFlag, run: larkBotPersonRemove,
	},
	{
		name: "repos list", summary: "the repositories registered under a name", flags: jsonFlag, run: reposList,
	},
	{
		name: "repos add", args: "<repo>", min: 1, max: 1,
		summary: "register a repository (owner/name or its URL) under a name a Run can use (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("name", "", "the name (default: the last part of its path)")
			fs.String("base", "", "branch worktrees start from when a Run names none")
			fs.String("connection", "", "the label of the host's connected account work on it uses")
			jsonFlag(fs)
		},
		run: reposAdd,
	},
	{
		name: "repos rm", args: "<name>", min: 1, max: 1, summary: "forget a registered repository (owner)",
		flags: jsonFlag, run: reposRemove,
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
		summary: "store a profile's YAML (owner); a changed source makes a new version",
		flags:   jsonFlag, run: profilesApply,
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
		name: "models list", summary: "every model, with its provider", flags: jsonFlag, run: modelsList,
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
