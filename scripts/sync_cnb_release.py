#!/usr/bin/env python3
"""Sync a built KimiCode+ release tag and assets to the CNB domestic mirror."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


DEFAULT_CNB_API = 'https://api.cnb.cool'
DEFAULT_CNB_WEB = 'https://cnb.cool'
DEFAULT_CNB_REPO = 'wssz527/kimicode-plus'
CNB_ACCEPT = 'application/vnd.cnb.api+json'


def ordered_assets(paths):
    """Order release files so manifest files are uploaded last."""
    resolved = [Path(item) for item in paths]
    missing = [str(item) for item in resolved if not item.is_file()]
    if missing:
        raise SystemExit(f'Missing release asset(s): {", ".join(missing)}')
    seen = {}
    for item in resolved:
        if item.name in seen:
            raise SystemExit(f'Duplicate asset filename: {item.name}')
        seen[item.name] = item
    manifest_names = ('manifest.json', 'plugins-manifest.json')
    regular = [item for item in resolved if item.name not in ('SHA256SUMS', *manifest_names)]
    sums = [item for item in resolved if item.name == 'SHA256SUMS']
    manifests = [item for item in resolved if item.name in manifest_names]
    return regular + sums + manifests


def is_prerelease(tag):
    version = tag.removeprefix('v')
    return '-' in version.split('+', 1)[0]


def close_response_socket(resp):
    """Release the socket behind a urllib response.

    `HTTPResponse.close()` only closes the buffered reader; when the server ends
    the connection itself, urllib leaves `resp.fp` attached to a `SocketIO` that
    still owns the socket. Dropping that object unclosed is what produced
    `ResourceWarning: unclosed <socket.socket>` under `-W default`, so close the
    socket explicitly before closing the response.
    """
    fp = getattr(resp, 'fp', None)
    sock_io = getattr(fp, 'raw', None)
    sock = getattr(sock_io, '_sock', None)
    if sock is not None:
        try:
            sock.close()
        except OSError:
            pass
    resp.close()


def cnb_json_request(url, token, method='GET', payload=None, timeout=30):
    headers = {
        'Authorization': f'Bearer {token}',
        'Accept': CNB_ACCEPT,
        'User-Agent': 'KimiCodePlus-ReleaseSync/1.0',
    }
    data = None
    if payload is not None:
        data = json.dumps(payload).encode('utf-8')
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            try:
                return resp.status, (json.loads(raw.decode('utf-8')) if raw.strip() else {})
            finally:
                close_response_socket(resp)
    except urllib.error.HTTPError as err:
        with err:
            body = err.read().decode('utf-8', errors='replace')[:500]
        return err.code, {'_raw': body}


def upload_file_put(upload_url, file_path, timeout=180):
    size = file_path.stat().st_size
    headers = {
        'Content-Length': str(size),
        'Content-Type': 'application/octet-stream',
    }
    # urlopen keeps proxy settings and redirect handling that a raw
    # http.client connection would bypass; the socket is closed explicitly in
    # the same block so the response object cannot linger until GC.
    with file_path.open('rb') as handle:
        req = urllib.request.Request(upload_url, data=handle, headers=headers, method='PUT')
        try:
            resp = urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as err:
            # A non-2xx answer arrives as HTTPError, which is itself a response
            # holding the socket. `with` never runs for it, so close it here or
            # the connection leaks until GC.
            with err:
                err.read()
            raise RuntimeError(f'Upload PUT failed for {file_path.name}: HTTP {err.code}') from None
        with resp:
            try:
                resp.read()
                if resp.status < 200 or resp.status >= 300:
                    raise RuntimeError(f'Upload PUT failed for {file_path.name}: HTTP {resp.status}')
            finally:
                close_response_socket(resp)


def ensure_release(api_base, repo, token, tag, notes, prerelease, make_latest=None):
    encoded_tag = urllib.parse.quote(tag, safe='')
    tag_url = f'{api_base}/{repo}/-/releases/tags/{encoded_tag}'
    status, data = cnb_json_request(tag_url, token, method='GET')
    if status == 200 and data.get('id'):
        release_id = str(data['id'])
        # Rolling channels such as plugins-latest keep the same release id across
        # runs; refresh its description so the CNB body never stays at the first
        # publication's notes while the packages underneath move on.
        if notes:
            patch_url = f'{api_base}/{repo}/-/releases/{release_id}'
            patch_status, patch_data = cnb_json_request(patch_url, token, method='PATCH', payload={'body': notes})
            if patch_status < 200 or patch_status >= 300:
                raise RuntimeError(
                    f'Failed to update CNB release {tag} notes: HTTP {patch_status} {patch_data.get("_raw", "")}'
                )
        return release_id
    if status != 404:
        raise RuntimeError(f'Failed to query CNB release {tag}: HTTP {status} {data.get("_raw", "")}')

    latest_flag = (not prerelease) if make_latest is None else bool(make_latest)
    create_url = f'{api_base}/{repo}/-/releases'
    payload = {
        'tag_name': tag,
        'name': tag,
        'body': notes,
        'draft': False,
        'prerelease': bool(prerelease),
        'make_latest': 'true' if latest_flag else 'false',
    }
    status, data = cnb_json_request(create_url, token, method='POST', payload=payload)
    if status not in (200, 201) or not data.get('id'):
        raise RuntimeError(f'Failed to create CNB release {tag}: HTTP {status} {data.get("_raw", "")}')
    return str(data['id'])


def upload_release_asset(api_base, repo, token, release_id, file_path, attempts=3):
    size = file_path.stat().st_size
    last_err = None
    for attempt in range(1, attempts + 1):
        try:
            req_url = f'{api_base}/{repo}/-/releases/{release_id}/asset-upload-url'
            status, data = cnb_json_request(
                req_url,
                token,
                method='POST',
                payload={'asset_name': file_path.name, 'size': size, 'overwrite': True},
            )
            if status < 200 or status >= 300:
                raise RuntimeError(f'asset-upload-url failed for {file_path.name}: HTTP {status} {data.get("_raw", "")}')
            upload_url = data.get('upload_url') or ''
            verify_url = data.get('verify_url') or ''
            if not upload_url or not verify_url:
                raise RuntimeError(f'Missing upload_url/verify_url for {file_path.name}')
            if not verify_url.startswith(('http://', 'https://')):
                verify_url = f'{api_base}{verify_url}'

            upload_file_put(upload_url, file_path)

            verify_status, verify_data = cnb_json_request(verify_url, token, method='POST')
            if verify_status < 200 or verify_status >= 300:
                raise RuntimeError(
                    f'asset confirmation failed for {file_path.name}: HTTP {verify_status} {verify_data.get("_raw", "")}'
                )
            return
        except Exception as err:
            last_err = err
            if attempt < attempts:
                time.sleep(attempt)
    raise RuntimeError(f'Failed to upload {file_path.name} after {attempts} attempts: {last_err}')


def push_git_refs(web_base, repo, token, tag, prerelease, push_main=True, force_tag=False):
    remote_url = f'{web_base.rstrip("/")}/{repo}.git'
    with tempfile.NamedTemporaryFile('w', prefix='cnb-askpass-', suffix='.sh', delete=False, encoding='utf-8') as script:
        script.write(
            '#!/bin/sh\n'
            'case "$1" in\n'
            '  *Username*|*username*) printf "%s\\n" "cnb" ;;\n'
            '  *) printf "%s\\n" "$CNB_GIT_PASSWORD" ;;\n'
            'esac\n'
        )
        askpass = Path(script.name)
    try:
        askpass.chmod(stat.S_IRWXU)
        env = os.environ.copy()
        env['GIT_ASKPASS'] = str(askpass)
        env['GIT_TERMINAL_PROMPT'] = '0'
        env['CNB_GIT_PASSWORD'] = token
        if push_main and not prerelease:
            subprocess.run(
                ['git', 'push', remote_url, 'HEAD:refs/heads/main'],
                env=env,
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                timeout=120,
            )
        refspec = f'+refs/tags/{tag}:refs/tags/{tag}' if force_tag else f'refs/tags/{tag}:refs/tags/{tag}'
        result = subprocess.run(
            ['git', 'push', remote_url, refspec],
            env=env,
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=120,
        )
        if result.returncode != 0:
            raise RuntimeError(f'git push tag {tag} to CNB failed: {result.stderr.strip()}')
    finally:
        try:
            askpass.unlink()
        except OSError:
            pass


def sync_release(
    api_base,
    web_base,
    repo,
    token,
    tag,
    notes,
    files,
    prerelease,
    push_git=True,
    make_latest=None,
    force_tag=False,
):
    api_base = api_base.rstrip('/')
    assets = ordered_assets(files)
    if push_git:
        push_main = True if make_latest is None else bool(make_latest)
        push_git_refs(web_base, repo, token, tag, prerelease, push_main=push_main, force_tag=force_tag)
    release_id = ensure_release(api_base, repo, token, tag, notes, prerelease, make_latest=make_latest)
    for asset_path in assets:
        print(f'Uploading {asset_path.name} ({asset_path.stat().st_size} bytes) to CNB {repo} {tag}...', flush=True)
        upload_release_asset(api_base, repo, token, release_id, asset_path)
    return release_id, [p.name for p in assets]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', required=True, help='Release tag name, e.g. v0.2.3')
    parser.add_argument('--repo', default=os.environ.get('CNB_REPO', DEFAULT_CNB_REPO), help='CNB repo slug')
    parser.add_argument('--api-base', default=os.environ.get('CNB_API_BASE', DEFAULT_CNB_API))
    parser.add_argument('--web-base', default=os.environ.get('CNB_WEB_BASE', DEFAULT_CNB_WEB))
    parser.add_argument('--notes', default=os.environ.get('RELEASE_NOTES', ''))
    parser.add_argument('--prerelease', action=argparse.BooleanOptionalAction, default=None)
    parser.add_argument('--make-latest', choices=('auto', 'true', 'false'), default='auto')
    parser.add_argument('--force-tag', action='store_true', help='Force-update rolling git tag on CNB (e.g. plugins-latest)')
    parser.add_argument('--skip-git-push', action='store_true', help='Skip pushing git tag/main to CNB')
    parser.add_argument('files', nargs='+', help='Release asset files to upload')
    args = parser.parse_args()

    token = os.environ.get('CNB_TOKEN', '').strip()
    if not token:
        raise SystemExit('CNB_TOKEN environment variable is required')

    prerelease = is_prerelease(args.tag) if args.prerelease is None else args.prerelease
    make_latest = None if args.make_latest == 'auto' else (args.make_latest == 'true')
    release_id, uploaded = sync_release(
        api_base=args.api_base,
        web_base=args.web_base,
        repo=args.repo,
        token=token,
        tag=args.tag,
        notes=args.notes,
        files=args.files,
        prerelease=prerelease,
        push_git=not args.skip_git_push,
        make_latest=make_latest,
        force_tag=args.force_tag,
    )
    print(f'Synced CNB release {args.tag} (id={release_id}): {", ".join(uploaded)}')


if __name__ == '__main__':
    main()
