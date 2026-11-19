#!/usr/bin/env python3
"""Run isolated acceptance against the actual Go executable."""
import argparse
import http.client
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request
from scenarios import verify_bridge, verify_plugin_safe_mode, verify_plugins, verify_registry_usage

OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

class Core:
    def __init__(self, binary, folder):
        self.folder = Path(folder)
        self.connection = None
        self.stderr_path = self.folder / 'core.stderr.log'
        with self.stderr_path.open('wb') as stderr:
            self.process = subprocess.Popen([str(binary), '--data-dir', str(folder)],
                                            stdout=subprocess.DEVNULL, stderr=stderr)
        runtime = self.folder / 'runtime.json'
        deadline = time.monotonic() + 15
        last_read_error = None
        try:
            while True:
                if self.process.poll() is not None:
                    raise AssertionError('core exited before readiness')
                try:
                    data = json.loads(runtime.read_text(encoding='utf-8'))
                    if data.get('pid') == self.process.pid:
                        break
                except (FileNotFoundError, PermissionError, json.JSONDecodeError) as error:
                    last_read_error = error
                if time.monotonic() > deadline:
                    raise AssertionError('core readiness timeout') from last_read_error
                time.sleep(.01)
        except Exception as error:
            diagnostics = self.diagnostics()
            self.stop(hard=True)
            raise AssertionError(f'{error}\n{diagnostics}') from error
        self.base, self.token = data['url'], data['pairingToken']
        address = urllib.parse.urlsplit(self.base)
        self.connection = http.client.HTTPConnection(address.hostname, address.port, timeout=15)

    def diagnostics(self):
        status = self.process.poll()
        with self.stderr_path.open('rb') as stderr:
            stderr.seek(max(0, os.fstat(stderr.fileno()).st_size - 4096))
            output = stderr.read(4096).decode('utf-8', errors='replace').strip()
        return f'core pid={self.process.pid} exit={status}\ncore stderr: {output or "(empty)"}'

    def request(self, path, body=None, method=None, token=True, headers=None, expected=200):
        values = {'Content-Type': 'application/json'}
        if token:
            values['Authorization'] = 'Bearer ' + self.token
        values.update(headers or {})
        method = method or ('GET' if body is None else 'POST')
        payload = None if body is None else json.dumps(body).encode('utf-8')
        try:
            self.connection.request(method, '/enhance/v1' + path, body=payload, headers=values)
            with self.connection.getresponse() as response:
                data = json.loads(response.read())
        except (OSError, http.client.HTTPException, json.JSONDecodeError) as error:
            raise AssertionError(f'{method} {path}: {error}\n{self.diagnostics()}') from error
        assert response.status == expected, (path, response.status, data)
        return data

    def stop(self, hard=False):
        if self.connection is not None:
            self.connection.close()
        if self.process.poll() is None:
            if hard:
                self.process.kill()
            elif os.name == 'nt':
                self.process.terminate()
            else:
                self.process.send_signal(signal.SIGTERM)
        self.process.wait(timeout=15)

