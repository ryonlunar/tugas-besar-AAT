#!/usr/bin/env python3
"""Print upstream callers per 5 s bucket from mock logs: python3 owners_window.py SINCE LABEL."""
import collections
import json
import subprocess
import sys
from datetime import datetime

NS = 'sistem-informasi-kebencanaan-bnpb'
SOURCES = {'bmkg-mock': '/seismic-events', 'pvmbg-mock': '/volcanic-reports'}
report = {'label': sys.argv[2]}
for service, path in SOURCES.items():
    out = subprocess.check_output(['kubectl', '--context', 'aat-dev', '-n', NS, 'logs', 'deployment/' + service,
                                   f'--since={sys.argv[1]}', '--tail=-1'], text=True)
    buckets = collections.defaultdict(set)
    callers = collections.Counter()
    for line in out.splitlines():
        if not line.startswith('{'):
            continue
        r = json.loads(line)
        if r.get('path') != path or r.get('status') != 200 or not r.get('aggregator_instance'):
            continue
        t = datetime.fromisoformat(r['time'].replace('Z', '+00:00')).timestamp()
        buckets[int(t // 5)].add(r['aggregator_instance'])
        callers[r['aggregator_instance']] += 1
    report[service] = {'requests_by_caller': dict(callers),
                       'buckets': len(buckets),
                       'buckets_with_two_callers': sum(1 for b in buckets.values() if len(b) > 1)}
print(json.dumps(report))
