#!/usr/bin/env bash
set -euo pipefail

# Run as the normal Ubuntu user inside the dedicated development VM.
if [[ $(id -u) == 0 ]]; then
  echo 'Run this as the VM user, without sudo.' >&2
  exit 1
fi
sudo apt-get update
sudo apt-get install -y buildah ca-certificates curl git jq openssl python3
sudo install -d /etc/containers/registries.conf.d
printf '%s\n' 'unqualified-search-registries = ["docker.io"]' 'short-name-mode = "disabled"' \
  | sudo tee /etc/containers/registries.conf.d/aat.conf >/dev/null

if ! command -v k3s >/dev/null; then
  curl --fail --silent --show-error --location https://get.k3s.io -o /tmp/aat-install-k3s.sh
  sudo env INSTALL_K3S_VERSION=v1.37.2+k3s1 sh /tmp/aat-install-k3s.sh server \
    --node-name aat-dev --node-label aat.dev/environment=development \
    --disable traefik --disable servicelb
fi
mkdir -p "$HOME/.kube"
if [[ ! -f "$HOME/.kube/aat-dev.yaml" ]]; then
  sudo install -m 600 -o "$(id -u)" -g "$(id -g)" \
    /etc/rancher/k3s/k3s.yaml "$HOME/.kube/aat-dev.yaml"
  KUBECONFIG="$HOME/.kube/aat-dev.yaml" kubectl config rename-context default aat-dev
fi
export KUBECONFIG="$HOME/.kube/aat-dev.yaml"
kubectl --context aat-dev wait node/aat-dev --for=condition=Ready --timeout=180s

if ! command -v helm >/dev/null; then
  helm_version=v3.21.3
  download_dir=$(mktemp -d)
  trap 'rm -rf "$download_dir"' EXIT
  curl -fsSL "https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz" -o "$download_dir/helm.tar.gz"
  curl -fsSL "https://get.helm.sh/helm-${helm_version}-linux-amd64.tar.gz.sha256sum" -o "$download_dir/checksum"
  expected=$(awk '{print $1}' "$download_dir/checksum")
  printf '%s  %s\n' "$expected" "$download_dir/helm.tar.gz" | sha256sum --check --status
  tar -xzf "$download_dir/helm.tar.gz" -C "$download_dir"
  sudo install -m 755 "$download_dir/linux-amd64/helm" /usr/local/bin/helm
fi
printf '\nVM ready. Use: export KUBECONFIG="$HOME/.kube/aat-dev.yaml"\n'
