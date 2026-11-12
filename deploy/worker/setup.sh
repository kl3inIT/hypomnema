#!/bin/sh
# What a worker's machine needs for this repository's checks, run by the node in a Task's new worktree
# (hypomnema.yaml, issue #186): Go at the version go.mod names, for the worker's own user, without sudo. The
# linters and generators come through `go tool`, so nothing else is installed for them. gcc, which `make race`
# needs, takes sudo and is left to CI.
#
# Go goes to $HOME/.local/go, where AGENTS.md tells an agent to look: $HOME/.local/go/bin/go.
set -eu

want=$(awk '$1 == "go" { print $2; exit }' go.mod)
dest="$HOME/.local/go"

has() { [ "$("$1" env GOVERSION 2>/dev/null)" = "go$want" ]; }

if has go || has "$dest/bin/go"; then
	exit 0
fi

case "$(uname -s)-$(uname -m)" in
Linux-x86_64) platform=linux-amd64 ;;
Linux-aarch64 | Linux-arm64) platform=linux-arm64 ;;
*)
	echo "no Go $want here, and this script installs it on Linux only" >&2
	exit 1
	;;
esac

file="go$want.$platform.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL --retry 2 -o "$tmp/$file" "https://dl.google.com/go/$file"
sum=$(curl -fsSL --retry 2 "https://dl.google.com/go/$file.sha256")
echo "$sum  $tmp/$file" | sha256sum -c - >/dev/null

# Unpacked beside its place and moved there, so a half-written toolchain is never found.
mkdir -p "$HOME/.local"
tar -xzf "$tmp/$file" -C "$tmp"
rm -rf "$dest"
mv "$tmp/go" "$dest"

"$dest/bin/go" version
