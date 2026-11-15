<!-- From the Google Workspace CLI, skills/gws-drive-upload/SKILL.md, v0.22.5 (Apache-2.0, https://github.com/googleworkspace/cli). Changed for Hypomnema: the front matter is removed, the prerequisite names this skill's guide, and links point at the references kept here. -->

# drive +upload

> Read this skill's guide first: how a command is run here, which account it acts as, and the rule for anything that writes.

Upload a file with automatic metadata

## Usage

```bash
gws drive +upload <file>
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `<file>` | ✓ | — | Path to file to upload |
| `--parent` | — | — | Parent folder ID |
| `--name` | — | — | Target filename (defaults to source filename) |

## Examples

```bash
gws drive +upload ./report.pdf
gws drive +upload ./report.pdf --parent FOLDER_ID
gws drive +upload ./data.csv --name 'Sales Data.csv'
```

## Tips

- MIME type is detected automatically.
- Filename is inferred from the local path unless --name is given.

> [!CAUTION]
> This is a **write** command — confirm with the user before executing.

## See Also

- The guide of this skill
- [gws-drive](gws-drive.md) — All manage files, folders, and shared drives commands