def run(binary):
    Path('work').mkdir(exist_ok=True)
    results = []
    with tempfile.TemporaryDirectory(prefix='acceptance-', dir='work') as folder:
        core = Core(binary, folder)
        try:
            health = core.request('/health')
            assert health['status'] == 'OK'
            assert all(v['state'] == 'READY' for v in health['modules']), health
            results.append('core independent readiness and all enabled modules')
            with OPENER.open(core.base + '/', timeout=10) as response:
                html = response.read().decode()
                assert '<script type="module"' in html and '/assets/' in html, 'embedded React production build missing'
                assert "frame-ancestors 'none'" in response.headers['Content-Security-Policy']
            results.append('embedded React production index and browser security headers')
            core.request('/health', token=False, expected=401)
            core.request('/health', headers={'Origin': 'https://foreign.example'}, expected=403)
            core.request('/health', headers={'Host': 'foreign.example'}, expected=403)
            pair = core.request('/pair', {'pairingToken': core.token, 'permissions': ['models:read']}, token=False)
            core.request('/usage', headers={'Authorization': 'Bearer ' + pair['sessionToken']}, expected=403)
            results.append('pairing / Host / Origin / resource permission enforcement')
            second = subprocess.run([str(binary), '--data-dir', folder], capture_output=True, text=True, timeout=10)
            assert second.returncode != 0
            results.append('second process cannot acquire owned data directory')
            sources = core.request('/chat/sources')['items']
            assert sources[0]['id'] == 'kimi'
            thread = core.request('/chat/threads', {'title': 'Acceptance mock chat', 'sourceId': 'custom-model', 'model': 'synthetic'}, expected=201)
            message_path = '/chat/threads/' + thread['id'] + '/messages'
            for _ in range(2):
                messages = core.request(message_path, {'text': 'Selected synthetic discussion'}, headers={'Idempotency-Key': 'acceptance-chat'})
            assert len(messages['messages']) == 2
            results.append('independent lightweight Mock chat and durable message idempotency')
            verify_registry_usage(core, results)
            recovery = verify_bridge(core, results)
            plugins = verify_plugins(core, results, folder)
            settings = core.request('/settings')
            settings['safeMode'] = True
            core.request('/settings', settings, method='PUT')
            core.request('/settings', settings, method='PUT', expected=409)
            assert core.request('/health')['safeMode'] is True
            verify_plugin_safe_mode(core, results, plugins['bundled'], plugins['token'])
            results.append('settings CAS and safe mode stop plugins and prevent activation')
            core.stop()
            core = Core(binary, folder)
            assert core.request('/health')['safeMode'] is True
            assert len(core.request('/chat/threads/' + thread['id'])['messages']) == 2
            records = {v['id']: v for v in core.request('/tasks')['items']}
            assert records[recovery['thirdId']]['state'] == 'DELIVERED'
            assert records[recovery['id']]['state'] == 'UNKNOWN'
            core.request('/tasks', {'intent': 'release-empty:1', 'task': recovery['task'], 'confirmed': True}, expected=409)
            assert len(core.request('/gateways')['items']) > 0
            assert core.request('/usage/summary')['requests'] == 6
            results.append('SQLite usage, chat, delivered/unknown outbox and safe mode recover after restart')
            settings = core.request('/settings')
            settings['safeMode'] = False
            core.request('/settings', settings, method='PUT')
            replayed = core.request('/tasks', {'intent': 'release-empty:1', 'task': recovery['task'], 'confirmed': True})
            assert replayed['state'] == 'UNKNOWN' and replayed['replayed'] is True
            assert core.request('/plugins/' + plugins['bundled'] + '/enable', {})['state'] == 'ENABLED'
            results.append('leaving safe mode permits a fresh plugin activation')
            previous_token = core.token
            core.stop(hard=True)
            core = Core(binary, folder)
            assert core.token != previous_token
            assert core.request('/tasks/' + recovery['id'] + '/reconcile', {})['state'] == 'UNKNOWN'
            assert core.request('/health')['safeMode'] is False
            assert core.request('/usage/summary')['requests'] == 6
            results.append('hard process termination releases ownership, recovers history and rotates pairing credentials')
            health = core.request('/health')
            assert health['compaction'] == 'POLICY_ONLY_NATIVE_UNCONNECTED'
            assert core.request('/workspace/compaction')['nativeConnected'] is False
            results.append('compaction policies persist while native execution remains disconnected')
        finally:
            core.stop()
    for text in results:
        print('PASS ' + text)
    print(json.dumps({'passed': len(results), 'failed': 0, 'binary': binary.name}, ensure_ascii=False))

if __name__ == '__main__':
    sys.stdout.reconfigure(encoding='utf-8')
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', required=True)
    args = parser.parse_args()
    run(Path(args.binary).resolve())
