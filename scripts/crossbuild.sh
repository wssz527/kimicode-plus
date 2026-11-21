#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
ldflags='-s -w'
if [ -n "${VERSION:-}" ]; then
  ldflags="$ldflags -X enhancedkimi/internal/server.Version=${VERSION#v}"
fi
for target in darwin/arm64 darwin/amd64 windows/amd64; do
  target_os=${target%/*}
  target_arch=${target#*/}
  suffix=''
  if [ "$target_os" = windows ]; then suffix='.exe'; fi
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -buildvcs=false -trimpath -ldflags="$ldflags" -o "dist/enhanced-kimi-$target_os-$target_arch$suffix" ./cmd/enhanced-kimi
done
# scripts/package.py refuses to package without this manifest, and refuses any
# binary whose digest does not match it. That is what stops a stale binary from
# the previous build from being shipped under a new version.
python3 - <<'PY'
import hashlib
import json
import os
import re
from pathlib import Path

root = Path.cwd()
dist = root / 'dist'
targets = ('darwin-arm64', 'darwin-amd64', 'windows-amd64')

version = os.environ.get('VERSION', '').strip()
if version.startswith('v'):
    version = version[1:]
if not version:
    source = (root / 'internal' / 'server' / 'server.go').read_text(encoding='utf-8')
    match = re.search(r'^var Version\s*=\s*"([^"]+)"', source, re.MULTILINE)
    if not match:
        raise SystemExit('Could not read the source version; set VERSION=<version>')
    version = match.group(1)

commit = ''
if os.environ.get('GIT_COMMIT'):
    commit = os.environ['GIT_COMMIT'].strip()
else:
    try:
        import subprocess
        result = subprocess.run(
            ['git', 'rev-parse', 'HEAD'],
            cwd=root, capture_output=True, text=True, timeout=5,
        )
        if result.returncode == 0:
            commit = result.stdout.strip()
    except Exception:
        commit = ''

binaries = []
for target in targets:
    name = 'enhanced-kimi-' + target + ('.exe' if target.startswith('windows') else '')
    path = dist / name
    if not path.is_file():
        raise SystemExit('Missing cross build: ' + name)
    data = path.read_bytes()
    binaries.append({
        'target': target,
        'name': name,
        'bytes': len(data),
        'sha256': hashlib.sha256(data).hexdigest(),
    })

manifest = {'version': version, 'binaries': binaries}
if commit:
    manifest['gitCommit'] = commit
(dist / 'build-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
print('Wrote dist/build-manifest.json for ' + version)
PY
