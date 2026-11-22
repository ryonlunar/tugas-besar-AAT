#!/usr/bin/env python3
"""Summarize probe JSONL around an event: python3 summarize.py FILE EPOCH [WINDOW_END_EPOCH]."""
import collections
import json
import sys

rows = [json.loads(line) for line in open(sys.argv[1])]
start = float(sys.argv[2])
end = float(sys.argv[3]) if len(sys.argv) > 3 else float('inf')
before = [r for r in rows if r['t'] < start]
during = [r for r in rows if start <= r['t'] < end]


def describe(name, part):
    if not part:
        print(name, 'no rows')
        return
    ms = sorted(r['ms'] for r in part)
    statuses = collections.Counter(str(r['status']) for r in part)
    partial = sum(1 for r in part if r.get('partial'))
    print(f"{name}: n={len(part)} statuses={dict(statuses)} partial={partial} "
          f"p50={ms[len(ms)//2]}ms p95={ms[int(len(ms)*0.95)-1]}ms max={ms[-1]}ms "
          f"span={part[-1]['t']-part[0]['t']:.0f}s")


describe('before', before)
describe('during', during)
for r in [r for r in during if r['status'] != 200][:8]:
    print('  non-200:', r)
