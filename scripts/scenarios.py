"""Synthetic business workflows for release-binary acceptance."""
import base64
import copy
import hashlib
import json


def task_for(session, task_id='acceptance-loop'):
    return {
        'version': 1, 'taskId': task_id, 'direction': 'CHAT_TO_CODE',
        'sessionId': session['id'], 'projectId': session.get('projectId') or 'mock-project',
        'source': {'site': 'kimi', 'url': 'https://example.invalid/synthetic-chat', 'threadId': 'synthetic-thread', 'messageIds': ['m1']},
        'goal': 'Continue the selected synthetic implementation discussion.',
        'messages': [{'id': 'm1', 'text': 'Use the approved mock implementation.', 'role': 'user'}],
        'diff': '', 'attachments': [], 'checkpoint': 1, 'parentCheckpoint': 0,
        'mode': 'PLAN',
    }


def submit_previewed(core, task, intent, behavior='OK'):
    preview = core.request('/tasks/preview', {'task': task})
    return core.request('/tasks', {'intent': intent, 'task': task, 'confirmed': True, 'confirmationHash': preview['confirmationHash'], 'mockBehavior': behavior})


def verify_bridge(core, results):
    session = core.request('/sessions')['items'][0]
    task = task_for(session)
    attachment = b'synthetic attachment bytes\n'
    task['attachments'] = [{'name': 'synthetic.txt', 'mime': 'text/plain', 'size': len(attachment), 'sha256': hashlib.sha256(attachment).hexdigest(), 'dataBase64': base64.b64encode(attachment).decode(), 'status': 'UNVERIFIED'}]
    first = submit_previewed(core, task, 'release-loop:1')
    assert first['state'] == 'DELIVERED' and first['checkpointAdvanced'] is True
    assert first['task']['attachments'][0]['status'] == 'STORED'
    assert 'dataBase64' not in first['task']['attachments'][0]
    exported = copy.deepcopy(task)
    exported.update({'webTarget': core.request('/web-targets')['items'][0], 'direction': 'CODE_TO_CHAT', 'checkpoint': 2, 'parentCheckpoint': 1, 'parentHash': first['checkpointHash'], 'linkedTaskId': task['taskId'], 'attachments': [], 'diff': '--- a/mock\n+++ b/mock\n@@ -1 +1 @@\n-before\n+after', 'source': {'site': 'code', 'url': 'https://example.invalid/synthetic-code', 'threadId': 'synthetic-code', 'messageIds': ['m2']}, 'messages': [{'id': 'm2', 'role': 'assistant', 'text': 'Synthetic progress and selected diff.'}]})
    second = submit_previewed(core, exported, 'release-loop:2')
    assert second['state'] == 'DELIVERED'
    continued = copy.deepcopy(exported)
    continued.update({'direction': 'CHAT_TO_CODE_CONTINUE', 'checkpoint': 3, 'parentCheckpoint': 2, 'parentHash': second['checkpointHash'], 'source': {'site': 'kimi', 'url': 'https://example.invalid/synthetic-chat', 'threadId': 'synthetic-thread', 'messageIds': ['m3']}, 'messages': [{'id': 'm3', 'role': 'user', 'text': 'Continue in the original Code session.'}], 'diff': ''})
    third = submit_previewed(core, continued, 'release-loop:3')
    assert third['state'] == 'DELIVERED' and third['task']['sessionId'] == first['task']['sessionId']
    replayed = core.request('/tasks', {'intent': 'release-loop:1', 'task': task, 'confirmed': True})
    assert replayed['id'] == first['id'] and replayed['replayed'] is True
    changed = copy.deepcopy(task)
    changed['goal'] = 'Different intent payload'
    core.request('/tasks', {'intent': 'release-loop:1', 'task': changed, 'confirmed': True}, expected=422)
    results.append('Chat to Code, Code to Chat, original Code continuation, attachment hash and durable intent replay')
    stale = copy.deepcopy(continued)
    stale.update({'checkpoint': 4, 'parentCheckpoint': 1, 'parentHash': first['checkpointHash']})
    core.request('/tasks', {'intent': 'release-loop:stale-parent', 'task': stale, 'confirmed': True}, expected=409)
    results.append('stale parent cannot fork the delivered task chain')

    timed = task_for(session, 'acceptance-timeout')
    unknown = submit_previewed(core, timed, 'release-timeout:1', 'TIMEOUT')
    assert unknown['state'] == 'UNKNOWN' and unknown['checkpointAdvanced'] is False
    repeated = core.request('/tasks', {'intent': 'release-timeout:1', 'task': timed, 'confirmed': True})
    assert repeated['state'] == 'UNKNOWN' and repeated['replayed'] is True
    changed = copy.deepcopy(timed)
    changed['goal'] = 'Changed content must not bypass unknown delivery'
    core.request('/tasks', {'intent': 'release-timeout:bypass', 'task': changed, 'confirmed': True}, expected=409)
    reconciled = core.request('/tasks/' + unknown['id'] + '/reconcile', {})
    assert reconciled['state'] == 'DELIVERED' and reconciled['checkpointAdvanced'] is True
    results.append('timeout is UNKNOWN, alternate intent blocked, manual receipt reconciliation advances checkpoint')

    empty = task_for(session, 'acceptance-empty')
    record = submit_previewed(core, empty, 'release-empty:1', 'EMPTY')
    assert record['state'] == 'UNKNOWN' and record.get('receipt') is None
    assert core.request('/tasks/' + record['id'] + '/reconcile', {})['state'] == 'UNKNOWN'
    results.append('empty receipt never becomes success, missing receipt reconciliation never resubmits')
    sensitive = task_for(session, 'acceptance-sensitive')
    sensitive['goal'] = 'Inspect synthetic data token=synthetic-abcdef-secret'
    preview = core.request('/tasks/preview', {'task': sensitive})
    assert 'synthetic-abcdef-secret' not in json.dumps(preview)
    redacted = core.request('/tasks', {'intent': 'release-sensitive:1', 'task': sensitive, 'confirmed': True, 'confirmationHash': preview['confirmationHash']})
    assert redacted['state'] == 'DELIVERED' and 'synthetic-abcdef-secret' not in json.dumps(redacted)
    results.append('sensitive preview and persisted task omit the synthetic secret')
    return {'task': empty, 'id': record['id'], 'thirdId': third['id']}


