#!/bin/sh
# What a worker's machine needs for this repository's checks, run by the node in a Task's new worktree
# (hypomnema.yaml, issue #186), for every profile, reviewers included (issue #718). It installs, for the worker's own
# user and without sudo, what is missing:
#
# - Go at the version go.mod names, from go.dev, checked against its SHA-256, under $HOME/.local/go. The linters and
#   generators come through `go tool`, so nothing else is installed for them.
# - Node 24, the latest 24.x from nodejs.org, checked against the release's SHASUMS256.txt (nodejs.org's "verifying
#   binaries"), under $HOME/.local/node.
# - pnpm at the version web/package.json names (packageManager), through Node's corepack.
#
# Their commands are linked into $HOME/.local/bin, which the node puts first on the worker's PATH, as a login shell's
# ~/.profile does on Debian and Ubuntu (internal/node/runner/env.go). A tool already on the PATH at the right version
# is used as it is, and a run that finds everything in place ends in a second or two.
#
# Left out, because they need sudo: gcc, which `make race` needs, and make itself (it has no official binary and
# building it takes a C compiler); without make, AGENTS.md says how the checks run. Playwright's Chromium is not
# installed here: it comes with the web client's packages, `pnpm --dir web install --frozen-lockfile` then
# `pnpm --dir web exec playwright install chromium` (into ~/.cache/ms-playwright), and the system libraries it needs
# are named, not installed, by `pnpm --dir web exec playwright install-deps --dry-run chromium`.
set -eu

prefix="$HOME/.local"
bin="$prefix/bin"
mkdir -p "$bin"
PATH="$bin:$PATH"
export PATH

case "$(uname -s)-$(uname -m)" in
Linux-x86_64) goarch=linux-amd64 nodearch=linux-x64 ;;
Linux-aarch64 | Linux-arm64) goarch=linux-arm64 nodearch=linux-arm64 ;;
*) goarch="" nodearch="" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { curl -fsSL --retry 2 "$@"; }

# link puts the named commands of dir into $HOME/.local/bin.
link() {
	dir=$1
	shift
	for name in "$@"; do
		ln -sfn "$dir/$name" "$bin/$name"
	done
}

# place moves an unpacked tree to dest: beside its place first, so a half-written one is never found.
place() {
	rm -rf "$2"
	mv "$1" "$2"
}

# Go.
go_want=$(awk '$1 == "go" { print $2; exit }' go.mod)
go_dest="$prefix/go"
go_is() { [ "$("$1" env GOVERSION 2>/dev/null)" = "go$go_want" ]; }

if go_is "$go_dest/bin/go"; then
	link "$go_dest/bin" go gofmt
elif ! go_is go; then
	[ -n "$goarch" ] || {
		echo "no Go $go_want here, and this script installs it on Linux only" >&2
		exit 1
	}
	file="go$go_want.$goarch.tar.gz"
	fetch -o "$tmp/$file" "https://dl.google.com/go/$file"
	echo "$(fetch "https://dl.google.com/go/$file.sha256")  $tmp/$file" | sha256sum -c - >/dev/null
	tar -xzf "$tmp/$file" -C "$tmp"
	place "$tmp/go" "$go_dest"
	link "$go_dest/bin" go gofmt
	"$go_dest/bin/go" version
fi

# Node 24.
node_major=24
node_dest="$prefix/node"
node_is() { "$1" --version 2>/dev/null | grep -q "^v$node_major\."; }

if node_is "$node_dest/bin/node"; then
	link "$node_dest/bin" node npm npx corepack
elif ! node_is node; then
	[ -n "$nodearch" ] || {
		echo "no Node $node_major here, and this script installs it on Linux only" >&2
		exit 1
	}
	base="https://nodejs.org/dist/latest-v$node_major.x"
	fetch -o "$tmp/SHASUMS256.txt" "$base/SHASUMS256.txt"
	file=$(awk -v want="-$nodearch.tar.gz" 'substr($2, length($2) - length(want) + 1) == want { print $2; exit }' \
		"$tmp/SHASUMS256.txt")
	[ -n "$file" ] || {
		echo "$base/SHASUMS256.txt names no $nodearch archive" >&2
		exit 1
	}
	fetch -o "$tmp/$file" "$base/$file"
	(cd "$tmp" && grep "  $file\$" SHASUMS256.txt | sha256sum -c - >/dev/null)
	tar -xzf "$tmp/$file" -C "$tmp"
	place "$tmp/${file%.tar.gz}" "$node_dest"
	link "$node_dest/bin" node npm npx corepack
	echo "node $("$node_dest/bin/node" --version)"
fi

# pnpm, at the version the web client names. Corepack's shim reads packageManager in web/ and bridges/zalo/; the
# global install makes the same version its default where no package.json names one, as at the repository's root.
pnpm_want=$(sed -n 's/.*"packageManager": *"pnpm@\([^"+]*\).*/\1/p' web/package.json)
if [ "$(pnpm --version 2>/dev/null)" != "$pnpm_want" ]; then
	export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
	corepack enable --install-directory "$bin" pnpm
	corepack install --global "pnpm@$pnpm_want" >/dev/null
	echo "pnpm $(pnpm --version)"
fi
