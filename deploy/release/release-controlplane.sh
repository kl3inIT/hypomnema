#!/bin/sh
# Builds the control plane for Linux into dist/, each with a manifest in the node's format (version, url, sha256),
# which `hypomnema update` reads (issue #279, as Beszel's hub reads its release assets). The web client must be
# built first (web/dist is embedded). Run by `make release-controlplane` after `make release-node`, which empties
# dist/; the release workflow publishes dist/ to a GitHub release.
#
#   release-controlplane.sh <version> <owner/repository> <go build ldflags>
set -eu

version=$1
repo=$2
ldflags=$3

test -f web/dist/index.html || { echo "build the web client first (make web-build)" >&2; exit 1; }
mkdir -p dist

for platform in linux-amd64 linux-arm64; do
  binary=hypomnema-$platform
  GOOS=${platform%-*} GOARCH=${platform#*-} CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" -o "dist/$binary" ./cmd/hypomnema
  sum=$(sha256sum "dist/$binary")
  printf 'version=%s\nurl=https://github.com/%s/releases/download/%s/%s\nsha256=%s\n' \
    "$version" "$repo" "$version" "$binary" "${sum%% *}" > "dist/controlplane-$platform.txt"
done
