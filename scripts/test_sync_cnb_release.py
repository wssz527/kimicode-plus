#!/usr/bin/env python3
"""Unit tests for scripts/sync_cnb_release.py against a local stub HTTP server."""
import gc
import http.server
import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest
import warnings

sys.path.insert(0, str(Path(__file__).resolve().parent))
import sync_cnb_release as sync


class FakeCNBServer:
    def __init__(self, existing_release_id=None):
        self.existing_release_id = existing_release_id
        self.created_payloads = []
        self.updated_bodies = []
        self.upload_order = []
        self.uploaded_bytes = {}
        self.confirmed = []
        self.fail_first_upload = False
        self._failed_once = False
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = 'HTTP/1.1'

            def log_message(self, format, *args):
                pass

            def _send_json(self, code, obj):
                data = json.dumps(obj).encode('utf-8')
                self.send_response(code)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.send_header('Connection', 'close')
                self.end_headers()
                self.close_connection = True
                self.wfile.write(data)

            def _read_json(self):
                length = int(self.headers.get('Content-Length') or 0)
                return json.loads(self.rfile.read(length).decode('utf-8')) if length else {}

            def do_GET(self):
                if self.headers.get('Authorization') != 'Bearer test-token':
                    self._send_json(401, {'error': 'unauthorized'})
                    return
                if self.path in ('/owner/repo/-/releases/tags/v0.2.4', '/owner/repo/-/releases/tags/plugins-latest'):
                    if outer.existing_release_id:
                        self._send_json(200, {'id': outer.existing_release_id})
                    else:
                        self._send_json(404, {'error': 'not found'})
                    return
                self._send_json(404, {'error': 'unexpected path'})

            def do_POST(self):
                if self.headers.get('Authorization') != 'Bearer test-token':
                    self._send_json(401, {'error': 'unauthorized'})
                    return
                body = self._read_json()
                if self.path == '/owner/repo/-/releases':
                    outer.created_payloads.append(body)
                    self._send_json(201, {'id': 'rel-42'})
                    return
                if self.path == '/owner/repo/-/releases/rel-42/asset-upload-url':
                    name = body['asset_name']
                    host = f'http://{self.server.server_address[0]}:{self.server.server_address[1]}'
                    self._send_json(201, {
                        'upload_url': f'{host}/upload/{name}',
                        'verify_url': f'/owner/repo/-/releases/rel-42/asset-upload-confirmation/tok/{name}',
                    })
                    return
                if self.path.startswith('/owner/repo/-/releases/rel-42/asset-upload-confirmation/tok/'):
                    name = self.path.rsplit('/', 1)[-1]
                    outer.confirmed.append(name)
                    self._send_json(200, {'ok': True})
                    return
                self._send_json(404, {'error': 'unexpected post'})

            def do_PATCH(self):
                if self.headers.get('Authorization') != 'Bearer test-token':
                    self._send_json(401, {'error': 'unauthorized'})
                    return
                body = self._read_json()
                if self.path == '/owner/repo/-/releases/rel-42':
                    outer.updated_bodies.append(body)
                    self._send_json(200, {'id': 'rel-42'})
                    return
                self._send_json(404, {'error': 'unexpected patch'})

            def do_PUT(self):
                length = int(self.headers.get('Content-Length') or 0)
                data = self.rfile.read(length)
                name = self.path.rsplit('/', 1)[-1]
                if outer.fail_first_upload and not outer._failed_once:
                    outer._failed_once = True
                    self.send_response(502)
                    self.send_header('Content-Length', '0')
                    self.end_headers()
                    self.close_connection = True
                    return
                outer.upload_order.append(name)
                outer.uploaded_bytes[name] = data
                self.send_response(200)
                self.send_header('Content-Length', '0')
                self.end_headers()
                self.close_connection = True

        self.httpd = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.httpd.daemon_threads = False
        self.httpd.block_on_close = True
        self.base_url = f'http://127.0.0.1:{self.httpd.server_address[1]}'
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.httpd.shutdown()
        self.thread.join(timeout=10)
        self.httpd.server_close()


