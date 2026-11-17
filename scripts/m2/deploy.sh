#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/aat-dev.yaml}"
namespace=sistem-informasi-kebencanaan-bnpb
kubectl --context aat-dev get nodes -l aat.dev/environment=development -o name | grep -q .
for command in helm python3 openssl buildah; do
  command -v "$command" >/dev/null || { echo "Missing $command; run scripts/m2/setup-vm.sh inside the VM." >&2; exit 1; }
done

python3 scripts/m2/prepare.py
mkdir -p .state/m2/images .state/m2/module-cache .state/m2/build-cache
for service in bmkg-mock pvmbg-mock aggregator auth client-api dashboard-consumer notifier-consumer; do
  image="aat.local/$service:m2-dev"
  sudo buildah bud --layers --format docker --isolation chroot --network host \
    --volume "$PWD/.state/m2/module-cache:/go/pkg/mod" \
    --volume "$PWD/.state/m2/build-cache:/root/.cache/go-build" \
    -t "$image" -f "services/$service/Dockerfile" .
  rm -f ".state/m2/images/$service.tar"
  sudo buildah push "$image" "docker-archive:$PWD/.state/m2/images/$service.tar:$image"
  sudo k3s ctr images import ".state/m2/images/$service.tar"
done

kubectl --context aat-dev apply -f infra/k8s/dev/namespace.yaml
kubectl --context aat-dev apply -f .state/m2/configuration.json
kubectl --context aat-dev -n "$namespace" apply -f infra/k8s/dev/network-policies.yaml \
  -f infra/k8s/dev/claims.yaml -f infra/k8s/dev/postgres.yaml
kubectl --context aat-dev -n "$namespace" rollout status deployment/postgres --timeout=240s
helm upgrade --install nats nats --repo https://nats-io.github.io/k8s/helm/charts/ \
  --version 2.15.2 --kube-context aat-dev --namespace "$namespace" \
  -f infra/k8s/dev/nats-values.yaml --wait --timeout 5m
kubectl --context aat-dev apply -k infra/k8s/dev
# The development tags are local; recreate pods to use freshly imported images.
kubectl --context aat-dev -n "$namespace" rollout restart deployment \
  bmkg-mock pvmbg-mock aggregator auth client-api dashboard-consumer notifier-consumer
for service in postgres bmkg-mock pvmbg-mock aggregator auth client-api dashboard-consumer notifier-consumer prometheus loki alloy grafana nats-exporter; do
  kubectl --context aat-dev -n "$namespace" rollout status "deployment/$service" --timeout=240s
done
kubectl --context aat-dev -n "$namespace" get pods
printf '\nRun: python3 scripts/m2/check.py\n'
