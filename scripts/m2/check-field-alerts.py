#!/usr/bin/env python3
"""Reconcile an independent coastal critical-event list with durable Field Team receipts."""

import argparse
from collections import Counter
import datetime as dt
import json
import math
from pathlib import Path
from urllib.parse import urlencode

import check


def timestamp(value):
    instant = dt.datetime.fromisoformat(value)
    if instant.utcoffset() is None:
        raise ValueError('Expected a timestamp with a time zone.')
    return instant


def p95(samples):
    return round(sorted(samples)[math.ceil(len(samples) * 0.95) - 1], 6) if samples else None


def reconcile(expected, receipts):
    counts = Counter()
    intake_delays = []
    delivery_delays = []
    for receipt in receipts:
        envelope = receipt['notification']
        event = envelope['data']
        identity = event['source_ref_id']
        if event['source'] != 'SENSOR_PANTAI' or not event['attributes']['critical'] or identity not in expected:
            continue
        counts[identity] += 1
        received = timestamp(receipt['received_at'])
        intake_delays.append((received - timestamp(event['attributes']['received_at'])).total_seconds())
        if expected[identity] is not None:
            delivery_delays.append((received - expected[identity]).total_seconds())
    missing = sorted(expected.keys() - counts.keys())
    duplicates = sorted(identity for identity, count in counts.items() if count > 1)
    invalid_delays = sum(delay < 0 for delay in delivery_delays)
    delivery_p95 = p95([delay for delay in delivery_delays if delay >= 0])
    full_timing = len(delivery_delays) == len(expected) and not invalid_delays and not missing and not duplicates
    late_receipts = sum(delay > 5 for delay in delivery_delays)
    late_budget = len(expected) - math.ceil(0.95 * len(expected))
    return {
        'expected_critical': len(expected),
        'matched_critical': len(counts),
        'missing_observation_ids': missing,
        'duplicate_observation_ids': duplicates,
        'intake_to_receipt_p95_seconds': p95([delay for delay in intake_delays if delay >= 0]),
        'availability_to_receipt_samples': len(delivery_delays),
        'negative_availability_delays': invalid_delays,
        'availability_to_receipt_p95_seconds': delivery_p95,
        'late_receipts': late_receipts,
        'late_receipt_budget': late_budget,
        'late_receipt_budget_fraction': round(late_receipts / late_budget, 6) if full_timing and late_budget else None,
        'target_met': full_timing and delivery_p95 is not None and delivery_p95 <= 5,
    }


def receipts_from(url, token):
    cursor = '0'
    while True:
        code, payload = check.request(url + '/v1/notifications?' + urlencode({'after': cursor}),
                                      headers={'Authorization': 'Bearer ' + token})
        if code != 200 or not isinstance(payload, dict):
            raise RuntimeError(f'Receiver could not provide a complete receipt scan (HTTP {code}).')
        yield from payload['items']
        next_cursor = payload.get('next_cursor')
        if next_cursor is None:
            return
        if int(next_cursor) <= int(cursor):
            raise RuntimeError('Receiver returned a non-advancing cursor.')
        cursor = next_cursor


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('expected', type=Path, help='JSON array of observation_id and optional available_at.')
    args = parser.parse_args()
    rows = json.loads(args.expected.read_text())
    expected = {}
    for row in rows:
        identity = row['observation_id']
        if not isinstance(identity, str) or not identity or identity in expected:
            parser.error('Expected unique, non-empty observation IDs.')
        available = row.get('available_at')
        expected[identity] = timestamp(available) if available is not None else None
    if not expected:
        parser.error('The expected list must contain critical observations.')
    credentials = json.loads((check.STATE / 'credentials.json').read_text())
    service = json.loads(check.kubectl('get', 'service', 'field-team-receiver', '-o', 'json'))['spec']
    url = 'http://{}:{}'.format(service['clusterIP'], service['ports'][0]['port'])
    report = reconcile(expected, receipts_from(url, credentials['FIELD_ALERT_TOKEN']))
    (check.STATE / 'check-field-alerts.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))
    return 0 if report['target_met'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
