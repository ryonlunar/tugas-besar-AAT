#!/usr/bin/env python3
"""Render immutable application image references and the one-time storage job."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2'
SERVICES = ['bmkg-mock', 'pvmbg-mock', 'aggregator', 'auth', 'client-api', 'dashboard-consumer', 'notifier-consumer', 'field-team-receiver']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['tag', 'render'])
    args = parser.parse_args()
    os.umask(0o077)
    digest = hashlib.sha256()
    paths = [ROOT / 'go.mod', ROOT / 'go.sum']
    for directory in ('contracts', 'pkg', 'services'):
        paths += [p for p in (ROOT / directory).rglob('*') if p.is_file() and (p.suffix in ('.go', '.sql', '.json') or p.name == 'Dockerfile')]
    for path in sorted(paths):
        digest.update(str(path.relative_to(ROOT)).encode() + b'\0' + path.read_bytes() + b'\0')
    tag = 'm2-' + digest.hexdigest()[:12]
    if args.action == 'tag':
        print(tag)
        return
    render = STATE / 'render'
    render.mkdir(parents=True, exist_ok=True)
    (render / 'kustomization.yaml').write_text(json.dumps({
        'apiVersion': 'kustomize.config.k8s.io/v1beta1', 'kind': 'Kustomization',
        'resources': ['../../../infra/k8s/cluster'],
        'images': [{'name': 'aat.local/' + service, 'newTag': tag} for service in SERVICES],
        'patches': [{'target': {'kind': 'Deployment', 'name': '|'.join(SERVICES)},
                     'patch': '- op: replace\n  path: /spec/template/spec/containers/0/imagePullPolicy\n  value: IfNotPresent\n'}],
    }, indent=2) + '\n')
    rendered = STATE / 'cluster-applications.yaml'
    rendered.write_bytes(subprocess.check_output(['kubectl', '--context', 'aat-dev', 'kustomize', str(render)]))
    # Use the original YAML for apply: client-side API defaulting can otherwise
    # introduce empty immutable PVC fields. JSON is only needed to build the Job.
    command = ['kubectl', '--context', 'aat-dev', 'create', '--dry-run=client', '-f', str(rendered), '-o', 'json']
    text = subprocess.check_output(command, text=True).strip()
    decoder, manifests = json.JSONDecoder(), []
    while text:
        item, end = decoder.raw_decode(text)
        manifests.extend(item['items'] if item['kind'] == 'List' else [item])
        text = text[end:].lstrip()
    aggregator = next(item for item in manifests if item['kind'] == 'Deployment' and item['metadata']['name'] == 'aggregator')
    pod = aggregator['spec']['template']
    pod['metadata']['labels']['app'] = 'aat-shard-migrate'
    pod['spec']['nodeSelector'] = {'aat.dev/control': 'true'}
    pod['spec'].pop('affinity', None)
    pod['spec']['restartPolicy'] = 'Never'
    container = pod['spec']['containers'][0]
    container['args'] = ['migrate-shards']
    for key in ('startupProbe', 'livenessProbe', 'readinessProbe'):
        container.pop(key, None)
    container['env'].append({'name': 'POSTGRES_PASSWORD', 'valueFrom': {'secretKeyRef': {'name': 'aat-credentials', 'key': 'POSTGRES_PASSWORD'}}})
    job = {'apiVersion': 'batch/v1', 'kind': 'Job',
           'metadata': {'name': 'aat-shard-migrate', 'namespace': aggregator['metadata']['namespace']},
           'spec': {'backoffLimit': 0, 'activeDeadlineSeconds': 360, 'template': pod}}
    (STATE / 'cluster-migration.json').write_text(json.dumps(job, indent=2) + '\n')


if __name__ == '__main__':
    main()
