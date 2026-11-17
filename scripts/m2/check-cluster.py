#!/usr/bin/env python3
"""Check multi-node placement, replication, source ownership, and a live trace."""
import json
import urllib.parse
import uuid

import check


def sql(pod, query):
    return json.loads(check.kubectl('exec', pod, '--', 'psql', '-U', 'postgres', '-d', 'aat', '-Atc', query))


def main():
    check.main()
    report = check.REPORT
    report['passed'] = False
    assert report['profile'] == 'multi-node-development', 'Deploy the multi-node profile first.'
    checks = report['checks']
    pods = json.loads(check.kubectl('get', 'pods', '-o', 'json'))['items']
    live = [pod for pod in pods if pod['status'].get('phase') == 'Running']
    routers = [pod for pod in live if pod['metadata'].get('labels', {}).get('app') == 'aggregator']
    assert len(routers) == 3 and len({pod['spec']['nodeName'] for pod in routers}) == 3, 'Expected three Aggregators on separate workers.'
    checks['aggregator_nodes'] = sorted(pod['spec']['nodeName'] for pod in routers)
    units = {}
    for i in range(4):
        name = f'shard-{i}'
        instances = [pod for pod in live if pod['metadata'].get('labels', {}).get('cluster-name') == name]
        nodes = {pod['spec']['nodeName'] for pod in instances}
        assert len(instances) == 3 and len(nodes) == 3, f'{name}: replicas must occupy three distinct nodes.'
        excluded = ['aat-dev', 'aat-worker-1', 'aat-worker-2', 'aat-worker-3'][i]
        assert excluded not in nodes, f'{name}: forbidden placement.'
        primary = [pod for pod in instances if pod['metadata']['labels'].get('postgres-role') == 'master']
        assert len(primary) == 1, f'{name}: expected one primary.'
        pod = primary[0]['metadata']['name']
        state = sql(pod, """SELECT json_build_object(
            'records',(SELECT count(*) FROM hazard_events),
            'pending',(SELECT count(*) FROM outbox WHERE published_at IS NULL),
            'streaming',(SELECT count(*) FROM pg_stat_replication WHERE state='streaming'),
            'synchronous',(SELECT count(*) FROM pg_stat_replication WHERE sync_state='sync'),
            'synchronous_commit',current_setting('synchronous_commit'))""")
        assert state['streaming'] == 2 and state['synchronous'] == 1 and state['synchronous_commit'] == 'on', f'{name}: replication is not fully restored.'
        recovery = json.loads(check.kubectl('exec', pod, '--', 'python3', '-c',
            'import json,urllib.request; d=json.load(urllib.request.urlopen("http://127.0.0.1:8008/config"))["postgresql"]; '
            'print(json.dumps({k:d.get(k) for k in ["use_pg_rewind","remove_data_directory_on_rewind_failure","remove_data_directory_on_diverged_timelines"]}))'))
        assert all(value is True for value in recovery.values()), f'{name}: automatic replica recovery is not configured.'
        state['recovery'] = recovery
        units[name] = dict(state, primary=pod, nodes=sorted(nodes))
    assert all(unit['records'] > 0 for unit in units.values()), 'Canonical data must span all four units.'
    checks['storage_units'] = units

    broker_pods = [pod for pod in live if pod['metadata'].get('labels', {}).get('app') == 'nats-ha']
    assert len(broker_pods) == 3 and len({p['spec']['nodeName'] for p in broker_pods}) == 3, 'Broker placement is not independent.'
    broker_streams = {}
    for i in range(3):
        pod = f'nats-ha-{i}'
        info = json.loads(check.kubectl('exec', pod, '-c', 'nats', '--', 'wget', '-qO', '-',
                                      'http://127.0.0.1:8222/jsz?accounts=true&streams=true&consumers=true&config=true'))
        broker_streams[pod] = next(stream for account in info.get('account_details', [])
                                  for stream in account.get('stream_detail', []) if stream['name'] == 'HAZARD_EVENTS')
    # Followers can retain stale peer-health observations after a leader change.
    # Validate replication using the current leader's view, not a fixed broker.
    stream = broker_streams[broker_streams['nats-ha-0']['cluster']['leader']]
    assert stream['config']['num_replicas'] == 3, 'Stream replication must be three.'
    assert len(stream['cluster']['replicas']) == 2 and all(p['current'] for p in stream['cluster']['replicas']), 'Stream replicas have not caught up.'
    consumers = stream.get('consumer_detail', [])
    assert {c['name'] for c in consumers} >= {'dashboard-v1', 'notifier-v1'}, 'Independent durable consumers missing.'
    for consumer in consumers:
        leader = consumer['cluster']['leader']
        authoritative = next(c for c in broker_streams[leader]['consumer_detail'] if c['name'] == consumer['name'])
        replicas = authoritative.get('cluster', {}).get('replicas', [])
        assert len(replicas) == 2 and all(p['current'] for p in replicas), 'Consumer state must also be replicated and current.'
    checks['jetstream_replicas'] = 3

    owners = {}
    for service, path in [('bmkg-mock', '/seismic-events'), ('pvmbg-mock', '/volcanic-reports')]:
        def source_owner():
            lines = check.kubectl('logs', 'deployment/' + service, '--since=15s', '--tail=-1').splitlines()
            records = [json.loads(line) for line in lines if line.startswith('{')]
            requests = [r for r in records if r.get('path') == path and r.get('status') == 200 and r.get('aggregator_instance')]
            callers = {r['aggregator_instance'] for r in requests}
            return callers.pop() if len(requests) >= 2 and len(callers) == 1 else None
        # A completed rollout can still be followed by the final source handoff.
        # Require a stable observation window rather than treating that handoff
        # as evidence of simultaneous duplicate polling.
        owners[service] = check.wait_for(source_owner)
    checks['source_callers'] = owners

    services = json.loads(check.kubectl('get', 'services', '-o', 'json'))['items']
    urls = {s['metadata']['name']: f"http://{s['spec']['clusterIP']}:{s['spec']['ports'][0]['port']}" for s in services}
    credentials = json.loads((check.STATE / 'credentials.json').read_text())
    status, token = check.request(urls['auth'] + '/v1/auth/login', 'POST',
                                  {'client_id': 'field-team', 'client_secret': credentials['FIELD_TEAM_CLIENT_SECRET']})
    assert status == 200
    trace_id = uuid.uuid4().hex
    headers = {'Authorization': 'Bearer ' + token['access_token'], 'traceparent': f'00-{trace_id}-{uuid.uuid4().hex[:16]}-01'}
    status, page = check.request(urls['client-api'] + '/v1/hazards?view=full&limit=3', headers=headers)
    assert status == 200 and page['meta']['partial'] is False and page['meta']['unavailable_units'] == [], 'Healthy fan-out must be complete.'
    assert all(state['status'] == 'fresh' for state in page['meta']['sources'].values()), 'Sources have not resumed successful ingestion.'
    cursor = page['meta'].get('next_cursor')
    assert cursor, 'Expected a continuation cursor.'
    status, next_page = check.request(urls['client-api'] + '/v1/hazards?view=full&limit=3&cursor=' + urllib.parse.quote(cursor), headers=headers)
    assert status == 200 and not ({e['hazard_id'] for e in page['data']} & {e['hazard_id'] for e in next_page['data']}), 'Cursor pagination repeated records.'

    def trace_present():
        try:
            status, body = check.request(urls['tempo'] + '/api/traces/' + trace_id, headers={'Accept': 'application/json'})
        except json.JSONDecodeError:
            return None
        batches = body.get('batches', body.get('resourceSpans', []))
        names = {a['value'].get('stringValue') for batch in batches for a in batch.get('resource', {}).get('attributes', []) if a['key'] == 'service.name'}
        spans = [span for batch in batches for scope in batch.get('scopeSpans', batch.get('instrumentationLibrarySpans', [])) for span in scope.get('spans', [])]
        return {'trace_id': trace_id, 'services': sorted(names), 'spans': len(spans)} if status == 200 and {'client-api', 'aggregator'} <= names and any(s['name'] == 'postgres.read_page' for s in spans) else None
    checks['http_database_trace'] = check.wait_for(trace_present)
    report['passed'] = True


if __name__ == '__main__':
    try:
        main()
    finally:
        (check.STATE / 'check-cluster.json').write_text(json.dumps(check.REPORT, indent=2) + '\n')
    print(json.dumps(check.REPORT, indent=2))
