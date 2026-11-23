#!/usr/bin/env python3
"""Collect license texts from the locked build dependencies."""
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
output = ['Enhanced Kimi Desktop — third-party notices', 'License notices for compiled Go, bundled browser dependencies and bundled plugins.']
raw = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=ROOT, text=True)
decoder = json.JSONDecoder()
while raw.strip():
    module, end = decoder.raw_decode(raw.lstrip())
    raw = raw.lstrip()[end:]
    directory = module.get('Dir')
    if not directory or module.get('Main'):
        continue
    files = sorted(p for p in Path(directory).iterdir() if p.is_file() and (p.name.upper().startswith('LICENSE') or p.name.upper() in ('COPYING', 'NOTICE', 'AUTHORS')))
    if not files:
        raise SystemExit('Missing dependency license: ' + module['Path'])
    output.append('\n\n=== ' + module['Path'] + '@' + module.get('Version', '') + ' ===')
    for file in files:
        output.extend(['\n--- ' + file.name + ' ---', file.read_text(errors='replace')])
root_go = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
output.extend(['\n\n=== Go standard library ===', (root_go / 'LICENSE').read_text()])
lock = json.loads((ROOT / 'web/package-lock.json').read_text())
for name, info in lock.get('packages', {}).items():
    if not name or info.get('dev'):
        continue
    directory = ROOT / 'web' / name
    files = sorted(p for p in directory.iterdir() if p.is_file() and (p.name.upper().startswith('LICENSE') or p.name.upper() in ('COPYING', 'NOTICE', 'OFL.TXT')))
    if not files:
        raise SystemExit('Missing browser dependency license: ' + name)
    output.append('\n\n=== ' + name.removeprefix('node_modules/') + '@' + info.get('version', '') + ' ===')
    for file in files:
        output.extend(['\n--- ' + file.name + ' ---', file.read_text(errors='replace')])
for file in sorted((ROOT / 'web/public/fonts').glob('OFL-*.txt')):
    output.extend(['\n\n=== Bundled font: ' + file.name + ' ===', file.read_text()])
# Plugins are shipped inside the binary, so their licenses belong in the same
# notice file. Each bundled plugin directory must carry a LICENSE; a plugin
# without one is a packaging error, not a reason to silently omit it.
embed_text = (ROOT / 'plugins' / 'embed.go').read_text(encoding='utf-8')
bundled_dirs = []
for line in embed_text.splitlines():
    stripped = line.strip()
    if stripped.startswith('//go:embed '):
        for token in stripped.removeprefix('//go:embed ').split():
            bundled_dirs.append(token.removeprefix('all:'))
for name in sorted(bundled_dirs):
    plugin = ROOT / 'plugins' / name
    if not plugin.is_dir():
        raise SystemExit('Missing bundled plugin directory: ' + name)
    license_file = plugin / 'LICENSE'
    if not license_file.is_file():
        raise SystemExit('Missing plugin license: ' + plugin.name)
    manifest = json.loads((plugin / 'plugin.json').read_text())
    output.extend([
        '\n\n=== Bundled plugin: ' + manifest.get('id', plugin.name) + '@' + manifest.get('version', '') + ' (' + manifest.get('license', 'UNKNOWN') + ') ===',
        license_file.read_text(errors='replace'),
    ])
    for notice_name in ('NOTICE', 'NOTICE.md'):
        notice_file = plugin / notice_name
        if notice_file.is_file():
            output.extend(['\n\n--- ' + notice_name + ' ---', notice_file.read_text(errors='replace')])
(ROOT / 'dist').mkdir(exist_ok=True)
target = ROOT / 'dist/THIRD_PARTY_NOTICES.txt'
target.write_text('\n'.join(output) + '\n')
print('Created', target.relative_to(ROOT))
