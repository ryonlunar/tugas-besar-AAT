#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/aat-dev.yaml}"
namespace=sistem-informasi-kebencanaan-bnpb
kube=(kubectl --context aat-dev -n "$namespace")
python3 scripts/m2/prepare.py
kubectl --context aat-dev apply -f infra/k8s/dev/namespace.yaml -f .state/m2/configuration.json
sensor_url=$(python3 -c 'import json; print(next(r["data"] for r in json.load(open(".state/m2/configuration.json"))["items"] if r["kind"]=="ConfigMap").get("PANTAI_BASE_URL",""))')
if [[ "$sensor_url" == http://sensor-pantai:8080 ]]; then
  "${kube[@]}" get secret pantai-token -o name >/dev/null
  "${kube[@]}" apply -f infra/k8s/coastal/sensor.yaml
fi
"${kube[@]}" apply -f infra/k8s/cluster/audit-access.yaml
"${kube[@]}" apply -f infra/k8s/cluster/registry.yaml
"${kube[@]}" rollout status deployment/registry --timeout=180s
bash scripts/m2/deploy-storage.sh
"${kube[@]}" apply -f infra/k8s/cluster/broker-policy.yaml
helm upgrade --install nats-ha nats --repo https://nats-io.github.io/k8s/helm/charts \
  --version 2.15.2 --kube-context aat-dev --namespace "$namespace" \
  -f infra/k8s/cluster/nats-values.yaml --wait --timeout 5m

tag=$(python3 scripts/m2/render-cluster.py tag)
mkdir -p .state/m2/images .state/m2/module-cache .state/m2/build-cache
for service in bmkg-mock pvmbg-mock aggregator auth client-api dashboard-consumer notifier-consumer field-team-receiver; do
  image="aat.local/$service:$tag"
  if ! sudo k3s ctr images list -q | grep -Fx "$image" >/dev/null; then
    sudo buildah bud --layers --format docker --isolation chroot --network host \
      --volume "$PWD/.state/m2/module-cache:/go/pkg/mod" \
      --volume "$PWD/.state/m2/build-cache:/root/.cache/go-build" \
      -t "$image" -f "services/$service/Dockerfile" .
    rm -f ".state/m2/images/$service.tar"
    sudo buildah push "$image" "docker-archive:$PWD/.state/m2/images/$service.tar:$image"
    sudo k3s ctr images import ".state/m2/images/$service.tar"
  fi
  sudo buildah push --tls-verify=false "$image" "docker://aat-control:30500/$service:$tag"
done
python3 scripts/m2/render-cluster.py render

if ! "${kube[@]}" get configmap aat-storage-cutover >/dev/null 2>&1; then
  # Stop the sole legacy writer before taking its canonical snapshot. Keep the
  # original database/PVC and broker/PVC for inspection after the transition.
  "${kube[@]}" apply -f infra/k8s/dev/claims.yaml -f infra/k8s/dev/postgres.yaml
  "${kube[@]}" rollout status deployment/postgres --timeout=180s
  if "${kube[@]}" get deployment aggregator >/dev/null 2>&1; then
    "${kube[@]}" scale deployment aggregator --replicas=0
    if [[ -n $("${kube[@]}" get pod -l 'app=aggregator,!job-name' -o name) ]]; then
      "${kube[@]}" wait pod -l 'app=aggregator,!job-name' --for=delete --timeout=90s
    fi
  fi
  "${kube[@]}" delete job aat-shard-migrate --ignore-not-found --wait=true
  kubectl --context aat-dev apply -f .state/m2/cluster-migration.json
  if ! "${kube[@]}" wait job/aat-shard-migrate --for=jsonpath='{.status.conditions[0].status}'=True --timeout=360s \
    || [[ $("${kube[@]}" get job aat-shard-migrate -o jsonpath='{.status.succeeded}') != 1 ]]; then
    "${kube[@]}" logs job/aat-shard-migrate
    echo 'Storage migration failed; the original database is preserved.' >&2
    exit 1
  fi
  "${kube[@]}" logs job/aat-shard-migrate
  "${kube[@]}" create configmap aat-storage-cutover --from-literal=completed=true
fi
kubectl --context aat-dev apply -f .state/m2/cluster-applications.yaml
if "${kube[@]}" get statefulset nats >/dev/null 2>&1; then
  "${kube[@]}" scale statefulset nats --replicas=0
fi
for service in bmkg-mock pvmbg-mock aggregator auth client-api dashboard-consumer notifier-consumer field-team-receiver prometheus loki alloy grafana tempo; do
  "${kube[@]}" rollout status "deployment/$service" --timeout=240s
done
if [[ "$sensor_url" == http://sensor-pantai:8080 ]]; then
  "${kube[@]}" rollout status deployment/sensor-pantai --timeout=180s
fi
"${kube[@]}" get pods -o wide
