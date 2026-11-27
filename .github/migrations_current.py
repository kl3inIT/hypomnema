import argparse
import json
from pathlib import PurePosixPath
import re
import subprocess


MIGRATIONS = PurePosixPath("internal/controlplane/orchestration/migrations")
MIGRATION_NAME = re.compile(r"([0-9]+)_[^/]*\.sql")
CONTEXT = "migrations-current"


def numbered_migrations(paths):
    for path in map(PurePosixPath, paths):
        match = MIGRATION_NAME.fullmatch(path.name)
        if path.parent == MIGRATIONS and match and len(match[1]) != 14:
            yield int(match[1])


def migration_status(last_on_main, added_paths):
    oldest = min(numbered_migrations(added_paths), default=None)
    if oldest is not None and oldest <= last_on_main:
        return "failure", f"{oldest} is taken on main: merge main and run make migration-fix"
    return "success", "Migration numbers are current"


def api(endpoint, *args):
    return subprocess.check_output(["gh", "api", endpoint, *args], text=True)


def main():
    parser = argparse.ArgumentParser(description="Check PR migration numbers against main")
    parser.add_argument("--base-ref", default="HEAD", help="local ref for main")
    parser.add_argument("--pr", type=int, help="check one PR, including one with no migration")
    parser.add_argument("--dry-run", action="store_true", help="print statuses without posting")
    args = parser.parse_args()

    base_paths = subprocess.check_output(
        ["git", "ls-tree", "-z", "--name-only", args.base_ref, "--", f"{MIGRATIONS}/"],
        text=True,
    ).split("\0")
    last_on_main = max(numbered_migrations(base_paths), default=0)
    pulls = "repos/{owner}/{repo}/pulls"
    if args.pr is None:
        prs = api(
            f"{pulls}?state=open&base=main&per_page=100",
            "--paginate", "--jq", ".[] | [.number, .head.sha] | @json",
        )
    else:
        prs = api(f"{pulls}/{args.pr}", "--jq", "[.number, .head.sha] | @json")

    print(f"Last migration on main: {last_on_main}", flush=True)
    for line in prs.splitlines():
        number, head = json.loads(line)
        # A rename adds its destination path, as in Makefile's git diff --no-renames.
        pages = api(
            f"{pulls}/{number}/files?per_page=100", "--paginate", "--jq",
            '[length, [.[] | select(.status == "added" or .status == "renamed") | .filename]] | @json',
        )
        file_count = 0
        added = []
        for page in pages.splitlines():
            count, filenames = json.loads(page)
            file_count += count
            added.extend(
                path for path in map(PurePosixPath, filenames)
                if path.parent == MIGRATIONS and MIGRATION_NAME.fullmatch(path.name)
            )
        # The REST endpoint returns at most 3000 changed files.
        if file_count >= 3000:
            raise SystemExit(f"PR #{number}: GitHub may have truncated its changed files")
        if args.pr is None and not added:
            continue

        state, description = migration_status(last_on_main, added)
        print(f"PR #{number} {head}: {CONTEXT}={state}: {description}", flush=True)
        if not args.dry_run:
            api(
                f"repos/{{owner}}/{{repo}}/statuses/{head}", "--method", "POST", "--silent",
                "-f", f"context={CONTEXT}", "-f", f"state={state}", "-f", f"description={description}",
            )


if __name__ == "__main__":
    main()
