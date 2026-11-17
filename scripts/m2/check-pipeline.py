#!/usr/bin/env python3
"""Check Audit API; optionally pause processing and exercise durable intake recovery."""
import argparse
import hashlib
import json
import re
import subprocess
import urllib.parse
import urllib.error

import check


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--exercise-recovery', action='store_true', help='Roll Aggregators with processing paused, replace one receiver, then restore processing.')
    args = parser.parse_args()
    credentials = json.loads((check.STATE / 'credentials.json').read_text())
    report = {'passed': False, 'recovery_exercised': args.exercise_recovery, 'checks': {}}
    checks = report['checks']
    log_path = check.STATE / 'audit-port-forward.log'
    paused = False
    forward = None
    try:
        with log_path.open('w') as log:
            def connect():
                nonlocal forward
                if forward is not None:
                    forward.terminate()
                    try:
                        forward.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        forward.kill()
                        forward.wait()
                log.seek(0)
                log.truncate()
                forward = subprocess.Popen(check.KUBE + ['port-forward', 'service/aggregator', ':8083', '--address=127.0.0.1'], stdout=log, stderr=log)
                def address():
                    if forward.poll() is not None:
                        raise RuntimeError('Audit port-forward stopped.')
                    match = re.search(r'Forwarding from 127\.0\.0\.1:(\d+)', log_path.read_text())
                    return 'http://127.0.0.1:' + match[1] if match else None
                return check.wait_for(address, seconds=20)
            base = connect()
            audit_headers = {'Authorization': 'Bearer ' + credentials['AUDIT_TOKEN']}
            owner_headers = {'X-Internal-Key': credentials['AGGREGATOR_API_KEY']}
            def request(path, headers=None, method='GET'):
                nonlocal base
                for attempt in range(2):
                    if forward.poll() is not None:
                        base = connect()
                    try:
                        return check.request(base + path, method=method, headers=headers or audit_headers)
                    except (urllib.error.URLError, TimeoutError, ConnectionResetError):
                        if attempt:
                            raise
                        base = connect()
            status, _ = request('/internal/audit/events', headers={'Authorization': 'Bearer incorrect'})
            assert status == 401, 'Invalid audit token accepted.'
            status, _ = request('/internal/v1/failed-messages', headers=audit_headers)
            assert status == 401, 'Audit token gained raw inspection privileges.'
            checks['credential_separation'] = True
            status, page = request('/internal/audit/events?limit=3')
            assert status == 200 and page['items'] and not page['partial']
            status, location = request('/internal/audit/location?hazard_id=' + page['items'][0]['hazard_id'])
            assert status == 200 and not location['partial'], 'Physical location incomplete.'
            assert len(location['locations']) == 3 and len({p['node'] for p in location['locations']}) == 3
            assert [p['role'] for p in location['locations']].count('primary') == 1
            checks['physical_copies'] = location['locations']
            def scan(strict=True):
                events, cursor = {}, ''
                while True:
                    suffix = '&cursor=' + urllib.parse.quote(cursor) if cursor else ''
                    status, page = request('/internal/audit/events?limit=1000' + suffix)
                    assert status == 200 and not page['partial']
                    for event in page['items']:
                        if strict:
                            assert event['hazard_id'] not in events, 'Audit pagination repeated an event.'
                        events[event['hazard_id']] = event
                    cursor = page['next_cursor']
                    if not cursor:
                        return events
            def rollout():
                check.kubectl('rollout', 'status', 'deployment/aggregator', '--timeout=240s')
                def settled():
                    pods = json.loads(check.kubectl('get', 'pods', '-l', 'app=aggregator,!job-name', '-o', 'json'))['items']
                    return len(pods) == 3 and all(not p['metadata'].get('deletionTimestamp') for p in pods)
                check.wait_for(settled, seconds=90)
            if args.exercise_recovery:
                deployment = json.loads(check.kubectl('get', 'deployment', 'aggregator', '-o', 'json'))
                variables = deployment['spec']['template']['spec']['containers'][0].get('env', [])
                original = next((e['value'] for e in variables if e['name'] == 'INGEST_PROCESSING_ENABLED'), None)
                runtime = json.loads(check.kubectl('get', 'configmap', 'aat-runtime', '-o', 'json'))['data']
                assert (original or runtime.get('INGEST_PROCESSING_ENABLED', 'true')).lower() == 'true', 'Recovery check requires processing initially enabled.'
                restore_setting = 'INGEST_PROCESSING_ENABLED=' + original if original is not None else 'INGEST_PROCESSING_ENABLED-'
                paused = True
                check.kubectl('set', 'env', 'deployment/aggregator', 'INGEST_PROCESSING_ENABLED=false')
                rollout()
            # Pause gives a quiescent canonical dataset for an exact checksum check.
            # Without pause, ingest may add/update records between these reads.
            before = scan() if args.exercise_recovery else {e['hazard_id']: e for e in page['items']}
            status, checksum = request('/internal/audit/checksum')
            assert status == 200 and not checksum['partial']
            calculated = hashlib.sha256('\n'.join(sorted((e['source_ref_id'] for e in before.values()), key=lambda s: s.encode('utf-8'))).encode('utf-8')).hexdigest()
            if args.exercise_recovery:
                assert checksum['count'] == len(before) and checksum['checksum'] == calculated, 'Checksum differs from the quiescent scan.'
                checks['checksum_matches_events'] = True
                def pending():
                    status, page = request('/internal/audit/unprocessed?limit=3')
                    return page['items'] if status == 200 and not page['partial'] and page['items'] else None
                inputs = check.wait_for(pending, seconds=60)
                checks['durable_inputs_while_processing_paused'] = len(inputs)
                # Replace a current source owner, with no changes to raw storage.
                source = inputs[0]['source']
                endpoint = '/seismic-events' if source == 'BMKG' else '/volcanic-reports'
                mock = 'bmkg-mock' if source == 'BMKG' else 'pvmbg-mock'
                def owner():
                    lines = check.kubectl('logs', 'deployment/' + mock, '--since=10s', '--tail=-1').splitlines()
                    requests = [json.loads(line) for line in lines if line.startswith('{')]
                    callers = [r.get('aggregator_instance') for r in requests if r.get('path') == endpoint and r.get('status') == 200]
                    return next((c for c in reversed(callers) if c), None)
                receiver = check.wait_for(owner)
                check.kubectl('delete', 'pod', receiver, '--wait=true', '--timeout=60s')
                rollout()
                for item in inputs:
                    status, detail = request('/internal/v1/failed-messages/' + item['message_id'], headers=owner_headers)
                    assert status == 200 and detail['payload_base64'], 'Committed raw input lost after receiver replacement.'
                checks['receiver_replacement_preserves_inputs'] = True
                status, queued = request('/internal/v1/failed-messages/' + inputs[0]['message_id'] + '/replay', headers=owner_headers, method='POST')
                assert status == 202 and queued['status'] == 'queued'
                check.kubectl('set', 'env', 'deployment/aggregator', restore_setting)
                check.kubectl('rollout', 'status', 'deployment/aggregator', '--timeout=240s')
                paused = False
                for item in inputs:
                    def processed(item=item):
                        status, _ = request('/internal/v1/failed-messages/' + item['message_id'], headers=owner_headers)
                        return status == 404
                    check.wait_for(processed, seconds=120)
                after = scan(strict=False)
                missing = set(before) - set(after)
                assert not missing, 'Baseline canonical IDs disappeared.'
                checks['records_before'] = len(before)
                checks['records_after'] = len(after)
                checks['missing_baseline_records'] = len(missing)
                checks['queued_inputs_processed_after_resume'] = True
            else:
                checks['audit_records_sampled'] = len(before)
                checks['checksum_count_observed'] = checksum['count']
            report['passed'] = True
    finally:
        try:
            if paused:
                check.kubectl('set', 'env', 'deployment/aggregator', restore_setting)
                check.kubectl('rollout', 'status', 'deployment/aggregator', '--timeout=240s')
        finally:
            if forward is not None:
                forward.terminate()
                try:
                    forward.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    forward.kill()
                    forward.wait()
            (check.STATE / ('check-pipeline-recovery.json' if args.exercise_recovery else 'check-pipeline.json')).write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
