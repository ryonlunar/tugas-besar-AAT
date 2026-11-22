#!/usr/bin/env python3
"""Continuous client read probe for node-loss experiments.

Usage (inside aat-dev, from ~/aat): python3 probe.py OUT.jsonl
Reads go through the public path: Auth login, then Client API -> Aggregator
Service (load balanced across instances) -> all shards. Stops when
OUT.jsonl.stop exists. Each request has a 5 s client timeout, so a hanging
request is recorded as "timeout" instead of blocking the probe.
"""
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

NS = 'sistem-informasi-kebencanaan-bnpb'
out = Path(sys.argv[1])
stop = Path(str(out) + '.stop')


def cluster_ip(name):
    return subprocess.check_output(['kubectl', '--context', 'aat-dev', '-n', NS, 'get', 'svc', name,
                                    '-o', 'jsonpath={.spec.clusterIP}:{.spec.ports[0].port}'], text=True).strip()


auth, api = cluster_ip('auth'), cluster_ip('client-api')
secret = json.loads(Path('.state/m2/credentials.json').read_text())['FIELD_TEAM_CLIENT_SECRET']


def call(url, data=None, headers=None):
    request = urllib.request.Request(url, data=data, headers=headers or {})
    with urllib.request.urlopen(request, timeout=5) as response:
        return response.status, json.loads(response.read() or b'{}')


token, token_at = None, 0.0
paths = ['/v1/hazards?limit=50', '/v1/hazards?source=BMKG&limit=50', '/v1/hazards?source=PVMBG&limit=50']
with out.open('a') as log:
    i = 0
    while not stop.exists():
        started = time.time()
        row = {'t': round(started, 3)}
        try:
            if token is None or started - token_at > 45:
                _, body = call(f'http://{auth}/v1/auth/login',
                               json.dumps({'client_id': 'field-team', 'client_secret': secret}).encode(),
                               {'Content-Type': 'application/json'})
                token, token_at = body['access_token'], started
            path = paths[i % len(paths)]
            i += 1
            row['path'] = path
            status, body = call(f'http://{api}{path}', headers={'Authorization': 'Bearer ' + token})
            row['status'] = status
        except urllib.error.HTTPError as error:
            row['status'] = error.code
            try:
                body = json.loads(error.read() or b'{}')
            except ValueError:
                body = {}
        except Exception as error:  # timeout, connection reset
            row['status'] = 'timeout' if 'timed out' in str(error) else 'conn_error'
            row['error'] = str(error)[:120]
            body = {}
        row['ms'] = round((time.time() - started) * 1000, 1)
        meta = body.get('meta', {}) if isinstance(body, dict) else {}
        for key in ('partial', 'unavailable_units'):
            if key in meta:
                row[key] = meta[key]
        if isinstance(body, dict) and 'error' in body:
            row['error_body'] = body['error']
        if isinstance(body, dict) and isinstance(body.get('data'), list):
            row['items'] = len(body['data'])
        log.write(json.dumps(row) + '\n')
        log.flush()
        time.sleep(0.25)
