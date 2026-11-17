#!/usr/bin/env python3
"""Manage the optional four-VM Multipass development environment."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[2]
STATE = ROOT / '.state' / 'm2' / 'host'
NODES = ['aat-dev', 'aat-worker-1', 'aat-worker-2', 'aat-worker-3']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--multipass', default=os.environ.get('AAT_MULTIPASS_BIN') or shutil.which('multipass') or shutil.which('multipass.exe') or 'multipass')
    parser.add_argument('action', choices=['up', 'start', 'stop', 'status', 'sync', 'deploy'])
    args = parser.parse_args()
    os.umask(0o077)
    STATE.mkdir(parents=True, exist_ok=True)

    def mp(*words, capture=False, **kwargs):
        command = [args.multipass, *words]
        if capture:
            return subprocess.check_output(command, text=True, **kwargs).strip()
        subprocess.run(command, check=True, **kwargs)

    def transfer(path, node, target):
        source = str(path.resolve())
        if args.multipass.lower().endswith('.exe') and os.name != 'nt':
            source = subprocess.check_output(['wslpath', '-w', source], text=True).strip()
        mp('transfer', source, f'{node}:{target}')

    def info():
        return {entry['name']: entry for entry in json.loads(mp('list', '--format', 'json', capture=True))['list']}

    def source_archive():
        files = subprocess.check_output(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard'], cwd=ROOT).split(b'\0')
        destination = STATE / 'source.tar.gz'
        with tarfile.open(destination, 'w:gz') as archive:
            for raw in files:
                name = os.fsdecode(raw)
                if name in ('go.mod', 'go.sum', '.env.example', '.dockerignore') or name.startswith(('contracts/', 'pkg/', 'services/', 'infra/', 'scripts/', 'docs/m2-')):
                    path = ROOT / name
                    if path.is_file():
                        archive.add(path, arcname=name)
        return destination

    def sync():
        transfer(source_archive(), NODES[0], 'aat-source.tar.gz')
        mp('exec', NODES[0], '--', 'bash', '-lc', 'mkdir -p ~/aat && tar -xzf ~/aat-source.tar.gz -C ~/aat')

    def refresh_hosts():
        control_ip = next(ip for ip in info()[NODES[0]]['ipv4'] if not ip.startswith(('10.42.', '10.43.')))
        ipaddress.ip_address(control_ip)
        code = "from pathlib import Path; p=Path('/etc/hosts'); lines=[s for s in p.read_text().splitlines() if not s.endswith('# aat-control')]; p.write_text('\\n'.join(lines)+'\\n'+__import__('sys').argv[1]+' aat-control # aat-control\\n')"
        for node in NODES:
            mp('exec', node, '--', 'sudo', 'python3', '-c', code, control_ip)
        return control_ip

    if args.action == 'status':
        mp('list')
        return
    if args.action == 'stop':
        mp('stop', *NODES)
        return
    if args.action == 'up':
        current = info()
        zone = current.get(NODES[0], {}).get('zone', {}).get('name')
        for node in NODES:
            if node not in current:
                command = ['launch', '24.04', '--name', node, '--cpus', '4' if node == NODES[0] else '2', '--memory', '8G' if node == NODES[0] else '3G', '--disk', '30G']
                if zone:
                    command += ['--zone', zone]
                mp(*command)
                if node == NODES[0]:
                    zone = info()[node].get('zone', {}).get('name')
            elif current[node]['state'] != 'Running':
                mp('start', node)
    elif args.action == 'start':
        mp('start', *NODES)
    if args.action in ('sync', 'deploy'):
        sync()
        if args.action == 'deploy':
            mp('exec', NODES[0], '--', 'bash', '-lc', 'cd ~/aat && bash scripts/m2/deploy-cluster.sh')
        return

    control_ip = refresh_hosts()
    if args.action == 'up':
        sync()
        mp('exec', NODES[0], '--', 'bash', '-lc', 'command -v k3s >/dev/null || bash ~/aat/scripts/m2/setup-vm.sh')
        mp('exec', NODES[0], '--', 'bash', '/home/ubuntu/aat/scripts/m2/configure-node.sh', 'server', control_ip, NODES[0])
        token = STATE / 'join-token'
        token.write_text(mp('exec', NODES[0], '--', 'sudo', 'cat', '/var/lib/rancher/k3s/server/node-token', capture=True))
        try:
            for node in NODES[1:]:
                transfer(ROOT / 'scripts/m2/configure-node.sh', node, 'configure-node.sh')
                transfer(token, node, 'aat-join-token')
                mp('exec', node, '--', 'chmod', '600', 'aat-join-token')
                mp('exec', node, '--', 'bash', 'configure-node.sh', 'agent', control_ip, node, 'aat-join-token')
                mp('exec', node, '--', 'rm', '-f', 'aat-join-token')
        finally:
            token.unlink(missing_ok=True)
    else:
        for node in NODES[1:]:
            # Agents reconnect using the refreshed hostname. Do not interrupt
            # systemd's automatic boot-time start while it is still activating.
            mp('exec', node, '--', 'sudo', 'systemctl', 'start', '--no-block', 'k3s-agent')
    kube = ['exec', NODES[0], '--', 'env', 'KUBECONFIG=/home/ubuntu/.kube/aat-dev.yaml', 'kubectl', '--context', 'aat-dev']
    for slot, node in enumerate(NODES):
        mp(*kube, 'wait', 'node/' + node, '--for=condition=Ready', '--timeout=180s')
        labels = [f'aat.dev/slot={slot}'] + [f'aat.dev/shard-{unit}={str(unit != slot).lower()}' for unit in range(4)]
        labels.append('aat.dev/control=' + str(slot == 0).lower())
        mp(*kube, 'label', 'node', node, *labels, '--overwrite')
    if args.action == 'start':
        namespaced = kube + ['-n', 'sistem-informasi-kebencanaan-bnpb']
        deployed = mp(*namespaced, 'get', 'configmap', 'aat-storage-cutover', '--ignore-not-found', '-o', 'name', capture=True)
        if deployed:
            for unit in range(4):
                mp(*namespaced, 'wait', f'statefulset/shard-{unit}', '--for=jsonpath={.status.readyReplicas}=3', '--timeout=600s')
            mp(*namespaced, 'rollout', 'status', 'statefulset/nats-ha', '--timeout=300s')
            mp(*namespaced, 'rollout', 'status', 'deployment/aggregator', '--timeout=180s')
    mp(*kube, 'get', 'nodes', '-o', 'wide')


if __name__ == '__main__':
    main()
