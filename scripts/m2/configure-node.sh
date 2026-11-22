#!/usr/bin/env bash
set -euo pipefail

role=${1:?Use: configure-node.sh server|agent control-ip node-name [token-file]}
control_ip=${2:?control IP required}
node_name=${3:?node name required}
if [[ $role != server && $role != agent ]]; then
  echo 'Role must be server or agent.' >&2
  exit 1
fi

iptables=/usr/sbin/iptables
if [[ ! -x $iptables ]]; then
  sudo apt-get update
  sudo apt-get install -y iptables
fi
if [[ $(readlink "$iptables") == /etc/alternatives/iptables ]]; then
  # The Agent resolves this path through /host. Keep the Ubuntu backend
  # selected by update-alternatives, using a link that stays inside /host.
  target=$(realpath --relative-to=/usr/sbin "$iptables")
  sudo ln -sfn "$target" "$iptables"
fi

sudo install -d /etc/rancher/k3s/config.yaml.d
sudo python3 - "$control_ip" <<'PY'
from pathlib import Path
import ipaddress
import sys
ip = str(ipaddress.ip_address(sys.argv[1]))
path = Path('/etc/hosts')
lines = [line for line in path.read_text().splitlines() if not line.endswith('# aat-control')]
path.write_text('\n'.join(lines) + f'\n{ip} aat-control # aat-control\n')
PY
registry=/etc/rancher/k3s/registries.yaml
if sudo test -f "$registry" && ! sudo head -n 1 "$registry" | grep -q 'AAT development'; then
  echo 'Existing registries.yaml is unmanaged; merge the aat.local mirror before proceeding.' >&2
  exit 1
fi
printf '%s\n' '# AAT development registry mirrors' 'mirrors:' '  aat.local:' '    endpoint:' '      - "http://aat-control:30500"' \
  | sudo tee "$registry" >/dev/null
if [[ $role == server ]]; then
  cat <<'YAML' | sudo tee /etc/rancher/k3s/config.yaml.d/20-aat-cluster.yaml >/dev/null
tls-san:
  - aat-control
embedded-registry: false
kube-controller-manager-arg:
  - node-monitor-grace-period=20s
kubelet-arg:
  - node-status-update-frequency=2s
YAML
  sudo systemctl restart k3s
else
  token_file=${4:?private join-token file required}
  sudo install -m 600 "$token_file" /etc/rancher/k3s/join-token
  sudo python3 - "$node_name" <<'PY'
from pathlib import Path
import json
import sys
config = {'server': 'https://aat-control:6443',
          'token-file': '/etc/rancher/k3s/join-token',
          'node-name': sys.argv[1],
          'node-label': ['aat.dev/environment=development'],
          'kubelet-arg': ['node-status-update-frequency=2s']}
Path('/etc/rancher/k3s/config.yaml.d/20-aat-cluster.yaml').write_text(json.dumps(config))
PY
  if ! command -v k3s >/dev/null; then
    sudo apt-get update
    sudo apt-get install -y ca-certificates curl
    curl -fsSL https://get.k3s.io -o /tmp/aat-install-agent.sh
    sudo env INSTALL_K3S_VERSION=v1.37.2+k3s1 INSTALL_K3S_EXEC=agent sh /tmp/aat-install-agent.sh
  else
    sudo systemctl restart --no-block k3s-agent
  fi
fi
