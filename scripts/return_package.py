#!/usr/bin/env python3
"""Assemble the reviewed 0.1.1 return with source, candidate and test bindings."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parents[1]
WORK = ROOT / 'work/review-0.1.1'
EVIDENCE = WORK / 'evidence'
STAGE = WORK / 'return'
BASE = '98624444347ae60089461cc539acb4dc855269f1'
CANDIDATE = '81b3f93656e73c2a980009c68f7ec43c9f27eff0481e98f94ef168641c6e893b'
CONFIG = 'a362b403254693a3ac6f03338bc65ad5b9888408'
BRIDGE = 'dcff2036c1972d20571ced71cce982412531acf5'
USAGE = '127620dfb1e225968fdd149559de44e37c0a71c1'
PUBLIC = '9a3ff361fcc851d9c3080624099cbe3b92db20f6'
BOUNDARY = '6d0b367bb8163c0a8e1b4210141b619ad8bebc6b'
BINDING = 'b05a085356d8c3d83681be31775758761652e7b1'


def sha(data):
    return hashlib.sha256(data).hexdigest()


def git(*args):
    return subprocess.check_output(['git', *args], cwd=ROOT)


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def archive_files(destination, entries):
    with zipfile.ZipFile(destination, 'w', zipfile.ZIP_DEFLATED, compresslevel=9) as output:
        for name, data, mode in sorted(entries):
            info = zipfile.ZipInfo(name, date_time=(2026, 11, 11, 0, 0, 0))
            info.external_attr = mode << 16
            output.writestr(info, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)


def sanitize(data):
    for original, replacement in [(str(ROOT), '<PROJECT>'), (str(Path.home()), '<USER_HOME>')]:
        data = data.replace(original.encode(), replacement.encode())
    return data


def main():
    assert not git('status', '--porcelain').strip(), 'Commit the final source before packaging'
    commit = git('rev-parse', 'HEAD').decode().strip()
    if STAGE.exists():
        shutil.rmtree(STAGE)
    for folder in ['source', 'release', 'evidence', 'patches']:
        (STAGE / folder).mkdir(parents=True, exist_ok=True)
    names = [v for v in git('ls-files', '-z').decode().split('\0') if v]
    source = []
    manifest = []
    for name in names:
        path = ROOT / name
        if not path.is_file():
            continue
        data = path.read_bytes()
        assert str(Path.home()).encode() not in data, 'Private path in source: ' + name
        assert path.name != 'runtime.json' and not name.endswith(('.db', '.db-wal', '.db-shm'))
        manifest.append({'path': name, 'bytes': len(data), 'sha256': sha(data)})
        source.append((name, data, 0o755 if path.stat().st_mode & 0o111 else 0o644))
    source_zip = STAGE / 'source/Enhanced-Kimi-Desktop-0.1.1-source.zip'
    archive_files(source_zip, source)
    write_json(STAGE / 'source_manifest.json', {'commit': commit, 'dirty': False, 'files': manifest})
    (STAGE / 'dirty.patch').write_bytes(git('diff', 'HEAD'))
    commits = []
    for line in git('log', '--reverse', '--format=%H %P %s', BASE + '..HEAD').decode().splitlines():
        fields = line.split(' ', 2)
        item = {'commit': fields[0], 'parent': fields[1], 'subject': fields[2]}
        commits.append(item)
        patch = git('diff', '--binary', item['parent'], item['commit'], '--', 'cmd', 'internal', 'web', 'adapters', 'contracts', 'scripts', '.github', 'go.mod', 'go.sum')
        assert str(Path.home()).encode() not in patch
        (STAGE / 'patches' / (item['commit'][:7] + '.patch')).write_bytes(patch)
    write_json(STAGE / 'commits.json', {'baseline': BASE, 'final': commit, 'commits': commits, 'patchScope': 'Production code, tests, build and CI only. Documentation is supplied in the complete final source; Git identity metadata is omitted.'})

    labels = [
        'config-red', 'dispatch-red', 'usage-red', 'attachment-red', 'frontend-baseline-reproduced',
        'config-contracts', 'bridge-final', 'usage-final', 'public-live', 'public-offline', 'public-pairing-platform',
        'catalog-boundaries', 'cleanup-go', 'cleanup-frontend', 'final-go-race', 'final-vet', 'final-frontend',
        'release-typecheck', 'release-build', 'candidate-review', 'candidate-acceptance', 'packaged-review',
        'packaged-acceptance', 'candidate-performance', 'crossbuild', 'release-packaging', 'release-build-info',
        'release-file-types', 'ui-detector', 'ui-sqlite-final', 'ui-concurrent-write', 'ui-final-sqlite', 'ui-cleanup-sqlite',
        'final-source-build',
    ]
    exported = []
    tests = []
    expected_failures = {'config-red', 'dispatch-red', 'usage-red', 'attachment-red'}
    for label in labels:
        record_path = EVIDENCE / (label + '.json')
        record = json.loads(record_path.read_text())
        log = EVIDENCE / (label + '.log')
        assert record['exitCode'] == (1 if label in expected_failures else 0), (label, record['exitCode'])
        for path in [record_path, log, EVIDENCE / (label + '.source.json')]:
            if not path.exists():
                continue
            raw = path.read_bytes()
            data = sanitize(raw)
            (STAGE / 'evidence' / path.name).write_bytes(data)
            exported.append({'file': path.name, 'originalSHA256': sha(raw), 'exportedSHA256': sha(data), 'privacyPathRedacted': raw != data})
        kind = 'UI_E2E' if label.startswith('ui-') else 'ISOLATED_RUNTIME' if label.startswith(('candidate-', 'packaged-')) else 'SOURCE'
        tests.append({'name': label, 'command': json.loads(sanitize(json.dumps(record['command']).encode())), 'cwd': '.', 'exitCode': record['exitCode'], 'expectedFailureReproduction': label in expected_failures, 'log': 'evidence/' + log.name, 'evidenceClass': kind, 'sourceCommitAtExecution': record.get('sourceCommit', 'not recorded by early wrapper'), 'sourceManifest': record.get('sourceManifest'), 'testedBinarySHA256': record.get('testedBinarySHA256')})
    extras = ['final-test-counts.json', 'baseline-inventory.json', 'race-source-comparison.json', 'ui-observations.json', 'performance.json', 'ui-stale-draft.png', 'ui-final-usage-clean.png', 'final-binding.json']
    for name in extras:
        raw = (EVIDENCE / name).read_bytes()
        data = raw if name.endswith('.png') else sanitize(raw)
        (STAGE / 'evidence' / name).write_bytes(data)
        exported.append({'file': name, 'originalSHA256': sha(raw), 'exportedSHA256': sha(data), 'privacyPathRedacted': raw != data})
    for name in ['frontend-review.mjs']:
        shutil.copyfile(WORK / name, STAGE / 'evidence' / name)
    (STAGE / 'evidence/baseline-attachment-regression_test.go').write_bytes((WORK / 'baseline/internal/bridge/review_attachment_test.go').read_bytes())
    raw_public = (WORK / 'public-api.json').read_bytes()
    (STAGE / 'evidence/public-api-initial.json').write_bytes(raw_public)
    write_json(STAGE / 'evidence/public-sample.json', {'source': 'https://models.dev/api.json', 'initialRawSHA256': sha(raw_public), 'fixture': json.loads((ROOT / 'internal/registry/testdata/public-modelsdev.json').read_text()), 'note': 'Saved real structure; the public directory can change between later fetches.'})
    write_json(STAGE / 'evidence/export_manifest.json', {'pathRedactionOnly': True, 'entries': exported, 'note': 'Raw test output is preserved except the local user/workspace path. Original and exported hashes are both retained; result lines and exit codes are unchanged.'})

    releases = json.loads((ROOT / 'dist/release/manifest.json').read_text())
    candidate_files = []
    for item in releases:
        archive = ROOT / 'dist/release' / item['archive']
        assert sha(archive.read_bytes()) == item['sha256']
        shutil.copyfile(archive, STAGE / 'release' / archive.name)
        binary_name = 'enhanced-kimi-' + item['target'] + ('.exe' if item['target'].startswith('windows') else '')
        binary_data = (ROOT / 'dist' / binary_name).read_bytes()
        candidate_files.append({**item, 'binarySHA256': sha(binary_data), 'build': 'PASS', 'nativeRun': 'PASS' if item['target'] == 'darwin-arm64' else 'NOT_RUN'})
    assert candidate_files[0]['binarySHA256'] == CANDIDATE
    assert sha((WORK / 'packaged/enhanced-kimi-darwin-arm64').read_bytes()) == CANDIDATE
    for label in ['candidate-review', 'candidate-acceptance', 'packaged-review', 'packaged-acceptance', 'candidate-performance']:
        assert json.loads((EVIDENCE / (label + '.json')).read_text())['testedBinarySHA256'] == CANDIDATE
    write_json(STAGE / 'candidate_manifest.json', {'sourceCommit': commit, 'dirty': False, 'sourceArchive': str(source_zip.relative_to(STAGE)), 'sourceArchiveSHA256': sha(source_zip.read_bytes()), 'testedBinarySHA256': CANDIDATE, 'artifacts': candidate_files})
    shutil.copyfile(ROOT / 'dist/release/SHA256SUMS', STAGE / 'release/SHA256SUMS')
    for name in ['CHANGELOG_REVIEW.md']:
        shutil.copyfile(ROOT / name, STAGE / name)

    finding_specs = [
        ('F01', CONFIG, ['config-red.log', 'frontend-baseline-reproduced.log'], ['TestReviewEndpointPolicy', 'TestReviewSharedProviderPolicy'], 'Only local configuration; no provider inference or keyed discovery verification.'),
        ('F02', CONFIG, ['config-red.log', 'frontend-baseline-reproduced.log'], ['TestReviewNoopImportKeepsRevision', 'TestReviewSnapshotsGapAndTwoEditors'], 'Local registry revisions do not claim official cross-process CAS.'),
        ('F03', CONFIG, ['config-red.log', 'frontend-baseline-reproduced.log'], ['TestReviewRollbackRejectsLaterEdit', 'TestReviewSnapshotsGapAndTwoEditors', 'fixed edit baseline survives unrelated live snapshot updates'], 'Conservative whole-entity rollback conflict refusal; no forced merge.'),
        ('F04', BRIDGE, ['attachment-red.log'], ['TestReviewAttachmentPreviewPersistsBytes', 'TestReviewAttachmentRestartScopeAndHTTPReceiver'], 'Native Kimi/website upload remains BLOCKED; receipt transport explicitly Mock.'),
        ('F05', BRIDGE, ['dispatch-red.log'], ['TestReviewSafeModeWinsBeforeDispatch', 'TestReviewProjectBindingRequired', 'TestDispatchAndSafetySwitchAreOrdered', 'TestReviewWebTargetAndTransportIsolation'], 'An admitted call completes before safe-mode write; real target acceptance/query contract remains unverified.'),
        ('F06', USAGE, ['usage-red.log'], ['TestReviewLaterUsageCompletionNotIgnored', 'TestObservationLifecycleOrderingConflictsAndRetry', 'TestNormalizeOverflowStaysUnknown'], 'No actual provider/official usage subscription connected.'),
        ('F07', CONFIG, ['config-red.log'], ['TestReviewIDOnlyRefreshPreservesName', 'TestReviewAutomaticRestoreAndNames'], 'Automatic candidates already lost in 0.1.0 cannot be reconstructed; absent remains UNKNOWN.'),
        ('F08', BINDING, ['baseline-inventory.json'], ['final source manifest verification', 'packaged binary equality', 'return archive inventory and hash verification'], 'Other OS native runtime, Windows ACL, code signing and remote CI remain NOT_RUN.'),
    ]
    findings = []
    for ident, fix, first, regression, remaining in finding_specs:
        findings.append({'id': ident, 'status': 'fixed', 'firstReproduction': ['evidence/' + p for p in first], 'fixCommit': fix, 'additionalCommits': [BOUNDARY] if ident in ['F01', 'F02', 'F06'] else [], 'regressionTests': regression, 'rawEvidence': ['evidence/final-go-race.log', 'evidence/packaged-review.log'] if ident != 'F08' else ['evidence/export_manifest.json', 'candidate_manifest.json', 'source_manifest.json'], 'remainingScope': remaining})
    public_line = next(v for v in (EVIDENCE / 'packaged-review.log').read_text().splitlines() if v.startswith('PUBLIC_METADATA '))
    public = json.loads(public_line.removeprefix('PUBLIC_METADATA '))
    counts = json.loads((EVIDENCE / 'final-test-counts.json').read_text())
    status = {'schemaVersion': 1, 'project': 'Enhanced Kimi Desktop', 'version': '0.1.1',
              'source': {'commit': commit, 'baselineCommit': BASE, 'dirty': False, 'dirtyPatch': 'dirty.patch', 'manifestPath': 'source_manifest.json', 'archiveSHA256': sha(source_zip.read_bytes()), 'archive': str(source_zip.relative_to(STAGE))},
              'candidate': {'platform': 'darwin-arm64', 'binarySHA256': CANDIDATE, 'archiveSHA256': releases[0]['sha256'], 'testedBinarySHA256': CANDIDATE},
              'findings': findings, 'tests': tests, 'testCounts': counts,
              'toolchain': {'go': 'go1.27.3 darwin/arm64', 'node': 'v24.19.0', 'npm': '10.9.2', 'CGO_ENABLED': '0', 'runtimeDependencies': 'No Node, Rust, Electron, Chromium or plugin-host runtime included. System browser required for UI.'},
              'publicMetadata': {'actualFetchPerformed': True, **public, 'rawEvidence': 'evidence/packaged-review.log', 'savedInitialResponse': 'evidence/public-api-initial.json'},
              'platforms': [{'platform': i['target'], 'build': 'PASS', 'nativeRun': i['nativeRun'], 'evidence': ['evidence/crossbuild.log', 'evidence/release-file-types.log'] + (['evidence/packaged-acceptance.log', 'evidence/packaged-review.log', 'evidence/ui-observations.json'] if i['target'] == 'darwin-arm64' else [])} for i in candidate_files],
              'performance': json.loads((EVIDENCE / 'performance.json').read_text()),
              'native': {'authorizedScope': 'Independent core, synthetic sessions, local test HTTP and public metadata only', 'currentDevelopmentKimiModified': False, 'realCredentialsAccessed': False, 'realModelRequests': 0, 'capabilitiesVerified': ['Synthetic protocol-1 isolated loopback pairing and read-only capability report'], 'blocked': ['Official permitted pairing entry', 'Real Code/Web delivery and attachments', 'OS CredentialStore', 'Cookie migration', 'Native compaction', 'Multi-session/Tower', 'Real KCT/Monitor integration']},
              'notes': ['No public release approval inferred. Packages are unsigned/unnotarized.', 'Cross builds are not native runtime validation. Windows ACL and remote CI are NOT_RUN.', 'Expected RED failures reproduce the reviewed baseline; final checks have zero failures.', 'UI observations identify both intermediate and final binary hashes.', 'Test output paths are privacy-redacted only; original/exported hashes are retained.'],
              'nextPriority': 'Authorize and identify an isolated permitted Desktop pairing/read-only capability channel; do not connect the current development instance.'}
    write_json(STAGE / 'REVIEW_STATUS.json', status)
    (STAGE / 'RETURN_README.md').write_text('''# Enhanced Kimi Desktop 0.1.1 Return

这是本轮稳定化源码和未签名候选，不代表原生 Desktop 已就绪或获准公开发布。

- `source/`：唯一完整源码 ZIP，含 Go/TS/React、锁文件、构建/验收/CI 与架构文档。解压后按 README 构建。
- `release/`：五平台各一份发布包，单一 Go 核心 + 内嵌 UI + 许可证 + RUN.txt，无 Node/Chromium。
- `CHANGELOG_REVIEW.md`：F01–F08 首次反例、根因、文件、提交、回归与剩余范围。
- `REVIEW_STATUS.json`：最终 clean commit、实际测试/skip/blocked、公开目录、平台、性能与下一优先任务。
- `source_manifest.json`、`candidate_manifest.json`、`commits.json`、`patches/`：源码清单、候选 SHA、提交关系和仅代码/测试/构建 patch。完整文档位于最终源码。
- `evidence/`：实际命令日志和退出码、RED 反例、源码清单、UI 观察和截图、性能、公共响应及样本。日志仅匿名化本机路径，保留原始/导出 SHA；不是重新编写的汇总日志。

## 使用与验证

解压本平台 release 包后启动程序，建议先指定新的私有数据目录：

```bash
./enhanced-kimi-darwin-arm64 --data-dir ./enhanced-data
```

从自己的 enhanced-data/runtime.json 打开 pairingUrl。运行文件是本增强服务凭据，勿分享。默认不带 --data-dir 时使用 OS 用户配置目录下 EnhancedKimiDesktop。Ctrl+C 只关闭增强核心。

源码目录命令：

```bash
sh scripts/build.sh
go test -race -count=1 ./...
go vet ./...
(cd web && npm run typecheck && npm test)
python3 scripts/acceptance.py --binary ./dist/enhanced-kimi
python3 scripts/review_acceptance.py --binary ./dist/enhanced-kimi --public
sh scripts/crossbuild.sh
python3 scripts/package.py
```

--public 只读取固定 models.dev 公共元数据，不发推理。依赖仅在源码构建时需要 Go/Node/npm/Python；正式运行不需要开发工具。网页入口是 URL_ONLY，Code/Web 交付、插件执行和轻量回复为 Mock。原生授权门槛见源码 docs/NATIVE_INTEGRATION.md。

Mac ARM 包内已实际验证。其他平台只有交叉构建，Windows ACL、签名公证和远程 CI 未验。不要将此包中的 synthetic fixtures 当作真实 Key、Cookie 或账号数据；返回包未包含运行凭据、数据库或官方数据。
''')
    payload = []
    for path in sorted(STAGE.rglob('*')):
        if path.is_file():
            data = path.read_bytes()
            assert str(Path.home()).encode() not in data, 'Private path in return: ' + str(path.relative_to(STAGE))
            assert path.name != 'runtime.json' and not path.name.endswith(('.db', '.db-shm', '.db-wal'))
            payload.append({'path': path.relative_to(STAGE).as_posix(), 'bytes': len(data), 'sha256': sha(data)})
    write_json(STAGE / 'return_manifest.json', {'files': payload})
    archive = ROOT.parent / 'EKD_0.1.1_Return.zip'
    entries = [(p.relative_to(STAGE).as_posix(), p.read_bytes(), 0o644) for p in STAGE.rglob('*') if p.is_file()]
    archive_files(archive, entries)
    with zipfile.ZipFile(archive) as output:
        assert output.testzip() is None
        for item in payload:
            assert sha(output.read(item['path'])) == item['sha256']
        with zipfile.ZipFile(source_zip) as src:
            for item in manifest:
                assert sha(src.read(item['path'])) == item['sha256']
    digest = sha(archive.read_bytes())
    archive.with_suffix('.zip.sha256').write_text(digest + '  ' + archive.name + '\n')
    print(json.dumps({'archive': archive.name, 'sha256': digest, 'bytes': archive.stat().st_size, 'sourceCommit': commit, 'sourceFiles': len(manifest), 'returnFiles': len(entries), 'candidateSHA256': CANDIDATE, 'verification': 'PASS'}, indent=2))


if __name__ == '__main__':
    main()
