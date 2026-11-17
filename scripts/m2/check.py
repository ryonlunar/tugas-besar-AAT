#!/usr/bin/env python3
"""Check the deployed k3s application flow and storage isolation."""
import datetime
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'
os.environ.setdefault('KUBECONFIG', str(Path.home() / '.kube' / 'aat-dev.yaml'))
KUBE = ['kubectl', '--context', 'aat-dev', '-n', NAMESPACE]
REPORT = {'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
          'profile': 'single-node-development', 'passed': False, 'checks': {}}


def kubectl(*args, stdin=None):
    return subprocess.check_output(KUBE + list(args), input=stdin, text=True)


def request(url, method='GET', payload=None, headers=None):
    data = None if payload is None else json.dumps(payload).encode()
    headers = dict(headers or {})
    if data is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=8) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def wait_for(check, seconds=45):
    deadline = time.monotonic() + seconds
    while True:
        try:
            result = check()
            if result:
                return result
        except (urllib.error.URLError, TimeoutError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError('Timed out waiting for a development check.')
        time.sleep(2)


def main():
    credentials = json.loads((STATE / 'credentials.json').read_text())
    clustered = bool(kubectl('get', 'configmap', 'aat-storage-cutover', '--ignore-not-found', '-o', 'name'))
    REPORT['profile'] = 'multi-node-development' if clustered else 'single-node-development'
    services = json.loads(kubectl('get', 'services', '-o', 'json'))['items']
    urls = {s['metadata']['name']: 'http://{}:{}'.format(
        s['spec']['clusterIP'], s['spec']['ports'][0]['port']) for s in services}
    checks = REPORT['checks']
    ready_services = ['bmkg-mock', 'pvmbg-mock', 'auth', 'client-api']
    if not clustered:
        ready_services.append('aggregator')
    # The VM host has no API-role identity for a remote Aggregator. Its private
    # endpoint is checked through Client API below, respecting NetworkPolicies.
    for name in ready_services:
        wait_for(lambda name=name: request(urls[name] + '/ready')[0] == 200)
    checks['http_readiness'] = True

    def login(client_id, key):
        status, body = request(urls['auth'] + '/v1/auth/login', 'POST',
                               {'client_id': client_id, 'client_secret': credentials[key]})
        assert status == 200 and body['expires_in'] == 60, 'Login or token TTL failed.'
        return {'Authorization': 'Bearer ' + body['access_token']}

    field = login('field-team', 'FIELD_TEAM_CLIENT_SECRET')
    media = login('media', 'MEDIA_CLIENT_SECRET')
    for source in ['BMKG', 'PVMBG']:
        path = '/v1/hazards?source=' + source + '&view=full&limit=1'
        body = wait_for(lambda: read_nonempty(urls['client-api'] + path, field))
        assert 'attributes' in body['data'][0], 'Full view omitted attributes.'
    checks['source_ingestion'] = True
    checks['token_ttl_seconds'] = 60

    status, summary = request(urls['client-api'] + '/v1/hazards?limit=1', headers=media)
    expected = {'hazard_id', 'source', 'hazard_type', 'severity', 'area_name',
                'occurred_at', 'ingested_at'}
    assert status == 200 and set(summary['data'][0]) == expected, 'Media summary failed.'
    status, _ = request(urls['client-api'] + '/v1/hazards?view=full', headers=media)
    assert status == 403, 'Media raw-data restriction failed.'
    checks['media_access_restriction'] = True
    for name, path, header, wrong in [
        ('bmkg-mock', '/seismic-events', 'X-BMKG-Key', 'PVMBG_TOKEN'),
        ('pvmbg-mock', '/volcanic-reports', 'Authorization', 'BMKG_API_KEY'),
    ]:
        value = credentials[wrong]
        status, _ = request(urls[name] + path, headers={header: 'Bearer ' + value if header == 'Authorization' else value})
        assert status in (401, 403), 'Source credential isolation failed.'
    checks['source_credential_isolation'] = True

    for name in ['dashboard-consumer', 'notifier-consumer']:
        def inspection(name=name):
            body = json.loads(kubectl('exec', 'deployment/' + name, '--', '/' + name, 'inspect'))
            return body if body['inbox_count'] > 0 else None
        body = wait_for(inspection)
        assert body['quarantine_count'] == 0, 'Unexpected quarantined canonical event.'
        checks[name] = {'inbox_count': body['inbox_count'], 'quarantine_count': 0}

    correlation = 'm2-development-check-' + uuid.uuid4().hex[:8]
    headers = dict(field, **{'X-Correlation-ID': correlation})
    status, _ = request(urls['client-api'] + '/v1/hazards?source=BMKG&limit=1', headers=headers)
    assert status == 200, 'Correlation request failed.'
    query = urllib.parse.urlencode({'query': '{service=~"client-api|aggregator"} |= "' + correlation + '"'})
    def logs_present():
        status, body = request(urls['loki'] + '/loki/api/v1/query_range?' + query)
        result = body.get('data', {}).get('result', [])
        return status == 200 and {'client-api', 'aggregator'} <= {row['stream']['service'] for row in result}
    wait_for(logs_present)
    checks['correlated_logs'] = True
    metrics_query = urllib.parse.urlencode({'query': 'up{job=~"applications|nats"}'})
    expected_services = {'bmkg-mock', 'pvmbg-mock', 'aggregator', 'auth', 'client-api',
                         'dashboard-consumer', 'notifier-consumer', 'nats'}
    def metrics_present():
        status, body = request(urls['prometheus'] + '/api/v1/query?' + metrics_query)
        rows = body.get('data', {}).get('result', [])
        up = {row['metric']['service'] for row in rows if row['value'][1] == '1'}
        return status == 200 and expected_services <= up
    wait_for(metrics_present)
    checks['metrics_targets'] = sorted(expected_services)
    assert request(urls['grafana'] + '/api/health')[0] == 200, 'Grafana is unavailable.'
    checks['grafana'] = True

    job_name = 'm2-policy-check-' + uuid.uuid4().hex[:8]
    stores = [f'shard-{i}' for i in range(4)] if clustered else ['postgres']
    code = 'stores=' + repr(stores) + '\n' + '''import socket
with socket.create_connection(("aggregator",8083),timeout=3): pass
for store in stores:
    store_ip=socket.gethostbyname(store)
    try:
        socket.create_connection((store_ip,5432),timeout=3)
    except (OSError,TimeoutError):
        print(store+": direct PostgreSQL connection denied")
    else:
        raise SystemExit(store+": direct PostgreSQL connection unexpectedly allowed")
'''
    job = {'apiVersion': 'batch/v1', 'kind': 'Job', 'metadata': {'name': job_name},
           'spec': {'backoffLimit': 0, 'activeDeadlineSeconds': 90,
                    'template': {'metadata': {'labels': {'app': 'client-api', 'role': 'api'}},
                                 'spec': {'restartPolicy': 'Never', 'automountServiceAccountToken': False,
                                          'containers': [{'name': 'check', 'image': 'python:3.12-alpine',
                                                          'command': ['python3', '-c', code],
                                                          'readinessProbe': {'tcpSocket': {'port': 1}},
                                                          'resources': {'requests': {'memory': '128Mi'},
                                                                        'limits': {'memory': '256Mi'}}}]}}}}
    try:
        kubectl('apply', '-f', '-', stdin=json.dumps(job))
        kubectl('wait', '--for=condition=complete', 'job/' + job_name, '--timeout=100s')
        checks['storage_network_isolation'] = True
    finally:
        kubectl('delete', 'job/' + job_name, '--ignore-not-found=true')
    REPORT['passed'] = True


def read_nonempty(url, headers):
    status, body = request(url, headers=headers)
    return body if status == 200 and body.get('data') else None


if __name__ == '__main__':
    try:
        main()
    finally:
        STATE.mkdir(parents=True, exist_ok=True)
        (STATE / 'check.json').write_text(json.dumps(REPORT, indent=2) + '\n')
    print(json.dumps(REPORT, indent=2))
