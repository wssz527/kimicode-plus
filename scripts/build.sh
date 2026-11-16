#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
(cd web && npm ci && npm run build && npm run build:host)
rm -rf internal/webassets/dist
mkdir -p internal/webassets/dist dist
touch internal/webassets/dist/.gitkeep
cp -R web/dist/. internal/webassets/dist/
mkdir -p internal/webassets/dist/host
cp -R web/dist-host/. internal/webassets/dist/host/
CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags='-s -w' -o dist/enhanced-kimi ./cmd/enhanced-kimi
python3 scripts/notices.py