def verify_plugins(core, results):
    assert core.request('/plugins/monitor', {'action': 'ENABLE'})['state'] == 'ENABLED'
    assert core.request('/plugins/monitor', {'action': 'ENABLE'})['resources'] == 3
    core.request('/plugins/fault-demo', {'action': 'ENABLE'})
    core.request('/plugins/fault-demo', {'action': 'FAULT'}, expected=502)
    plugins = {v['id']: v for v in core.request('/plugins')['items']}
    assert plugins['monitor']['state'] == 'ENABLED' and plugins['monitor']['resources'] == 3
    assert plugins['fault-demo']['state'] == 'FAULTED' and plugins['fault-demo']['resources'] == 0
    assert core.request('/plugins/health')['processes'] == 0
    results.append('simulated plugin repeat-enable, isolated fault and zero Node/plugin-host processes')


def verify_registry_usage(core, results):
    core.request('/providers', {'id': 'kimi', 'name': 'Kimi synthetic', 'protocol': 'openai', 'baseUrl': 'https://api.moonshot.cn/v1'}, expected=201)
    discovery = core.request('/models/discovery', {'providerId': 'kimi'})
    assert discovery['mock'] is True and discovery['external'] is False
    records = discovery['records']
    preview = core.request('/models/preview', {'records': records})
    imported = core.request('/models/import', {'records': records, 'revision': preview['revision']})
    core.request('/models/import', {'records': records, 'revision': preview['revision']}, expected=409)
    model_id = records[0]['id']
    overridden = core.request('/models/override', {'providerId': 'kimi', 'id': model_id, 'field': 'context', 'value': None, 'revision': imported['revision']})
    model = next(v for v in core.request('/models')['items'] if v['id'] == model_id and v['providerId'] == 'kimi')
    assert model['capabilities']['context']['value'] is None
    assert model['capabilities']['context']['source'] == 'manual'
    rolled = core.request('/model-changes/' + str(overridden['changeId']) + '/rollback', {'revision': overridden['revision']})
    edited = core.request('/providers/kimi', {'name': 'Kimi configured synthetic', 'protocol': 'openai', 'baseUrl': 'https://example.invalid/v1', 'secretRef': None, 'revision': rolled['revision']}, method='PUT')
    assert edited['provider']['baseUrl'] == 'https://example.invalid/v1'
    core.request('/model-changes/' + str(edited['changeId']) + '/rollback', {'revision': edited['revision']})
    results.append('local model discovery, persisted import, field UNKNOWN override, CAS and configuration rollback')
    catalog = {'retrieved_at': '2026-11-10T08:00:00Z', 'source': 'https://models.dev/api.json', 'providers': {'synthetic-provider': {'id': 'synthetic-provider', 'name': 'Synthetic metadata', 'models': {'synthetic-model': {'id': 'synthetic-model', 'limit': {'context': 8192, 'output': 1024}, 'tool_call': True}}}}}
    preview = core.request('/models/modelsdev', {'catalog': catalog, 'preview': True})
    core.request('/models/modelsdev', {'catalog': catalog, 'revision': preview['revision']})
    metadata = next(v for v in core.request('/models')['items'] if v['providerId'] == 'synthetic-provider')
    assert metadata['capabilities']['context']['source'] == 'modelsdev'
    assert metadata['capabilities']['vision']['value'] is None
    results.append('saved metadata catalog import with provenance and unobserved vision remaining UNKNOWN')
    core.request('/usage/demo', {})
    first = core.request('/usage/summary')
    core.request('/usage/demo', {})
    summary = core.request('/usage/summary')
    assert summary['requests'] == first['requests'] == 6
    assert core.request('/quotas')['total'] == 2
    assert summary['input'] is None and summary['estimate']['complete'] is False
    assert summary['actual']['amount'] is None
    assert core.request('/usage/unlinked')['total'] == 1
    bill = {'observationId': 'acceptance-bill', 'providerId': 'anthropic', 'amount': 12.5, 'currency': 'USD', 'source': 'manual'}
    for invalid in (None, 1e12):
        core.request('/usage/actual', {**bill, 'observationId': 'acceptance-invalid', 'amount': invalid}, expected=400)
    core.request('/usage/actual', bill, expected=201)
    duplicate = core.request('/usage/actual', bill)
    assert duplicate['duplicate'] is True and duplicate['amount'] == 12.5
    core.request('/usage/actual', {**bill, 'amount': 13}, expected=409)
    results.append('unknown and oversized billing rejected; retries without timestamps return the persisted observation')
    provider = core.request('/usage/summary?providerId=anthropic')
    session = core.request('/usage/summary?sessionId=session-demo')
    assert provider['actual']['amount'] == 12.5
    assert session['actual']['amount'] is None and session['actual']['providerLedger']['amount'] == 12.5
    assert all(v['remaining'] is None for v in core.request('/quotas')['items'])
    results.append('usage deduplication, retry distinction, partial-known totals and four separate ledgers')
    core.request('/adapter', {'mode': 'UNAVAILABLE'}, method='PUT')
    assert core.request('/models')['total'] >= len(records)
    assert core.request('/usage/summary')['requests'] == 6
    core.request('/adapter', {'mode': 'AVAILABLE'}, method='PUT')
    results.append('model and usage history remain usable when the Desktop adapter is unavailable')
