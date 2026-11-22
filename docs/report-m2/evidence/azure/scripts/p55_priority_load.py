#!/usr/bin/env python3
"""P5.5 small overload test: Media read flood + Tim Lapangan report stream, together.

Usage (inside aat-dev from ~/aat): python3 p55_priority_load.py MEDIA_THREADS FIELD_THREADS SECONDS
Media threads loop GET /v1/hazards?limit=50 as fast as possible; Field threads
loop POST /field-reports and GET /v1/hazards (field-team token). Records per-request status
and latency and prints 429 share and p95 per client. Not the 10,000-VU P2.6 test.
"""
import collections
import json
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

NS = 'sistem-informasi-kebencanaan-bnpb'
media_threads, field_threads, seconds = int(sys.argv[1]), int(sys.argv[2]), float(sys.argv[3])


def svc(name):
    return subprocess.check_output(['kubectl', '--context', 'aat-dev', '-n', NS, 'get', 'svc', name, '-o',
                                    'jsonpath={.spec.clusterIP}:{.spec.ports[0].port}'], text=True).strip()


AUTH, API = 'http://' + svc('auth'), 'http://' + svc('client-api')
creds = json.loads(Path('.state/m2/credentials.json').read_text())
body = Path('contracts/examples/field-report.json').read_bytes()


def login(client, key):
    request = urllib.request.Request(AUTH + '/v1/auth/login', json.dumps(
        {'client_id': client, 'client_secret': creds[key]}).encode(), {'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.loads(response.read())['access_token']


tokens = {'media': login('media', 'MEDIA_CLIENT_SECRET'), 'field': login('field-team', 'FIELD_TEAM_CLIENT_SECRET')}
lock = threading.Lock()
results = collections.defaultdict(list)
stop_at = time.time() + seconds


def refresher():
    while time.time() < stop_at:
        time.sleep(40)
        tokens['media'] = login('media', 'MEDIA_CLIENT_SECRET')
        tokens['field'] = login('field-team', 'FIELD_TEAM_CLIENT_SECRET')


def one(kind, request):
    started = time.time()
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            response.read()
            status = response.status
    except urllib.error.HTTPError as error:
        status = error.code
    except Exception:
        status = 'conn_error'
    with lock:
        results[kind].append((status, (time.time() - started) * 1000))


def media_loop():
    while time.time() < stop_at:
        one('media_read', urllib.request.Request(API + '/v1/hazards?limit=50',
                                                 headers={'Authorization': 'Bearer ' + tokens['media']}))


def field_loop(index):
    i = 0
    while time.time() < stop_at:
        i += 1
        headers = {'Authorization': 'Bearer ' + tokens['field'], 'Content-Type': 'application/json',
                   'Idempotency-Key': f'load-{index}-{i}-{time.time_ns()}'}
        one('field_report', urllib.request.Request(API + '/field-reports', body, headers))
        one('field_read', urllib.request.Request(API + '/v1/hazards?limit=50',
                                                 headers={'Authorization': 'Bearer ' + tokens['field']}))
        time.sleep(0.2)


threads = [threading.Thread(target=refresher, daemon=True)]
threads += [threading.Thread(target=media_loop) for _ in range(media_threads)]
threads += [threading.Thread(target=field_loop, args=(i,)) for i in range(field_threads)]
for t in threads:
    t.start()
for t in threads[1:]:
    t.join()
summary = {'media_threads': media_threads, 'field_threads': field_threads, 'seconds': seconds}
for kind, rows in results.items():
    codes = collections.Counter(str(s) for s, _ in rows)
    ms = sorted(m for s, m in rows if s in (200, 202))
    summary[kind] = {'n': len(rows), 'rps': round(len(rows) / seconds, 1), 'codes': dict(codes),
                     'rejected_429_pct': round(100 * codes.get('429', 0) / len(rows), 2),
                     'success_pct': round(100 * sum(v for k, v in codes.items() if k in ('200', '202')) / len(rows), 2),
                     'p50_ms': round(ms[len(ms) // 2], 1) if ms else None,
                     'p95_ms': round(ms[int(len(ms) * 0.95) - 1], 1) if ms else None}
print(json.dumps(summary, indent=2))
