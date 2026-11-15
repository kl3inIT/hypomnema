<!-- From the Google Workspace CLI, skills/gws-gmail-triage/SKILL.md, v0.22.5 (Apache-2.0, https://github.com/googleworkspace/cli). Changed for Hypomnema: the front matter is removed, the prerequisite names this skill's guide, and links point at the references kept here. -->

# gmail +triage

> Read this skill's guide first: how a command is run here, which account it acts as, and the rule for anything that writes.

Show unread inbox summary (sender, subject, date)

## Usage

```bash
gws gmail +triage
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--max` | — | 20 | Maximum messages to show (default: 20) |
| `--query` | — | — | Gmail search query (default: is:unread) |
| `--labels` | — | — | Include label names in output |

## Examples

```bash
gws gmail +triage
gws gmail +triage --max 5 --query 'from:boss'
gws gmail +triage --format json | jq '.[].subject'
gws gmail +triage --labels
```

## Tips

- Read-only — never modifies your mailbox.
- Defaults to table output format.

## See Also

- The guide of this skill
- [gws-gmail](gws-gmail.md) — All send, read, and manage email commands
