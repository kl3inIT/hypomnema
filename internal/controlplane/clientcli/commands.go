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
			fs.String("repo", "", "repository the work happens in, owner/name")
			fs.String("base", "", "branch the worktree starts from (default main)")
			fs.String("role", "implement", "role of the Task")
			fs.String("runtime", "", "runtime, instead of the role's default")
			fs.String("model", "", "model, instead of the role's default")
			fs.Bool("detach", false, "create the Run and return without following it")
		},
		run: ownerRun,
	},
	{
		name: "runs create", args: "[objective]", max: -1, summary: "create a Run; a coordinator token coordinates it",
		flags: func(fs *flag.FlagSet) {
			fs.String("repo", "", "repository the work happens in, owner/name")
			fs.String("base", "", "branch the worktrees start from (default main)")
			fs.String("objective-file", "", "read the objective from this file, or - for standard input")
			fs.String("key", "", "idempotency key; repeat a create whose result was lost with the same key")
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
			fs.String("role", "implement", "the Task's role")
			fs.String("spec", "", "the Task's spec")
			fs.String("spec-file", "", "read the spec from this file, or - for standard input")
			fs.String("depends-on", "", "comma-separated refs or Task ids it depends on")
			fs.String("runtime", "", "runtime, instead of the role's default")
			fs.String("model", "", "model, instead of the role's default")
			fs.String("effort", "", "effort, instead of the role's default")
			fs.String("key", "", "idempotency key; repeat a create whose result was lost with the same key")
			jsonFlag(fs)
		},
		run: tasksCreate,
	},
	{
		name: "tasks show", args: "<task_id>", min: 1, max: 1, summary: "a Task with its latest Dispatch and result",
		flags: jsonFlag, run: tasksShow,
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
