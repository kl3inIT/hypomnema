<!-- From the Google Workspace CLI, skills/gws-gmail-read/SKILL.md, v0.22.5 (Apache-2.0, https://github.com/googleworkspace/cli). Changed for Hypomnema: the front matter is removed, the prerequisite names this skill's guide, and links point at the references kept here. -->

# gmail +read

> Read this skill's guide first: how a command is run here, which account it acts as, and the rule for anything that writes.

Read a message and extract its body or headers

## Usage

```bash
gws gmail +read --id <ID>
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--id` | ✓ | — | The Gmail message ID to read |
| `--headers` | — | — | Include headers (From, To, Subject, Date) in the output |
| `--format` | — | text | Output format (text, json) |
| `--html` | — | — | Return HTML body instead of plain text |
| `--dry-run` | — | — | Show the request that would be sent without executing it |

## Examples

```bash
gws gmail +read --id 18f1a2b3c4d
gws gmail +read --id 18f1a2b3c4d --headers
gws gmail +read --id 18f1a2b3c4d --format json | jq '.body'
```

## Tips

- Converts HTML-only messages to plain text automatically.
- Handles multipart/alternative and base64 decoding.

## See Also

- The guide of this skill
- [gws-gmail](gws-gmail.md) — All send, read, and manage email commands
