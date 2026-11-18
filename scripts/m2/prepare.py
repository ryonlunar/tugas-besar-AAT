#!/usr/bin/env python3
"""Generate private development configuration once, without copying M1 secrets."""
import base64
import json
import os
from pathlib import Path
import secrets
import subprocess

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
NAMESPACE = 'sistem-informasi-kebencanaan-bnpb'
SECRET_KEYS = (
    'BMKG_API_KEY', 'PVMBG_TOKEN', 'AGGREGATOR_API_KEY', 'CONSUMER_DEMO_KEY',
    'POSTGRES_PASSWORD', 'NATS_TOKEN', 'MEDIA_CLIENT_SECRET',
    'FIELD_TEAM_CLIENT_SECRET', 'BNPB_INTERNAL_CLIENT_SECRET', 'AUDIT_TOKEN', 'FIELD_ALERT_TOKEN',
)


def prepare():
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    credentials_path = STATE / 'credentials.json'
    if credentials_path.exists():
        credentials = json.loads(credentials_path.read_text())
        if set(credentials) - set(SECRET_KEYS) or not all(credentials.values()):
            raise ValueError('Invalid saved credentials; inspect the private state directory.')
    else:
        credentials = {key: secrets.token_hex(32) for key in SECRET_KEYS}
        credentials_path.write_text(json.dumps(credentials, indent=2) + '\n')
    for key in SECRET_KEYS:
        credentials.setdefault(key, secrets.token_hex(32))
    credentials_path.write_text(json.dumps(credentials, indent=2) + '\n')
    private_key, public_key = STATE / 'jwt-private.pem', STATE / 'jwt-public.pem'
    if private_key.exists() != public_key.exists():
        raise ValueError('Incomplete saved JWT key pair; refusing to replace an existing key.')
    if not private_key.exists():
        subprocess.run(['openssl', 'genpkey', '-algorithm', 'RSA', '-pkeyopt',
                        'rsa_keygen_bits:2048', '-out', str(private_key)], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['openssl', 'pkey', '-in', str(private_key), '-pubout',
                        '-out', str(public_key)], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    runtime = {}
    for line in (ROOT / '.env.example').read_text().splitlines():
        if line and not line.startswith('#') and '=' in line:
            key, value = line.split('=', 1)
            if key not in SECRET_KEYS:
                runtime[key] = value
    runtime.update(POSTGRES_HOST='postgres', METRICS_ADDR=':9091')
    runtime_path = STATE / 'runtime.env'
    if not runtime_path.exists():
        runtime_path.write_text(''.join(f'{key}={value}\n' for key, value in runtime.items()))
    for line in runtime_path.read_text().splitlines():
        if line and not line.startswith('#'):
            key, value = line.split('=', 1)
            if key in SECRET_KEYS:
                raise ValueError('Keep credentials out of runtime.env.')
            runtime[key] = value
    resources = [
        {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'aat-credentials'},
         'type': 'Opaque', 'stringData': credentials},
        {'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'aat-runtime'},
         'data': runtime},
    ]
    for name, path in [('private', private_key), ('public', public_key)]:
        resources.append({'apiVersion': 'v1', 'kind': 'Secret',
                          'metadata': {'name': 'aat-jwt-' + name}, 'type': 'Opaque',
                          'data': {path.name: base64.b64encode(path.read_bytes()).decode()}})
    for resource in resources:
        resource['metadata']['namespace'] = NAMESPACE
    (STATE / 'configuration.json').write_text(json.dumps(
        {'apiVersion': 'v1', 'kind': 'List', 'items': resources}, indent=2) + '\n')
    print('Private configuration ready in .state/m2/ (existing credentials retained).')


if __name__ == '__main__':
    prepare()
