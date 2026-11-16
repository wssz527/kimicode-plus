#!/usr/bin/env python3
import datetime
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'work/review-0.1.1/evidence'
OUT.mkdir(parents=True, exist_ok=True)
label, *command = sys.argv[1:]
env = os.environ.copy()
local_go = ROOT.parent / 'work/toolchains/go/bin'
if local_go.is_dir():
    env['PATH'] = str(local_go) + os.pathsep + env['PATH']
started = datetime.datetime.now(datetime.timezone.utc).isoformat()
result = subprocess.run(command, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
log = OUT / (label + '.log')
log.write_bytes(result.stdout)
source_commit = subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()
paths = subprocess.check_output(['git','ls-files','-z','--cached','--others','--exclude-standard'],cwd=ROOT).decode().split('\0')
source_manifest = {name: hashlib.sha256((ROOT/name).read_bytes()).hexdigest() for name in sorted(set(paths)) if name and (ROOT/name).is_file()}
manifest_raw = json.dumps(source_manifest,sort_keys=True,separators=(',',':')).encode()
manifest_path = OUT / (label + '.source.json')
manifest_path.write_bytes(manifest_raw)
dirty = bool(subprocess.check_output(['git','status','--porcelain'],cwd=ROOT))
record = {'sourceCommit':source_commit,'dirty':dirty,'sourceManifest':manifest_path.name,'sourceManifestSHA256':hashlib.sha256(manifest_raw).hexdigest(),'command': command, 'cwd': '.', 'startedAt': started,
          'finishedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
          'exitCode': result.returncode, 'log': log.name,
          'logSha256': hashlib.sha256(result.stdout).hexdigest(), 'status': 'EXECUTED'}
if '--binary' in command:
    candidate = Path(command[command.index('--binary')+1])
    if not candidate.is_absolute(): candidate=ROOT/candidate
    record['testedBinarySHA256']=hashlib.sha256(candidate.read_bytes()).hexdigest()
(OUT / (label + '.json')).write_text(json.dumps(record, indent=2) + '\n')
if os.environ.get('EKD_EVIDENCE_QUIET') != '1':
    sys.stdout.buffer.write(result.stdout)
sys.exit(result.returncode)
