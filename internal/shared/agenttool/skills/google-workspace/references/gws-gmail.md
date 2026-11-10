<!-- From the Google Workspace CLI, skills/gws-gmail/SKILL.md, v0.22.5 (Apache-2.0, https://github.com/googleworkspace/cli). Changed for Hypomnema: the front matter is removed, the prerequisite names this skill's guide, and links point at the references kept here. -->

# gmail (v1)

> Read this skill's guide first: how a command is run here, which account it acts as, and the rule for anything that writes.

```bash
gws gmail <resource> <method> [flags]
```

## Helper Commands

| Command | Description |
|---------|-------------|
| [`+send`](gws-gmail-send.md) | Send an email |
| [`+triage`](gws-gmail-triage.md) | Show unread inbox summary (sender, subject, date) |
| [`+reply`](gws-gmail-reply.md) | Reply to a message (handles threading automatically) |
| [`+reply-all`](gws-gmail-reply-all.md) | Reply-all to a message (handles threading automatically) |
| [`+forward`](gws-gmail-forward.md) | Forward a message to new recipients |
| [`+read`](gws-gmail-read.md) | Read a message and extract its body or headers |
| `+watch` | Watch for new emails and stream them as NDJSON |

## API Resources

### users

  - `getProfile` — Gets the current user's Gmail profile.
  - `stop` — Stop receiving push notifications for the given user mailbox.
  - `watch` — Set up or update a push notification watch on the given user mailbox.
  - `drafts` — Operations on the 'drafts' resource
  - `history` — Operations on the 'history' resource
  - `labels` — Operations on the 'labels' resource
  - `messages` — Operations on the 'messages' resource
  - `settings` — Operations on the 'settings' resource
  - `threads` — Operations on the 'threads' resource

## Discovering Commands

Before calling any API method, inspect it:

```bash
# Browse resources and methods
gws gmail --help

# Inspect a method's required params, types, and defaults
gws schema gmail.<resource>.<method>
```

Use `gws schema` output to build your `--params` and `--json` flags.
