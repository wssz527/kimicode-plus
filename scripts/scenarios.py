"""Synthetic business workflows for release-binary acceptance."""
import base64
import copy
import hashlib
import json
from pathlib import Path


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


def make_local_plugin(folder, plugin_id, name='Local acceptance plugin', capabilities=None, corrupt=False):
    """Write a real plugin package on disk for the local-install flow.

    The acceptance run installs this through the public endpoint, so the whole
    zip/folder validation path is exercised instead of a hardcoded builtin.
    """
    root = Path(folder) / ('acceptance-plugin-' + plugin_id.replace('.', '-'))
    root.mkdir(parents=True, exist_ok=True)
    manifest = {
        'id': plugin_id, 'name': name, 'version': '1.0.0', 'apiVersion': 1,
        'description': 'Acceptance fixture installed from a local folder.',
        'hostVersions': ['1.*'],
        'entry': {'renderer': 'renderer.js'},
        'capabilities': capabilities if capabilities is not None else ['ui:sidebar', 'storage'],
    }
    (root / 'plugin.json').write_text(json.dumps(manifest), encoding='utf-8')
    (root / 'renderer.js').write_text('export function activate(ctx) { ctx.log("info", "acceptance"); }\n', encoding='utf-8')
    (root / 'LICENSE').write_text('MIT\n', encoding='utf-8')
    if corrupt:
        (root / 'renderer.js').unlink()
    return str(root)


def verify_plugins(core, results, folder):
    bundled = 'kimicode-plus.monitor'
    items = {v['id']: v for v in core.request('/plugins')['items']}
    assert 'kimicode-plus.example-hello' not in items, items
    assert bundled in items, items
    assert items[bundled]['source'] == 'BUNDLED' and items[bundled]['enabled'] is False
    assert items['kimicode-plus.planner']['enabled'] is False
    assert items['kimicode-plus.scheduler']['enabled'] is False
    results.append('bundled plugins are released on first run without example-hello and start disabled')

    enabled_plugins = []
    enabled = core.request('/plugins/' + bundled + '/enable', {})
    enabled_plugins.append(bundled)
    assert enabled['state'] == 'ENABLED' and enabled['token']
    token = enabled['token']
    assert core.request('/plugins/' + bundled + '/action/runtime.info', {}, headers={'X-Plugin-Token': token})['value']['id'] == bundled
    core.request('/plugins/' + bundled + '/action/config.save', {'params': {'patch': {'x': 1}}}, headers={'X-Plugin-Token': token}, expected=403)
    core.request('/plugins/' + bundled + '/action/runtime.info', {}, expected=401)
    results.append('plugin identity token gates actions and an ungranted capability is refused')

    activity = core.request('/plugins/' + bundled + '/activity')['items']
    assert any(v['kind'] == 'ENABLE' for v in activity), activity
    results.append('system-initiated plugin actions land in the activity log')

    local = 'acceptance.local'
    root = make_local_plugin(folder, local)
    # Installing requires a consent decision, so the flow is preview (no write),
    # then install with the confirmed list. A missing list is a refusal, not a
    # silent grant of everything the manifest asked for.
    preview = core.request('/plugins/preview', {'root': root})
    assert preview['id'] == local and preview['capabilities'] == ['storage', 'ui:sidebar'], preview
    core.request('/plugins/install', {'root': root}, expected=400)
    installed = core.request('/plugins/install', {'root': root, 'packageDigest': preview['packageDigest'], 'capabilities': preview['capabilities']}, expected=201)
    assert installed['id'] == local and installed['source'] == 'LOCAL'
    assert installed['capabilities'] == ['storage', 'ui:sidebar'], installed
    core.request('/plugins/' + local + '/enable', {})
    enabled_plugins.append(local)
    core.request('/plugins/' + local + '/disable', {})
    core.request('/plugins/' + local + '/uninstall', {})
    assert local not in {v['id'] for v in core.request('/plugins')['items']}
    results.append('preview, explicit capability consent, install, enable, disable and uninstall complete')

    # A narrower grant is accepted and stored as given.
    narrow_root = make_local_plugin(folder, 'acceptance.narrow', capabilities=['ui:sidebar', 'storage'])
    narrow_preview = core.request('/plugins/preview', {'root': narrow_root})
    assert narrow_preview['capabilities'] == ['storage', 'ui:sidebar'], narrow_preview
    narrow = core.request('/plugins/install', {'root': narrow_root, 'packageDigest': narrow_preview['packageDigest'], 'capabilities': ['ui:sidebar']}, expected=201)
    assert narrow['capabilities'] == ['ui:sidebar'], narrow
    # The capability the user did not confirm is not usable even though the
    # manifest declares it: enabling issues a token, but storage is still denied.
    narrow_token = core.request('/plugins/' + narrow['id'] + '/enable', {})['token']
    enabled_plugins.append(narrow['id'])
    core.request('/plugins/' + narrow['id'] + '/action/storage.set', {'key': 'k', 'value': 1}, headers={'X-Plugin-Token': narrow_token}, expected=403)
    core.request('/plugins/' + narrow['id'] + '/disable', {})
    core.request('/plugins/' + narrow['id'] + '/uninstall', {})
    results.append('a grant narrower than the manifest is stored exactly as confirmed')

    broken = make_local_plugin(folder, 'acceptance.broken', corrupt=True)
    core.request('/plugins/preview', {'root': broken}, expected=400)
    results.append('a package missing its declared entry file is refused before install')

    escaped = make_local_plugin(folder, 'acceptance.escape', capabilities=['root:everything'])
    core.request('/plugins/preview', {'root': escaped}, expected=400)
    results.append('an unknown capability is refused at install time')

    print('本次验收启用的插件:', ', '.join(enabled_plugins))
    # Leave the bundled plugin running: the safe-mode step below asserts that
    # turning safe mode on actually stops it, which requires it to be live.
    return {'bundled': bundled, 'token': token}


def verify_plugin_safe_mode(core, results, bundled, token):
    items = {v['id']: v for v in core.request('/plugins')['items']}
    assert items[bundled]['enabled'] is False and items[bundled]['state'] == 'BLOCKED', items[bundled]
    core.request('/plugins/' + bundled + '/enable', {}, expected=409)
    # The token issued before safe mode was switched on must no longer identify
    # the plugin. A 401 here is the proof that revocation is not cosmetic.
    core.request('/plugin-runtime/runtime.info', {'token': token}, expected=401)
    results.append('safe mode stops the running plugin, refuses activation and revokes its token')


def verify_registry_usage(core, results):
    core.request('/models', expected=404)
    core.request('/providers', {'id': 'kimi', 'name': 'Kimi synthetic', 'protocol': 'openai', 'baseUrl': 'https://api.moonshot.cn/v1'}, expected=404)
    core.request('/models/preview', {'records': []}, expected=404)
    core.request('/model-changes', expected=404)
    assert len(core.request('/gateways')['items']) > 0
    results.append('standalone model and provider HTTP routes are removed while gateway metadata remains available')
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
    assert len(core.request('/gateways')['items']) > 0
    assert core.request('/usage/summary')['requests'] == 6
    core.request('/adapter', {'mode': 'AVAILABLE'}, method='PUT')
    results.append('gateway and usage history remain usable when the Desktop adapter is unavailable')
