#!/usr/bin/env python3
"""Exercise the storage/ownership prototype using disposable PostgreSQL databases.

Run inside the configured development VM. Existing application data is untouched.
The databases and temporary Go test container are removed after the run.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'
os.environ.setdefault('KUBECONFIG', str(Path.home() / '.kube' / 'aat-dev.yaml'))
KUBE = ['kubectl', '--context', 'aat-dev', '-n', NAMESPACE]


def postgres(sql):
    subprocess.run(KUBE + ['exec', '-i', 'deployment/postgres', '--', 'sh', '-ec',
                          'psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1'],
                   input=sql, text=True, check=True)


def main():
    os.umask(0o077)
    nodes = json.loads(subprocess.check_output(KUBE + [
        'get', 'nodes', '-l', 'aat.dev/environment=development', '-o', 'json']))['items']
    if not nodes:
        raise RuntimeError('Run this against the configured development VM.')
    credentials = json.loads((STATE / 'credentials.json').read_text())
    config = json.loads(subprocess.check_output(KUBE + ['get', 'configmap', 'aat-runtime', '-o', 'json']))['data']
    ip = json.loads(subprocess.check_output(KUBE + ['get', 'service', 'postgres', '-o', 'json']))['spec']['clusterIP']
    run_id = uuid.uuid4().hex[:8]
    databases = [f'aat_foundation_{run_id}_shard_{i}_test' for i in range(4)]
    private = STATE / f'foundation-{run_id}.json'
    created = []
    container = None
    try:
        for database in databases:
            postgres(f'CREATE DATABASE {database};\n')
            created.append(database)
        user = urllib.parse.quote(config['POSTGRES_USER'], safe='')
        password = urllib.parse.quote(credentials['POSTGRES_PASSWORD'], safe='')
        private.write_text(json.dumps([
            f'postgresql://{user}:{password}@{ip}:5432/{database}?sslmode=disable&connect_timeout=2'
            for database in databases]))
        container = subprocess.check_output(['sudo', 'buildah', 'from', 'golang:1.23-alpine'], text=True).strip()
        subprocess.run([
            'sudo', 'buildah', 'run', '--isolation', 'chroot', '--network', 'host',
            '--workingdir', '/workspace',
            '--volume', f'{ROOT}:/workspace:ro',
            '--volume', f'{STATE}/module-cache:/go/pkg/mod',
            '--volume', f'{STATE}/build-cache:/root/.cache/go-build',
            '--volume', f'{private}:/run/foundation-dsns.json:ro',
            '--env', 'AAT_FOUNDATION_DSN_FILE=/run/foundation-dsns.json',
            container, '--', 'go', 'test', '-v', '-count=1', '-timeout=60s',
            './services/aggregator/internal/foundationtest',
        ], check=True)
    finally:
        cleanup_errors = []
        if container:
            result = subprocess.run(['sudo', 'buildah', 'rm', container], stdout=subprocess.DEVNULL)
            if result.returncode:
                cleanup_errors.append('temporary Buildah container')
        private.unlink(missing_ok=True)
        for database in reversed(created):
            try:
                postgres(f'DROP DATABASE {database} WITH (FORCE);\n')
            except subprocess.CalledProcessError:
                cleanup_errors.append(database)
        if cleanup_errors:
            message = 'Cleanup failed for: ' + ', '.join(cleanup_errors)
            if sys.exc_info()[0] is not None:
                print(message, file=sys.stderr)
            else:
                raise RuntimeError(message)


if __name__ == '__main__':
    main()
