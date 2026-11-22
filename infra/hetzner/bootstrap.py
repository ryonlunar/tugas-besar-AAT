"""Bootstrap the ordinary M2 integration checkout on temporary benchmark VMs."""
import concurrent.futures
import json
from pathlib import Path
import shlex
import subprocess
import sys
import time

run = Path(sys.argv[1]).resolve()
nodes = json.loads((run / 'nodes.json').read_text())
ssh_options = [
    '-i', str(run / 'ssh'), '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes',
    '-o', 'StrictHostKeyChecking=accept-new', '-o', f'UserKnownHostsFile={run / "known_hosts"}',
    '-o', 'ConnectTimeout=6', '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3',
]
control = 'aat-dev'


def ssh(node, command, *, data=None, upload=None, capture=False, log=None, timeout=600):
    target = 'root@' + nodes[node]['public_ipv4']
    args = ['ssh', *ssh_options, target, command]
    options = dict(input=data, stdin=upload, check=True, timeout=timeout)
    if capture:
        return subprocess.run(args, stdout=subprocess.PIPE, **options).stdout
    if log:
        with (run / log).open('ab') as file:
            subprocess.run(args, stdout=file, stderr=subprocess.STDOUT, **options)
    else:
        subprocess.run(args, **options)


def ready(node):
    for _ in range(30):
        try:
            ssh(node,
                'cloud-init status --wait >/dev/null || cloud-init status --long >&2; '
                'command -v python3 >/dev/null',
                log=f'{node}.log', timeout=45)
            return
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
            time.sleep(3)
    raise RuntimeError(f'{node} SSH or cloud-init unavailable')


def private_config(node, *, k3s=True):
    addr = nodes[node]['private_ipv4']
    script = f'''
set -euo pipefail
addr={addr}
for n in $(seq 1 40); do
  iface=$(ip -j -4 address show | python3 -c 'import json,sys; print(next((i["ifname"] for i in json.load(sys.stdin) for a in i["addr_info"] if a["local"]==sys.argv[1]), ""))' "$addr")
  if [ -n "$iface" ]; then break; fi
  if [ "$n" = 3 ] && [ -d /sys/class/net/enp7s0 ]; then
    cat > /etc/systemd/network/30-aat-private.network <<'NETWORK'
[Match]
Name=enp7s0
[Network]
DHCP=ipv4
[DHCPv4]
RouteMetric=100
UseDNS=false
NETWORK
    networkctl reload
    networkctl reconfigure enp7s0
  fi
  sleep 2
done
[ -n "$iface" ]
if [ '{int(k3s)}' = 1 ]; then
  install -d /etc/rancher/k3s/config.yaml.d
  printf 'node-ip: %s\nflannel-iface: %s\n' "$addr" "$iface" > /etc/rancher/k3s/config.yaml.d/10-aat-private.yaml
fi
'''
    ssh(node, 'bash -s', data=script.encode(), log=f'{node}.log')


def prepare_control():
    ready(control)
    private_config(control)
    with (run / 'source.tar.gz').open('rb') as archive:
        ssh(control, "set -e; getent passwd ubuntu >/dev/null || useradd -m -s /bin/bash ubuntu; "
            "printf 'ubuntu ALL=(ALL) NOPASSWD:ALL\\n' > /etc/sudoers.d/90-aat-ubuntu; "
            "chmod 440 /etc/sudoers.d/90-aat-ubuntu; "
            "install -d -o ubuntu -g ubuntu /home/ubuntu/aat; "
            "runuser -u ubuntu -- tar -xzf - -C /home/ubuntu/aat", upload=archive,
            log='aat-dev.log')
    ip = nodes[control]['private_ipv4']
    ssh(control, 'runuser -l ubuntu -c ' + shlex.quote(
        f'cd ~/aat && bash scripts/m2/setup-vm.sh && bash scripts/m2/configure-node.sh server {ip} aat-dev'),
        log='aat-dev.log')


def prepare_agent(node):
    ready(node)
    private_config(node)
    ssh(node, 'umask 077; cat > /root/configure-node.sh',
        data=(run / 'configure-node.sh').read_bytes(), log=f'{node}.log')


def join_agent(node, token):
    ip = nodes[control]['private_ipv4']
    ssh(node, 'umask 077; cat > /root/join-token', data=token, log=f'{node}.log')
    ssh(node, f"trap 'rm -f /root/join-token' EXIT; "
        f'bash /root/configure-node.sh agent {ip} {node} /root/join-token', log=f'{node}.log')


def prepare_loadgen():
    node = 'aat-loadgen'
    ready(node)
    private_config(node, k3s=False)
    ssh(node, 'bash -s', data=b"""
set -euo pipefail
umask 077
install -d /root/aat-bench
cd /root/aat-bench
archive=k6-v2.4.0-linux-amd64.tar.gz
curl -fsSL --retry 3 "https://github.com/grafana/k6/releases/download/v2.4.0/$archive" -o "$archive"
printf '%s  %s\n' 5a5ecf23529ccf93135bc7e5b0c1cd5126ecf5f2672e22eed1556a0ab74abb87 "$archive" | sha256sum -c
tar -xzf "$archive" --strip-components=1 --wildcards '*/k6'
rm "$archive"
./k6 version
""", log='aat-loadgen.log')


if __name__ == '__main__':
    workers = [f'aat-worker-{index}' for index in range(1, 4)]
    if len(sys.argv) > 2:
        node = sys.argv[2]
        if node not in nodes:
            raise SystemExit(f'Unknown node {node}')
        target = 'root@' + nodes[node]['public_ipv4']
        raise SystemExit(subprocess.call(['ssh', *ssh_options, target, *sys.argv[3:]]))
    with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        setup = pool.submit(prepare_control)
        agents = {node: pool.submit(prepare_agent, node) for node in workers}
        loadgen = pool.submit(prepare_loadgen)
        setup.result()
        token = ssh(control, 'cat /var/lib/rancher/k3s/server/agent-token', capture=True)
        for future in agents.values():
            future.result()
        list(pool.map(lambda node: join_agent(node, token), workers))
        loadgen.result()
    commands = []
    for index, node in enumerate([control, *workers]):
        labels = [f'aat.dev/slot={index}', f'aat.dev/control={str(index == 0).lower()}']
        labels += [f'aat.dev/shard-{shard}={str(index != shard).lower()}' for shard in range(4)]
        commands.append('k3s kubectl label node ' + node + ' ' + ' '.join(labels) + ' --overwrite')
    ssh(control, ' && '.join([
        'k3s kubectl wait node --all --for=condition=Ready --timeout=150s',
        *commands, 'k3s kubectl get nodes -o wide',
    ]), log='aat-dev.log')
    print('Four private K3s nodes and the k6 runner are ready.', flush=True)
    ssh(control, "runuser -l ubuntu -c 'cd ~/aat && bash scripts/m2/deploy-cluster.sh'",
        log='deploy.log', timeout=1700)
