#!/usr/bin/env python3
"""Prepare four replicated PostgreSQL units and private client configuration."""
import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'
UNITS = [f'shard-{i}' for i in range(4)]


def secret(name, data, kind='Opaque'):
    return {'apiVersion': 'v1', 'kind': 'Secret',
            'metadata': {'name': name, 'namespace': NAMESPACE},
            'type': kind, 'stringData': data}


def save(name, resources):
    (STATE / name).write_text(json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': resources}, indent=2) + '\n')


def prepare():
    resources, certificates = [], {}
    for name in UNITS:
        directory = STATE / 'tls' / name
        directory.mkdir(parents=True, exist_ok=True)
        certificate, key = directory / 'tls.crt', directory / 'tls.key'
        if certificate.exists() != key.exists():
            raise ValueError(f'Incomplete saved TLS identity for {name}; refusing to replace it.')
        if not certificate.exists():
            dns_names = [name, name + '.' + NAMESPACE, name + '.' + NAMESPACE + '.svc', name + '.' + NAMESPACE + '.svc.cluster.local']
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '3650',
                            '-keyout', str(key), '-out', str(certificate), '-subj', '/CN=' + name,
                            '-addext', 'subjectAltName=' + ','.join('DNS:' + dns for dns in dns_names)],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        certificates[name + '.crt'] = certificate.read_text()
        resources.append(secret(name + '-tls', {'tls.crt': certificate.read_text(), 'tls.key': key.read_text()}, 'kubernetes.io/tls'))
        resources.append({
            'apiVersion': 'acid.zalan.do/v1', 'kind': 'postgresql',
            'metadata': {'name': name, 'namespace': NAMESPACE},
            'spec': {
                'teamId': 'aat', 'numberOfInstances': 3,
                'postgresql': {'version': '16', 'parameters': {
                    'shared_buffers': '64MB', 'work_mem': '1MB', 'maintenance_work_mem': '32MB',
                    'max_connections': '100', 'max_wal_size': '256MB', 'min_wal_size': '64MB',
                    'synchronous_commit': 'on', 'log_min_duration_statement': '1000'}},
                'users': {'aggregator': []}, 'databases': {'aat': 'aggregator'},
                'volume': {'size': '2Gi', 'storageClass': 'local-path'},
                'nodeAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': {'nodeSelectorTerms': [
                    {'matchExpressions': [{'key': 'aat.dev/' + name, 'operator': 'In', 'values': ['true']}]}]}},
                'resources': {'requests': {'memory': '512Mi', 'cpu': '100m'}, 'limits': {'memory': '1Gi', 'cpu': '2'}},
                'patroni': {'ttl': 20, 'loop_wait': 2, 'retry_timeout': 3,
                            'synchronous_mode': True, 'synchronous_mode_strict': True,
                            'synchronous_node_count': 1, 'failsafe_mode': False,
                            'initdb': {'data-checksums': 'true'}},
                'spiloFSGroup': 103, 'tls': {'secretName': name + '-tls'},
                'enableLogicalBackup': False,
            }})
    resources.append(secret('aat-shard-certs', certificates))
    save('cluster-storage.json', resources)


def credentials():
    shards = []
    for name in UNITS:
        result = subprocess.check_output(['kubectl', '--context', 'aat-dev', '-n', NAMESPACE,
                                          'get', 'secret', f'aggregator.{name}.credentials.postgresql.acid.zalan.do', '-o', 'json'])
        data = json.loads(result)['data']
        username = base64.b64decode(data['username']).decode()
        password = base64.b64decode(data['password']).decode()
        shards.append({'unit': name, 'dsn': f'postgresql://{quote(username, safe="")}:{quote(password, safe="")}@{name}:5432/aat?sslmode=verify-full&sslrootcert=/run/shard-certs/{name}.crt'})
    save('cluster-credentials.json', [secret('aat-shards', {'shards.json': json.dumps(shards)})])


def configure_recovery():
    kube = ['kubectl', '--context', 'aat-dev', '-n', NAMESPACE]
    settings = json.loads(subprocess.check_output(kube + ['get', 'configmap', 'postgres-recovery', '-o', 'json']))['data']
    for name in UNITS:
        # These Patroni settings are not exposed by the operator's CRD. Apply
        # them to DCS idempotently; no database files are changed by this command.
        subprocess.run(kube + ['wait', '--for=create', 'pod/' + name + '-0', '--timeout=180s'], check=True)
        subprocess.run(kube + ['wait', 'pod/' + name + '-0', '--for=condition=Ready', '--timeout=180s'], check=True)
        command = kube + ['exec', name + '-0', '--', 'patronictl', '-c', '/home/postgres/postgres.yml', 'edit-config', name, '--force']
        for key, value in sorted(settings.items()):
            command += ['--set', 'postgresql.' + key + '=' + value]
        subprocess.run(command, check=True, stdout=subprocess.DEVNULL)
        print(name + ': automatic rewind/rebuild configured')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'credentials', 'configure-recovery'], default='prepare', nargs='?')
    args = parser.parse_args()
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True)
    {'prepare': prepare, 'credentials': credentials, 'configure-recovery': configure_recovery}[args.action]()


if __name__ == '__main__':
    main()
