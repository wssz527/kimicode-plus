#!/usr/bin/env python3
"""Tests for release packaging and update metadata in scripts/package.py.

Every case runs in a temporary directory: the real dist/ is never read or
written, because a packaging test must not be able to publish anything.

Run with: python3 scripts/test_package.py
"""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('darwin-arm64', 'darwin-amd64', 'windows-amd64')


def binary_name(target):
    return f'enhanced-kimi-{target}{".exe" if target.startswith("windows") else ""}'


class PackageHarness:
    """A throwaway project root with just enough for package.py to run."""

    def __init__(self, version='0.2.0'):
        self.version = version
        self.root = Path(tempfile.mkdtemp(prefix='ekd-package-test-'))
        (self.root / 'scripts').mkdir()
        (self.root / 'internal' / 'server').mkdir(parents=True)
        (self.root / 'dist').mkdir()
        shutil.copy2(ROOT / 'scripts' / 'package.py', self.root / 'scripts' / 'package.py')
        (self.root / 'LICENSE').write_text('license\n', encoding='utf-8')
        (self.root / 'internal' / 'server' / 'server.go').write_text(
            f'package server\n\nvar Version = "{version}"\n', encoding='utf-8'
        )
        (self.root / 'dist' / 'THIRD_PARTY_NOTICES.txt').write_text('notices\n', encoding='utf-8')
        self.binaries = {}
        for target in TARGETS:
            path = self.root / 'dist' / binary_name(target)
            path.write_bytes(f'binary for {target}\n'.encode('utf-8'))
            self.binaries[binary_name(target)] = path

    def write_manifest(self, version=None, digests=None, include_commit=True):
        entries = []
        for target in TARGETS:
            name = binary_name(target)
            if digests is not None and name in digests and digests[name] is None:
                continue
            data = self.binaries[name].read_bytes()
            digest = (digests or {}).get(name) or hashlib.sha256(data).hexdigest()
            entries.append({
                'target': target,
                'name': name,
                'bytes': len(data),
                'sha256': digest,
            })
        manifest = {'version': version if version is not None else self.version, 'binaries': entries}
        if include_commit:
            manifest['gitCommit'] = 'a' * 40
        (self.root / 'dist' / 'build-manifest.json').write_text(
            json.dumps(manifest, indent=2) + '\n', encoding='utf-8'
        )

    def run(self, *args):
        return subprocess.run(
            [sys.executable, str(self.root / 'scripts' / 'package.py'), *args],
            cwd=self.root, capture_output=True, text=True, timeout=120,
        )

    def cleanup(self):
        shutil.rmtree(self.root, ignore_errors=True)


