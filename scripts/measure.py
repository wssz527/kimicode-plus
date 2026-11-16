#!/usr/bin/env python3
"""Measure unsigned release binary; never launches the official client."""
import argparse
import gzip
import json
from pathlib import Path
import platform
import statistics
import subprocess
import tempfile
import time
from acceptance import Core

parser = argparse.ArgumentParser()
parser.add_argument('--binary', required=True)
parser.add_argument('--output', default='work/performance.json')
args = parser.parse_args()
binary = Path(args.binary).resolve()
Path('work').mkdir(exist_ok=True)
rounds = []
for _ in range(5):
    with tempfile.TemporaryDirectory(prefix='measure-', dir='work') as folder:
        started = time.perf_counter()
        core = Core(binary, folder)
        try:
            core.request('/health')
            startup = (time.perf_counter() - started) * 1000
            time.sleep(1)
            rss = None
            if platform.system() in ('Darwin', 'Linux'):
                rss = int(subprocess.check_output(['ps', '-o', 'rss=', '-p', str(core.process.pid)], text=True).strip()) * 1024
            exited = time.perf_counter()
            core.stop()
            shutdown = (time.perf_counter() - exited) * 1000
            rounds.append({'startupMs': startup, 'idleRSSBytes': rss, 'shutdownMs': shutdown})
        finally:
            core.stop()
summary = {}
for key in rounds[0]:
    values = [v[key] for v in rounds if v[key] is not None]
    summary[key] = None if not values else {'median': statistics.median(values), 'min': min(values), 'max': max(values)}
report = {'platform': platform.platform(), 'binary': binary.name, 'binaryBytes': binary.stat().st_size, 'gzipBytes': len(gzip.compress(binary.read_bytes(), compresslevel=9, mtime=0)), 'method': '5 fresh independent databases; process start to authenticated HTTP readiness; 10ms polling; warm OS cache; idle RSS after 1 second; core only, browser excluded', 'rounds': rounds, 'summary': summary}
output = Path(args.output)
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(report, ensure_ascii=False, indent=2))
