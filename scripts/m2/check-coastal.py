#!/usr/bin/env python3
"""Verify coastal reads, receiver receipts, and analytics on an integrated development stack."""
import collections
import datetime as dt
import json
import re
import subprocess
import tempfile
import urllib.parse
import urllib.request

import check


def event_window(audit_url, credentials):
    headers = {'Authorization': 'Bearer ' + credentials['AUDIT_TOKEN']}
    code, first = check.request(audit_url + '/internal/audit/events?source=SENSOR_PANTAI&limit=1', headers=headers)
    assert code == 200 and not first['partial'] and first['items'], 'No complete coastal sample from Audit.'
    start = dt.datetime.fromisoformat(first['items'][0]['occurred_at']).replace(minute=0, second=0, microsecond=0)
    end = start + dt.timedelta(minutes=30)
    areas, severity, series = collections.Counter(), collections.Counter(), collections.Counter()
    cursor = ''
    while True:
        query = urllib.parse.urlencode({'limit': 250, **({'cursor': cursor} if cursor else {})})
        code, page = check.request(audit_url + '/internal/audit/events?' + query, headers=headers)
        assert code == 200 and not page['partial'], 'Audit window cannot be compared while units are missing.'
        for event in page['items']:
            occurred = dt.datetime.fromisoformat(event['occurred_at'])
            if not start <= occurred < end:
                continue
            areas[event['area_name']] += 1
            severity[event['severity']] += 1
            series[event['hazard_type']] += 1
            warnings = event['attributes'].get('tsunami_warnings')
            if event['hazard_type'] == 'SEISMIC' and isinstance(warnings, list) and warnings:
                series['TSUNAMI'] += 1
        cursor = page['next_cursor']
        if cursor is None:
            break
    assert areas, 'Choose a populated, stable window.'
    return start, end, areas, severity, series


def main():
    credentials = json.loads((check.STATE / 'credentials.json').read_text())
    services = json.loads(check.kubectl('get', 'service', '-o', 'json'))['items']
    urls = {s['metadata']['name']: 'http://{}:{}'.format(s['spec']['clusterIP'], s['spec']['ports'][0]['port']) for s in services}
    status, auth = check.request(urls['auth'] + '/v1/auth/login', 'POST',
                                 {'client_id': 'field-team', 'client_secret': credentials['FIELD_TEAM_CLIENT_SECRET']})
    assert status == 200
    headers = {'Authorization': 'Bearer ' + auth['access_token']}
    api = urls['client-api']
    code, coastal = check.request(api + '/v1/hazards?source=SENSOR_PANTAI&view=full&limit=1', headers=headers)
    assert code == 200 and not coastal['meta']['partial'] and coastal['data']
    assert coastal['data'][0]['hazard_type'] == 'COASTAL'
    with urllib.request.urlopen(api + '/analytics/', timeout=3) as response:
        assert '<title>Analitik Kebencanaan</title>' in response.read().decode()
    with tempfile.TemporaryFile(mode='w+') as log:
        with subprocess.Popen(check.KUBE + ['port-forward', 'service/aggregator', ':8083', '--address=127.0.0.1'],
                              stdout=log, stderr=log) as forward:
            try:
                def address():
                    assert forward.poll() is None, 'Audit port-forward exited.'
                    log.seek(0)
                    match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+)', log.read())
                    return 'http://127.0.0.1:' + match[1] if match else None

                start, end, areas, severity, series = event_window(check.wait_for(address, seconds=15), credentials)
            finally:
                forward.terminate()
    # Refresh the short-lived token after the Audit scan.
    code, auth = check.request(urls['auth'] + '/v1/auth/login', 'POST',
                               {'client_id': 'field-team', 'client_secret': credentials['FIELD_TEAM_CLIENT_SECRET']})
    assert code == 200
    headers = {'Authorization': 'Bearer ' + auth['access_token']}
    query = urllib.parse.urlencode({'from': start.isoformat(), 'to': end.isoformat()})
    code, aggregated = check.request(api + '/v1/analytics?' + query, headers=headers)
    assert code == 200 and not aggregated['partial'], aggregated
    assert aggregated['by_area'] == areas and aggregated['by_severity'] == severity
    assert aggregated['total_events'] == sum(areas.values()) and aggregated['by_category'] == series
    assert aggregated['coverage'] == {'available_units': 4, 'total_units': 4}
    categories = collections.Counter()
    for point in aggregated['series']:
        categories[point['category']] += point['count']
    assert categories == series
    code, receipts = check.request(urls['field-team-receiver'] + '/v1/notifications',
                                   headers={'Authorization': 'Bearer ' + credentials['FIELD_ALERT_TOKEN']})
    assert code == 200
    assert all(r['notification']['data']['attributes']['critical'] for r in receipts), 'Noncritical event reached Field Team.'
    report = {'passed': True, 'coastal_source_status': coastal['meta']['sources']['SENSOR_PANTAI']['status'],
              'audit_window': [start.isoformat(), end.isoformat()], 'analytics_matches_audit': True,
              'analytics_events': sum(areas.values()), 'categories': dict(categories),
              'critical_receipts_sampled': len(receipts)}
    (check.STATE / 'check-coastal.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