class PackageManifestGateTest(unittest.TestCase):
    def setUp(self):
        self.harness = PackageHarness()

    def tearDown(self):
        self.harness.cleanup()

    def test_matching_manifest_packages_successfully(self):
        self.harness.write_manifest()
        result = self.harness.run('--version', self.harness.version)
        self.assertEqual(result.returncode, 0, result.stderr)
        release = self.harness.root / 'dist' / 'release'
        self.assertTrue((release / 'manifest.json').is_file())
        self.assertTrue((release / 'SHA256SUMS').is_file())
        for target in TARGETS:
            extension = 'zip' if target.startswith('windows') else 'tar.gz'
            archive = release / f'enhanced-kimi-{self.harness.version}-{target}.{extension}'
            self.assertTrue(archive.is_file(), archive.name)
        published = json.loads((release / 'manifest.json').read_text(encoding='utf-8'))
        self.assertEqual([entry['version'] for entry in published], [self.harness.version] * len(TARGETS))

    def test_release_metadata_is_written_to_every_manifest_entry(self):
        self.harness.write_manifest()
        with mock.patch.dict(os.environ, {
            'RELEASE_PUBLISHED_AT': '2026-11-22T08:00:00Z',
            'RELEASE_NOTES': '修复更新检查\n\n保留 SHA-256 校验',
        }):
            result = self.harness.run('--version', self.harness.version)
        self.assertEqual(result.returncode, 0, result.stderr)
        published = json.loads(
            (self.harness.root / 'dist' / 'release' / 'manifest.json').read_text(encoding='utf-8')
        )
        for entry in published:
            self.assertEqual(entry['publishedAt'], '2026-11-22T08:00:00Z')
            self.assertEqual(entry['releaseNotes'], '修复更新检查\n\n保留 SHA-256 校验')

    def test_missing_manifest_is_refused_with_a_rebuild_hint(self):
        result = self.harness.run('--version', self.harness.version)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('build-manifest.json', result.stderr)
        self.assertIn(f'VERSION={self.harness.version} sh scripts/crossbuild.sh', result.stderr)
        self.assertFalse((self.harness.root / 'dist' / 'release' / 'manifest.json').exists())

    def test_invalid_json_is_refused(self):
        (self.harness.root / 'dist' / 'build-manifest.json').write_text('{not json', encoding='utf-8')
        result = self.harness.run('--version', self.harness.version)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not valid JSON', result.stderr)
        self.assertIn('crossbuild.sh', result.stderr)
        self.assertFalse((self.harness.root / 'dist' / 'release' / 'manifest.json').exists())

    def test_version_mismatch_is_refused(self):
        self.harness.write_manifest(version='0.1.9')
        result = self.harness.run('--version', self.harness.version)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("built for version '0.1.9'", result.stderr)
        self.assertIn(f"VERSION={self.harness.version} sh scripts/crossbuild.sh", result.stderr)
        self.assertFalse((self.harness.root / 'dist' / 'release' / 'manifest.json').exists())

    def test_digest_mismatch_is_refused(self):
        self.harness.write_manifest()
        # A stale binary from an older build: same name, different bytes.
        (self.harness.root / 'dist' / binary_name('darwin-arm64')).write_bytes(b'stale build\n')
        result = self.harness.run('--version', self.harness.version)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(binary_name('darwin-arm64'), result.stderr)
        self.assertIn('does not match', result.stderr)
        self.assertIn(f'VERSION={self.harness.version} sh scripts/crossbuild.sh', result.stderr)
        self.assertFalse((self.harness.root / 'dist' / 'release' / 'manifest.json').exists())

    def test_manifest_that_omits_a_binary_is_refused(self):
        self.harness.write_manifest(digests={binary_name('windows-amd64'): None})
        result = self.harness.run('--version', self.harness.version)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(binary_name('windows-amd64'), result.stderr)
        self.assertIn('does not record', result.stderr)
        self.assertFalse((self.harness.root / 'dist' / 'release' / 'manifest.json').exists())

    def test_version_flag_overrides_the_source_version(self):
        # The packaging target comes from --version, so a manifest for that
        # version is what has to match - not whatever server.go currently says.
        self.harness.write_manifest(version='0.3.0')
        result = self.harness.run('--version', '0.3.0')
        self.assertEqual(result.returncode, 0, result.stderr)
        published = json.loads(
            (self.harness.root / 'dist' / 'release' / 'manifest.json').read_text(encoding='utf-8')
        )
        self.assertEqual({entry['version'] for entry in published}, {'0.3.0'})

    def test_v_prefixed_version_matches_an_unprefixed_manifest(self):
        self.harness.write_manifest(version='0.2.0')
        result = self.harness.run('--version', 'v0.2.0')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_the_real_dist_is_never_touched(self):
        # The harness builds its own root, so nothing above can reach the
        # repository's dist/. Proving it here keeps a future refactor from
        # pointing the tests at the real output directory.
        real_dist = ROOT / 'dist'
        before = sorted(p.name for p in real_dist.iterdir()) if real_dist.is_dir() else []
        self.harness.write_manifest()
        result = self.harness.run('--version', self.harness.version)
        self.assertEqual(result.returncode, 0, result.stderr)
        after = sorted(p.name for p in real_dist.iterdir()) if real_dist.is_dir() else []
        self.assertEqual(before, after)


if __name__ == '__main__':
    unittest.main(verbosity=2)
