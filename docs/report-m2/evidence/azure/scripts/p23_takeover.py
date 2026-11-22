#!/usr/bin/env python3
"""P2.3: kill the instance fetching BMKG/PVMBG, measure fetch gap from mock logs.

Run inside aat-dev from ~/aat with KUBECONFIG set. The gap is measured from the
upstream mocks' own request log: last successful request from the old owner to
first successful request from any other instance.
"""
import json
import subprocess
import time
from datetime import datetime

NS = 'sistem-informasi-kebencanaan-bnpb'
KUBE = ['kubectl', '--context', 'aat-dev', '-n', NS]
SOURCES = {'bmkg-mock': '/seismic-events', 'pvmbg-mock': '/volcanic-reports'}


def requests(service, since='120s'):
    out = subprocess.check_output(KUBE + ['logs', 'deployment/' + service, f'--since={since}', '--tail=-1'], text=True)
    rows = []
    for line in out.splitlines():
        if line.startswith('{'):
            r = json.loads(line)
            if r.get('path') == SOURCES[service] and r.get('status') == 200 and r.get('aggregator_instance'):
                rows.append(r)
    return rows


def stamp(r):
    value = r.get('time') or r.get('ts') or r.get('timestamp')
    return datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()


owners = {s: requests(s, '20s')[-1]['aggregator_instance'] for s in SOURCES}
print('owners_before', owners)
victim = owners['bmkg-mock']
killed_at = time.time()
subprocess.run(KUBE + ['delete', 'pod', victim, '--grace-period=0', '--force'], check=True,
               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
print('killed', victim, 'at', killed_at)
result = {}
deadline = killed_at + 120
while time.time() < deadline and len(result) < len(SOURCES):
    time.sleep(2)
    for service in SOURCES:
        if service in result:
            continue
        rows = requests(service, '180s')
        old = [stamp(r) for r in rows if r['aggregator_instance'] == victim]
        new = [r for r in rows if r['aggregator_instance'] != victim and stamp(r) > killed_at - 1]
        if new and owners[service] == victim:
            last_old = max(old) if old else None
            first_new = stamp(new[0])
            result[service] = {'old_owner': victim, 'new_owner': new[0]['aggregator_instance'],
                               'last_old_request': last_old, 'first_new_request': first_new,
                               'gap_seconds': round(first_new - last_old, 2) if last_old else None,
                               'since_kill_seconds': round(first_new - killed_at, 2)}
        elif owners[service] != victim:
            later = {r['aggregator_instance'] for r in rows if stamp(r) > killed_at + 5}
            result[service] = {'owner_unchanged': owners[service], 'callers_after_kill': sorted(later)}
print(json.dumps(result, indent=2))