class SyncCNBReleaseTest(unittest.TestCase):
    def setUp(self):
        self.tmpdir = Path(tempfile.mkdtemp(prefix='cnb-sync-test-'))
        self.files = []
        for name, content in [
            ('manifest.json', b'[{"version":"0.2.4"}]'),
            ('SHA256SUMS', b'abc  enhanced-kimi-0.2.4-darwin-arm64.tar.gz\n'),
            ('enhanced-kimi-0.2.4-darwin-arm64.tar.gz', b'archive-payload'),
            ('THIRD_PARTY_NOTICES.txt', b'notices'),
        ]:
            path = self.tmpdir / name
            path.write_bytes(content)
            self.files.append(path)

    def tearDown(self):
        for item in self.tmpdir.iterdir():
            item.unlink()
        self.tmpdir.rmdir()

    def test_manifest_is_uploaded_last_and_release_created(self):
        server = FakeCNBServer()
        self.addCleanup(server.close)
        release_id, uploaded = sync.sync_release(
            api_base=server.base_url,
            web_base=server.base_url,
            repo='owner/repo',
            token='test-token',
            tag='v0.2.4',
            notes='Release notes',
            files=self.files,
            prerelease=False,
            push_git=False,
        )
        self.assertEqual(release_id, 'rel-42')
        self.assertEqual(len(server.created_payloads), 1)
        self.assertEqual(server.created_payloads[0]['make_latest'], 'true')
        self.assertEqual(uploaded[-2:], ['SHA256SUMS', 'manifest.json'])
        self.assertEqual(server.upload_order, uploaded)
        self.assertEqual(server.confirmed, uploaded)
        self.assertEqual(server.uploaded_bytes['manifest.json'], b'[{"version":"0.2.4"}]')

    def test_existing_release_is_reused_and_transient_upload_retries(self):
        server = FakeCNBServer(existing_release_id='rel-42')
        server.fail_first_upload = True
        self.addCleanup(server.close)
        release_id, uploaded = sync.sync_release(
            api_base=server.base_url,
            web_base=server.base_url,
            repo='owner/repo',
            token='test-token',
            tag='v0.2.4',
            notes='Retry notes',
            files=self.files,
            prerelease=False,
            push_git=False,
        )
        self.assertEqual(release_id, 'rel-42')
        self.assertEqual(server.created_payloads, [])
        self.assertEqual(server.updated_bodies, [{'body': 'Retry notes'}])
        self.assertEqual(server.confirmed, uploaded)

    def test_existing_release_notes_are_updated_for_rolling_channel(self):
        server = FakeCNBServer(existing_release_id='rel-42')
        self.addCleanup(server.close)
        release_id, uploaded = sync.sync_release(
            api_base=server.base_url,
            web_base=server.base_url,
            repo='owner/repo',
            token='test-token',
            tag='plugins-latest',
            notes='Plugin channel update 1.1.2',
            files=self.files,
            prerelease=False,
            push_git=False,
            make_latest=False,
        )
        self.assertEqual(release_id, 'rel-42')
        self.assertEqual(server.created_payloads, [])
        self.assertEqual(server.updated_bodies, [{'body': 'Plugin channel update 1.1.2'}])
        self.assertEqual(server.confirmed, uploaded)

    def test_existing_release_without_notes_is_not_patched(self):
        server = FakeCNBServer(existing_release_id='rel-42')
        self.addCleanup(server.close)
        release_id, _ = sync.sync_release(
            api_base=server.base_url,
            web_base=server.base_url,
            repo='owner/repo',
            token='test-token',
            tag='v0.2.4',
            notes='',
            files=self.files,
            prerelease=False,
            push_git=False,
        )
        self.assertEqual(release_id, 'rel-42')
        self.assertEqual(server.updated_bodies, [])

    def test_prerelease_detection(self):
        self.assertFalse(sync.is_prerelease('v0.2.4'))

    def test_failed_upload_response_is_closed(self):
        # A non-2xx PUT arrives as HTTPError, which owns the socket. If it is not
        # closed there, the failed attempt leaves a connection open until GC and
        # the suite reports `ResourceWarning: unclosed <socket.socket>`.
        server = FakeCNBServer()
        server.fail_first_upload = True
        self.addCleanup(server.close)
        with warnings.catch_warnings(record=True) as caught:
            warnings.simplefilter('always', ResourceWarning)
            with self.assertRaises(RuntimeError) as raised:
                sync.upload_file_put(f'{server.base_url}/upload/asset.bin', self.files[2])
            gc.collect()
        self.assertIn('HTTP 502', str(raised.exception))
        leaked = [w for w in caught if issubclass(w.category, ResourceWarning)]
        self.assertEqual(leaked, [], f'unclosed sockets: {[str(w.message) for w in leaked]}')
        # The rejected attempt never counted as an upload.
        self.assertEqual(server.upload_order, [])

    def test_plugin_channel_uploads_plugins_manifest_last_and_keeps_non_latest(self):
        plugin_dir = Path(tempfile.mkdtemp(prefix='cnb-plugins-test-'))
        self.addCleanup(lambda: [p.unlink() for p in plugin_dir.iterdir()] and plugin_dir.rmdir())
        files = []
        for name, content in [
            ('plugins-manifest.json', b'{"format":"kimicode-plus.plugins-manifest.v1"}'),
            ('kimicode-plus.kct-1.0.5.zip', b'kct-zip'),
            ('kimicode-plus.monitor-1.1.1.zip', b'monitor-zip'),
        ]:
            p = plugin_dir / name
            p.write_bytes(content)
            files.append(p)
        server = FakeCNBServer()
        self.addCleanup(server.close)
        release_id, uploaded = sync.sync_release(
            api_base=server.base_url,
            web_base=server.base_url,
            repo='owner/repo',
            token='test-token',
            tag='plugins-latest',
            notes='Plugin channel update',
            files=files,
            prerelease=False,
            push_git=False,
            make_latest=False,
        )
        self.assertEqual(release_id, 'rel-42')
        self.assertEqual(len(server.created_payloads), 1)
        self.assertEqual(server.created_payloads[0]['make_latest'], 'false')
        self.assertEqual(server.created_payloads[0]['prerelease'], False)
        self.assertEqual(uploaded[-1], 'plugins-manifest.json')
        self.assertEqual(server.upload_order, uploaded)
        self.assertTrue(sync.is_prerelease('v0.2.4-rc.1'))
        self.assertTrue(sync.is_prerelease('v0.2.4-beta.2+build1'))


if __name__ == '__main__':
    unittest.main()
