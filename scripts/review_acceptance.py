#!/usr/bin/env python3
"""0.1.1 regressions against an isolated release executable, without inference."""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
import urllib.error
import urllib.request
from acceptance import Core, OPENER
from scenarios import task_for, submit_previewed


def raw_request(core, path, data=None, headers=None, expected=200):
    values = {'Authorization': 'Bearer ' + core.token}
    values.update(headers or {})
    req = urllib.request.Request(core.base + '/enhance/v1' + path, data=data, headers=values)
    try:
        response = OPENER.open(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    payload = response.read()
    assert response.status == expected, (path, response.status, payload[:200])
    return payload


def run(binary, public):
    results = []
    with tempfile.TemporaryDirectory(prefix='review-binary-', dir='work') as folder:
        core = Core(binary, folder)
        try:
            provider = {'id': 'my.gateway', 'name': 'Local instance', 'protocol': 'openai', 'baseUrl': 'http://127.0.0.1:11434/v1'}
            core.request('/providers', provider, expected=201)
            for bad in ['https://example.com/v1?key=secret', 'http://example.com/v1']:
                core.request('/providers', {**provider, 'id': 'bad', 'baseUrl': bad}, expected=400)
            records = [{'providerId': 'my.gateway', 'id': 'review-model', 'name': 'Synthetic test metadata', 'capabilities': {'context': {'value': 8192, 'source': 'registry', 'fetchedAt': '2026-11-11T00:00:00Z'}}}]
            before = core.request('/models/preview', {'records': records})
            imported = core.request('/models/import', {'records': records, 'revision': before['revision']})
            repeated = core.request('/models/import', {'records': records, 'revision': imported['revision']})
            assert repeated['revision'] == imported['revision']
            pair = core.request('/pair', {'pairingToken': core.token}, token=False)
            snap = core.request('/registry/snapshot?providerId=my.gateway', headers={'Authorization': 'Bearer ' + pair['sessionToken']})
            edit = {k: snap['provider'].get(k) for k in ['name', 'protocol', 'baseUrl', 'secretRef']}
            edit.update(name='Edited after no-op and re-pair', revision=snap['revision'])
            saved = core.request('/providers/my.gateway', edit, method='PUT')
            core.request('/providers/my.gateway', {**edit, 'name': 'Stale editor'}, method='PUT', expected=409)
            rollback = core.request('/model-changes/' + str(imported['changeId']) + '/preview')
            model = core.request('/models')['items'][0]
            model_id = model['id']
            baseline = core.request('/registry/snapshot?providerId=my.gateway&id=' + model_id)
            override = core.request('/models/override', {'providerId': 'my.gateway', 'id': model_id, 'field': 'context', 'value': None, 'revision': baseline['revision']})
            core.request('/models/override', {'providerId': 'my.gateway', 'id': model_id, 'field': 'output', 'value': None, 'revision': baseline['revision']}, expected=409)
            conflict = core.request('/model-changes/' + str(imported['changeId']) + '/preview')
            assert conflict['conflicts']
            core.request('/model-changes/' + str(imported['changeId']) + '/rollback', {'revision': override['revision']}, expected=409)
            restored = core.request('/models/metadata', {'providerId': 'my.gateway', 'id': model_id, 'field': 'context', 'restoreAutomatic': True, 'revision': override['revision']})
            assert restored['model']['capabilities']['context']['source'] != 'manual'
            named = core.request('/models/metadata', {'providerId': 'my.gateway', 'id': model_id, 'field': 'displayName', 'value': 'My chosen name', 'revision': restored['revision']})
            id_only = [{'providerId': 'my.gateway', 'id': model_id, 'capabilities': {}}]
            kept = core.request('/models/import', {'records': id_only, 'revision': named['revision']})
            assert core.request('/registry/snapshot?providerId=my.gateway&id=' + model_id)['model']['name'] == 'My chosen name'
            with sqlite3.connect(Path(folder) / 'enhancement.db') as db:
                assert db.execute('SELECT base_url FROM registry_providers WHERE id=?', ('my.gateway',)).fetchone()[0] == provider['baseUrl']
            results.append('F01 F02 F03 F07: loopback, dotted ID, no-op re-pair edit, stale provider/capability rejection, rollback conflict, name and automatic restore, SQLite')

            data = b'original durable attachment\x00\x01\xff\n'
            digest = hashlib.sha256(data).hexdigest()
            blob = json.loads(raw_request(core, '/attachments', data, {'Content-Type': 'application/octet-stream', 'X-Attachment-SHA256': digest, 'X-Attachment-Type': 'application/octet-stream'}, expected=201))
            assert blob['status'] == 'STORED'
            task = task_for(core.request('/sessions')['items'][0], 'review-file')
            task['attachments'] = [{**blob, 'name': 'selected.bin'}]
            preview = core.request('/tasks/preview', {'task': task})
            assert 'dataBase64' not in json.dumps(preview)
            core.stop()
            core = Core(binary, folder)
            out = core.request('/tasks', {'task': preview['task'], 'intent': 'review-file:1', 'confirmed': True, 'confirmationHash': preview['confirmationHash']})
            assert out['state'] == 'DELIVERED' and out['receipt']['transport'] == 'MOCK_CODE'
            assert out['receipt']['attachments'][0]['status'] == 'UPLOADED'
            download = '/tasks/' + out['id'] + '/attachments/' + blob['blobRef']
            assert hashlib.sha256(raw_request(core, download)).hexdigest() == digest
            core.request('/attachments/collect', {})
            assert raw_request(core, download) == data
            raw_request(core, '/tasks/' + out['id'] + '/attachments/unapproved', expected=404)
            scoped = core.request('/pair', {'pairingToken': core.token, 'permissions': ['models:read']}, token=False)
            raw_request(core, download, headers={'Authorization': 'Bearer ' + scoped['sessionToken']}, expected=403)
            results.append('F04: binary upload, byte-free preview, restart before dispatch, validated attachment receipt, scoped durable download and GC retention')

            session = core.request('/sessions')['items'][0]
            bad = task_for(session, 'wrong-project')
            bad['projectId'] = 'another-project'
            core.request('/tasks', {'task': bad, 'intent': 'wrong-project:1', 'confirmed': True}, expected=409)
            web = task_for(session, 'web-target')
            web['direction'] = 'CODE_TO_CHAT'
            web['webTarget'] = core.request('/web-targets')['items'][0]
            wrong = copy.deepcopy(web)
            wrong['webTarget']['threadId'] = 'different-thread'
            core.request('/tasks', {'task': wrong, 'intent': 'wrong-web:1', 'confirmed': True}, expected=409)
            delivered = submit_previewed(core, web, 'web-target:1')
            assert delivered['receipt']['transport'] == 'MOCK_WEB'
            settings = core.request('/settings')
            core.request('/settings', {**settings, 'safeMode': True}, method='PUT')
            core.request('/tasks', {'task': task_for(session, 'safe-block'), 'intent': 'safe-block:1', 'confirmed': True}, expected=409)
            settings = core.request('/settings')
            core.request('/settings', {**settings, 'safeMode': False}, method='PUT')
            results.append('F05: actual project and explicit web target binding; separate MOCK_CODE/MOCK_WEB receipts; safe mode refusal')

            event = {'providerId': 'synthetic', 'model': 'm', 'requestId': 'life', 'attemptId': '1', 'sessionId': 'isolated', 'projectId': 'review', 'agentId': 'main', 'role': 'main', 'protocol': 'openai', 'source': 'gateway', 'at': '2026-11-11T00:00:00Z', 'observationState': 'PARTIAL', 'sequence': 1, 'counts': {'input': 100}}
            core.request('/usage', {'events': [event]})
            final = {**event, 'observationState': 'FINAL', 'sequence': 2, 'counts': {'input': 100, 'output': 20}}
            assert core.request('/usage', {'events': [final]})['updated'] == 1
            assert core.request('/usage', {'events': [final]})['duplicates'] == 1
            core.request('/usage', {'events': [event]})
            conflict = {**event, 'source': 'official', 'sequence': 3, 'counts': {'input': 120}}
            assert core.request('/usage', {'events': [conflict]})['conflicts'] == 1
            retry = {**final, 'attemptId': '2'}
            assert core.request('/usage', {'events': [retry]})['added'] == 1
            items = core.request('/usage')['items']
            assert len(items) == 2 and all(v['output'] == 20 for v in items)
            core.request('/usage/demo', {})
            clean = core.request('/usage/demo/clear', {})
            assert clean['events'] == 6 and core.request('/usage')['total'] == 2
            results.append('F06: partial completion, terminal deduplication, stale observation, conflicting partial, retry attempt and selective demo cleanup')

            if public:
                status = core.request('/models/catalog/refresh', {})
                assert status['state'] == 'FETCHED' and status['bytes'] > 0
                body = {'sourceProvider': 'moonshotai', 'targetProvider': 'my.gateway', 'preview': True, 'revision': core.request('/registry/snapshot')['revision']}
                p = core.request('/models/catalog/import', body)
                applied = core.request('/models/catalog/import', {**body, 'preview': False, 'revision': p['revision'], 'catalogHash': p['catalogHash']})
                assert applied['imported'] > 0
                conditional = core.request('/models/catalog/refresh', {})
                assert conditional['sha256'] == status['sha256']
                print('PUBLIC_METADATA ' + json.dumps({'source': status['source'], 'state': status['state'], 'bytes': status['bytes'], 'sha256': status['sha256'], 'retrievedAt': status['retrievedAt'], 'checkedAt': status['checkedAt'], 'etag': status['etag'], 'conditionalState': conditional['state'], 'imported': applied['imported'], 'inferenceRequests': 0}))
                results.append('real fixed-source public HTTP fetch, source hash, explicit instance import, conditional cache; zero inference')
            core.stop()
            core = Core(binary, folder)
            assert core.request('/usage')['total'] == 2
            assert raw_request(core, download) == data
            assert core.request('/registry/snapshot?providerId=my.gateway')['provider']['name'] == 'Edited after no-op and re-pair'
            results.append('final restart recovers registry, usage and original attachment bytes')
        finally:
            core.stop()
    for item in results:
        print('PASS ' + item)
    print(json.dumps({'passed': len(results), 'failed': 0, 'publicFetchExecuted': public, 'binarySHA256': hashlib.sha256(binary.read_bytes()).hexdigest()}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    parser.add_argument('--public', action='store_true')
    args = parser.parse_args()
    run(Path(args.binary).resolve(), args.public)
