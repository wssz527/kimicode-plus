#!/usr/bin/env python3
"""Collect license texts from the locked build dependencies."""
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
output = ['Enhanced Kimi Desktop — third-party notices', 'License notices for compiled Go, bundled browser dependencies and adapted UI styles.']
output.extend(['\n\n=== bfjnbvf/kimi-code-monitor UI structure and styles @1d529f0b424a708971cfac097238df444f3b5beb ===', '''MIT License

Copyright (c) 2026 十叶

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.'''])
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
(ROOT / 'dist').mkdir(exist_ok=True)
target = ROOT / 'dist/THIRD_PARTY_NOTICES.txt'
target.write_text('\n'.join(output) + '\n')
print('Created', target.relative_to(ROOT))
