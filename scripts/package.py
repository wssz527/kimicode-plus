#!/usr/bin/env python3
"""Package previously verified platform binaries with dependency notices."""
import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import zipfile

root = Path(__file__).resolve().parents[1]
dist = root / 'dist'
notices = dist / 'THIRD_PARTY_NOTICES.txt'
if not notices.is_file():
    raise SystemExit('Run scripts/notices.py first')
release = dist / 'release'
release.mkdir(exist_ok=True)
manifest = []
usage = ('Enhanced Kimi Desktop 0.1.1 — stabilization release with public metadata\n'
         'Run the binary with --data-dir ./enhanced-data .\n'
         'Open pairingUrl from enhanced-data/runtime.json in a browser.\n'
         'No Node.js, Rust, Electron or Chromium is included or required.\n'
         'Only the enhancement service is started; official Desktop integration is not enabled.\n'
         'Website links are URL entries, not full website adapters. Model replies and Code task receipts are Mock.\n'
         'Public metadata is real read-only; native Desktop is not connected. Keep runtime.json private. On Windows use a private user directory protected by your user ACL.\n')
for target in ['darwin-arm64', 'darwin-amd64', 'windows-amd64', 'windows-arm64', 'linux-amd64']:
    name = 'enhanced-kimi-' + target + ('.exe' if target.startswith('windows') else '')
    binary = dist / name
    if not binary.is_file():
        raise SystemExit('Missing cross build: ' + name)
    entries = [(name, binary.read_bytes(), 0o755), ('THIRD_PARTY_NOTICES.txt', notices.read_bytes(), 0o644), ('RUN.txt', usage.encode(), 0o644)]
    if target.startswith('windows'):
        archive = release / ('enhanced-kimi-0.1.1-' + target + '.zip')
        with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
            for filename, data, mode in entries:
                info = zipfile.ZipInfo(filename)
                info.external_attr = mode << 16
                output.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    else:
        archive = release / ('enhanced-kimi-0.1.1-' + target + '.tar.gz')
        with archive.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', compresslevel=9, mtime=0) as compressed, tarfile.open(fileobj=compressed, mode='w') as output:
            for filename, data, mode in entries:
                info = tarfile.TarInfo(filename)
                info.size, info.mode, info.mtime = len(data), mode, 0
                output.addfile(info, io.BytesIO(data))
    manifest.append({'target': target, 'binaryBytes': binary.stat().st_size, 'packageBytes': archive.stat().st_size, 'unpackedBytes': sum(len(v[1]) for v in entries), 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest(), 'archive': archive.name})
(release / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
(release / 'SHA256SUMS').write_text(''.join(v['sha256'] + '  ' + v['archive'] + '\n' for v in manifest))
print(json.dumps(manifest, indent=2))
