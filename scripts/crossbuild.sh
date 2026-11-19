#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
ldflags='-s -w'
if [ -n "${VERSION:-}" ]; then
  ldflags="$ldflags -X enhancedkimi/internal/server.Version=${VERSION#v}"
fi
for target in darwin/arm64 darwin/amd64 windows/amd64 windows/arm64 linux/amd64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  suffix=''
  if [ "$target_os" = windows ]; then suffix='.exe'; fi
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -buildvcs=false -trimpath -ldflags="$ldflags" -o "dist/enhanced-kimi-$target_os-$target_arch$suffix" ./cmd/enhanced-kimi
done
