#!/bin/sh
# Builds the node for every platform the install script supports into dist/, each with the manifest the script
# reads (version, url, sha256), as Omnara's per-platform release manifests. Run by `make release-node`; the
# release workflow publishes dist/ to a GitHub release.
#
#   release-node.sh <version> <owner/repository> <go build ldflags>
set -eu

version=$1
repo=$2
ldflags=$3

rm -rf dist
mkdir -p dist

for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  binary=hypomnema-node-$platform
  GOOS=${platform%-*} GOARCH=${platform#*-} CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" -o "dist/$binary" ./cmd/hypomnema-node
  sum=$(sha256sum "dist/$binary")
  printf 'version=%s\nurl=https://github.com/%s/releases/download/%s/%s\nsha256=%s\n' \
    "$version" "$repo" "$version" "$binary" "${sum%% *}" > "dist/$platform.txt"
done
