#!/usr/bin/env python3
"""Print Audit checksum per source through a port-forward (inside aat-dev, from ~/aat)."""
import json
import socket
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

NS = 'sistem-informasi-kebencanaan-bnpb'
token = json.loads(Path('.state/m2/credentials.json').read_text())['AUDIT_TOKEN']
with socket.socket() as s:
    s.bind(('127.0.0.1', 0))
    port = s.getsockname()[1]
forward = subprocess.Popen(['kubectl', '--context', 'aat-dev', '-n', NS, 'port-forward', 'service/aggregator',
                            f'{port}:8083', '--address=127.0.0.1'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    for _ in range(40):  # wait for the port-forward to listen (slow under memory pressure)
        try:
            socket.create_connection(('127.0.0.1', port), timeout=1).close()
            break
        except OSError:
            time.sleep(0.5)
    result = {'label': sys.argv[1] if len(sys.argv) > 1 else '', 'at': time.strftime('%Y-%m-%dT%H:%M:%S%z')}
    for source in ('BMKG', 'PVMBG', 'SENSOR_PANTAI'):
        request = urllib.request.Request(f'http://127.0.0.1:{port}/internal/audit/checksum?source={source}',
                                         headers={'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(request, timeout=10) as response:
            body = json.loads(response.read())
        result[source] = {k: body.get(k) for k in ('count', 'checksum', 'partial', 'unavailable_units')}
    ids, keys, cursor, partial = [], [], None, False
    while True:
        query = '/internal/audit/events?limit=1000' + (f'&cursor={urllib.parse.quote(cursor)}' if cursor else '')
        request = urllib.request.Request(f'http://127.0.0.1:{port}{query}', headers={'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(request, timeout=30) as response:
            body = json.loads(response.read())
        partial = partial or body.get('partial', False)
        ids += [item['hazard_id'] for item in body['items']]
        keys +=[(item['source'], item['source_ref_id']) for item in body['items']]
        cursor = body.get('next_cursor')
        if not cursor:
            break
    result['events_scanned'] = len(ids)
    result['events_partial'] = partial
    result['duplicate_source_refs'] = len(keys) - len(set(keys))
    if len(sys.argv) > 2:
        Path(sys.argv[2]).write_text('\n'.join(sorted(ids)) + '\n')
    print(json.dumps(result))
finally:
    forward.terminate()
