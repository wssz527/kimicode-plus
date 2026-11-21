#!/usr/bin/env python3
"""Package platform binaries with the project license, dependency notices, and checksums."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import tarfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('darwin-arm64', 'darwin-amd64', 'windows-amd64')
VERSION_PATTERN = re.compile(
    r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
    r'(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?'
)


def release_version(value):
    value = value.removeprefix('v')
    match = VERSION_PATTERN.fullmatch(value)
    if not match or any(
        part.isdigit() and len(part) > 1 and part.startswith('0')
        for part in (match[4] or '').split('.')
    ):
        raise ValueError('Expected a semantic version such as 0.2.0 or v0.2.0')
    return value


def source_version():
    source = (ROOT / 'internal/server/server.go').read_text(encoding='utf-8')
    match = re.search(r'^var Version\s*=\s*"([^"]+)"', source, re.MULTILINE)
    if not match:
        raise ValueError('Could not find Version in internal/server/server.go')
    return match[1]


def binary_name(target):
    return f'enhanced-kimi-{target}{".exe" if target.startswith("windows") else ""}'


def load_build_manifest(dist, version):
    """Read the manifest scripts/crossbuild.sh wrote and verify the binaries.

    A package must never mix a binary from an earlier build with a new version
    string. The manifest records the version and the digest of every binary that
    build produced, so both have to match before anything is archived.
    """
    manifest_path = dist / 'build-manifest.json'
    if not manifest_path.is_file():
        raise SystemExit(
            f'Missing {manifest_path.name}. Rebuild the binaries for {version} first: '
            f'VERSION={version} sh scripts/crossbuild.sh'
        )
    try:
        manifest = json.loads(manifest_path.read_text(encoding='utf-8'))
    except (OSError, json.JSONDecodeError) as error:
        raise SystemExit(
            f'{manifest_path.name} is not valid JSON ({error}). '
            f'Rebuild the binaries: VERSION={version} sh scripts/crossbuild.sh'
        )
    if not isinstance(manifest, dict):
        raise SystemExit(f'{manifest_path.name} must contain a JSON object')
    recorded = manifest.get('version')
    if recorded != version:
        raise SystemExit(
            f'{manifest_path.name} was built for version {recorded!r}, but this package is {version!r}. '
            f'Rebuild with: VERSION={version} sh scripts/crossbuild.sh'
        )
    entries = manifest.get('binaries')
    if not isinstance(entries, list) or not entries:
        raise SystemExit(f'{manifest_path.name} lists no binaries. Rebuild: VERSION={version} sh scripts/crossbuild.sh')
    digests = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise SystemExit(f'{manifest_path.name} has a malformed binary entry')
        name, digest = entry.get('name'), entry.get('sha256')
        if not isinstance(name, str) or not isinstance(digest, str):
            raise SystemExit(f'{manifest_path.name} has a binary entry without name/sha256')
        digests[name] = digest
    for target in TARGETS:
        name = binary_name(target)
        expected = digests.get(name)
        if expected is None:
            raise SystemExit(
                f'{manifest_path.name} does not record {name}. '
                f'Rebuild: VERSION={version} sh scripts/crossbuild.sh'
            )
        path = dist / name
        if not path.is_file():
            raise SystemExit(f'Missing cross build: {name}')
        actual = hashlib.sha256(path.read_bytes()).hexdigest()
        if actual != expected:
            raise SystemExit(
                f'{name} does not match {manifest_path.name} (built binary changed after the manifest was written). '
                f'Rebuild: VERSION={version} sh scripts/crossbuild.sh'
            )
    return manifest


def run_instructions(version, target, name):
    lines = [f'KimiCode+ {version}', '', 'Extract this archive into a folder you can write to.']
    if target.startswith('darwin'):
        lines.extend([
            'If macOS blocks the first run, verify the downloaded archive against SHA256SUMS.',
            'After trying to run it, open System Settings > Privacy & Security, choose Open Anyway, then confirm and retry.',
            'See https://support.apple.com/en-us/102445 for the macOS steps.',
            'Open the official Kimi Code desktop client, then run these commands in Terminal:',
            f'  chmod +x ./{name}',
            f'  ./{name} --allow-daily-instance',
        ])
    else:
        lines.extend([
            'Run this command in PowerShell to start the core and open or attach to the official client:',
            f'  .\\{name} launch --allow-daily-instance',
        ])
    lines.extend([
        '',
        'When running the core directly, use the pairing URL printed at startup to open its browser interface.',
        'Add --data-dir <path> to use a separate data directory. Keep runtime.json private.',
        'On macOS and Windows, enable automatic attachment under Status and Diagnostics to start the core at sign-in.',
        'For the latest version, open KimiCode+ settings and check for updates under Status and Diagnostics.',
    ])
    return ('\n'.join(lines) + '\n').encode('utf-8')


def write_archive(archive, entries):
    if archive.suffix == '.zip':
        with zipfile.ZipFile(archive, 'w') as output:
            for filename, data, mode in entries:
                info = zipfile.ZipInfo(filename)
                info.create_system = 3
                info.external_attr = (0o100000 | mode) << 16
                output.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    else:
        with archive.open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', compresslevel=9, mtime=0) as compressed, tarfile.open(fileobj=compressed, mode='w') as output:
            for filename, data, mode in entries:
                info = tarfile.TarInfo(filename)
                info.size, info.mode, info.mtime = len(data), mode, 0
                output.addfile(info, io.BytesIO(data))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', help='Release version (defaults to VERSION, then the core source version)')
    args = parser.parse_args()
    try:
        version = release_version(args.version or os.environ.get('VERSION') or source_version())
    except ValueError as error:
        parser.error(str(error))

    dist = ROOT / 'dist'
    notices = dist / 'THIRD_PARTY_NOTICES.txt'
    if not notices.is_file():
        raise SystemExit('Run scripts/notices.py first')
    notice_data = notices.read_bytes()
    license_data = (ROOT / 'LICENSE').read_bytes()
    # Every binary must be the one this version's cross build produced. Doing
    # this before the first archive is written means a mismatch leaves no
    # half-published release directory behind.
    load_build_manifest(dist, version)
    binaries = [dist / binary_name(target) for target in TARGETS]
    for binary in binaries:
        if not binary.is_file():
            raise SystemExit(f'Missing cross build: {binary.name}')

    release = dist / 'release'
    release.mkdir(exist_ok=True)
    manifest = []
    for target, binary in zip(TARGETS, binaries):
        extension = 'zip' if target.startswith('windows') else 'tar.gz'
        archive = release / f'enhanced-kimi-{version}-{target}.{extension}'
        binary_data = binary.read_bytes()
        entries = [
            (binary.name, binary_data, 0o755),
            ('LICENSE', license_data, 0o644),
            ('THIRD_PARTY_NOTICES.txt', notice_data, 0o644),
            ('RUN.txt', run_instructions(version, target, binary.name), 0o644),
        ]
        write_archive(archive, entries)
        manifest.append({
            'version': version,
            'target': target,
            'binaryName': binary.name,
            'archive': archive.name,
            'binaryBytes': len(binary_data),
            'packageBytes': archive.stat().st_size,
            'unpackedBytes': sum(len(data) for _, data, _ in entries),
            'sha256': hashlib.sha256(archive.read_bytes()).hexdigest(),
        })
    manifest_json = json.dumps(manifest, indent=2)
    (release / 'manifest.json').write_text(manifest_json + '\n', encoding='utf-8')
    (release / 'SHA256SUMS').write_text(''.join(f'{entry["sha256"]}  {entry["archive"]}\n' for entry in manifest), encoding='utf-8')
    print(manifest_json)


if __name__ == '__main__':
    main()
