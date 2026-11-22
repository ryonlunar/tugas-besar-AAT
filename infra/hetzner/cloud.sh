#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  echo 'Usage: cloud.sh plan RUN SSH_CIDR [GIT_REF] | up RUN | ssh RUN NODE [COMMAND] | down RUN' >&2
  exit 2
}

[[ $# -ge 2 ]] || usage
action=$1
name=$2
shift 2
[[ $name =~ ^[a-z0-9]([a-z0-9-]{0,22}[a-z0-9])?$ ]] || usage

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
state="$repo/.state/hetzner/$name"

if [[ $action == ssh ]]; then
  [[ $# -ge 1 ]] || usage
  exec python3 "$here/bootstrap.py" "$state" "$@"
fi

if [[ -z ${HCLOUD_TOKEN:-} ]]; then
  HCLOUD_TOKEN=$(<"${AAT_HCLOUD_TOKEN_FILE:-$HOME/.config/aat-hetzner/token}")
  export HCLOUD_TOKEN
fi

case "$action" in
  plan)
    [[ $# -ge 1 && $# -le 2 ]] || usage
    mkdir -p "$repo/.state/hetzner"
    mkdir "$state"
    cp "$here/main.tf" "$state/"
    [[ ! -f "$here/.terraform.lock.hcl" ]] || cp "$here/.terraform.lock.hcl" "$state/"
    ref=$(git -C "$repo" rev-parse --verify "${2:-HEAD}^{commit}")
    printf '%s\n' "$ref" > "$state/source-commit"
    git -C "$repo" archive --format=tar.gz --output="$state/source.tar.gz" "$ref" \
      go.mod go.sum .env.example .dockerignore contracts pkg services infra scripts
    git -C "$repo" show "$ref:scripts/m2/configure-node.sh" > "$state/configure-node.sh"
    ssh-keygen -q -t ed25519 -N '' -C "aat-$name" -f "$state/ssh"
    python3 - "$state" "$name" "$1" <<'PY'
import ipaddress
import json
from pathlib import Path
import sys

directory, name, cidr = Path(sys.argv[1]), sys.argv[2], sys.argv[3]
network = ipaddress.IPv4Network(cidr)
if network.prefixlen != 32:
    raise SystemExit('Give one IPv4 address with a /32 prefix for SSH.')
variables = {'run_id': name, 'ssh_cidr': str(network),
             'ssh_public_key': (directory / 'ssh.pub').read_text().strip()}
(directory / 'terraform.tfvars.json').write_text(json.dumps(variables))
PY
    tofu -chdir="$state" init -input=false
    tofu -chdir="$state" plan -input=false -out=create.tfplan
    ;;
  up)
    [[ $# -eq 0 && -f "$state/create.tfplan" ]] || usage
    date +%s > "$state/start-epoch"
    tofu -chdir="$state" apply -input=false create.tfplan
    tofu -chdir="$state" output -json nodes > "$state/nodes.json"
    python3 -u "$here/bootstrap.py" "$state"
    ;;
  down)
    [[ $# -eq 0 && -f "$state/main.tf" ]] || usage
    tofu -chdir="$state" destroy -input=false -auto-approve -lock-timeout=45s
    [[ -z $(tofu -chdir="$state" state list) ]]
    touch "$state/cleaned"
    rm -f "$state/ssh"
    ;;
  *) usage ;;
esac
