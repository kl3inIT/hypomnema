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
			"google by signing in, an app of lark with its app_id and app_secret read from a file, superset with " +
			"its address, a user name and a password read from a file, a role of an aws account with its ARN and " +
			"the key that assumes it (owner)",
		flags: func(fs *flag.FlagSet) {
			fs.String("label", "default", "the name of this account, such as work")
			fs.String("token-file", "", "for a git host: the file holding the token, or - for standard input")
			fs.String("app-file", "", "for an app such as lark: a JSON file with its app_id and app_secret, or - "+
				"for standard input; without it, a label that exists is checked again")
			fs.String("services", "", "for a service that signs in: what to connect, such as gmail:read,calendar "+
				"(:read is read only)")
			fs.String("url", "", "for superset: its address, such as https://superset.example.com")
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
			supersetFlags(fs)
		},
		run: supersetChart,
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
		name: "integrations actions", args: "<integration>:<label>", min: 1, max: 1,
		summary: "what agents may do with an account, action by action (owner)", flags: jsonFlag,
		run: integrationsActions,
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
			fs.String("bot", "", "the bot the channel talks to; coordinator for a new channel")
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
		summary: "make a bot, or change its settings; what is left out stays (owner, or the bot coordinator)",
		flags: func(fs *flag.FlagSet) {
			fs.String("provider", "", "the provider of the model, when more than one offers it")
			fs.String("model", "", "the model pi runs for the bot; empty for pi's default")
			fs.String("effort", "", "the reasoning effort: low, medium, high, xhigh or max")
			fs.String("persona-file", "", "a file with what the bot is and how it behaves, or - for standard input")
			fs.String("skills", "", "the skills the bot gets beside those for everyone, separated by commas; \"\" for none")
			fs.String("skills-off", "", "the skills for everyone the bot does not get, separated by commas; \"\" for none")
			fs.String("connections", "", "only these connections, integration:label separated by commas; \"\" for none")
			fs.Bool("all-connections", false, "every connection the owner switched on for chats")
			fs.String("workers", "", "only these worker profiles, separated by commas; \"\" for none")
			fs.Bool("all-workers", false, "every worker profile")
			jsonFlag(fs)
		},
		run: botsSet,
	},
	{
		name: "bots rm", args: "<bot>", min: 1, max: 1,
		summary: "remove a bot that has no chats and no memory (owner, or the bot coordinator)",
		flags:   jsonFlag, run: botsRemove,
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
		summary: "what the bot coordinator made, changed or removed of bots and profiles in a chat (owner)",
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
		name:    "model-key",
		summary: "the model gateway's key of the chat pi runs for; pi's models.json runs this at each request",
		run:     modelKey,
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
		summary: "store a profile's YAML (owner, or the bot coordinator); a changed source makes a new version",
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
		name: "browser status", summary: "the browser every agent shares: its state and its tabs (owner)",
		flags: jsonFlag, run: browserStatus,
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
